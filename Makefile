VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: check build run test vet linux docker clean

# 本地与 CI 运行同一条检查命令
check:
	@test -z "$$(gofmt -l .)" || (echo "以下文件需要 gofmt:"; gofmt -l .; exit 1)
	go vet ./...
	go test -race -count=1 ./...

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
