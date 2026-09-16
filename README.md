# HTTP Capture

HTTP Capture 是面向日常 Android 开发的抓包代理接入工具。它不在手机里实现 MITM 或保存请求内容；Android App 通过按应用 `VpnService`，把所选应用的流量转发到电脑上的内嵌 Proxify、Charles、mitmproxy 或自定义 HTTP Proxy。

仓库包含：

- `app`：原生 Kotlin + Compose 控制 App，最低 Android 8.0（API 26）。
- `debug-trust`：仅供业务 App 的 Debug 构建接入的用户 CA 信任 AAR，最低 API 21。
- `sample-client`：HTTP/HTTPS 联调样例。
- `cli`：纯 Go 单文件工具，内嵌 Proxify，负责配对、代理进程、抓包会话、SQLite/FTS5 索引、本地 Web 查看、结构化批量导出、HAR 导出和 Charles 兼容。
- `native`：固定版本 tun2proxy 的 Android 构建脚本和安全补丁。

最新完整用法见 [USAGE.md](USAGE.md)；CLI 参数细节见 [cli/README.md](cli/README.md)。

## 快速开始

推荐使用 V0.4 的 APK 一键联动流程。电脑端保持 `serve` 前台运行，手机端扫码后，APK 主按钮和快捷磁贴可以直接创建、停止并归档电脑端抓包会话。

1. 给 Linux AMD64 CLI 增加执行权限：

   ```bash
   chmod +x cli/bin/httpcapture-linux-amd64
   ./cli/bin/httpcapture-linux-amd64 version
   ```

2. 在电脑启动前台服务并生成 v4 配对二维码；CLI 会自动识别局域网 IP：

   ```bash
   ./cli/bin/httpcapture-linux-amd64 serve
   ```

   `serve` 会同时提供本地 Web 查看器和受认证手机控制接口；本地 Web 只监听 `127.0.0.1`。

3. APK 扫码导入，自动下载代理配置和 CA，按提示安装 CA。
4. 选择一个或多个目标 App；支持按应用名、包名模糊搜索。
5. 在 APK 点“开始抓包”或使用快捷磁贴。
6. 停止后，在本机 Web 查看、搜索和导出：

   ```bash
   ./cli/bin/httpcapture-linux-amd64 web
   ```

   会话目录为 `~/httpcapture-sessions/<captureId>/`，包含 `meta.json`、`traffic.jsonl` 和 `session.har`。

继续使用 Charles 时无需由 CLI 启动代理，导出 Charles CA 后执行 `httpcapture pair --engine charles`。mitmproxy 作为备用引擎继续保留。

如果二维码在终端显示太大或错位，可以关闭终端二维码，只使用 PNG：

```bash
./cli/bin/httpcapture-linux-amd64 serve \
  --terminal-qr never \
  --out pair.png
```

如果电脑有多个网卡、VPN、Docker 网卡，自动识别的 IP 不对，再用 `--control-host 192.168.1.10` 手动覆盖。

普通 `pair` 仍可用于只导入代理和 CA 的场景，但它不具备 APK 控制 CLI 会话能力。`pair` 执行后不会立即退出，这是正常行为：CLI 正在提供一次性配置下载服务。APK 导入成功后 CLI 会自动退出；也可以按 `Ctrl+C` 取消。完整参数、证书查找规则和常见问题见 [CLI 使用文档](cli/README.md)。

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

V0.4 已新增 `serve` 默认生成的配对协议 v4。开发阶段不兼容旧的本地代理配置，升级 APK 与 CLI 后应重新扫码。

当前 `serve` 默认提供本机 Web、自动识别局域网 IP、生成 APK 配对二维码，并开放受认证的手机控制 HTTPS；只想查看本机 Web 时使用 `--no-pair`。APK 导入 v4 配置后，主按钮和快捷磁贴会先通知 CLI 创建记录会话，再启动 VPN，并在停止时先停 VPN 再通知 CLI 归档。联动失败不会静默降级，用户需要在 APK 中显式选择“仅启动 VPN”。
