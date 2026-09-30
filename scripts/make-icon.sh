#!/usr/bin/env bash
#
# 由 packaging/icon.svg 生成应用图标 packaging/macos/AppIcon.icns，并同步窗口里用的 icon.svg
#
# 用法：scripts/make-icon.sh（需要 Xcode 命令行工具里的 swiftc）
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
svg="$root/packaging/icon.svg"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# 1、编译渲染工具
swiftc -O "$root/packaging/render-icon.swift" -o "$work/render"

# 2、各尺寸
set_dir="$work/AppIcon.iconset"
mkdir -p "$set_dir"
for s in 16 32 128 256 512; do
  "$work/render" "$svg" "$set_dir/icon_${s}x${s}.png" "$s"
  "$work/render" "$svg" "$set_dir/icon_${s}x${s}@2x.png" "$((s * 2))"
done

# 3、打包成 icns，窗口图标用同一份 SVG
iconutil -c icns "$set_dir" -o "$root/packaging/macos/AppIcon.icns"
cp "$svg" "$root/internal/server/web/icon.svg"
echo "已生成：${root}/packaging/macos/AppIcon.icns"
