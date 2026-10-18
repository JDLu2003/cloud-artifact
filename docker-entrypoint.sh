#!/bin/sh
# 以 root 启动时修正数据目录权限，然后降权到 artifact 用户运行
set -e
if [ "$(id -u)" = "0" ]; then
  mkdir -p "${ARTIFACT_DATA_DIR:-/data}"
  if [ "$(stat -c %u "${ARTIFACT_DATA_DIR:-/data}")" != "10001" ]; then
    chown -R 10001:10001 "${ARTIFACT_DATA_DIR:-/data}"
  fi
  exec su-exec artifact artifactd "$@"
fi
exec artifactd "$@"
