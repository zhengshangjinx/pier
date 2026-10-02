Pier —— 本地多项目服务的启停工具

怎么装
  在终端里跑：sh install.sh
  它会装进当前用户（~/.local/bin 等），不需要 root。
  不想装也可以直接用：./pier-gui 开界面，./pier 用命令行。

运行要有什么
  图形界面用系统的 GTK3 与 WebKit2GTK 4.0 显示，运行时需要这两个库：

    Debian / Ubuntu   sudo apt install libgtk-3-0 libwebkit2gtk-4.0-37
    Fedora            sudo dnf install gtk3 webkit2gtk4.0
    Arch              sudo pacman -S gtk3 webkit2gtk-4.0

  命令行那一个（pier）不依赖图形库，可以在没有桌面环境的机器上跑。

数据放在哪
  ~/.pier/（清单、状态、日志、缓存的二进制都在里面）
  想换位置就设环境变量 PIER_HOME。

其它
  界面里第一次打开是空的，点侧栏的「新建分组」「添加服务」，
  或者让 Pier 直接读一份 YAML：./pier-gui --config 你的清单.yaml
