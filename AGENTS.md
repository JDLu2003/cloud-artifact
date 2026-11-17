# AGENTS.md

## 目录

- `cmd/artifactd`：入口与命令行；`cluster.go` 是多机接入（集群节点、文件推送/拉取、写请求转发、R2 备份）。
- `internal/store`：SQLite 访问。**所有写入必须经过 `s.Exec` 或 `s.Tx`**（`replicate.go`），不要直接 `s.DB.Exec`，否则多机时不会同步。SQL 里不要用 `datetime('now')`、`random()` 等非确定性函数，时间和 ID 在 Go 里生成后作为参数传入。
- `internal/blob`：文件存本机 `<artifact_id>/v<版本>/<路径>`；本机缺失时经 fetcher 从其他机器或 R2 取回。
- `internal/artifact`：发布、版本、GC。发布时先写文件、`Replicate` 推送给其他机器，再提交元数据。
- 集群复制逻辑在公开库 `github.com/JDLu2003/vps-cluster`。

## 规则

- 具体配置（地址、密钥、R2 参数）只放环境变量或管理后台设置，不进代码和 CI。
- CI 只做 `make check` 与镜像构建，不做任何数据操作。
- 新的数据库迁移追加到 `migrations` 末尾，旧快照必须能被新代码启动时自动迁移。

## 本地开发

```bash
make check
```

本地多机：每个实例用不同的 `ARTIFACT_DATA_DIR`、`ARTIFACT_PORT`、`CLUSTER_PORT`，共用一个 `CLUSTER_SEED`（`openssl rand -hex 32`），第二台起设置 `CLUSTER_PEERS=127.0.0.1:<第一台集群端口>`，并设置 `NODE_ID`、`PUBLIC_HOST=127.0.0.1`。

## 发布与回滚

合并到 `main` 即发布 `ghcr.io/jdlu2003/cloud-artifact:latest` 与 `:<sha>`。回滚：在 VPS 面板「机器 → artifact 额外配置」里写 `ARTIFACT_TAG=<旧 sha>`（agent 会按面板配置重写各机器的 `.env`，直接改 `.env` 会被覆盖）。
