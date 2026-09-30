#!/usr/bin/env bash
#
# 打包 macOS 桌面应用 HomeGuard.app（同时支持 Intel 与 Apple 芯片），产物放到 dist/
#
# 用法：scripts/package-mac.sh [--install]
#   --install  打包后安装到「应用程序」文件夹；正在运行的旧守护会先退出（退出时会继续被暂停的程序）
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dist="$root/dist"
app="$dist/HomeGuard.app"
version="1.0.0"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# 1、两种架构分别编译后合并（窗口依赖系统网页引擎，需要开启 cgo）
cd "$root"
for arch in arm64 amd64; do
  cc="clang -arch $([ "$arch" = amd64 ] && echo x86_64 || echo arm64)"
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="$cc" go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$work/homeguard-$arch" ./cmd/homeguard
done

# 2、应用目录结构
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/MacOS/homeguard" "$work/homeguard-arm64" "$work/homeguard-amd64"
sed "s/__VERSION__/$version/g" "$root/packaging/macos/Info.plist" > "$app/Contents/Info.plist"
cp "$root/packaging/macos/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"

# 3、本机签名（未上架分发时系统要求至少有本地签名才能运行）
codesign --force --deep -s - "$app"
echo "已打包：${app}（版本 ${version}）"

# 4、安装：先让旧守护退出，再替换；改名前的 CheckClaude.app 一并移除
if [ "${1:-}" = "--install" ]; then
  if [ -x "/Applications/CheckClaude.app/Contents/MacOS/checkclaude" ]; then
    "/Applications/CheckClaude.app/Contents/MacOS/checkclaude" quit 2>/dev/null || true
    sleep 1
  fi
  pkill -f "/Applications/CheckClaude.app/Contents/MacOS/" 2>/dev/null || true
  rm -rf "/Applications/CheckClaude.app"

  if [ -x "/Applications/HomeGuard.app/Contents/MacOS/homeguard" ]; then
    "/Applications/HomeGuard.app/Contents/MacOS/homeguard" quit 2>/dev/null || true
    sleep 1
  fi
  pkill -f "/Applications/HomeGuard.app/Contents/MacOS/" 2>/dev/null || true
  sleep 1
  rm -rf "/Applications/HomeGuard.app"
  cp -R "$app" "/Applications/HomeGuard.app"
  echo "已安装：/Applications/HomeGuard.app"
fi
