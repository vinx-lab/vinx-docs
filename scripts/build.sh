#!/usr/bin/env bash
# 编译 vinx-docs 静态二进制到 dist/。
#
#   scripts/build.sh          编译本机平台：dist/vinx-docs（Windows 上是 vinx-docs.exe）
#   scripts/build.sh all      交叉编译发布用的全部平台：dist/vinx-docs-<os>-<arch>[.exe]
#
# 版本号默认取 internal/cli 里的 Version，可用环境变量 VERSION 覆盖（例如 VERSION=0.2.0-rc1）。
# 纯 Go 编译（CGO_ENABLED=0），不依赖系统 C 库；-trimpath 去掉本机路径，-s -w 去掉调试信息。
set -euo pipefail

cd "$(dirname "$0")/.."

PLATFORMS=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64)
VERSION_VAR=github.com/vinx-lab/vinx-docs/internal/cli.Version

if [[ -z "${VERSION:-}" ]]; then
  VERSION=$(sed -n 's/^var Version = "\(.*\)"$/\1/p' internal/cli/cli.go)
fi
if [[ -z "$VERSION" ]]; then
  echo "读不到 internal/cli/cli.go 里的 Version" >&2
  exit 1
fi

LDFLAGS="-s -w -X ${VERSION_VAR}=${VERSION}"
mkdir -p dist

build() { # build <goos> <goarch> <输出文件>
  echo "编译 $1/$2 -> $3"
  CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath -ldflags "$LDFLAGS" -o "$3" ./cmd/vinx-docs
}

case "${1:-}" in
  "")
    goos=$(go env GOOS)
    goarch=$(go env GOARCH)
    ext=""
    [[ "$goos" == windows ]] && ext=".exe"
    build "$goos" "$goarch" "dist/vinx-docs${ext}"
    ;;
  all)
    for platform in "${PLATFORMS[@]}"; do
      goos=${platform%/*}
      goarch=${platform#*/}
      ext=""
      [[ "$goos" == windows ]] && ext=".exe"
      build "$goos" "$goarch" "dist/vinx-docs-${goos}-${goarch}${ext}"
    done
    ;;
  *)
    echo "用法：scripts/build.sh [all]" >&2
    exit 2
    ;;
esac

echo "版本 ${VERSION}，输出："
ls -l dist/
