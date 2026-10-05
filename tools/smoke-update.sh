#!/bin/sh
# 手工验收用：在临时目录里造一份假安装，配一个本地假 release，把 `pier update`
# 整条路走一遍——下载、校验、解压、换文件。不碰真实数据，也不碰 GitHub。
#
#   tools/smoke-update.sh cli     裸命令行那种安装（tar.gz）
#   tools/smoke-update.sh bundle  整包那种安装（zip）
#
# 产物一律留在 $TMPDIR/pier-smoke 下，看完可以直接删。
set -eu

here="$(cd "$(dirname "$0")/.." && pwd)"
mode="${1:-cli}"

work="$(mktemp -d "${TMPDIR:-/tmp}/pier-smoke.XXXXXX")"
home="$work/home"
install="$work/install"
mkdir -p "$home" "$install"

echo "==> 编一份「旧的」0.2.0（带打包记号，自更新只认这一种）"
build() {
	go build -ldflags "-X github.com/zhengshangjinx/pier/internal/version.Version=$1 \
	  -X github.com/zhengshangjinx/pier/internal/version.released=1" -o "$2" .
}

if [ "$mode" = bundle ]; then
	app="$work/Applications/Pier.app"
	mkdir -p "$app/Contents/MacOS"
	printf '<plist/>' > "$app/Contents/Info.plist"
	build 0.2.0 "$app/Contents/MacOS/pier"
	build 0.2.0 "$app/Contents/MacOS/pier-gui"
	exe="$app/Contents/MacOS/pier"
else
	build 0.2.0 "$install/pier"
	exe="$install/pier"
fi

echo "==> 编一份「新的」0.3.0，装进假产物里"
stage="$work/stage"
mkdir -p "$stage"

if [ "$mode" = bundle ]; then
	new="$stage/Pier.app"
	mkdir -p "$new/Contents/MacOS"
	printf '<plist/>' > "$new/Contents/Info.plist"
	build 0.3.0 "$new/Contents/MacOS/pier"
	build 0.3.0 "$new/Contents/MacOS/pier-gui"
	asset="Pier-0.3.0-macos-universal.zip"
	(cd "$stage" && zip -qr "$work/$asset" Pier.app)
else
	inner="$stage/pier-0.3.0-macos-universal"
	mkdir -p "$inner"
	build 0.3.0 "$inner/pier"
	asset="Pier-0.3.0-macos-universal.tar.gz"
	(cd "$stage" && tar czf "$work/$asset" pier-0.3.0-macos-universal)
fi

(cd "$work" && shasum -a 256 "./$asset" > SHA256SUMS)

echo "==> 起一个本地的假 release"
cat > "$work/server.py" <<'PY'
import http.server, json, os, sys, functools
root, port = sys.argv[1], int(sys.argv[2])
name = sys.argv[3]
# 校验和走的是 SHA256SUMS 那个资产（发布里本来就有），不是资产自带的 digest：
# 那条路要真发一版才试得到，这里两个都摆上，走的是常见的那条。
assets = [
    {"name": n, "browser_download_url": f"http://127.0.0.1:{port}/dl/{n}",
     "size": os.path.getsize(os.path.join(root, n))}
    for n in (name, "SHA256SUMS")
]
release = {
    "tag_name": "v0.3.0",
    "body": "假的一版，只用来看更新这条路通不通。",
    "html_url": "https://example.invalid/releases/tag/v0.3.0",
    "published_at": "2026-10-01T00:00:00Z",
    "assets": assets,
}
class H(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=root, **kw)
    def do_GET(self):
        if self.path == "/repos/zhengshangjinx/pier/releases/latest":
            body = json.dumps(release).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path.startswith("/dl/"):
            self.path = "/" + self.path[4:]
        super().do_GET()
    def log_message(self, *a):
        pass
H.port = port
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY

port=8931
python3 "$work/server.py" "$work" "$port" "$asset" &
server=$!
trap 'kill $server 2>/dev/null || true' EXIT

echo "==> 跑 pier update"
before="$(PIER_HOME="$home" "$exe" version)"
echo "    更新前：$before"
PIER_HOME="$home" PIER_UPDATE_BASE_URL="http://127.0.0.1:$port" "$exe" update

echo "==> 看看换成了什么"
if [ "$mode" = bundle ]; then
	after="$(PIER_HOME="$home" "$app/Contents/MacOS/pier" version)"
	[ -e "$app.old" ] && { echo "!! 旧的还留在 $app.old"; exit 1; }
else
	after="$(PIER_HOME="$home" "$install/pier" version)"
fi
echo "    更新后：$after"
case "$after" in
*0.3.0*) ;;
*) echo "!! 没换成 0.3.0"; exit 1 ;;
esac

echo "==> 再跑一次 update：该说已经是最新"
PIER_HOME="$home" PIER_UPDATE_BASE_URL="http://127.0.0.1:$port" "$exe" update || true

echo
echo "全程没事。现场留在这里：$work"
