#!/bin/sh
#
# 把 Pier 装进当前用户，不需要 root：
#
#   ~/.local/bin/pier-gui                     图形界面
#   ~/.local/bin/pier                         命令行
#   ~/.local/share/applications/pier.desktop  应用菜单里的入口
#   ~/.local/share/icons/hicolor/1024x1024/apps/pier.png
#
# 想装到系统里就自己把上面几处换成 /usr/local/bin 之类，再改 .desktop 里的 Exec。
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
bin="$HOME/.local/bin"
apps="$HOME/.local/share/applications"
icon="$HOME/.local/share/icons/hicolor/1024x1024/apps"

mkdir -p "$bin" "$apps" "$icon"
install -m 755 "$here/pier-gui" "$bin/pier-gui"
install -m 755 "$here/pier" "$bin/pier"
install -m 644 "$here/pier.png" "$icon/pier.png"

# .desktop 里的 Exec 必须是绝对路径：桌面环境不会去找 tar 包解在哪，
# 也不看安装目录。这里按实际装的路径写死。
sed "s|^Exec=.*|Exec=$bin/pier-gui|" "$here/pier.desktop" >"$apps/pier.desktop"

update-desktop-database "$apps" 2>/dev/null || true
gtk-update-icon-cache -t -f "$HOME/.local/share/icons/hicolor" 2>/dev/null || true

echo "装好了：$bin/pier-gui 打开界面，$bin/pier 用命令行"
case ":$PATH:" in
*":$bin:"*) ;;
*) echo "提示：$bin 不在 PATH 上，往 shell 配置里加一句 export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac
