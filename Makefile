VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: check build run test vet linux docker clean worker-test worker-deploy

# 本地与 CI 运行同一条检查命令
check:
	@test -z "$$(gofmt -l .)" || (echo "以下文件需要 gofmt:"; gofmt -l .; exit 1)
	go vet ./...
	go test -race -count=1 ./...
	@if command -v node >/dev/null; then $(MAKE) --no-print-directory worker-test; else echo "未安装 node，跳过 Worker 测试"; fi

worker-test:
	node --test worker/test/*.test.mjs

# 部署内容 Worker（运维操作，不在 CI 里执行）。参数都从命令行或环境变量传入，不写进仓库：
#   make worker-deploy DOMAIN=content.example.com BUCKET=<R2 桶名> [PREFIX=<与 ARTIFACT_S3_PREFIX 相同>]
# 首次部署后设置签名密钥（在任一运行 artifact 的机器上取得）：
#   docker exec artifact artifactd content-key | npx wrangler secret put CONTENT_KEY -c worker/wrangler.generated.toml
worker-deploy:
	@test -n "$(DOMAIN)" && test -n "$(BUCKET)" || (echo "用法：make worker-deploy DOMAIN=content.example.com BUCKET=桶名 [PREFIX=前缀]"; exit 1)
	@printf '%s\n' \
	  'name = "artifact-content"' \
	  'main = "src/index.js"' \
	  'compatibility_date = "2026-10-01"' \
	  'workers_dev = false' \
	  'routes = [{ pattern = "$(DOMAIN)", custom_domain = true }]' \
	  '[vars]' \
	  'PREFIX = "$(PREFIX)"' \
	  '[[r2_buckets]]' \
	  'binding = "BUCKET"' \
	  'bucket_name = "$(BUCKET)"' > worker/wrangler.generated.toml
	cd worker && npx --yes wrangler@4 deploy -c wrangler.generated.toml

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/artifactd ./cmd/artifactd

run: build
	ARTIFACT_DATA_DIR=./data ./bin/artifactd serve

test:
	go test ./...

vet:
	go vet ./...

# 交叉编译 Linux 可执行文件（服务器上没有 Go 时使用）
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/artifactd-linux-amd64 ./cmd/artifactd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/artifactd-linux-arm64 ./cmd/artifactd

docker:
	docker compose build

clean:
	rm -rf bin
