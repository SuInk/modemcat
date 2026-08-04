#!/bin/sh
set -eu

PUBLIC_HOST=${1:-}
TUNNEL_NAME=${2:-djsmsforward}
CLOUDFLARED=$(command -v cloudflared || true)
CLOUDFLARED_DIR="${HOME}/.cloudflared"
CONFIG_FILE="${CLOUDFLARED_DIR}/djsmsforward.yml"
CERT_FILE="${CLOUDFLARED_DIR}/cert.pem"
APP_COMMAND=$(command -v djsmsforward || true)
LAUNCH_AGENT_LABEL="com.djsmsforward.cloudflared"
LAUNCH_AGENT="${HOME}/Library/LaunchAgents/${LAUNCH_AGENT_LABEL}.plist"
LOG_DIR="${HOME}/Library/Logs/DJSMSForward"

if [ -z "${PUBLIC_HOST}" ]; then
  echo "用法：$0 sms.example.com [tunnel-name]" >&2
  exit 2
fi
case "${PUBLIC_HOST}" in
  *[!A-Za-z0-9.-]*|.*|*..*|*.) echo "请提供不含协议和路径的完整 DNS hostname。" >&2; exit 2 ;;
esac
case "${PUBLIC_HOST}" in
  *.*) ;;
  *) echo "hostname 必须包含域名，例如 sms.example.com。" >&2; exit 2 ;;
esac
case "${TUNNEL_NAME}" in
  ""|*[!A-Za-z0-9_-]*) echo "Tunnel 名称只能包含字母、数字、下划线和连字符。" >&2; exit 2 ;;
esac
if [ -z "${CLOUDFLARED}" ]; then
  echo "未找到 cloudflared，请先执行 brew install cloudflared。" >&2
  exit 1
fi
if [ -z "${APP_COMMAND}" ]; then
  echo "未找到 djsmsforward 命令，请先安装 DJSMSForward。" >&2
  exit 1
fi

mkdir -p "${CLOUDFLARED_DIR}"
chmod 700 "${CLOUDFLARED_DIR}"
if [ ! -f "${CERT_FILE}" ]; then
  echo "即将打开 Cloudflare 授权页面。"
  "${CLOUDFLARED}" tunnel login
fi

CERTIFICATE_JSON=$(awk '
  /BEGIN ARGO TUNNEL TOKEN/ { capture = 1; next }
  /END ARGO TUNNEL TOKEN/ { capture = 0 }
  capture { printf "%s", $0 }
' "${CERT_FILE}" | base64 -D)
CERTIFICATE_ZONE_ID=$(printf '%s' "${CERTIFICATE_JSON}" | plutil -extract zoneID raw -o - - 2>/dev/null || true)
CERTIFICATE_API_TOKEN=$(printf '%s' "${CERTIFICATE_JSON}" | plutil -extract apiToken raw -o - - 2>/dev/null || true)
if [ -z "${CERTIFICATE_ZONE_ID}" ] || [ -z "${CERTIFICATE_API_TOKEN}" ]; then
  echo "无法读取 Cloudflare 授权证书的区域信息，请重新执行 cloudflared tunnel login。" >&2
  exit 1
fi
ZONE_JSON=$(curl -fsS \
  -H "Authorization: Bearer ${CERTIFICATE_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  "https://api.cloudflare.com/client/v4/zones/${CERTIFICATE_ZONE_ID}")
ZONE_NAME=$(printf '%s' "${ZONE_JSON}" | plutil -extract result.name raw -o - - 2>/dev/null || true)
if [ -z "${ZONE_NAME}" ]; then
  echo "无法确认 Cloudflare 授权区域，请重新登录后再试。" >&2
  exit 1
fi
case "${PUBLIC_HOST}" in
  "${ZONE_NAME}"|*."${ZONE_NAME}") ;;
  *)
    echo "当前 Cloudflare 证书授权的是 ${ZONE_NAME}，不能创建 ${PUBLIC_HOST}。" >&2
    echo "请移走 ~/.cloudflared/cert.pem，重新登录并选择正确区域。" >&2
    exit 1
    ;;
esac

TUNNEL_JSON=$("${CLOUDFLARED}" tunnel list --name "${TUNNEL_NAME}" --output json)
TUNNEL_ID=$(printf '%s' "${TUNNEL_JSON}" | plutil -extract 0.id raw -o - - 2>/dev/null || true)
if [ -z "${TUNNEL_ID}" ]; then
  "${CLOUDFLARED}" tunnel create "${TUNNEL_NAME}"
  TUNNEL_JSON=$("${CLOUDFLARED}" tunnel list --name "${TUNNEL_NAME}" --output json)
  TUNNEL_ID=$(printf '%s' "${TUNNEL_JSON}" | plutil -extract 0.id raw -o - -)
fi

CREDENTIALS_FILE="${CLOUDFLARED_DIR}/${TUNNEL_ID}.json"
if [ ! -f "${CREDENTIALS_FILE}" ]; then
  echo "缺少 Tunnel 凭据文件：${CREDENTIALS_FILE}" >&2
  exit 1
fi
if [ -f "${CONFIG_FILE}" ] && ! grep -q '^# Managed by DJSMSForward$' "${CONFIG_FILE}"; then
  echo "已有非 DJSMSForward 管理的 ${CONFIG_FILE}，为避免覆盖已停止。" >&2
  exit 1
fi

umask 077
TEMPORARY_CONFIG="${CONFIG_FILE}.tmp.$$"
{
  echo "# Managed by DJSMSForward"
  echo "tunnel: ${TUNNEL_ID}"
  echo "credentials-file: ${CREDENTIALS_FILE}"
  echo "ingress:"
  echo "  - hostname: ${PUBLIC_HOST}"
  echo "    service: http://127.0.0.1:7575"
  echo "  - service: http_status:404"
} >"${TEMPORARY_CONFIG}"
mv -f "${TEMPORARY_CONFIG}" "${CONFIG_FILE}"

"${CLOUDFLARED}" tunnel --config "${CONFIG_FILE}" ingress validate
"${CLOUDFLARED}" tunnel route dns "${TUNNEL_ID}" "${PUBLIC_HOST}"
"${APP_COMMAND}" cloudflare "https://${PUBLIC_HOST}"

mkdir -p "${HOME}/Library/LaunchAgents" "${LOG_DIR}"
TEMPORARY_AGENT="${LAUNCH_AGENT}.tmp.$$"
plutil -create xml1 "${TEMPORARY_AGENT}"
plutil -insert Label -string "${LAUNCH_AGENT_LABEL}" "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments -xml '<array/>' "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.0 -string "${CLOUDFLARED}" "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.1 -string tunnel "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.2 -string --config "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.3 -string "${CONFIG_FILE}" "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.4 -string run "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.5 -string --dns-resolver-addrs "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.6 -string 1.1.1.1:53 "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.7 -string --protocol "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.8 -string http2 "${TEMPORARY_AGENT}"
plutil -insert ProgramArguments.9 -string "${TUNNEL_ID}" "${TEMPORARY_AGENT}"
plutil -insert RunAtLoad -bool true "${TEMPORARY_AGENT}"
plutil -insert KeepAlive -bool true "${TEMPORARY_AGENT}"
plutil -insert ProcessType -string Background "${TEMPORARY_AGENT}"
plutil -insert StandardOutPath -string "${LOG_DIR}/cloudflared.log" "${TEMPORARY_AGENT}"
plutil -insert StandardErrorPath -string "${LOG_DIR}/cloudflared.log" "${TEMPORARY_AGENT}"
mv -f "${TEMPORARY_AGENT}" "${LAUNCH_AGENT}"
chmod 600 "${LAUNCH_AGENT}"

launchctl bootout "gui/$(id -u)/${LAUNCH_AGENT_LABEL}" >/dev/null 2>&1 || true
launchctl bootstrap "gui/$(id -u)" "${LAUNCH_AGENT}"
launchctl kickstart -k "gui/$(id -u)/${LAUNCH_AGENT_LABEL}"
"${APP_COMMAND}" service install

echo
echo "Cloudflare Tunnel 已配置：https://${PUBLIC_HOST}"
echo "DJSMSForward 与 Tunnel 均已设置为登录后自动运行。"
echo "建议在 Cloudflare Access 中仅允许你的邮箱访问该 hostname。"
