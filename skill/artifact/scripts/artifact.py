#!/usr/bin/env python3
"""Artifact 服务命令行工具（只依赖 Python 3.8+ 标准库）。

常用：
  artifact.py whoami                      查看身份、配额，检查技能是否需要更新
  artifact.py check <目录或文件>           发布前检查，不保存
  artifact.py publish <目录或文件> -m 说明  发布；同一路径再次发布会更新同一个 artifact
  artifact.py edit <ID> --old 旧文本 --new 新文本 -m 说明
  artifact.py list [-q 关键词]
  artifact.py pull <ID> <目录>             取回源码继续修改

运行 artifact.py <命令> -h 查看每个命令的参数。
"""

import argparse
import base64
import http.client
import io
import json
import os
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request

SKILL_VERSION = "{{SKILL_VERSION}}"
DEFAULT_SERVER = "{{SERVER_URL}}"
CONFIG_DIR = os.path.expanduser(os.environ.get("ARTIFACT_CONFIG_DIR", "~/.config/artifact"))
CONFIG_PATH = os.path.join(CONFIG_DIR, "config.json")
STATE_PATH = os.path.join(CONFIG_DIR, "state.json")
SKILL_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TEXT_EXT = {
    ".html", ".htm", ".css", ".js", ".mjs", ".json", ".map", ".md", ".markdown", ".txt", ".csv", ".tsv",
    ".xml", ".svg", ".yaml", ".yml", ".geojson", ".gltf",
}
SKIP_DIRS = {"node_modules", "__pycache__", ".git", ".svn", ".hg", ".venv", "venv", ".idea", ".vscode"}
LEVEL_ZH = {"error": "错误", "warn": "警告", "info": "建议"}
VIS_ZH = {"private": "私有", "unlisted": "持链接可看", "public": "公开"}


# ---------- 配置与状态 ----------

def die(msg, code=1):
    print(msg, file=sys.stderr)
    sys.exit(code)


def load_json(path, default):
    try:
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return default
    except (OSError, ValueError) as e:
        die("读取 %s 失败：%s" % (path, e))


def save_json(path, data, private=False):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
    if private:
        os.chmod(tmp, 0o600)
    os.replace(tmp, path)


def config():
    cfg = load_json(CONFIG_PATH, {})
    server = os.environ.get("ARTIFACT_SERVER") or cfg.get("server") or ""
    key = os.environ.get("ARTIFACT_API_KEY") or cfg.get("api_key") or ""
    if not server and not DEFAULT_SERVER.startswith("{{"):
        server = DEFAULT_SERVER
    if not server or not key:
        die("缺少配置：没有找到服务地址或 API Key。\n"
            "请让用户提供 API Key，然后运行：\n"
            "  python3 %s setup --server <服务地址> --key <API Key>" % os.path.abspath(__file__))
    return server.rstrip("/"), key


# ---------- HTTP ----------

class APIError(Exception):
    def __init__(self, status, code, message, data=None):
        super().__init__(message)
        self.status, self.code, self.message, self.data = status, code, message, data or {}


_DIRECT = urllib.request.build_opener(urllib.request.ProxyHandler({}))
_use_direct = os.environ.get("ARTIFACT_NO_PROXY") == "1"


def _open(req, timeout):
    """先按环境变量里的代理访问；代理连不上或返回 502/503/504 时改为直连，并在本进程内记住。"""
    global _use_direct
    if _use_direct:
        return _DIRECT.open(req, timeout=timeout)
    try:
        return urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        has_proxy = any(os.environ.get(k) for k in ("http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY"))
        if has_proxy and e.code in (502, 503, 504) and "json" not in (e.headers.get("Content-Type") or ""):
            _use_direct = True
            return _DIRECT.open(req, timeout=timeout)
        raise
    except urllib.error.URLError:
        _use_direct = True
        return _DIRECT.open(req, timeout=timeout)


def request(method, path, body=None, raw=False, server=None, key=None, timeout=120):
    if server is None:
        server, key = config()
    url = server + path
    headers = {"Authorization": "Bearer " + key, "User-Agent": "artifact-skill/" + SKILL_VERSION}
    data = None
    if body is not None:
        data = json.dumps(body, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json; charset=utf-8"
    attempts = 3 if method == "GET" else 1
    for i in range(attempts):
        req = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with _open(req, timeout) as resp:
                payload = resp.read()
                if raw:
                    return payload
                return json.loads(payload.decode("utf-8")) if payload else {}
        except (http.client.RemoteDisconnected, ConnectionResetError, http.client.IncompleteRead) as e:
            if i + 1 < attempts:
                time.sleep(1 + i)
                continue
            if method == "GET":
                die("连接 %s 时被中断（%s），稍后重试。" % (server, e.__class__.__name__))
            die("请求发出后连接被中断（%s），服务端可能已经处理成功。\n"
                "先运行 list 或 versions 确认结果，再决定是否重试，避免重复发布。" % e.__class__.__name__)
        except urllib.error.HTTPError as e:
            payload = e.read()
            try:
                err = json.loads(payload.decode("utf-8")).get("error", {})
            except ValueError:
                err = {"code": "http_%d" % e.code, "message": payload.decode("utf-8", "replace")[:300] or e.reason}
            raise APIError(e.code, err.get("code", ""), err.get("message", str(e)), err.get("data"))
        except urllib.error.URLError as e:
            if isinstance(e.reason, (ConnectionResetError, http.client.RemoteDisconnected)) and i + 1 < attempts:
                time.sleep(1 + i)
                continue
            die("无法连接 %s：%s\n检查服务地址是否正确、网络是否可达。" % (server, e.reason))


def call(method, path, body=None, raw=False):
    try:
        return request(method, path, body, raw)
    except APIError as e:
        call_error(e)


# ---------- 文件 ----------

def collect(path):
    """返回 (文件字典, 是否目录)。文件字典的值是 bytes。"""
    path = os.path.abspath(path)
    if os.path.isfile(path):
        with open(path, "rb") as f:
            return {os.path.basename(path): f.read()}, False
    if not os.path.isdir(path):
        die("路径不存在：%s" % path)
    files = {}
    total = 0
    for root, dirs, names in os.walk(path):
        dirs[:] = sorted(d for d in dirs if not d.startswith(".") and d not in SKIP_DIRS)
        for n in sorted(names):
            if n.startswith(".") or n.endswith((".pyc", ".swp", "~")):
                continue
            full = os.path.join(root, n)
            rel = os.path.relpath(full, path).replace(os.sep, "/")
            size = os.path.getsize(full)
            if size > 20 * 1024 * 1024:
                print("跳过过大的文件：%s（%.1f MB）" % (rel, size / 1048576), file=sys.stderr)
                continue
            with open(full, "rb") as f:
                files[rel] = f.read()
            total += size
    if not files:
        die("目录里没有可发布的文件：%s" % path)
    if total > 40 * 1024 * 1024:
        print("提示：总大小 %.1f MB，可能超过服务端限额。" % (total / 1048576), file=sys.stderr)
    return files, True


def encode_files(files):
    out = {}
    for rel, data in files.items():
        ext = os.path.splitext(rel)[1].lower()
        if ext in TEXT_EXT:
            try:
                out[rel] = {"content": data.decode("utf-8"), "encoding": "utf-8"}
                continue
            except UnicodeDecodeError:
                pass
        out[rel] = {"content": base64.b64encode(data).decode("ascii"), "encoding": "base64"}
    return out


def state_key(server, path):
    return server + "|" + os.path.abspath(path)


def print_warnings(ws, limit=None):
    if not ws:
        print("检查结果：没有发现问题。")
        return
    counts = {"error": 0, "warn": 0, "info": 0}
    for w in ws:
        counts[w.get("level", "info")] = counts.get(w.get("level", "info"), 0) + 1
    print("检查结果：%d 个错误，%d 个警告，%d 条建议" % (counts["error"], counts["warn"], counts["info"]))
    shown = ws if limit is None else ws[:limit]
    for w in shown:
        cnt = "（%d 处）" % w["count"] if w.get("count", 0) > 1 else ""
        print("  [%s] %s %s%s：%s" % (LEVEL_ZH.get(w["level"], w["level"]), w.get("file", ""), w["rule"], cnt, w["message"]))
        for ex in w.get("examples") or []:
            print("      · %s" % ex)
    if counts["error"]:
        print("先修正“错误”再重新发布；“警告”逐条判断，确实要改的就改；“建议”按需处理。")


# ---------- 命令 ----------

def cmd_setup(a):
    server = a.server.rstrip("/")
    try:
        me = request("GET", "/api/v1/me", server=server, key=a.key)
    except APIError as e:
        die("验证失败（%s）：%s" % (e.status, e.message))
    save_json(CONFIG_PATH, {"server": server, "api_key": a.key}, private=True)
    print("已保存凭据到 %s（权限 600）。" % CONFIG_PATH)
    show_me(me)


def show_me(me):
    u, q, used = me["user"], me["quota"], me["usage"]
    print("用户：%s（%s）" % (u["name"], "管理员" if u["role"] == "admin" else "普通用户"))
    print("服务：%s，版本 %s" % (me["server"], me.get("server_version", "?")))
    print("用量：%d / %d 个 artifact，存储 %.1f / %.0f MB，单文件上限 %.0f MB" % (
        used["artifacts"], q["artifacts"], used["storage_bytes"] / 1048576, q["storage_bytes"] / 1048576, q["file_bytes"] / 1048576))
    print("默认可见性：%s" % VIS_ZH.get(me.get("default_visibility"), me.get("default_visibility")))
    sv = me.get("skill_version")
    if sv and SKILL_VERSION != sv and not SKILL_VERSION.startswith("{{"):
        print("技能有新版本（本地 %s，服务端 %s）。运行 `python3 %s self-update` 更新。" % (SKILL_VERSION, sv, os.path.abspath(__file__)))


def cmd_whoami(a):
    show_me(call("GET", "/api/v1/me"))


def cmd_check(a):
    files, _ = collect(a.path)
    body = {"files": encode_files(files)}
    if a.entry:
        body["entry"] = a.entry
    res = call("POST", "/api/v1/check", body)
    print("入口文件：%s" % (res.get("entry") or "（未找到）"))
    print_warnings(res.get("warnings", []))


def cmd_publish(a):
    server, _ = config()
    files, is_dir = collect(a.path)
    state = load_json(STATE_PATH, {})
    skey = state_key(server, a.path)
    target = a.id
    base = None
    if not target and not a.new and skey in state:
        target = state[skey]["id"]
        base = state[skey].get("version")
    body = {"files": encode_files(files)}
    for k in ("title", "description", "visibility", "entry"):
        v = getattr(a, k)
        if v:
            body[k] = v
    if a.message:
        body["message"] = a.message
    res = None
    if target:
        body["mode"] = "replace" if is_dir else "merge"
        if base is not None and not a.force and not a.id:
            body["base_version"] = base
        try:
            res = request("PUT", "/api/v1/artifacts/" + urllib.parse.quote(target), body)
        except APIError as e:
            if e.status in (404, 410) and not a.id:
                print("记录中的 artifact %s 已不存在，改为新建。" % target, file=sys.stderr)
                body.pop("mode", None)
                body.pop("base_version", None)
                target = None
            else:
                call_error(e)
    if not target:
        try:
            res = request("POST", "/api/v1/artifacts", body)
        except APIError as e:
            call_error(e)
    state[skey] = {"id": res["id"], "version": res["version"], "updated": int(time.time())}
    save_json(STATE_PATH, state)
    if res.get("unchanged"):
        print("内容没有变化，仍是 v%d：%s" % (res["version"], res["url"]))
    else:
        verb = "已新建" if res.get("created") else "已更新为"
        print("%s v%d：%s" % (verb, res["version"], res["url"]))
    print("标题：%s（%s），入口 %s，%d 个文件" % (res["title"], VIS_ZH.get(res["visibility"], res["visibility"]), res["entry"], res["files"]))
    print_warnings(res.get("warnings", []))
    if a.json:
        print(json.dumps(res, ensure_ascii=False, indent=2))


def call_error(e):
    hint = ""
    if e.status == 409:
        hint = ("\n服务端已有更新的版本（v%s），可能来自别的会话或用户。"
                "先运行 pull 取回最新内容并合并，再发布；确认要覆盖时加 --force。" % e.data.get("current_version", "?"))
    elif e.status == 401:
        hint = "\nAPI Key 无效或已吊销。请用户在管理后台重新签发，再运行 setup。"
    elif e.status == 413:
        hint = "\n可以压缩图片、删掉不用的文件，或请管理员提高限额。"
    elif e.status == 429:
        hint = "\n稍等片刻再试。"
    die("错误（%s %s）：%s%s" % (e.status, e.code, e.message, hint))


def read_arg(value, file_value):
    if file_value:
        with open(file_value, "r", encoding="utf-8") as f:
            return f.read()
    return value


def cmd_edit(a):
    edits = []
    if a.edits:
        src = sys.stdin.read() if a.edits == "-" else open(a.edits, "r", encoding="utf-8").read()
        edits = json.loads(src)
        if isinstance(edits, dict):
            edits = edits.get("edits", [])
    else:
        old = read_arg(a.old, a.old_file)
        new = read_arg(a.new, a.new_file)
        if old is None or new is None:
            die("需要 --old/--new（或 --old-file/--new-file），或用 --edits 提供 JSON。")
        edits = [{"path": a.file or "", "old_str": old, "new_str": new}]
    body = {"edits": edits}
    if a.message:
        body["message"] = a.message
    res = call("POST", "/api/v1/artifacts/%s/edits" % urllib.parse.quote(a.id), body)
    print("已更新为 v%d：%s" % (res["version"], res["url"]))
    print_warnings(res.get("warnings", []))
    sync_state_version(res)


def sync_state_version(res):
    """局部编辑、回滚后，把本地映射里的版本号同步到最新，避免下次 publish 误报冲突。"""
    server, _ = config()
    state = load_json(STATE_PATH, {})
    changed = False
    for k, v in state.items():
        if k.startswith(server + "|") and v.get("id") == res["id"]:
            v["version"] = res["version"]
            changed = True
    if changed:
        save_json(STATE_PATH, state)


def fmt_time(ts):
    return time.strftime("%Y-%m-%d %H:%M", time.localtime(ts))


def cmd_list(a):
    q = {"limit": str(a.limit)}
    if a.query:
        q["q"] = a.query
    if a.deleted:
        q["deleted"] = "1"
    res = call("GET", "/api/v1/artifacts?" + urllib.parse.urlencode(q))
    items = res.get("items", [])
    if not items:
        print("没有找到 artifact。")
        return
    print("共 %d 个，显示 %d 个：" % (res.get("total", len(items)), len(items)))
    for it in items:
        print("  %s  v%-3d %s  %s  %s" % (it["id"], it["current_version"], fmt_time(it["updated_at"]), VIS_ZH.get(it["visibility"], ""), it["title"]))
        print("      %s" % it["url"])


def cmd_info(a):
    res = call("GET", "/api/v1/artifacts/" + urllib.parse.quote(a.id))
    print("%s\n  链接：%s\n  当前版本：v%d，%s，入口 %s，浏览 %d 次" % (
        res["title"], res["url"], res["current_version"], VIS_ZH.get(res["visibility"], ""), res["entry"], res["views"]))
    if res.get("description"):
        print("  描述：%s" % res["description"])
    print("  文件：")
    for f in res.get("files", []):
        print("    %-40s %8d 字节  %s" % (f["path"], f["size"], f["mime"]))


def cmd_versions(a):
    res = call("GET", "/api/v1/artifacts/%s/versions" % urllib.parse.quote(a.id))
    src = {"publish": "发布", "edit": "局部编辑", "restore": "恢复"}
    for v in res.get("versions", []):
        cur = "（当前）" if v["number"] == res["current_version"] else ""
        print("  v%-3d %s  %-4s %s %s" % (v["number"], fmt_time(v["created_at"]), src.get(v["source"], v["source"]), v.get("message") or "", cur))


def cmd_diff(a):
    q = {"format": "unified"}
    if a.to:
        q["to"] = str(a.to)
    if a.frm:
        q["from"] = str(a.frm)
    out = call("GET", "/api/v1/artifacts/%s/diff?%s" % (urllib.parse.quote(a.id), urllib.parse.urlencode(q)), raw=True)
    sys.stdout.write(out.decode("utf-8", "replace"))


def cmd_restore(a):
    res = call("POST", "/api/v1/artifacts/%s/restore" % urllib.parse.quote(a.id), {"version": a.version})
    print("已用 v%d 的内容生成新版本 v%d：%s" % (a.version, res["version"], res["url"]))
    sync_state_version(res)


def cmd_pull(a):
    server, _ = config()
    info = call("GET", "/api/v1/artifacts/" + urllib.parse.quote(a.id))
    ver = a.version or info["current_version"]
    vinfo = call("GET", "/api/v1/artifacts/%s/versions/%d" % (urllib.parse.quote(a.id), ver))
    dest = os.path.abspath(a.dir)
    os.makedirs(dest, exist_ok=True)
    for f in vinfo["files"]:
        target = os.path.normpath(os.path.join(dest, f["path"]))
        if not target.startswith(dest + os.sep):
            die("文件路径越界：%s" % f["path"])
        data = call("GET", "/api/v1/artifacts/%s/versions/%d/files/%s" % (urllib.parse.quote(a.id), ver, urllib.parse.quote(f["path"])), raw=True)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with open(target, "wb") as fh:
            fh.write(data)
    state = load_json(STATE_PATH, {})
    state[state_key(server, dest)] = {"id": a.id, "version": info["current_version"] if not a.version else ver, "updated": int(time.time())}
    save_json(STATE_PATH, state)
    print("已把 v%d 的 %d 个文件取回到 %s。" % (ver, len(vinfo["files"]), dest))
    print("修改后运行 publish %s，会更新同一个 artifact。" % dest)


def cmd_set(a):
    body = {}
    for k in ("title", "description", "visibility"):
        v = getattr(a, k)
        if v is not None:
            body[k] = v
    if not body:
        die("至少提供 --title、--description、--visibility 之一。")
    res = call("PATCH", "/api/v1/artifacts/" + urllib.parse.quote(a.id), body)
    print("已更新：%s（%s）%s" % (res["title"], VIS_ZH.get(res["visibility"], ""), res["url"]))


def cmd_delete(a):
    if not a.yes:
        die("删除需要加 --yes。只在用户明确要求删除时执行。")
    call("DELETE", "/api/v1/artifacts/" + urllib.parse.quote(a.id))
    print("已移入回收站。所有者或管理员可以在后台恢复。")


def cmd_libs(a):
    res = call("GET", "/api/v1/libs")
    for f in res.get("files", []):
        print(f)
    print("\n在页面中直接用 <script src=\"/_lib/...\"> 引用，服务器本地提供，无需外网。")


def cmd_self_update(a):
    server, key = config()
    data = request("GET", "/skill.tar.gz", raw=True, server=server, key=key)
    parent = os.path.dirname(SKILL_DIR)
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as tf:
        for m in tf.getmembers():
            name = os.path.normpath(m.name)
            if name.startswith("..") or os.path.isabs(name) or not (name == "artifact" or name.startswith("artifact" + os.sep)):
                die("技能包内容异常：%s" % m.name)
            if not (m.isfile() or m.isdir()):
                die("技能包含不支持的条目：%s" % m.name)
        if os.path.basename(SKILL_DIR) != "artifact":
            die("技能目录名不是 artifact，无法自动更新：%s" % SKILL_DIR)
        for m in tf.getmembers():
            target = os.path.join(parent, os.path.normpath(m.name))
            if m.isdir():
                os.makedirs(target, exist_ok=True)
                continue
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with tf.extractfile(m) as src, open(target, "wb") as dst:
                dst.write(src.read())
            os.chmod(target, 0o755 if "/scripts/" in m.name else 0o644)
    print("技能已更新到服务端最新版本：%s" % SKILL_DIR)


def main():
    p = argparse.ArgumentParser(prog="artifact.py", description="发布和管理 Artifact。", formatter_class=argparse.RawDescriptionHelpFormatter, epilog=__doc__)
    sub = p.add_subparsers(dest="cmd", metavar="<命令>")

    s = sub.add_parser("setup", help="保存服务地址和 API Key 并验证")
    s.add_argument("--server", required=True)
    s.add_argument("--key", required=True)
    s.set_defaults(fn=cmd_setup)

    s = sub.add_parser("whoami", help="查看身份、配额和技能版本")
    s.set_defaults(fn=cmd_whoami)

    s = sub.add_parser("check", help="发布前检查（不保存）")
    s.add_argument("path")
    s.add_argument("--entry")
    s.set_defaults(fn=cmd_check)

    s = sub.add_parser("publish", help="发布目录或单个文件")
    s.add_argument("path")
    s.add_argument("-m", "--message", help="本次修改说明，会显示在版本历史里")
    s.add_argument("--title")
    s.add_argument("--description", help="一句话描述，显示在列表里")
    s.add_argument("--visibility", choices=["private", "unlisted", "public"])
    s.add_argument("--entry", help="入口文件，默认 index.html")
    s.add_argument("--id", help="更新指定的 artifact")
    s.add_argument("--new", action="store_true", help="强制新建，不更新之前的 artifact")
    s.add_argument("--force", action="store_true", help="忽略版本冲突，直接覆盖")
    s.add_argument("--json", action="store_true", help="同时输出完整 JSON")
    s.set_defaults(fn=cmd_publish)

    s = sub.add_parser("edit", help="对当前版本做字符串替换，生成新版本")
    s.add_argument("id")
    s.add_argument("--file", help="要修改的文件，默认入口文件")
    s.add_argument("--old")
    s.add_argument("--new")
    s.add_argument("--old-file", help="从文件读取 old 文本（适合多行）")
    s.add_argument("--new-file", help="从文件读取 new 文本")
    s.add_argument("--edits", help="JSON 文件（- 表示标准输入），格式 [{\"path\",\"old_str\",\"new_str\"}]")
    s.add_argument("-m", "--message")
    s.set_defaults(fn=cmd_edit)

    s = sub.add_parser("list", help="列出自己的 artifact")
    s.add_argument("-q", "--query")
    s.add_argument("--limit", type=int, default=20)
    s.add_argument("--deleted", action="store_true", help="只看回收站")
    s.set_defaults(fn=cmd_list)

    s = sub.add_parser("info", help="查看 artifact 详情和文件")
    s.add_argument("id")
    s.set_defaults(fn=cmd_info)

    s = sub.add_parser("versions", help="列出版本历史")
    s.add_argument("id")
    s.set_defaults(fn=cmd_versions)

    s = sub.add_parser("diff", help="对比两个版本（默认当前与上一版）")
    s.add_argument("id")
    s.add_argument("--from", dest="frm", type=int)
    s.add_argument("--to", type=int)
    s.set_defaults(fn=cmd_diff)

    s = sub.add_parser("restore", help="用某个旧版本的内容生成新版本")
    s.add_argument("id")
    s.add_argument("version", type=int)
    s.set_defaults(fn=cmd_restore)

    s = sub.add_parser("pull", help="把源码取回到本地目录")
    s.add_argument("id")
    s.add_argument("dir")
    s.add_argument("--version", type=int)
    s.set_defaults(fn=cmd_pull)

    s = sub.add_parser("set", help="修改标题、描述或可见性")
    s.add_argument("id")
    s.add_argument("--title")
    s.add_argument("--description")
    s.add_argument("--visibility", choices=["private", "unlisted", "public"])
    s.set_defaults(fn=cmd_set)

    s = sub.add_parser("delete", help="移入回收站（仅在用户明确要求时）")
    s.add_argument("id")
    s.add_argument("--yes", action="store_true")
    s.set_defaults(fn=cmd_delete)

    s = sub.add_parser("libs", help="列出服务器内置的前端库")
    s.set_defaults(fn=cmd_libs)

    s = sub.add_parser("self-update", help="从服务端更新本技能")
    s.set_defaults(fn=cmd_self_update)

    a = p.parse_args()
    if not getattr(a, "fn", None):
        p.print_help()
        sys.exit(2)
    a.fn(a)


if __name__ == "__main__":
    main()
