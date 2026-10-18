# 部署

## 选择构建方式

| 场景 | 做法 |
|---|---|
| 服务器内存 ≥ 2 GB | `docker compose up -d --build`，在容器里编译 |
| 服务器内存 < 2 GB | 在另一台机器 `make linux`，把 `bin/` 一起传上去，`.env` 里设 `ARTIFACT_DOCKERFILE=Dockerfile.prebuilt` |
| 不用 Docker | `make build` 后直接运行 `bin/artifactd serve`，用 systemd 托管 |

编译纯 Go 的 SQLite（modernc.org/sqlite）峰值内存约 1.5–2 GB。在 1 GB 内存、没有 swap 的机器上编译，可能触发 OOM，连带影响同机的其他服务。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ARTIFACT_PUBLIC_URL` | `http://localhost:8790` | 对外地址。链接、技能包、提示词都用它，必须改 |
| `ARTIFACT_PORT` | `8790` | 监听端口（容器内固定 8790） |
| `ARTIFACT_HOST_PORT` | `8790` | compose 映射到宿主机的端口 |
| `ARTIFACT_DATA_DIR` | `./data` | 数据目录（容器内 `/data`） |
| `ARTIFACT_ADMIN_USER` | `admin` | 首次启动创建的管理员 |
| `ARTIFACT_ADMIN_PASSWORD` | 空 | 留空则随机生成，只在首次启动日志里出现 |
| `ARTIFACT_MIN_FREE_DISK_MB` | `300` | 磁盘剩余低于此值时拒绝写入 |
| `ARTIFACT_MAX_BODY_MB` | `64` | 单个请求体上限 |
| `ARTIFACT_TRUST_PROXY` | `false` | 在反向代理后面时设为 `true` |
| `ARTIFACT_CONTENT_ORIGIN` | 空 | artifact 内容使用的独立源（见下文） |
| `ARTIFACT_SECRET` | 空 | 签名密钥；留空则自动生成并存在数据库里 |

## 资源限制

`docker-compose.yml` 默认限制容器内存 256 MB、CPU 1 核，并设置 `GOMEMLIMIT=200MiB`，让 Go 运行时在接近上限前主动回收内存。日志按 10 MB 轮转，最多 3 个文件。

服务本身常驻内存十几 MB。主要的增长来自同时处理的大文件上传，单个请求体上限由 `ARTIFACT_MAX_BODY_MB` 控制。

## 加上 HTTPS

以 Caddy 为例，域名 `artifact.example.com` 已解析到服务器：

```
artifact.example.com {
    reverse_proxy 127.0.0.1:8790
}
```

`.env` 中：

```
ARTIFACT_PUBLIC_URL=https://artifact.example.com
ARTIFACT_TRUST_PROXY=true
ARTIFACT_HOST_PORT=127.0.0.1:8790
```

修改 `ARTIFACT_PUBLIC_URL` 后，已安装技能的 agent 需要重新运行一次 `setup`（或手动修改 `~/.config/artifact/config.json` 里的 `server`）。

nginx 需要额外设置 `client_max_body_size 64m;`，并传递 `X-Forwarded-For` 和 `X-Forwarded-Proto`。

## 独立的内容源（可选）

默认情况下，artifact 内容和后台在同一个源下，依靠 CSP sandbox 隔离。想要更彻底的隔离，可以给内容单独配置一个域名，指向同一个服务：

```
ARTIFACT_CONTENT_ORIGIN=https://usercontent.example.com
```

服务会把内容地址生成到这个域名下，后台和查看页的 iframe 也会相应放行。

## 防火墙

只需要开放 `ARTIFACT_HOST_PORT`。如果用了反向代理，把端口绑定在 `127.0.0.1` 上，只开放 80/443。

## 从旧版本升级

```bash
git pull
docker compose up -d --build
```

数据库迁移在启动时自动执行，升级前建议先备份（见 README）。
