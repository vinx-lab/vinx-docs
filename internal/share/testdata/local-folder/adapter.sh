#!/bin/sh
# 外部适配器示例（协议 v1，spec 0008）：把文件「发布」到本机目录 config.root。框架测试和贡献者参考用。
# 只读便捷环境变量；stdout 只输出事件行。FAIL_ON=<方法> 时模拟一次 network 错误，HANG_ON=<方法> 时卡住（测超时）。
set -u
emit_error() { printf '{"event":"error","code":"%s","message":"%s"}\n' "$1" "$2"; exit 0; }
[ "${VINX_SHARE_PROTOCOL:-}" = 1 ] || emit_error unsupported "不支持的协议版本：${VINX_SHARE_PROTOCOL:-}"
root="${VINX_SHARE_CONFIG_ROOT:-}"
[ -n "$root" ] || emit_error invalid_config "缺少配置 root"
[ "${FAIL_ON:-}" = "$VINX_SHARE_METHOD" ] && emit_error network "模拟的网络错误"
[ "${HANG_ON:-}" = "$VINX_SHARE_METHOD" ] && sleep 30
public=false
[ "${VINX_SHARE_PUBLIC:-0}" = 1 ] && public=true

case "${VINX_SHARE_METHOD:-}" in
check)
  mkdir -p "$root" || emit_error permission_denied "目录不可写"
  printf '{"event":"result","data":{"account":"%s"}}\n' "$root"
  ;;
publish)
  mkdir -p "$root" || emit_error permission_denied "目录不可写"
  # 位置由上次的 remoteId 或幂等键决定：重试覆盖同一个文件。
  id="${VINX_SHARE_REMOTE_ID:-}"
  [ -n "$id" ] || id="$VINX_SHARE_KEY"
  cp "$VINX_SHARE_FILE" "$root/$id" || emit_error internal "复制失败"
  printf '%s' "$VINX_SHARE_NAME" > "$root/$id.name"
  printf '{"event":"result","data":{"url":"https://example.invalid/%s","remoteId":"%s","public":%s}}\n' "$id" "$id" "$public"
  ;;
revoke)
  if [ -f "$root/$VINX_SHARE_REMOTE_ID" ]; then
    rm -f "$root/$VINX_SHARE_REMOTE_ID" "$root/$VINX_SHARE_REMOTE_ID.name"
    gone=false
  else
    gone=true
  fi
  printf '{"event":"result","data":{"alreadyGone":%s}}\n' "$gone"
  ;;
*)
  emit_error unsupported "不支持的方法：${VINX_SHARE_METHOD:-}"
  ;;
esac
