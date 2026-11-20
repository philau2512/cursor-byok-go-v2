#!/bin/bash
# 一键安装脚本：用于在 Windows 上交叉编译后拷贝到 Mac 的应用包。
# 依次执行：补执行权限 -> 去除隔离属性 -> ad-hoc 签名（Apple Silicon 必须）-> 安装并启动。
set -e
cd "$(dirname "$0")"

APP="$(find . -maxdepth 1 -type d -name '*.app' | head -n 1)"
if [ -z "$APP" ]; then
  echo "错误：当前目录下未找到 .app 应用包，请确认已完整解压。" >&2
  read -n 1 -r -s -p "按任意键退出..."
  exit 1
fi

echo "正在安装: $APP"
chmod +x "$APP/Contents/MacOS/"*
xattr -cr "$APP" 2>/dev/null || true
codesign --force --deep --sign - "$APP"

if [ -e "/Applications/$APP" ]; then
  rm -rf "/Applications/$APP"
fi
if ! mv "$APP" /Applications/ 2>/dev/null; then
  mkdir -p "$HOME/Applications"
  if [ -e "$HOME/Applications/$APP" ]; then
    rm -rf "$HOME/Applications/$APP"
  fi
  mv "$APP" "$HOME/Applications/"
  DEST="$HOME/Applications/$APP"
else
  DEST="/Applications/$APP"
fi

open "$DEST"
echo "完成：已安装并启动 $DEST"
read -n 1 -r -s -p "按任意键退出..."
