# 构建 Linux 版 Pier 的容器（界面与命令行都从这里出）。
#
# 为什么是 Ubuntu 22.04：webview 的 cgo 指令写的是 webkit2gtk-4.0，
# 而 Debian 12 / Ubuntu 24.04 起只剩 4.1（包名与 .pc 名都换了），装不上。
# 22.04 是还带 4.0 的那一档里最省事的。
#
# 一个容器出两种架构：
#   - 本机架构（这台 Mac 上是 arm64）用系统自带的 gcc，原生编译；
#   - amd64 用 gcc-x86-64-linux-gnu 交叉编译，GTK / WebKit 的头文件与 .pc 另装
#     一份 amd64 的。包名加 `:amd64` 就够了：这些文件落在 /usr/lib/x86_64-linux-gnu、
#     /usr/include/x86_64-linux-gnu 这类架构专有的目录里，与本机那套各占各的。
#
# 只有 libwebkit2gtk-4.0-dev 不能这么装：同一族的 libgtk-3-dev、
# libjavascriptcoregtk-4.0-dev、libsoup2.4-dev 都标了 Multi-Arch: same，两个架构
# 可以并存，唯独它没标、还显式写了 Conflicts，apt 一见就判互斥（实为打包疏漏，
# 文件其实也分得开）。交叉编译缺的就是它那一个 .pc 与一根 .so 软链，于是绕开 apt
# 直接拆：apt-get download 取 .deb，dpkg-deb -x 解到 /。**不进 dpkg 数据库**——
# 登记了的话，此后每次 apt 操作都要面对一个它认为装不上的包。
#
# 另有一条不显眼的前提：linux-libc-dev 是 Multi-Arch: same，两个架构的版本必须
# 一致，否则互相 Breaks。用镜像站时两个 pocket 未必同步，实测华为云的 ubuntu-ports
# 落后一档（arm64 停在 5.15.0-194，amd64 已到 -198）就整个装不上。用官方源没这个
# 问题（Ubuntu 自己保证同步），用镜像站要挑两边同步的——南大是。
#
# 编译产物在 22.04 上动态链接 glibc 2.35 与 GTK3：目标机器要有
# libgtk-3-0 与 libwebkit2gtk-4.0-37，这两个在主流发行版里都是常规依赖。
#
# 四处官方源在有些网络下到不了（Docker Hub、archive.ubuntu.com、go.dev、
# proxy.golang.org 都可能连不上），下面四个 build-arg 各自能把它们换成镜像；
# 默认一律是官方地址，不传就是原来那套。package.sh 会把同名的 PIER_* 环境变量
# 转成它们。
#   BASE_IMAGE  Docker Hub 拉不动时换成镜像站里的同名镜像
#   APT_MIRROR  只给主机名，例 mirror.nju.edu.cn（sources.list 里几处都换掉）
#   GO_DL_URL   Go 压缩包的下载前缀，例 https://mirror.nju.edu.cn/golang
#   GOPROXY     编的时候去哪儿取依赖，例 https://goproxy.cn,direct
ARG BASE_IMAGE=ubuntu:22.04

FROM ${BASE_IMAGE}

ARG APT_MIRROR=
ARG GO_DL_URL=https://go.dev/dl
ARG GOPROXY=
# 空串等于没设：go 命令对空的 GOPROXY 会退回官方默认值。
ENV GOPROXY=${GOPROXY}

ENV DEBIAN_FRONTEND=noninteractive

# apt 这一步要排在前面：下面下载 Go 压缩包用的是 curl，而镜像里没有 curl，
# 得先由这里装上。几个源一律走 http——这时候 ca-certificates 还没装，
# 走 https 会卡在证书上。没给 APT_MIRROR 时 sed 换的就是它自己，等于没换。
#
# 另一处非改不可的地方：arm64 的镜像里源写的是 ports.ubuntu.com，那儿**只有 arm64**，
# 下面要装的 libgtk-3-dev:amd64 之流在那儿一个都找不到（apt 只会一路 Ign 过去）。
# 所以 ports 那几条打上 [arch=arm64]，再单独挂一条 [arch=amd64] 的源。
#
# 下载特意分成「先只下、下不齐再来一遍」两步再安装：有些网络（比如本机这条）
# 会在连续传输几十兆之后把连接掐掉，而 apt 的 Acquire::Retries 只管单个文件、
# 管不了这个。好在已经取到的 .deb 留在缓存里，重来一遍只补缺的那些。
# 正常网络上第一遍就过，这个循环不多花一秒。
RUN set -eux; \
	echo 'Acquire::Retries "5";' > /etc/apt/apt.conf.d/80-retries; \
	apt_host="${APT_MIRROR:-archive.ubuntu.com}"; \
	ports_host="${APT_MIRROR:-ports.ubuntu.com}"; \
	sed -i "s|http://archive.ubuntu.com|http://${apt_host}|g" /etc/apt/sources.list; \
	sed -i "s|http://security.ubuntu.com|http://${apt_host}|g" /etc/apt/sources.list; \
	sed -i "s|deb http://ports.ubuntu.com/ubuntu-ports/|deb [arch=arm64] http://${ports_host}/ubuntu-ports/|g" /etc/apt/sources.list; \
	dpkg --add-architecture amd64; \
	if [ "$(dpkg --print-architecture)" != amd64 ]; then \
		{ \
			echo "deb [arch=amd64] http://${apt_host}/ubuntu jammy main restricted universe multiverse"; \
			echo "deb [arch=amd64] http://${apt_host}/ubuntu jammy-updates main restricted universe multiverse"; \
			echo "deb [arch=amd64] http://${apt_host}/ubuntu jammy-security main restricted universe multiverse"; \
		} >/etc/apt/sources.list.d/amd64.list; \
	fi; \
	apt-get update; \
	pkgs="ca-certificates curl git pkg-config build-essential \
		libgtk-3-dev libwebkit2gtk-4.0-dev \
		gcc-x86-64-linux-gnu g++-x86-64-linux-gnu \
		libgtk-3-dev:amd64 libwebkit2gtk-4.0-37:amd64 \
		libjavascriptcoregtk-4.0-dev:amd64 libsoup2.4-dev:amd64"; \
	round=0; \
	until apt-get -d install -y --no-install-recommends $pkgs; do \
		round=$((round + 1)); \
		if [ "$round" -ge 10 ]; then echo "试了 $round 遍还是下不齐" >&2; exit 1; fi; \
		sleep 5; \
	done; \
	apt-get install -y --no-install-recommends $pkgs; \
	cd /tmp; \
	apt-get download libwebkit2gtk-4.0-dev:amd64; \
	dpkg-deb -x libwebkit2gtk-4.0-dev_*.deb /; \
	rm -rf /tmp/libwebkit2gtk-4.0-dev_*.deb /var/lib/apt/lists/*

# Go 用官方包，不用 Ubuntu 源里的：22.04 源里是 1.18，跑不动这份代码
# （go.mod 要求 1.24）。
ARG GO_VERSION=1.24.13
RUN set -eux; \
	arch="$(dpkg --print-architecture)"; \
	case "$arch" in \
		arm64) goarch=arm64 ;; \
		amd64) goarch=amd64 ;; \
		*) echo "不支持的架构：$arch" >&2; exit 1 ;; \
	esac; \
	curl -fsSL "${GO_DL_URL}/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tgz; \
	tar -C /usr/local -xzf /tmp/go.tgz; \
	rm /tmp/go.tgz
ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin

# 构建时把仓库挂在 /src，产物写到 /out；见同目录的 build.sh。
WORKDIR /src
