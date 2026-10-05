#!/bin/sh
#
# 在 tools/pkg/linux/Dockerfile 起的容器里执行：把 Linux 版的两个二进制
# （界面 pier-gui、命令行 pier）编到 /out 下，按 <名字>-linux-<架构> 命名。
#
# 两种架构的差别只有三样：交叉编译器、pkg-config 去哪找 .pc 文件。
# pkg-config 这一条必须显式给：默认那几处是给本机架构的，
# 交叉编译时若不换掉，会把 arm64 的 gtk 头文件喂给 amd64 的编译器。
set -eux

out="${1:-/out}"
mkdir -p "$out"

# 版本号由 package.sh 经 PIER_VERSION 传进来（容器里看不到宿主机的变量）。
# 不给就是空串：手动起容器编一份调试用的时候，编出来的就是没有版本号的 dev 构建，
# 与在源码树里直接 go build 一模一样。
# 第二个 -X 是「这份是打包产物」的记号，见 internal/version。
ldflags_version=""
if [ -n "${PIER_VERSION:-}" ]; then
	ldflags_version="-X github.com/zhengshangjinx/pier/internal/version.Version=$PIER_VERSION"
	ldflags_version="$ldflags_version -X github.com/zhengshangjinx/pier/internal/version.released=1"
fi

build_arch() {
	target="$1" cc="$2" cxx="$3" pcdir="$4"
	export GOOS=linux GOARCH="$target" CGO_ENABLED=1 CC="$cc" CXX="$cxx"
	export PKG_CONFIG_LIBDIR="$pcdir:/usr/share/pkgconfig"

	# 界面：webview 是 C++ 源码，Linux 上要链 GTK 与 WebKit，必须开 cgo。
	go build -trimpath -ldflags "-s -w $ldflags_version" -o "$out/pier-gui-linux-$target" ./gui
	# 命令行：纯 Go，各架构一份。
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $ldflags_version" -o "$out/pier-linux-$target" .
}

build_arch amd64 x86_64-linux-gnu-gcc x86_64-linux-gnu-g++ /usr/lib/x86_64-linux-gnu/pkgconfig
build_arch arm64 gcc g++ /usr/lib/aarch64-linux-gnu/pkgconfig

ls -l "$out"
