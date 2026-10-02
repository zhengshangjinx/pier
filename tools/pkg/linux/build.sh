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

build_arch() {
	target="$1" cc="$2" cxx="$3" pcdir="$4"
	export GOOS=linux GOARCH="$target" CGO_ENABLED=1 CC="$cc" CXX="$cxx"
	export PKG_CONFIG_LIBDIR="$pcdir:/usr/share/pkgconfig"

	# 界面：webview 是 C++ 源码，Linux 上要链 GTK 与 WebKit，必须开 cgo。
	go build -trimpath -ldflags "-s -w" -o "$out/pier-gui-linux-$target" ./gui
	# 命令行：纯 Go，各架构一份。
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$out/pier-linux-$target" .
}

build_arch amd64 x86_64-linux-gnu-gcc x86_64-linux-gnu-g++ /usr/lib/x86_64-linux-gnu/pkgconfig
build_arch arm64 gcc g++ /usr/lib/aarch64-linux-gnu/pkgconfig

ls -l "$out"
