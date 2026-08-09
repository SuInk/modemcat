#!/bin/sh
# ModemCat 一键安装脚本
#
#   curl -fsSL https://raw.githubusercontent.com/SuInk/modemcat/main/scripts/install.sh | sh
#
# 装指定版本：
#   curl -fsSL .../install.sh | MODEMCAT_VERSION=v0.1.0 sh
#
# 脚本会从 GitHub Release 下载发行包、**校验 SHA-256**、解压后调用包内的 install。
# 校验失败即中止：以 curl | sh 方式运行的脚本没有第二道防线，这一步不能跳过。
set -eu

REPO=${MODEMCAT_REPO:-SuInk/modemcat}
VERSION=${MODEMCAT_VERSION:-latest}
WORK_DIR=""

log() { printf '%s\n' "$*"; }
die() { printf 'ModemCat 安装失败：%s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "${WORK_DIR}" ] && [ -d "${WORK_DIR}" ] && rm -rf "${WORK_DIR}"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- 环境检查

[ "$(uname -s)" = "Darwin" ] || die "当前只提供 macOS 版本。"
[ "$(uname -m)" = "arm64" ] || die "当前发行包仅支持 Apple Silicon（M 系列芯片）。"

for tool in curl unzip shasum; do
  command -v "${tool}" >/dev/null 2>&1 || die "缺少命令 ${tool}。"
done

# ---------------------------------------------------------------- 解析版本

if [ "${VERSION}" = "latest" ]; then
  log "正在查询最新版本…"
  API_URL="https://api.github.com/repos/${REPO}/releases/latest"
  VERSION=$(curl -fsSL "${API_URL}" 2>/dev/null |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1) ||
    die "查询最新版本失败，请检查网络，或用 MODEMCAT_VERSION 指定版本。"
  [ -n "${VERSION}" ] || die "没有找到任何已发布版本。可用 MODEMCAT_VERSION 指定。"
fi
log "准备安装 ${VERSION}"

# ---------------------------------------------------------------- 下载

BASE="https://github.com/${REPO}/releases/download/${VERSION}"
# 名称必须与 scripts/package-macos-arm64.sh 的产物一致，否则每次发布都 404
ARCHIVE="ModemCat-macOS-arm64-${VERSION}.zip"
WORK_DIR=$(mktemp -d) || die "无法创建临时目录。"

log "正在下载 ${ARCHIVE}…"
curl -fL --progress-bar "${BASE}/${ARCHIVE}" -o "${WORK_DIR}/${ARCHIVE}" ||
  die "下载失败。确认 ${VERSION} 存在且包含 macOS-arm64 发行包。"

# ---------------------------------------------------------------- 校验

log "正在校验 SHA-256…"
if curl -fsSL "${BASE}/${ARCHIVE}.sha256" -o "${WORK_DIR}/${ARCHIVE}.sha256" 2>/dev/null; then
  EXPECTED=$(awk '{print $1}' "${WORK_DIR}/${ARCHIVE}.sha256" | head -n 1)
  ACTUAL=$(shasum -a 256 "${WORK_DIR}/${ARCHIVE}" | awk '{print $1}')
  [ -n "${EXPECTED}" ] || die "校验文件为空，拒绝安装。"
  if [ "${EXPECTED}" != "${ACTUAL}" ]; then
    printf '期望 %s\n实际 %s\n' "${EXPECTED}" "${ACTUAL}" >&2
    die "SHA-256 不匹配，安装已中止。请勿使用该文件。"
  fi
  log "校验通过。"
else
  # 没有校验文件就无法确认下载内容，宁可停下也不装一个来路不明的二进制。
  die "该版本没有提供 .sha256 校验文件，无法验证下载内容，安装已中止。
如确认要继续，请手动下载并核对后运行包内的 ./install。"
fi

# ---------------------------------------------------------------- 安装

log "正在解压…"
unzip -q "${WORK_DIR}/${ARCHIVE}" -d "${WORK_DIR}/unpacked" || die "解压失败。"

PACKAGE_DIR=$(find "${WORK_DIR}/unpacked" -maxdepth 2 -name install -type f -print -quit 2>/dev/null | xargs -I {} dirname {})
[ -n "${PACKAGE_DIR}" ] && [ -f "${PACKAGE_DIR}/install" ] || die "发行包结构异常，未找到 install。"

# 从浏览器之外下载的文件同样会被打上隔离属性，装完首次运行会被 Gatekeeper 拦下。
xattr -dr com.apple.quarantine "${PACKAGE_DIR}" 2>/dev/null || true
chmod +x "${PACKAGE_DIR}/install" 2>/dev/null || true

log ""
log "接下来安装到 /usr/local，macOS 可能要求输入管理员密码。"
log ""
sh "${PACKAGE_DIR}/install"

log ""
log "安装完成。启动："
log "  modemcat start"
