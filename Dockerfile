# syntax=docker/dockerfile:1
# 构建阶段：纯 Go 编译，无 CGO
FROM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=0.1.0
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags "-s -w -X main.Version=${VERSION}" -o /out/artifactd ./cmd/artifactd

# 运行阶段：约 30 MB
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata su-exec \
 && adduser -D -H -u 10001 artifact \
 && mkdir -p /data && chown artifact:artifact /data
COPY --from=build /out/artifactd /usr/local/bin/artifactd
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
ENV ARTIFACT_DATA_DIR=/data ARTIFACT_PORT=8790 TZ=Asia/Shanghai
EXPOSE 8790
VOLUME /data
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8790/healthz >/dev/null || exit 1
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["serve"]
