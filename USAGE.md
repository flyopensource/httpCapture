# HTTP Capture 使用文档

本文档对应当前 V0.4 开发版。APK 与 CLI 需要配套使用；升级其中一端后，建议重新扫码导入配置。

## 1. 推荐流程：APK 一键联动 CLI 抓包

这是当前推荐的日常开发流程。电脑端运行 `serve`，手机端扫码后，APK 主按钮和快捷磁贴可以直接创建、停止并归档电脑端抓包会话。

### 1.1 准备 CLI

当前只发布 Linux AMD64 单文件 CLI：

```bash
chmod +x httpcapture-linux-amd64
sudo install -m 0755 httpcapture-linux-amd64 /usr/local/bin/httpcapture
httpcapture version
```

如果不安装到系统目录，也可以直接执行 `./httpcapture-linux-amd64`。

### 1.2 启动一键联动服务

先确认电脑和手机在同一个可互访局域网。通常直接运行：

```bash
httpcapture serve
```

CLI 会自动识别手机可访问的电脑局域网 IP。需要固定控制端口时：

```bash
httpcapture serve --control-port 39000
```

说明：

- `serve` 会保持前台运行，不要关闭这个终端。
- 本地 Web 查看器只监听 `127.0.0.1`，不会暴露到局域网。
- 手机控制接口使用独立 HTTPS 身份和逐设备 token。
- Proxify 是默认引擎；如果受管 Proxify 未运行，`serve` 会尝试启动。
- 停止一次抓包会话不会停止 Proxify 代理进程。

如果电脑有多个网卡、VPN、Docker 网卡，自动识别的 IP 不对，再用 `--control-host` 手动覆盖：

```bash
httpcapture serve --control-host 192.168.1.10
```

如果二维码在终端显示太大或错位，直接使用生成的 PNG：

```bash
httpcapture serve --terminal-qr never --out pair.png
```

### 1.3 手机 APK 操作

1. 打开 HTTP Capture APK。
2. 点击“扫码导入”，扫描 CLI 生成的 v4 二维码。
3. APK 自动下载代理配置和 CA 公钥证书。
4. 按提示安装 CA。
5. 选择一个或多个目标 App；支持按应用名、包名模糊搜索。
6. 点击“开始抓包”。
7. 开发完成后点击“停止抓包”。

停止后，CLI 会归档会话到：

```text
./httpcapture-sessions/<captureId>/
  meta.json
  traffic.jsonl
  session.har
```

### 1.4 快捷磁贴

APK 支持添加系统快捷磁贴“抓包开关”。

- 首次 VPN 授权仍需要进入 APK 完成。
- 磁贴复用当前代理配置和应用选择。
- 磁贴操作前会查询 CLI 状态。
- 如果 CLI 不可达、电脑端已有未知活动会话、或本机与电脑端 `captureId` 不一致，磁贴不会静默接管或降级，会打开 APK 显示原因。

### 1.5 联动失败时

有控制配置时，默认执行联动抓包。CLI 会话没有创建成功前，APK 不会启动 VPN。

失败后 APK 会显示具体原因，并提供“仅启动 VPN”按钮。这个模式只做代理转发，电脑端不会创建或归档受管抓包会话。

## 2. 查看、搜索和导出抓包数据

`serve` 已内置本地 Web 查看器。也可以单独启动：

```bash
httpcapture web
```

默认地址：

```text
http://127.0.0.1:9080/
```

端口冲突时：

```bash
httpcapture web --port 9081
httpcapture web --sessions /path/to/httpcapture-sessions --no-open
```

如果要查看旧版本保存在用户目录下的会话，可以显式指定：

```bash
httpcapture web --sessions ~/httpcapture-sessions
```

Web 能力：

- 按时间倒序查看历史会话。
- 按引擎、包名、设备、时间范围筛选。
- 搜索 URL、Host、Path、Method、Status、Header、文本 Body。
- 按 Method、状态码或状态组、Content-Type、耗时、大小过滤。
- 查看 Query、Headers、请求体、响应体。
- 在请求详情中复制为 cURL；完整文本请求体会写入命令，二进制或截断 Body 会提示未包含。
- 下载 HAR、Body，打开会话目录。
- 将会话移动到 `.trash`，不是永久删除。
- 实时显示活动 `traffic.jsonl` 中已完整写入的请求。
- 支持暂停/继续页面更新。

批量导出：

- 导出所有 Proxify 会话。
- 导出当前会话全部请求。
- 导出当前搜索/过滤条件的全部命中项，跨分页生效。
- 手动选择多个请求或多个会话导出。

导出格式是结构化 ZIP：

```text
manifest.json
sessions/<captureId>/traffic.jsonl
```

`manifest.json` 会记录导出范围、条件、会话、记录数、跳过数、警告、字段来源和校验信息。Body 缺失、损坏记录或采集截断会标记为 `partial`，不会误报完整成功。

## 3. 普通代理配对模式

普通 `pair` 只把代理地址、端口和 CA 导入 APK，不具备 App 控制 CLI 会话能力。它适合只想用 APK 转发流量到现有代理的场景。

Proxify：

```bash
httpcapture proxy proxify start
httpcapture pair --host 192.168.1.10
```

Charles：

```bash
httpcapture pair \
  --engine charles \
  --host 192.168.1.10 \
  --port 8888 \
  --cert charles-ca.cer \
  --name dev-charles
```

mitmproxy：

```bash
httpcapture proxy mitm start
httpcapture pair --engine mitmproxy --host 192.168.1.10
```

普通 `pair` 会启动一次性临时下载服务，APK 导入成功后 CLI 自动退出。二维码默认 3 分钟有效，且只能下载一次。

## 4. 手工录制命令

如果不使用 APK 一键联动，也可以手工标记会话：

```bash
httpcapture record start \
  --package com.example.demo \
  --device Pixel-5 \
  --client-ip 192.168.1.20
```

`record start` 默认保持前台，每 5 秒显示状态。按 `Ctrl+C` 会安全停止并归档。也可以在另一终端执行：

```bash
httpcapture record status
httpcapture record stop
```

状态输出不会打印 URL、Header、Cookie、Token、请求体或响应体。

## 5. 管理已配对设备

每次 `serve` 会给扫码手机签发一组独立控制凭据。

查看设备：

```bash
httpcapture control devices
httpcapture control devices --all
httpcapture control devices --json
```

撤销旧手机或测试模拟器：

```bash
httpcapture control revoke --device-id device-...
```

撤销后，该 APK 旧配置不能继续调用控制接口，需要重新扫码。

## 6. Debug APK 抓 HTTPS

HTTP Capture 本身不绕过 Android 安全策略。自研业务 App 如需抓 HTTPS，应只在 Debug 构建接入 `debug-trust`，让 Debug APK 信任用户安装的 CA。

边界：

- 安装对应 CA 后，接入 Debug Trust 的 Debug APK 可抓遵循系统信任策略的 HTTPS。
- Release 构建不要接入 Debug Trust。
- 不绕过 Certificate Pinning。
- 不关闭 hostname 校验。
- 不使用 TrustAll。
- 未接入的第三方 App：HTTP 尽力转发；HTTPS 是否可解密不保证。

## 7. Charles 使用方式

只想继续使用 Charles 查看请求时：

1. 在 Charles 中开启代理，默认 `8888`。
2. 从 Charles 导出 CA 公钥证书，使用 DER/PEM/CER，不要使用 `.p12/.pfx`。
3. 普通配对：

   ```bash
   httpcapture pair --engine charles --host 192.168.1.10 --port 8888 --cert charles-ca.cer
   ```

如需 CLI 控制 Charles Recording，需要先在 Charles 的 `Proxy Settings > Web Interface` 启用 Web Interface：

```bash
httpcapture serve --engine charles
```

Charles 模式默认不清空当前会话；只有用户显式使用 `--clear` 时才允许清空，并且清空前先备份。

## 8. 构建

Debug 构建：

```bash
./gradlew --no-parallel --max-workers=1 :app:assembleDebug :sample-client:assembleDebug :debug-trust:assemble
cd cli && go test ./...
```

Linux AMD64 CLI：

```bash
cd cli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o bin/httpcapture-linux-amd64 .
```

Release APK 只生成：

- `app-armeabi-v7a-release.apk`
- `app-arm64-v8a-release.apk`

正式签名密钥和密码不得进入 Git。使用仓库外的 `key.properties` 或环境变量注入。

## 9. 当前限制

- 当前不做 Rewrite、Breakpoint、Map Local、请求重放。
- APK 不保存请求内容。
- 多 App 同时抓包时，CLI 只保存本次包名集合，不声明逐请求归属。
- Proxify/Martian 会将客户端 HTTP/2 降级为 HTTP/1.1。
- SSE/长响应可能被代理内核缓冲。
- WebSocket 实测不能可靠透传，当前不承诺支持。
- UDP/QUIC 不是当前目标。
- Web 查看器只监听本机回环地址，不能开放给局域网。
