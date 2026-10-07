# Web Inbox for reMarkable

把 iPhone、iPad 或电脑上的网页、ChatGPT 对话和文件，通过同一 Wi‑Fi
直接发送到 reMarkable。整个服务运行在平板本机，不需要 reMarkable Cloud
或第三方中转服务器。

> 当前为测试版：不是 reMarkable 官方软件，也不是免配置的一键安装包。
> Paper Pro 系列已有两台设备的使用验证，但本次加固后的安装脚本仍需完整实机回归；
> reMarkable 1/2 尚未实机验证。请先备份，不要在不了解 Developer Mode 风险时安装。

## 功能

- 公开网页：抓取正文并生成排版适合电子墨水屏的 EPUB
- Safari 页面：通过快捷指令发送已经显示出来的内容，包括需要登录的页面
- 文本与 Markdown：生成 EPUB，并对 ChatGPT 对话做分角色排版
- 文件：PDF、无 DRM 的 EPUB、RMDOC、JPG/JPEG、PNG
- 图片：自动转成白底、保持比例的 PDF
- iPhone 分享表单：从 Safari、文件和照片中直接调用
- 局域网配对：每台设备首次安装时生成独立的 128 位随机密钥

单个文件上限为 100 MiB；图片最多 1600 万像素；EPUB 解压内容最多 128 MiB、
2000 个条目。正文请求最多 4 MiB。一次发送一个文件，服务同时只处理一个任务，
忙碌时返回提示而不排队。Web Inbox 不会绕过网页登录、付费墙或 DRM。

## 兼容性

| 设备 | CPU | 当前状态 |
| --- | --- | --- |
| reMarkable Paper Pro 系列 | ARM64 | 既有版本在两台 OS 3.28 设备使用过；新版安装回归待完成 |
| reMarkable 2 | ARMv7 | 尚未实机验证，安装需明确选择实验支持 |
| reMarkable 1 | ARMv7 | 尚未实机验证，安装需明确选择实验支持 |

兼容的前提是系统中仍有原生 `xochitl`、USB Web Interface 和 systemd。
reMarkable OS 更新可能改变这些内部接口；升级系统后如果服务消失，请重新运行
安装脚本。微信读书等“接管屏幕”的第三方应用会停止 `xochitl`，这时必须先退回
reMarkable 原生桌面才能导入文档。

## 安装前准备

1. 先同步或备份重要资料。
2. Paper Pro 系列需要开启 Developer Mode。开启会降低设备安全性，而且首次开启
   可能清除本机数据，请先阅读 reMarkable 官方说明。
3. 在平板设置中打开 **Storage → USB web interface**。
4. 用 USB 数据线连接电脑，并确认可以运行：

   ```sh
   ssh root@10.11.99.1
   ```

## 安装

从 GitHub 下载或克隆本仓库，然后在 macOS 或 Linux 电脑上运行：

```sh
git clone https://github.com/Phantomicio/remarkable-web-inbox.git
cd remarkable-web-inbox
chmod +x install.sh uninstall.sh
./install.sh
```

脚本会检测架构、检查前提条件、校验 Release 下载、安装服务，并检查 HTTP 服务和
原生导入器是否可连接。自检失败不会显示安装成功，也不会重启原生界面。
成功后输出：

```text
PAIRING_KEY=每台设备不同的随机密钥
WEB_INBOX_URL=http://平板的Wi-Fi地址:8765/?key=随机密钥
MDNS_CANDIDATE=http://设备主机名.local:8765/
```

让手机和平板连接同一个 Wi‑Fi，在 Safari 打开输出的 `WEB_INBOX_URL` 即可。
详细的两条 iPhone 快捷指令配置见 [IPHONE_SETUP.md](IPHONE_SETUP.md)。

`MDNS_CANDIDATE` 只是候选固定名称，不代表已从手机验证可达。请按手机设置文档
测试后再用于快捷指令；不要照抄其他人的主机名或配对密钥。安装自检不等于文档
已成功导入：还须拔掉 USB 发送一个小文件，并重启平板后重试。

reMarkable 1/2 的实验性安装需要显式选择：

```sh
WEB_INBOX_ALLOW_UNTESTED=1 ./install.sh
```

原生 Windows 安装尚不支持；当前说明面向有 SSH/SCP 工具的 macOS/Linux。
连接不同设备时若 SSH 报主机密钥变更，应核实连接的是哪台设备，不要关闭主机验证。

如果 Release 尚未下载，也可以先在本机运行 `make build`；安装脚本会自动使用
`dist/` 中与设备架构匹配的文件。

## 卸载

连接 USB 后运行：

```sh
./uninstall.sh
```

卸载移除服务、程序和配对密钥，不删除已导入到原生资料库的文档。
重新安装后会生成新密钥，需要更新手机快捷指令。

## 从源码构建

需要 Go 1.26 或更高版本，发布构建请使用仍获安全维护的最新稳定版 Go，
不要用旧版工具链发布。GitHub 工作流使用 `stable` 并执行 `govulncheck`；
Go 的维护政策见 [官方发布说明](https://go.dev/doc/devel/release#policy)。

```sh
make test
make build
```

输出位于 `dist/`：

- `web-inbox-linux-armv7`：reMarkable 1/2
- `web-inbox-linux-arm64`：Paper Pro 系列
- `SHA256SUMS`：下载校验值

## 安全说明

- 服务默认监听平板的 `8765` 端口，只应在可信局域网内使用。
- 传输使用普通 HTTP，不要把端口转发到互联网，也不要在公共 Wi‑Fi 上使用。
- 配对密钥相当于本地密码；不要把自己的快捷指令或配对链接公开。
- 每个人安装后都会生成自己的密钥，所以不存在一条所有人都能直接使用的通用
  快捷指令。当前发布提供手动搭建教程，不包含已签名、可一键导入的 `.shortcut`；
  分享自己的快捷指令前必须去掉真实地址和密钥。
- 网页提取的每次新连接只拨号到已检查的公共 IP，重定向也受同样限制，不使用
  环境代理。它不是完整的浏览器，也不是面向互联网的托管服务。

## 工作原理

Web Inbox 是一个无 CGO 的 Go 服务。它把文本转换成 EPUB，把图片转换成 PDF，
然后交给 reMarkable 自带的 USB Web Interface 导入器。拔掉 USB 后，服务只恢复
本机 USB 网络接口供导入器内部使用，不会重启 `xochitl`。

## 已知限制

- 网站如果依赖大量 JavaScript、Cloudflare 或登录状态，平板通常无法直接抓取；
  请用 Safari 快捷指令读取当前页面，或打印成 PDF 后发送。
- ChatGPT 分享链接可能拒绝平板直接抓取；Safari 快捷指令可发送当前已显示的对话。
- Wi‑Fi IP 会改变。已验证可用的 `.local` 名称可避免手动改 IP，但某些路由器会
  禁止 mDNS 或设备互访，平板休眠也可能断网；这时需要唤醒平板、换非访客网络，
  或使用当前 IP。同型号设备的默认主机名可能相同，必须确认连接的是自己的设备。
- HEIC、WebP、GIF、Office 文件和受 DRM 保护的 EPUB 不支持；图片请先转 JPG/PNG，
  其他文档先转 PDF。照片超过 1600 万像素时先缩小。暂不支持一次多选多个文件。
- 上传大文件可能超时；先检查资料库是否已经收到，避免直接重试导致重复文档。
- Safari 提取只包含当前已经加载的文字，长对话可能不完整；图片、公式、表格
  不能保证保留。字体取决于平板已有字体，网页排版不是逐像素复刻。
- 目前只在 ARM64 的 reMarkable OS 3.28 实机验证。ARMv7 构建通过测试和交叉编译，
  但在更多硬件上验证前应视为实验性支持。

## 发布者检查

参见 [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md)。只推送源码不足以完成安装分发：
必须发布与源码匹配的两个架构二进制和 `SHA256SUMS`。本仓库的 tag 工作流会生成
这些文件，但仍需检查运行结果并做全新安装、断开 USB、重启后的验收。

## License

MIT
