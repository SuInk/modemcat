# DJOneHub for macOS（Apple Silicon）

适用于搭载 Apple M 系列芯片的 Mac，以及大疆一代 4G 模块（USB `2ca3:4006`）。

本包只包含本地后台进程和浏览器 WebUI，不安装原生 macOS `.app` 或菜单栏界面。

## 安装（推荐）

1. 将下载的 ZIP 完整解压，不要只单独拖出其中某个文件。
2. 打开“终端”，输入 `cd `（后面留一个空格），把解压得到的文件夹拖入终端，然后按回车。
3. 执行一次安装命令：

   ```sh
   ./install
   ```

   安装过程可能要求输入 Mac 管理员密码。输入密码时终端不会显示圆点或星号，这是正常现象。

安装完成后，可以在终端的任意目录执行：

```sh
djonehub start
```

程序会自动打开 `http://127.0.0.1:7575`。启动程序的终端需要保持打开；按 `Control+C` 即可停止。

首次打开会要求设置管理员账号和至少 12 字节的密码。密码只以 bcrypt 哈希保存在 `~/Library/Application Support/DJOneHub/auth.json`，不会明文保存；文件权限为 `0600`。WebUI 顶部可以修改账号密码或退出，修改凭据会注销所有旧会话。

也可以在另一个终端的任意目录执行：

```sh
djonehub stop
```

## 免安装使用

如果不想安装，也可以一直保留解压后的文件夹，在该目录执行：

   ```sh
   ./djonehub start
   ```

## 常用命令

```sh
djonehub status       # 查看状态
djonehub logs         # 查看实时日志
djonehub open         # 重新打开管理网页
djonehub start --demo # 不连接硬件，打开演示界面
djonehub service install # 登录后自动在后台运行
djonehub service status  # 查看后台服务状态
djonehub service remove  # 停止并移除后台服务
```

## Cloudflare Tunnel 远程访问

先在本机打开 `http://127.0.0.1:7575` 并设置管理员账号，然后在发行包目录运行：

```sh
./cloudflare-setup sms.example.com
```

脚本会把指定 HTTPS hostname 安全转发到本机 WebUI，并安装 DJOneHub 与 Tunnel 的用户级后台服务。DJOneHub 仍只监听回环地址，并只接受脚本配置的公网 Host。Mac 必须保持开机并已登录；建议在 Cloudflare Zero Trust 中再为该 hostname 配置只允许本人邮箱的 Access 策略。

## macOS 阻止打开时

本软件目前没有 Apple Developer ID 签名。首次启动如果被 macOS 阻止，请打开“系统设置 → 隐私与安全性”，确认仍要打开。

如果系统仍提示文件损坏，可在当前目录执行：

```sh
xattr -dr com.apple.quarantine ./djonehub ./bin ./lib
./djonehub start
```

请只对从可信发布页面下载并核对过 SHA-256 的文件执行该命令。

## 日志

程序日志保存在：

```text
~/Library/Logs/DJOneHub/djonehub.log
```

终端只显示启动、停止和错误摘要，不会持续刷出底层 USB 日志。

## 来电与提醒

WebUI 会显示来电和最近记录，并可拒接当前来电。提醒页面可分别启用浏览器通知、Bark 和 Telegram Bot；Bark 与 Telegram 在 WebUI 关闭后仍由本地后台发送。

短信收件箱会把最近 500 条记录持久保存到 `~/Library/Application Support/DJOneHub/sms-inbox.json`，服务或 Mac 重启后仍会恢复，文件权限为 `0600`。模块 `ME` 自动清理默认关闭；手动清理模块 `ME` 不会删除本地历史或 SIM 卡 `SM` 短信。

通知凭据保存在 `~/Library/Application Support/DJOneHub/notifications.json`，文件权限为 `0600`。短信正文默认不发送给第三方，Bark 和 Telegram 也默认关闭。

## 定时任务

WebUI 可以按天数和本地时间周期发送短信，并支持启停、立即执行和查看最近 100 条执行记录。失败任务会在下一个指定时间重试；每次成功或失败都会发送到当前已启用且配置完整的浏览器、Bark 和 Telegram 通道。

任务需要 DJOneHub 后台持续运行。配置与短信内容保存在 `~/Library/Application Support/DJOneHub/scheduled-tasks.json`，文件权限为 `0600`。

本功能不配置 Wi-Fi Calling / VoWiFi。由于模块没有已验证可用的双向 USB 语音音频，WebUI 不提供接听或拨号。USB 忙于较长操作、固件不输出呼叫事件或来电极短时，提醒可能延迟或漏报。

## 当前限制

- 支持 macOS 13 Ventura 至 macOS 26 Tahoe；当前发行包仅支持 Apple Silicon，不支持 Intel Mac。
- 仅监听本机地址，局域网中的其他设备无法直接访问管理网页。
- 本项目为非官方工具，与 DJI、Quectel、运营商及 eSIM 卡片厂商无隶属或授权关系。
- 使用短信、蜂窝数据和 eSIM 前，请确认运营商资费、漫游规则及当地法律要求。
