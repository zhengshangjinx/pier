Pier —— 本地多项目服务的启停工具

怎么用
  双击 pier-gui.exe 打开图形界面。
  pier.exe 是同一套功能的命令行版本（pier --help 看用法）。

运行要有什么
  图形界面用系统的 WebView2 显示。Windows 11 自带；
  Windows 10 上若没装过，去这里装一次（微软官方，免费）：
  https://developer.microsoft.com/microsoft-edge/webview2/
  命令行那一个（pier.exe）不需要 WebView2。

数据放在哪
  %USERPROFILE%\.pier\（清单、状态、日志、缓存的二进制都在里面）
  想换位置就设环境变量 PIER_HOME。

其它
  界面里第一次打开是空的，点侧栏的「新建分组」「添加服务」，
  或者让 Pier 直接读一份 YAML：pier-gui.exe --config 你的清单.yaml

  第一次运行时 Windows 可能弹「已保护你的电脑」（SmartScreen）——
  这个包没有代码签名证书，点「更多信息 → 仍要运行」即可。
