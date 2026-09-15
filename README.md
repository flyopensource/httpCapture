# HTTP Capture

HTTP Capture 是面向日常 Android 开发的抓包代理接入工具。它不在手机里实现 MITM 或保存请求内容；Android App 通过按应用 `VpnService`，把所选应用的流量转发到电脑上的内嵌 Proxify、Charles、mitmproxy 或自定义 HTTP Proxy。

仓库包含：

- `app`：原生 Kotlin + Compose 控制 App，最低 Android 8.0（API 26）。
- `debug-trust`：仅供业务 App 的 Debug 构建接入的用户 CA 信任 AAR，最低 API 21。
- `sample-client`：HTTP/HTTPS 联调样例。
- `cli`：纯 Go 单文件工具，内嵌 Proxify，负责配对、代理进程、抓包会话、SQLite/FTS5 索引、本地 Web 查看、结构化批量导出、HAR 导出和 Charles 兼容。
- `native`：固定版本 tun2proxy 的 Android 构建脚本和安全补丁。

## 快速开始

1. 给 Linux AMD64 CLI 增加执行权限并启动内嵌 Proxify，不需要安装 Go、Python 或额外代理程序：

   ```bash
   chmod +x cli/bin/httpcapture-linux-amd64
   ./cli/bin/httpcapture-linux-amd64 proxy proxify start
   ```

2. 生成配对二维码；Proxify 是默认引擎，`--host` 可省略并自动检测局域网 IPv4：

   ```bash
   ./cli/bin/httpcapture-linux-amd64 pair \
     --host 192.168.1.10 \
     --out pair.png
   ```

3. 保持 CLI 运行，在 Android App 中扫码；App 会自动下载并校验代理配置和 CA，成功后 CLI 自动退出。
4. 按提示安装 CA，然后选择一个或多个应用。
5. 在电脑执行 `httpcapture record start --package com.example.app`，命令会保持前台；然后在 APK 中开始抓包。
6. 完成后在前台按 `Ctrl+C` 正常停止/归档，或从另一终端执行 `httpcapture record stop`。会话目录包含 `meta.json`、`traffic.jsonl` 和 `session.har`。
7. 执行 `httpcapture web`，在只监听本机的页面中搜索、查看和批量导出 Proxify 请求；也可执行 `httpcapture serve`，保持本地 Web 与会话状态前台可见。

继续使用 Charles 时无需由 CLI 启动代理，导出 Charles CA 后执行 `httpcapture pair --engine charles`。mitmproxy 作为备用引擎继续保留。

配对二维码只包含有效约 3 分钟的一次性局域网地址和配置包指纹，不包含完整 CA，因此终端显示更小。手机与电脑需要处于可互相访问的局域网；扫码页固定为竖屏。

`pair` 执行后不会立即退出，这是正常行为：CLI 正在提供一次性配置下载服务。APK 导入成功后 CLI 会自动退出；也可以按 `Ctrl+C` 取消。完整参数、证书查找规则和常见问题见 [CLI 使用文档](cli/README.md)。

Android 11 及更高版本禁止普通 App 直接安装 CA。HTTP Capture 会把证书写到 `Downloads/HTTP Capture` 并打开系统安全设置；用户仍需在系统的“安装 CA 证书”页面选择该文件。这是 Android 平台限制，不是 App 权限缺失。

## 构建

```bash
./gradlew --no-parallel --max-workers=1 :app:assembleDebug :sample-client:assembleDebug :debug-trust:assemble
cd cli && go test ./...
```

Linux AMD64 发布版 CLI：

```bash
cd cli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o bin/httpcapture-linux-amd64 .
```

`app` 的 Release 只生成两个独立 APK，不生成 Universal 或 x86_64 Release APK：

- `app-armeabi-v7a-release.apk`
- `app-arm64-v8a-release.apk`

Debug 仍生成包含 `armeabi-v7a`、`arm64-v8a` 和 `x86_64` 的 Universal APK，保留 Android 模拟器调试能力。

重新构建转发内核需要 Rust、`cargo-ndk` 和 Android NDK：

```bash
./native/build-tun2proxy.sh
```

脚本固定使用 tun2proxy `v0.8.3` / `e271de19683937f23d3f8f0eb4df0a61fc4a6e50`，并应用 Android 停止 VPN 时不得结束整个 App 进程的补丁。

## 开源与签名安全

仓库不保存任何正式 Android 签名密钥或密码。Release 密钥应放在仓库目录之外，签名配置从本地 `key.properties`、环境变量或 CI 加密 Secret 注入；不得把 `.jks`、`.keystore`、`.p12/.pfx`、`key.properties`、`keystore.properties` 或 `signing.properties` 强制加入 Git。

本地构建可以在项目根目录创建已忽略的 `key.properties`：

```properties
storeFile=/path/outside/repository/httpcapture-release.jks
storePassword=从密码管理器读取
keyAlias=httpcapture-release
keyPassword=从密码管理器读取
```

CI 可以改用以下环境变量，不提供仓库内默认 Release 密钥：

```bash
export HTTPCAPTURE_SIGNING_STORE_FILE=/path/outside/repository/httpcapture-release.jks
export HTTPCAPTURE_SIGNING_STORE_PASSWORD='从密码管理器读取'
export HTTPCAPTURE_SIGNING_KEY_ALIAS=httpcapture-release
export HTTPCAPTURE_SIGNING_KEY_PASSWORD='从密码管理器读取'
./gradlew --no-parallel --max-workers=1 :app:assembleRelease
```

四项签名变量必须同时提供。正式构建后应使用 Android SDK 的 `apksigner verify --verbose --print-certs <apk>` 检查签名及证书指纹。

提交前还应检查待提交文件和 Git 历史。`.gitignore` 不能保护已经提交过的密钥；一旦密钥进入历史，应立即停止使用并轮换，而不是只删除当前文件。

代理会话、真实 CA、配对二维码和导出数据也属于本地开发产物，不应进入公开仓库。

## 能力边界

- 接入 `debug-trust` 的 Debug APK：安装当前代理的有效 CA 后，可抓遵循 Android 系统信任策略的 HTTPS。
- 未接入的第三方 APK：HTTP 尽力转发；HTTPS 是否可解密不作保证。
- 证书锁定、自带证书库、Cronet 特殊配置等不在首版兼容范围。
- 多 App 同时抓包时，代理数据不能可靠判断每条请求属于哪个 Android 包；CLI 只保存本次应用集合标签。
- 当前内嵌 Proxify 会把 HTTP/2 客户端连接降级为 HTTP/1.1；UDP/QUIC 不是当前目标，HTTPS 客户端通常会回退到 TCP。
- SSE/长响应可能被缓冲，WebSocket 实测不能可靠透传；当前版本不支持这两类流量。

V0.4 继续使用配对协议 v3。开发阶段不兼容旧的本地代理配置，升级 APK 与 CLI 后应重新扫码。

当前 `serve` 仅提供本机 Web 和前台会话管理；本地 Web 已支持对已写完 JSONL 事务的实时增量索引与页面刷新。APK→CLI 直接控制和配对 v4 尚未实现，不能将 APK 的 VPN 状态当成电脑端受管会话已经开启。
