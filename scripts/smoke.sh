#!/usr/bin/env bash
# 对一个运行中的 Artifact 服务做冒烟测试。
# 用法：scripts/smoke.sh <服务地址> <API Key>
# 会新建一个测试 artifact，走一遍发布、更新、局部编辑、冲突、对比、回滚、可见性和删除，最后移入回收站。
set -euo pipefail
S=${1:?服务地址}; K=${2:?API Key}
C=(curl -sS --noproxy '*' -H "Authorization: Bearer $K" -H 'Content-Type: application/json')
pass=0; fail=0
check() { if [ "$2" = "$3" ]; then echo "通过  $1"; pass=$((pass+1)); else echo "失败  $1：期望 $3，实际 $2"; fail=$((fail+1)); fi; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$1"; }

check "健康检查" "$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "$S/healthz")" 200
check "无 Key 被拒绝" "$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "$S/api/v1/me")" 401

R=$("${C[@]}" -X POST "$S/api/v1/artifacts" -d '{"message":"冒烟测试","files":{"index.html":"<title>冒烟测试</title><p>第一版</p>"}}')
ID=$(echo "$R" | json 'd["id"]')
check "新建" "$(echo "$R" | json 'd["version"]')" 1

R=$("${C[@]}" -X PUT "$S/api/v1/artifacts/$ID" -d '{"base_version":1,"files":{"data.json":"{\"a\":1}"}}')
check "合并更新" "$(echo "$R" | json 'd["files"]')" 2

check "版本冲突" "$("${C[@]}" -o /dev/null -w '%{http_code}' -X PUT "$S/api/v1/artifacts/$ID" -d '{"base_version":1,"files":{"x.txt":"x"}}')" 409

R=$("${C[@]}" -X POST "$S/api/v1/artifacts/$ID/edits" -d '{"edits":[{"old_str":"第一版","new_str":"第三版"}]}')
check "局部编辑" "$(echo "$R" | json 'd["version"]')" 3

check "编辑未命中" "$("${C[@]}" -o /dev/null -w '%{http_code}' -X POST "$S/api/v1/artifacts/$ID/edits" -d '{"edits":[{"old_str":"不存在","new_str":"x"}]}')" 422

D=$("${C[@]}" "$S/api/v1/artifacts/$ID/diff?from=1&to=3")
check "对比" "$(echo "$D" | json 'd["changed_files"]')" 2

R=$("${C[@]}" -X POST "$S/api/v1/artifacts/$ID/restore" -d '{"version":1}')
check "回滚生成新版本" "$(echo "$R" | json 'd["version"]')" 4

H=$(curl -sS --noproxy '*' -D - -o /dev/null "$S/c/$ID/v4/-/")
check "内容在沙箱中" "$(echo "$H" | grep -ci '^content-security-policy: sandbox allow-scripts')" 1
check "旧版本需签名" "$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "$S/c/$ID/v1/-/index.html")" 403

"${C[@]}" -o /dev/null -X PATCH "$S/api/v1/artifacts/$ID" -d '{"visibility":"private"}'
check "私有后匿名不可见" "$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "$S/a/$ID")" 404

check "移入回收站" "$("${C[@]}" -o /dev/null -w '%{http_code}' -X DELETE "$S/api/v1/artifacts/$ID")" 200
check "技能包可下载" "$(curl -s --noproxy '*' "$S/skill.tar.gz" | tar -tz | grep -c '^artifact/SKILL.md$')" 1

echo "共 $((pass+fail)) 项，通过 $pass 项，失败 $fail 项。测试 artifact：$ID（已在回收站）"
[ "$fail" -eq 0 ]
