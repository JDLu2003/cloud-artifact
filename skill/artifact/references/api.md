# REST API

没有 Python 或需要在别的程序里调用时读。`scripts/artifact.py` 只是这些接口的封装。

## 目录

1. 鉴权与错误
2. 发布与更新
3. 局部编辑
4. 查询、版本、对比、回滚
5. 其他

服务地址：`{{SERVER_URL}}`。下文用 `$S` 表示服务地址，`$K` 表示 API Key。

## 1. 鉴权与错误

所有 `/api/v1/` 接口都需要请求头：

```
Authorization: Bearer art_xxxxxxxx
```

出错时返回非 2xx 状态码和：

```json
{"error": {"code": "conflict", "message": "版本冲突：……", "data": {"current_version": 5}}}
```

| 状态码 | code | 含义 |
|---|---|---|
| 400 | bad_request、bad_path、no_entry … | 参数有误，看 message |
| 401 | unauthorized | 没带 Key、Key 无效或已吊销 |
| 403 | quota_artifacts、user_disabled | 超出数量限额或用户被停用 |
| 404 | not_found | 不存在，或不属于你 |
| 409 | conflict | `base_version` 与服务端当前版本不一致 |
| 410 | deleted | artifact 在回收站里 |
| 413 | file_too_large、version_too_large、quota_storage | 超出大小限额 |
| 415 | bad_type | 不支持的文件类型 |
| 422 | edit_failed | 局部编辑的 old_str 没找到或不唯一 |
| 429 | rate_limited | 请求太频繁，看 `Retry-After` 头 |
| 507 | disk_full | 服务器磁盘不足 |

## 2. 发布与更新

### 新建：`POST /api/v1/artifacts`

```bash
curl -sS -X POST "$S/api/v1/artifacts" -H "Authorization: Bearer $K" \
  -H 'Content-Type: application/json' --data @- <<'JSON'
{
  "title": "Redis 缓存迁移方案",
  "description": "把会话缓存从单机迁到集群的步骤与回滚预案",
  "visibility": "unlisted",
  "message": "初版",
  "files": {
    "index.html": "<title>Redis 缓存迁移方案</title>…",
    "img/arch.png": {"content": "<base64>", "encoding": "base64"}
  }
}
JSON
```

- `files` 的值可以是字符串（UTF-8 文本），也可以是 `{"content", "encoding"}`，`encoding` 为 `utf-8` 或 `base64`。
- `title` 省略时取页面的 `<title>`（Markdown 取 front matter 或第一个一级标题）。
- `visibility`：`private` 私有、`unlisted` 持链接可看（默认）、`public` 公开。
- `entry` 省略时自动找 `index.html` 等。

也可以用 multipart 上传，字段名 `meta` 放上面的 JSON（不含 files），文件字段名写成 `file:<路径>`：

```bash
curl -sS -X POST "$S/api/v1/artifacts" -H "Authorization: Bearer $K" \
  -F 'meta={"message":"初版"}' -F 'file:index.html=@index.html' -F 'file:img/arch.png=@img/arch.png'
```

返回（201）：

```json
{"id": "Xk3fP9aQ2m", "version": 1, "url": "$S/a/Xk3fP9aQ2m", "title": "…", "visibility": "unlisted",
 "entry": "index.html", "kind": "html", "files": 2, "size": 48211, "created": true, "unchanged": false,
 "warnings": [{"level": "warn", "rule": "zh-spacing", "file": "index.html", "message": "…", "count": 3, "examples": ["…"]}]}
```

### 更新：`PUT /api/v1/artifacts/{id}`

请求体同上，另有：

- `mode`：`merge`（默认）只增加或替换传入的文件，未传的保留；`replace` 以本次传入的文件为全部内容。
- 删除某个文件：在 `files` 里把它的值设为 `null`（merge 模式）。
- `base_version`：你修改所基于的版本号。服务端当前版本不同时返回 409，防止覆盖别人的修改。

内容与当前版本完全相同时不生成新版本，返回 `"unchanged": true`。

### 只改元数据：`PATCH /api/v1/artifacts/{id}`

```json
{"title": "新标题", "description": "…", "visibility": "private"}
```

不生成新版本。

## 3. 局部编辑：`POST /api/v1/artifacts/{id}/edits`

```json
{
  "base_version": 3,
  "message": "修正第二节的数字",
  "edits": [
    {"path": "index.html", "old_str": "延迟降低了 60%", "new_str": "延迟降低了 58%"},
    {"old_str": "<h2>背景</h2>", "new_str": "<h2>为什么要迁移</h2>"}
  ]
}
```

- `path` 省略时为入口文件。
- 每个 `old_str` 必须在当前内容中**恰好出现一次**，包括空格和换行；按顺序执行，全部成功才生成新版本。
- 失败返回 422，`data.index` 是出错的那一条。

## 4. 查询、版本、对比、回滚

| 方法与路径 | 作用 |
|---|---|
| `GET /api/v1/me` | 身份、配额、用量、技能版本、CDN 白名单 |
| `GET /api/v1/artifacts?q=关键词&limit=20&offset=0` | 列出自己的 artifact，`q` 全文检索；`deleted=1` 看回收站 |
| `GET /api/v1/artifacts/{id}` | 详情、当前版本文件清单、最近 20 个版本 |
| `GET /api/v1/artifacts/{id}/versions` | 全部版本 |
| `GET /api/v1/artifacts/{id}/versions/{n}` | 某个版本的信息与文件清单（`n` 可写 `latest`） |
| `GET /api/v1/artifacts/{id}/versions/{n}/files/{path}` | 下载某个版本的某个文件原文 |
| `GET /api/v1/artifacts/{id}/diff?from=2&to=5` | 对比，返回 JSON；加 `format=unified` 返回纯文本 diff。省略时对比当前版本与上一版 |
| `POST /api/v1/artifacts/{id}/restore` `{"version": 2}` | 以 v2 的内容生成新版本 |
| `DELETE /api/v1/artifacts/{id}` | 移入回收站 |

## 5. 其他

- `POST /api/v1/check`：请求体同发布，只做检查不保存，返回 `{"entry", "warnings", "summary"}`。
- `GET /api/v1/libs`：内置前端库清单。
- `GET /skill.tar.gz`：最新技能包（无需鉴权）。
- 查看页：`$S/a/{id}`；不带外框的页面：`$S/a/{id}/raw`。所有者登录后台后还能看到 `/a/{id}/history`、`/a/{id}/diff`、`/a/{id}/source`。
