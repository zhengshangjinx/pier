Pier —— 本地多项目服务的启停工具

怎么装
  双击 install.cmd。装完：
    · 开始菜单里搜 Pier —— 打开图形界面
    · 新开一个终端，敲 pier —— 用命令行
  装到 %LOCALAPPDATA%\Programs\Pier，只改你自己的 PATH，不需要管理员。
  装完这个文件夹就可以删掉了。

  不想装也行：pier-gui.exe 直接双击就能开界面（bin\pier.exe 是命令行那一份），
  只是一个在文件夹里，一个是完整路径，用起来不方便。

  install.cmd 会调用同目录的 install.ps1，里面写清了它到底动了什么。

运行要有什么
  图形界面用系统的 WebView2 显示。Windows 11 自带；
  Windows 10 上若没装过，去这里装一次（微软官方，免费）：
  https://developer.microsoft.com/microsoft-edge/webview2/
  命令行那一个（pier.exe）不需要 WebView2。

数据放在哪
  %USERPROFILE%\.pier\（清单、状态、日志、缓存的二进制都在里面）
  想换位置就设环境变量 PIER_HOME。

其它
  界面第一次打开是空的，点侧栏的「新建分组」「添加服务」，
  或者让 Pier 直接读一份 YAML：pier --config 你的清单.yaml

  第一次运行时 Windows 可能弹「已保护你的电脑」（SmartScreen）——
  这个包没有代码签名证书，点「更多信息 → 仍要运行」即可。

卸载
  删掉 %LOCALAPPDATA%\Programs\Pier，再到「系统属性 → 环境变量」里
  把用户 PATH 中的那一项去掉，开始菜单的 Pier 也一并删掉。
