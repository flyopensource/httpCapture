# HTTP Capture

HTTP Capture 是面向日常 Android 开发的 Charles 抓包助手。它不替代 Charles，也不在手机里保存请求内容；Android App 通过按应用 `VpnService` 把所选应用的流量转发到电脑上的 Charles。

仓库包含：

- `app`：原生 Kotlin + Compose 控制 App，最低 Android 8.0（API 26）。
- `debug-trust`：仅供业务 App 的 Debug 构建接入的用户 CA 信任 AAR，最低 API 21。
- `sample-client`：HTTP/HTTPS 联调样例。
- `cli`：纯 Go 配对、Charles 录制控制与导出过滤工具。
- `native`：固定版本 tun2proxy 的 Android 构建脚本和安全补丁。

## 快速开始

1. 在 Charles 开启 Proxy，并确认端口（默认 `8888`）。
2. 导出 Charles CA 公钥证书，或使用 Charles 默认 CA 文件。
3. 在电脑生成二维码：

   ```bash
   cd cli
   go build -o bin/httpcapture .
   bin/httpcapture pair --host 192.168.1.10 --port 8888 --out pair.png
   ```

4. 在 Android App 中扫码，按提示安装 CA，然后选择一个或多个应用。
5. 首次点击“开始抓包”确认系统 VPN 权限。以后可直接使用 App 或快捷磁贴启停。

Android 11 及更高版本禁止普通 App 直接安装 CA。HTTP Capture 会把证书写到 `Downloads/HTTP Capture` 并打开系统安全设置；用户仍需在系统的“安装 CA 证书”页面选择该文件。这是 Android 平台限制，不是 App 权限缺失。

## 构建

```bash
./gradlew --no-parallel --max-workers=1 :app:assembleDebug :sample-client:assembleDebug :debug-trust:assemble
cd cli && go test ./...
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

Charles 会话、真实 CA、配对二维码和导出数据也属于本地开发产物，不应进入公开仓库。完整约束见 [REQUIREMENTS.md](REQUIREMENTS.md#11-开源与构建签名要求)。

## 能力边界

- 接入 `debug-trust` 的 Debug APK：安装有效 Charles CA 后，可抓遵循 Android 系统信任策略的 HTTPS。
- 未接入的第三方 APK：HTTP 尽力转发；HTTPS 是否可解密不作保证。
- 证书锁定、自带证书库、Cronet 特殊配置等不在首版兼容范围。
- 多 App 同时抓包时，Charles 导出数据不能可靠判断每条请求属于哪个 Android 包；CLI 只保存本次应用集合标签。
- UDP/QUIC 不是首版目标；HTTPS 客户端通常会回退到 TCP。

完整产品约束见 [REQUIREMENTS.md](REQUIREMENTS.md)。
