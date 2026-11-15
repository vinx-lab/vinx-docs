#!/bin/sh
# 对适配器目录跑离线一致性测试（spec 0008 第 10 节）：test/ 里的桩程序放在 PATH 最前面，
# 依次检查 check、publish（首次、重复、更新、公开）、revoke（两次）、不支持的协议和方法。
# 用法：tests/share-adapters/run.sh internal/share/testdata/local-folder
# 只用于外部适配器；内置 Go 适配器的一致性测试在它自己的 go test 里跑（如 internal/share/adapters/gdrive），CI 必过。
set -eu
[ $# -eq 1 ] || { echo "用法：$0 <适配器目录>" >&2; exit 2; }
cd "$(dirname "$0")/../.."
dir=$(cd "$1" && pwd)
exec go run ./cmd/vinx-docs share-adapter conformance "$dir"
