# iPhone 快捷指令设置

安装脚本会显示每台 reMarkable 自己的 Wi‑Fi 地址和配对密钥。下面用：

- `REMARKABLE_HOST` 表示平板当前 Wi‑Fi IP，或在手机上验证可用的 `.local` 主机名
- `PAIRING_KEY` 表示安装时生成的配对密钥

不要照抄示例值，也不要把自己的配对密钥公开。

## 先测试网页入口

让 iPhone 和 reMarkable 连接同一个 Wi‑Fi，在 Safari 打开：

```text
http://REMARKABLE_HOST:8765/?key=PAIRING_KEY
```

能看到“发送到 reMarkable”页面后，再创建下面两条快捷指令。

### 换 Wi‑Fi 不用反复改地址

1. 安装脚本可能输出 `MDNS_CANDIDATE`，例如 `http://设备主机名.local:8765/`。
2. 保持手机和平板同一 Wi‑Fi、平板处于唤醒状态，用手机 Safari 打开该地址，
   输入**自己的**配对密钥。确认能进入发送页面，并试发一个小文件。
3. 成功后，下面两条快捷指令都使用这个主机名作为 `REMARKABLE_HOST`。
4. 在另一个 Wi‑Fi 上让手机和平板一起连接，再试一次。不支持 mDNS 的网络仍需
   使用平板当前 IP；访客网络若隔离设备，IP 也无法解决，须换允许设备互通的网络。

不要把某一台设备的 `.local` 名称当成所有 reMarkable 通用地址。同型号设备可能
重名；如果密钥不匹配或接收设备不对，先退回自己的 Wi‑Fi IP。路由器 DHCP 地址
保留仅对该路由器有效，不能解决跨 Wi‑Fi 更换地址。

当前只提供手动搭建步骤，不包含可直接导入的已签名快捷指令。首次配置需要填入
地址和密钥，后续不需要每次输入。两条指令保持分开，减少输入类型误判。

## 快捷指令一：发送网页到 reMarkable

这条指令直接读取 Safari 当前已经显示的页面，所以适合普通网页、需要登录的页面
以及 ChatGPT 对话。

1. 新建快捷指令，命名为“发送网页到 reMarkable”。
2. 打开快捷指令详情，启用“在共享表单中显示”，输入类型选择“Safari 网页”。
3. 添加“在网页上运行 JavaScript”，网页选择“快捷指令输入”，粘贴：

   ```javascript
   const nl = String.fromCharCode(10);
   const gap = nl + nl;
   const messages = [...document.querySelectorAll('[data-message-author-role]')];
   const body = messages.map((node) => {
     const role = node.getAttribute('data-message-author-role') === 'user'
       ? '用户' : 'ChatGPT';
     return role + gap + node.innerText.trim();
   }).join(gap + '---' + gap);
   const title = document.title
     .replace(' - ChatGPT', '')
     .replace(' – ChatGPT', '')
     .trim() || '网页';
   const content = body
     || document.querySelector('main')?.innerText
     || document.body.innerText;
   completion('# ' + title + gap + content + gap + '来源：' + location.href);
   ```

4. 添加“URL”操作，填写：

   ```text
   http://REMARKABLE_HOST:8765/api/send
   ```

5. 添加“获取 URL 内容”：
   - URL：选择上一步的“URL”
   - 方法：`POST`
   - 请求正文：`JSON`
   - 添加字段 `content`，类型选“文本”，值选择 JavaScript 的结果
   - 请求头添加 `X-Web-Inbox-Key`，值填写 `PAIRING_KEY`
6. 在 Safari 打开网页，点击分享 →“发送网页到 reMarkable”。首次使用时，iOS
   可能会询问是否允许快捷指令运行 JavaScript；选择允许。

如果只想让平板直接抓取公开文章，也可以向 `/api/send` POST 一个名为 `url` 的
表单字段。需要登录或依赖 JavaScript 的网页仍应使用上面的 Safari 方案。

## 快捷指令二：发送文件到 reMarkable

1. 新建快捷指令，命名为“发送文件到 reMarkable”。
2. 打开快捷指令详情，启用“在共享表单中显示”。输入类型保留“文件”和“图像”；
   也可以保留系统默认的全部类型。
3. 添加“URL”操作，填写：

   ```text
   http://REMARKABLE_HOST:8765/api/upload
   ```

4. 添加“获取 URL 内容”：
   - URL：选择上一步的“URL”
   - 方法：`POST`
   - 请求正文：`表单`
   - 添加字段 `file`，类型选“文件”，值选择“快捷指令输入”
   - 请求头添加 `X-Web-Inbox-Key`，值填写 `PAIRING_KEY`
5. 在“文件”或“照片”中打开分享菜单，选择“发送文件到 reMarkable”。

支持 PDF、无 DRM 的 EPUB、RMDOC、JPG/JPEG 和 PNG。图片会转换为 PDF；DOCX、
PPTX 和 TXT 请先转换为 PDF；HEIC、WebP 请先转成 JPG/PNG。单个文件上限为
100 MiB，图片最多 1600 万像素，EPUB 解压内容最多 128 MiB／2000 个条目。
一次只选一个文件，不要同时运行两个发送任务。

## 把快捷指令发给另一个人

可以先复制两条快捷指令，把主机名/IP 和密钥替换成对方设备安装时显示的值，再通过
AirDrop 或 iCloud 链接发送。不能直接分享自己的版本，否则对方的请求会指向你的
平板，而且会泄露你的配对密钥。

## 故障排查

- “请求超时”：确认手机和平板在同一 Wi‑Fi，并核对平板当前 IP。
- `.local` 打不开而 IP 可以：该网络或设备的 mDNS 不可用，先用 IP，不要重装服务。
- 出现“允许访问本地网络”或新地址的连接授权时，需要由用户允许；VPN 若拦截局域网，
  可暂时断开后对比测试。
- 分享菜单没有快捷指令：检查“在共享表单中显示”和输入类型；ChatGPT 正文提取请从
  Safari 页面分享，不是仅从 ChatGPT App 分享一个链接。确认 iCloud 已同步新配置。
- “another transfer is in progress”：等上一次发送结束，再重试。
- “invalid pairing key”：检查是不是指向了别人的同名设备，或卸载重装后仍使用旧密钥。
- 可以打开网页入口，但导入超时：退出微信读书等接管应用，回到原生桌面再试。
- 系统升级后失效：重新连接 USB 并运行 `./install.sh`。
- 网页正文不完整：改用 Safari 阅读器、发送 PDF，或使用“发送文件”快捷指令。
- 大文件超时：先查看平板是否已经收到；网络超时不一定代表导入失败，重试可能产生副本。
