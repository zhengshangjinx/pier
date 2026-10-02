#!/usr/bin/env bash
#
# 按各平台的习惯分别打包，产物全部落在 dist/：
#
#   Pier-<版本>-macos-universal.dmg / .zip   里面是 Pier.app（Intel 与 Apple 芯片同一份）
#   Pier-<版本>-macos-universal.tar.gz       命令行 pier
#   Pier-<版本>-windows-amd64.zip            命令行 pier.exe + 界面 pier-gui.exe
#   Pier-<版本>-linux-amd64.tar.gz           同上 + .desktop + 图标 + install.sh
#   Pier-<版本>-linux-arm64.tar.gz
#   SHA256SUMS                               以上全部的校验和
#
# 用法：./package.sh [版本号]（不给就用 0.1.0）
#
# 界面是 cgo 的（webview 是一份 C++ 源码），所以每一份都必须在能拿到该平台
# 头文件的机器上编：macOS 直接编；Windows 用 Homebrew 的 mingw-w64；
# Linux 的 GTK / WebKit 头文件只有 Linux 上有，走 Docker（见 tools/pkg/linux）。
set -euo pipefail

# 显式设定 PATH，不读用户 shell profile：本机 profile 有问题，
# 非交互执行时会把整个脚本带崩。
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

cd "$(dirname "$0")"
MODULE_DIR="$(pwd)"
VERSION="${1:-0.1.0}"
DIST="$MODULE_DIR/dist"
APP="$MODULE_DIR/build/Pier.app"

rm -rf "$DIST"
mkdir -p "$DIST"
WORK="$(mktemp -d)"
# SYSO 是打包途中临时放进 gui/ 的 Windows 图标资源，见 Windows 那一节。正常路径上
# 编完就删；这里再兜一次，脚本中途出错也不会把它留在源码树里。
SYSO="$MODULE_DIR/gui/rsrc_windows_amd64.syso"
trap 'rm -rf "$WORK"; rm -f "$SYSO"' EXIT

# ── macOS ────────────────────────────────────────────────────────────────
# .app 的组装交给 build-app.sh：那是本机调试也在用的同一条路，
# 两份组装方式迟早会在 Info.plist 或签名上分叉。
echo "==> macOS：界面（通用二进制）"
PIER_VERSION="$VERSION" "$MODULE_DIR/build-app.sh" >/dev/null
# 落地时目录名必须还是 Pier.app：`cp -R` 的目标名就是新名字，
# 写成 "$WORK/app" 的话 zip 与 dmg 里那个 bundle 会变成叫「app」，
# 拖进「应用程序」之后访达显示的就是它（bundle 名看的是目录名）。
cp -R "$APP" "$WORK/Pier.app"

echo "==> macOS：命令行（通用二进制）"
mkdir -p "$WORK/macos-cli"
for arch in arm64 amd64; do
	CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" \
		go build -trimpath -ldflags "-s -w" -o "$WORK/macos-cli/pier.$arch" .
done
lipo -create -output "$WORK/macos-cli/pier" "$WORK/macos-cli/pier.arm64" "$WORK/macos-cli/pier.amd64"

# zip：不需要挂载就能拿到 .app，解压出来直接拖进「应用程序」。
ditto -c -k --sequesterRsrc --keepParent "$WORK/Pier.app" "$DIST/Pier-$VERSION-macos-universal.zip"

# dmg：macOS 上分发 app 的常规形式。里面放一个「应用程序」的替身，
# 打开之后把图标拖过去就是安装，不用先解压、再切到访达去找目标目录。
mkdir -p "$WORK/dmg"
cp -R "$WORK/Pier.app" "$WORK/dmg/"
ln -s /Applications "$WORK/dmg/应用程序"
hdiutil create -quiet -volname "Pier $VERSION" -srcfolder "$WORK/dmg" -ov -format UDZO \
	"$DIST/Pier-$VERSION-macos-universal.dmg"

mkdir -p "$WORK/pier-$VERSION-macos-universal"
mv "$WORK/macos-cli/pier" "$WORK/pier-$VERSION-macos-universal/"
tar -czf "$DIST/Pier-$VERSION-macos-universal.tar.gz" -C "$WORK" "pier-$VERSION-macos-universal"

# ── Windows ──────────────────────────────────────────────────────────────
# mingw-w64 从 Homebrew 装：brew install mingw-w64。
# CC 与 CXX 都要给——cgo 编 .cc 文件用的是 CXX，只设 CC 的话它会去找
# 目标平台默认的 g++，在 macOS 上找不到 Windows 的头文件。
echo "==> Windows：命令行 + 界面"
if ! command -v x86_64-w64-mingw32-gcc >/dev/null; then
	echo "    缺少 mingw-w64（brew install mingw-w64），跳过 Windows 两份" >&2
else
	WINDIR="$WORK/pier-$VERSION-windows-amd64"
	mkdir -p "$WINDIR"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags "-s -w" -o "$WINDIR/pier.exe" .
	# 界面这一份要 -H=windowsgui：不给的话双击之后会先弹一个黑色控制台窗口，
	# 关掉它界面也跟着没了。
	#
	# 图标得走 Windows 自己的资源段：go build 出来的 exe 里没有 .rsrc 这一节，
	# 任务栏、资源管理器与标题栏就只给一个默认图标。用 mkicon 画一份多档 .ico，
	# windres 编成 COFF。.syso 必须和包同目录才会被采用，所以先落进 gui/、编完删掉
	# （顶层 trap 也兜了一遍）——它的名字带着 _windows_amd64，mac 与 Linux 编的时候
	# 根本不会看它。
	go run ./tools/mkicon "$WORK/pier.ico"
	printf '1 ICON "%s"\n' "$WORK/pier.ico" >"$WORK/pier.rc"
	x86_64-w64-mingw32-windres -O coff -o "$SYSO" "$WORK/pier.rc"
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
		CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
		go build -trimpath -ldflags "-s -w -H=windowsgui" -o "$WINDIR/pier-gui.exe" ./gui
	rm -f "$SYSO"
	cp "$MODULE_DIR/tools/pkg/windows/README.txt" "$WINDIR/README.txt"
	(cd "$WORK" && zip -qr "$DIST/Pier-$VERSION-windows-amd64.zip" "pier-$VERSION-windows-amd64")
fi

# ── Linux ────────────────────────────────────────────────────────────────
echo "==> Linux：命令行 + 界面（容器里编）"
if ! command -v docker >/dev/null; then
	echo "    缺少 docker，跳过 Linux 两份" >&2
else
	# 官方源连不上时用这几个变量指镜像（都可选，不给就是官方地址）：
	#   PIER_BASE_IMAGE  PIER_APT_MIRROR  PIER_GO_DL  PIER_GOPROXY
	# 对应 tools/pkg/linux/Dockerfile 顶上那四个 build-arg，含义见那里。
	build_args=""
	if [ -n "${PIER_BASE_IMAGE:-}" ]; then build_args="$build_args --build-arg BASE_IMAGE=$PIER_BASE_IMAGE"; fi
	if [ -n "${PIER_APT_MIRROR:-}" ]; then build_args="$build_args --build-arg APT_MIRROR=$PIER_APT_MIRROR"; fi
	if [ -n "${PIER_GO_DL:-}" ]; then build_args="$build_args --build-arg GO_DL_URL=$PIER_GO_DL"; fi
	if [ -n "${PIER_GOPROXY:-}" ]; then build_args="$build_args --build-arg GOPROXY=$PIER_GOPROXY"; fi
	# 这里是有意不加引号的：上面几个值都是主机名或 URL，不含空格。
	docker build -q --platform linux/arm64 $build_args -t pier-linux-build "$MODULE_DIR/tools/pkg/linux" >/dev/null
	mkdir -p "$WORK/linux-out"
	# 源码只读挂进去；模块缓存与构建缓存用两个卷，第二次打包不用重新下载。
	docker run --rm \
		-v "$MODULE_DIR":/src:ro \
		-v pier-linux-gomod:/root/go/pkg/mod \
		-v pier-linux-gocache:/root/.cache/go-build \
		-v "$WORK/linux-out":/out \
		pier-linux-build sh /src/tools/pkg/linux/build.sh /out >/dev/null

	# 图标只有 1024 这一档：桌面环境会自己缩，多塞几档只是多几个文件。
	go run ./tools/mkicon "$WORK/pier.png"
	for arch in amd64 arm64; do
		LDIR="$WORK/pier-$VERSION-linux-$arch"
		mkdir -p "$LDIR"
		# 容器里出来的名字是 <名字>-linux-<架构>，见 tools/pkg/linux/build.sh。
		cp "$WORK/linux-out/pier-linux-$arch" "$LDIR/pier"
		cp "$WORK/linux-out/pier-gui-linux-$arch" "$LDIR/pier-gui"
		cp "$WORK/pier.png" "$LDIR/pier.png"
		cp "$MODULE_DIR/tools/pkg/linux/Pier.desktop" "$LDIR/pier.desktop"
		cp "$MODULE_DIR/tools/pkg/linux/install.sh" "$LDIR/install.sh"
		cp "$MODULE_DIR/tools/pkg/linux/README.txt" "$LDIR/README.txt"
		chmod +x "$LDIR/install.sh"
		tar -czf "$DIST/Pier-$VERSION-linux-$arch.tar.gz" -C "$WORK" "pier-$VERSION-linux-$arch"
	done
fi

# ── 校验和 ───────────────────────────────────────────────────────────────
echo "==> SHA256SUMS"
(cd "$DIST" && shasum -a 256 ./*.zip ./*.dmg ./*.tar.gz >SHA256SUMS)

echo
# 变量名一律带花括号：本机的 /bin/bash 是 3.2，紧跟其后的全角冒号会被它
# 当成变量名的一部分（`DIST：: unbound variable`），后面那句 ls 根本轮不到。
echo "产物在 ${DIST}："
ls -1 "$DIST"
