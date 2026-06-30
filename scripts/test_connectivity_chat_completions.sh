#!/usr/bin/env sh
set -eu

TARGET_URL="${1:-http://10.223.8.44:8000/v1/chat/completions}"
CONNECT_TIMEOUT="${CONNECT_TIMEOUT:-5}"
MAX_TIME="${MAX_TIME:-30}"
MODEL="${MODEL:-gpt-4o-mini}"
AUTH_TOKEN="${AUTH_TOKEN:-}"

TMP_BODY="$(mktemp)"
TMP_HEADERS="$(mktemp)"
cleanup() {
  rm -f "$TMP_BODY" "$TMP_HEADERS"
}
trap cleanup EXIT

PAYLOAD=$(cat <<EOF
{
  "model": "$MODEL",
  "messages": [
    {
      "role": "user",
      "content": "ping"
    }
  ],
  "stream": false,
  "max_tokens": 8
}
EOF
)

HEADER_ARGS=(
  -H "Content-Type: application/json"
)

if [[ -n "$AUTH_TOKEN" ]]; then
  HEADER_ARGS+=( -H "Authorization: Bearer $AUTH_TOKEN" )
fi

echo "== DNS / TCP / HTTP 连通性测试 =="
echo "目标地址: $TARGET_URL"
echo "connect-timeout: ${CONNECT_TIMEOUT}s"
echo "max-time: ${MAX_TIME}s"
echo "model: $MODEL"

echo "\n[1/2] 测试 TCP 端口连通性..."
python3 - "$TARGET_URL" <<'PY'
import socket
import sys
from urllib.parse import urlparse

url = urlparse(sys.argv[1])
host = url.hostname
port = url.port or (443 if url.scheme == 'https' else 80)
print(f"host={host} port={port}")
try:
    with socket.create_connection((host, port), timeout=5):
        print("TCP connect: OK")
except Exception as e:
    print(f"TCP connect: FAIL - {e}")
    sys.exit(2)
PY

echo "\n[2/2] 测试 HTTP 请求..."
HTTP_CODE=$(curl -sS -o "$TMP_BODY" -D "$TMP_HEADERS" \
  --connect-timeout "$CONNECT_TIMEOUT" \
  --max-time "$MAX_TIME" \
  -w '%{http_code}' \
  -X POST "$TARGET_URL" \
  "${HEADER_ARGS[@]}" \
  --data "$PAYLOAD" || true)

echo "HTTP status: $HTTP_CODE"
echo "\n--- Response Headers ---"
cat "$TMP_HEADERS"
echo "--- Response Body ---"
cat "$TMP_BODY"
echo

echo "\n[诊断提示]"
case "$HTTP_CODE" in
  200)
    echo "请求已成功到达并获得正常响应。"
    ;;
  000)
    echo "curl 未收到 HTTP 响应，优先排查网络、端口、防火墙、超时或 TLS。"
    ;;
  401|403)
    echo "网络基本连通，但鉴权失败。请检查 Authorization Token。"
    ;;
  404)
    echo "网络连通，但接口路径可能不正确。"
    ;;
  405)
    echo "网络连通，但请求方法不被允许。"
    ;;
  408|504)
    echo "网络可达但请求超时，重点排查上游处理耗时、网关超时和首包超时。"
    ;;
  5*)
    echo "网络已到达目标服务，但服务端发生 5xx。"
    ;;
  *)
    echo "请结合响应头和响应体进一步判断。"
    ;;
esac
