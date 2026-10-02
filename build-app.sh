#!/usr/bin/env bash
#
# 把图形界面组装成一个可双击运行的 macOS .app。
#
# 生成的 bundle 只在本机使用，用临时签名（ad-hoc）即可，不需要开发者证书。
# 也不用安装任何额外工具：只要有 Go 和 Xcode 命令行工具（sips、iconutil、codesign
# 都是系统自带的）。
set -euo pipefail

# 显式设定 PATH，不读用户 shell profile：本机 profile 有问题，
# 非交互执行时会把整个脚本带崩。
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

cd "$(dirname "$0")"
MODULE_DIR="$(pwd)"
BUILD_DIR="$MODULE_DIR/build"
APP="$BUILD_DIR/Pier.app"
BIN_NAME="pier-gui"

# 默认出通用二进制（arm64 + x86_64），Intel 机器与 Apple 芯片共用一个 .app。
# 调试时设 PIER_ARCH=arm64 只编本机那一份，省掉一半编译时间。
PIER_ARCH="${PIER_ARCH:-universal}"

# 版本号写进 Info.plist。package.sh 会用 PIER_VERSION 覆盖它；
# 直接跑这个脚本时用下面这个默认值。
VERSION="${PIER_VERSION:-0.1.0}"

echo "==> 清理 build 目录"
# build/ 只放打包产物，整个清掉重建：只删当前这个 .app 的话，改过名的旧产物
# 会一直留在里面，而访达里两个图标并排摆着，点错的那个是打不开的。
# 图标的中间文件放临时目录，不落进 build/。
rm -rf "$BUILD_DIR"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> 编译界面二进制（${PIER_ARCH}）"
# 服务清单在 Pier 自己的数据目录里，不再需要把某份 YAML 的路径编进二进制。
#
# 界面这一层必须开 cgo——webview 是一份 C++ 源码，macOS 上要链 -framework WebKit。
# 而 cgo 交叉编译到另一个架构不需要额外工具链：Xcode 的 SDK 本身是两套架构都在的，
# clang 用 -arch 就能出另一份，所以这里直接编两次再 lipo 拼起来。
case "$PIER_ARCH" in
universal)
	# macho 目录放中间产物，不落进 build/（那里只放最终产物）。
	MACHO="$WORK/macho"
	mkdir -p "$MACHO"
	for arch in arm64 amd64; do
		echo "    - darwin/$arch"
		# -trimpath 去掉二进制里的本机路径（/Users/<名字>/...）：这份 .app 是要发出去的，
		# 里面不该带着打包这台机器的目录结构。
		CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" \
			go build -trimpath -o "$MACHO/$BIN_NAME.$arch" ./gui
	done
	lipo -create -output "$APP/Contents/MacOS/$BIN_NAME" \
		"$MACHO/$BIN_NAME.arm64" "$MACHO/$BIN_NAME.amd64"
	;;
arm64 | amd64)
	CGO_ENABLED=1 GOOS=darwin GOARCH="$PIER_ARCH" \
		go build -trimpath -o "$APP/Contents/MacOS/$BIN_NAME" ./gui
	;;
*)
	echo "PIER_ARCH 只能是 universal / arm64 / amd64，收到：$PIER_ARCH" >&2
	exit 1
	;;
esac
lipo -archs "$APP/Contents/MacOS/$BIN_NAME" 2>/dev/null | sed 's/^/    架构：/'

echo "==> 生成图标"
go run ./tools/mkicon "$WORK/icon.png"

ICONSET="$WORK/icon.iconset"
mkdir -p "$ICONSET"
# sips 的 -z 是「高 宽」；iconset 要求一组固定的尺寸与文件名。
while read -r px name; do
	sips -z "$px" "$px" "$WORK/icon.png" --out "$ICONSET/icon_$name.png" >/dev/null
done <<'SIZES'
16 16x16
32 16x16@2x
32 32x32
64 32x32@2x
128 128x128
256 128x128@2x
256 256x256
512 256x256@2x
512 512x512
1024 512x512@2x
SIZES
# 文件名必须和 Info.plist 的 CFBundleIconFile 大小写一致（Pier ↔ Pier.icns）：
# 系统按区分大小写的方式找图标，对不上就显示一个空白的占位图标，Dock 里也一样。
ICON_NAME="Pier"
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/$ICON_NAME.icns"

echo "==> 写入 Info.plist"
cat >"$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Pier</string>
	<key>CFBundleDisplayName</key>
	<string>Pier</string>
	<key>CFBundleIdentifier</key>
	<string>local.pier.panel</string>
	<key>CFBundleExecutable</key>
	<string>pier-gui</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>__PIER_VERSION__</string>
	<key>CFBundleVersion</key>
	<string>1</string>
	<key>CFBundleIconFile</key>
	<string>Pier</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.developer-tools</string>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
PLIST

# 版本号在上面那份 plist 里留成了占位符，这里替换掉——写死一个数的话，
# 打包时改的那一处和这里就会各说各的。
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $VERSION" "$APP/Contents/Info.plist" >/dev/null

echo "==> 临时签名"
# 不签名的话，Apple Silicon 上从访达启动可能被拦下。
# 临时签名是本机行为，不需要证书，也不联网。
if codesign --force --sign - "$APP" 2>/dev/null; then
	echo "    已签名（ad-hoc）"
else
	echo "    签名失败——不影响本机运行，但首次打开可能需要在「系统设置 → 隐私与安全性」里放行"
fi

echo "==> 刷新系统的应用登记"
# macOS 会缓存 App 的图标与信息，同一路径重新打包后访达、Dock 可能还显示旧的（或空白的）图标。
# 让 LaunchServices 重新登记这个 bundle，再碰一下修改时间让访达重绘。
LSREGISTER="/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
[[ -x "$LSREGISTER" ]] && "$LSREGISTER" -f "$APP" || true
touch "$APP"

echo
echo "完成：$APP"
echo "双击打开，或执行：open '$APP'"
