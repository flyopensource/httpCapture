# httpCapture 需求文档

- 文档版本：`0.3`
- 状态：首版需求基线
- 更新日期：`2026-09-12`
- 产品范围：Android APK、Android Debug Trust SDK、桌面端配对 CLI
- 发布方式：公开源代码仓库

## 1. 背景

Android 手机使用 Charles 抓包时，通常需要进入当前 Wi-Fi 的网络设置，手动填写电脑 IP 和 Charles 端口；停止抓包后还需要手动关闭代理。更换电脑或者 Charles 根证书后，还需要重新配置连接及证书。

httpCapture 的目标是简化这个过程：通过电脑端 CLI 生成二维码，Android App 扫码导入 Charles 配置和 CA 公钥证书，选择一个或多个目标 App 后，一键开始或停止抓包。

## 2. 产品目标

### 2.1 核心目标

1. 不再手动修改 Android Wi-Fi HTTP 代理。
2. 不依赖 root、ADB 授权或设备所有者权限。
3. 使用 Android `VpnService`，只转发用户选择的一个或多个 App。
4. 将目标 App 的网络流量转发到电脑端 Charles。
5. 支持多台 Charles 电脑及不同 CA 证书的导入和更新。
6. 自研 APK 接入 Debug Trust SDK 后，可以通过 Charles 调试 HTTPS。
7. 日常重复抓包时，尽量做到一次点击即可开始或停止。

### 2.2 非目标

首版不开发另一个 Charles，也不在手机端实现以下功能：

- 请求和响应查看器
- 手机端 HTTPS MITM 引擎
- Rewrite、Breakpoint、Map Local、脚本
- 请求重放或 API 测试
- HAR 会话管理
- 绕过 Certificate Pinning
- 修改或注入第三方 APK
- iOS 客户端

## 3. 系统组成与职责

```text
电脑端 httpcapture-cli
    │
    │ 短二维码：一次性下载地址、会话令牌、配置包 SHA-256
    ▼
httpCapture Android APK
    ├── 导入和更新 Charles 配置
    ├── 引导安装 CA 到 Android 用户证书区
    ├── 选择一个或多个目标 App
    └── 通过 VpnService 将目标流量转发到 Charles
             │
             ▼
        电脑端 Charles
             │
             ▼
          目标服务器

自研业务 APK
    └── 仅在 Debug 构建中接入 debug-trust SDK，信任用户 CA
```

### 3.1 httpCapture Android APK

负责 Charles 配置、证书导入、目标 App 选择、VPN 授权、流量转发以及运行状态管理。

### 3.2 Debug Trust SDK

负责让可控制源码的 Debug APK 信任 Android 用户证书区中的 Charles CA。SDK 不负责 VPN、代理转发或 Charles 配置管理。

### 3.3 桌面端 CLI

负责读取或接收 Charles 的连接信息和 CA 公钥证书，将配对数据编码为二维码。CLI 不保存、不读取、不传输 Charles CA 私钥。

## 4. 核心用户流程

### 4.1 首次连接一台 Charles 电脑

1. 用户启动电脑端 Charles。
2. 用户执行 `httpcapture-cli pair`。
3. CLI 获取或提示用户选择电脑 IP、Charles 端口及 CA 公钥证书。
4. CLI 在选定的局域网 IPv4 上启动一次性临时配对服务，并在终端显示短二维码，也可输出二维码图片。
5. 用户打开 httpCapture APK 扫描二维码；扫码页面固定为竖屏，不得忽略系统竖屏锁定而强制横屏。
6. App 自动从临时服务下载配置和 CA，校验整个配置包的 SHA-256 后保存，不要求用户打开浏览器或手动下载文件。
7. App 完成导入后确认本次会话，CLI 立即关闭临时服务；未完成的会话默认 3 分钟失效。
8. 如果 CA 尚未安装：Android 8–10 调起系统证书安装器；Android 11+ 将 CA 文件写入系统 Downloads 集合并打开安全设置，由用户在系统设置中确认安装。
9. 用户完成系统确认。
10. 用户选择一个或多个需要抓包的 App。
11. 用户首次启动时完成 Android VPN 授权。
12. App 将所选 App 的流量转发到 Charles。

### 4.2 日常重复抓包

1. 用户启动 Charles。
2. httpCapture 使用上次的 Charles 配置和 App 选择。
3. 用户点击“开始抓包”。
4. 使用结束后点击“停止抓包”。

### 4.3 更换电脑或更新 Charles CA

1. 在目标电脑执行 `httpcapture-cli pair`。
2. App 扫描新二维码。
3. App 比较已有配置的 IP、端口和 CA 指纹。
4. 仅 IP 或端口变化时，直接更新连接配置。
5. CA 变化时，保存新证书并调起系统证书安装界面。
6. CA 未变化时，不重复要求安装。

## 5. Android APK 功能需求

### 5.1 技术形态

- 原生 Android 应用
- Kotlin
- Jetpack Compose UI
- 最低支持 Android 8.0（API 26）
- 使用 Android `VpnService`
- VPN 转发核心可以使用成熟的 TUN 到 HTTP Proxy 实现
- 不使用 Flutter
- 不要求 root 或日常 ADB 操作
- Release 分别生成 `armeabi-v7a` 和 `arm64-v8a` 两个独立 APK，不发布 Universal 或 `x86_64` Release APK
- Debug 构建保留 `x86_64`，用于 Android 模拟器调试

### 5.2 Android 快捷磁贴

首版应提供 Android 下拉控制中心快捷磁贴，名称为“Charles 抓包”或“httpCapture”。

快捷磁贴行为：

- 未运行时点击：使用上次选中的 Charles 配置和目标 App 开始抓包。
- 运行中点击：停止抓包并关闭 VPN。
- 未完成 VPN 授权、未配置 Charles 或未选择目标 App 时：打开 httpCapture 主界面完成必要配置。
- 长按磁贴：打开 httpCapture 主界面。
- 磁贴应区分未运行、连接中、运行中和错误状态。
- 磁贴状态必须与实际 VPN 服务状态一致，不能只保存一个可能过期的本地开关值。

### 5.3 Charles 配置管理

每个 Charles 配置至少包含：

| 字段 | 说明 |
| --- | --- |
| 配置 ID | App 内部唯一标识 |
| 电脑名称 | 例如“公司电脑”“家里电脑” |
| Host/IP | 手机可以访问的电脑地址 |
| 端口 | Charles HTTP Proxy 端口，默认 `8888` |
| CA 公钥证书 | 从二维码导入的公开证书，不包含私钥 |
| CA SHA-256 | 用于识别证书是否变化 |
| CA 有效期 | 用于过期提醒 |
| 安装状态 | 对应 CA 是否存在于 Android 用户证书区 |
| 最近连接状态 | 最近一次检测结果及时间 |

App 应支持：

- 新增、编辑、删除和切换 Charles 配置
- 保存多台电脑配置
- 启动前检测 Charles IP 和端口
- 区分地址变化与 CA 变化
- CA 相同时只更新 IP 或端口
- CA 变化时要求安装新证书
- 用户可以在 APK 内直接修改现有配置的 IP/Host 和端口，修改时保留电脑名称、CA 和配置 ID
- IP/Host 不得包含协议、路径或空格，端口必须为 `1..65535`；抓包运行中不得修改当前目标地址
- 配置删除后不自动删除系统 CA
- 提供 Android 系统证书管理入口

### 5.4 二维码导入

App 应提供“扫描电脑”入口，并完成：

1. 解析二维码协议版本。
2. 校验一次性临时下载地址、会话令牌和配置包 SHA-256。
3. 自动通过局域网 HTTP 下载配置包，禁止跳转，并限制连接时间、读取时间和最大响应大小。
4. 重新计算整个配置包的 SHA-256，必须与二维码中的值一致。
5. 解析 CA 公钥证书并重新计算 CA SHA-256 指纹。
6. 校验证书指纹、CA 用途、有效期及基本格式。
7. 保存配置后向 CLI 确认成功，使一次性临时服务立即关闭。
8. 展示电脑名称、IP、端口、证书名称、有效期和指纹。

二维码不携带完整 CA，因此应保持较低数据密度。首版只支持 v2 临时配对协议。配对不依赖云服务；手机和电脑必须位于可互相访问的局域网。扫码页面固定为竖屏，进入和退出扫码页面不得出现强制横屏跳转。

### 5.5 Charles CA 导入与更新

App 支持通过以下方式获得 CA：

- 扫描 CLI 生成的二维码
- App 内系统文件选择器
- 从文件管理器使用“通过 httpCapture 打开”

支持的证书格式：

- DER 编码的 `.cer`、`.crt`
- PEM 编码的 `.pem`、`.cer`、`.crt`

不支持：

- `.p12`
- `.pfx`
- 任何包含 CA 私钥的文件

证书不能只保存在 httpCapture 的私有目录中。为了让其他 Debug APK 信任该 CA，最终必须由 Android 系统将 CA 安装到用户证书区。Android 8–10 可通过 `KeyChain.createInstallIntent()` 进入安装器；Android 11+ 禁止普通 App 用该 Intent 安装 CA，因此 App 应通过 `MediaStore.Downloads` 输出证书、直接打开系统安全设置，并明确提示用户选择刚写出的文件。

受 Android 安全限制，普通 App 不能静默安装或删除系统 CA。每个新 CA 第一次安装时，必须由用户完成系统确认。

不同电脑的 Charles CA 可以同时安装。切换已配对电脑时，如果对应 CA 已安装，则不需要重新安装。

### 5.6 目标 App 选择

App 应展示已安装应用的：

- 图标
- 应用名称
- 包名

并支持：

- 按名称或包名搜索
- 选择一个或多个 App
- 仅将选中的 App 加入 VPN 允许列表
- 保存上次选择
- 已卸载 App 自动从选择结果中移除
- 没有选择任何 App 时禁止开始抓包
- 打开应用选择器时，应用枚举、名称和图标读取必须在后台线程执行，并在完成前显示 Loading
- 加载失败时显示原因和重试入口；加载完成但搜索无结果时显示空状态

首版不提供全机抓包模式，避免影响无关应用。

### 5.7 VPN 启停

主界面提供一个明确的主操作按钮：

- 未运行：`开始抓包`
- 连接中：`正在连接 Charles`
- 运行中：`停止抓包`
- 异常：显示可读的具体原因

启动前检查：

1. 已选择至少一个目标 App。
2. 已选择有效的 Charles 配置。
3. Charles Host/IP 和端口可连接。
4. 已获得 Android VPN 授权，或者立即发起授权。
5. 检查关联 CA 状态。

CA 未安装不应阻止 HTTP 抓包，但必须提示 HTTPS 可能无法解密，并提供证书安装入口。

### 5.8 VPN 流量范围

首版目标范围：

- 指定 App 分流
- IPv4
- TCP
- HTTP
- HTTPS CONNECT 隧道
- DNS 域名映射

首版暂不承诺：

- 普通 UDP 转发
- HTTP/3 / QUIC
- IPv6
- 与其他 VPN 同时运行

不支持的流量不得在界面显示“已成功抓取”。如果 QUIC 无法使用，可以由目标网络库自行回退到 HTTP/2 或 HTTP/1.1；不能回退时，应在使用说明中明确兼容边界。

### 5.9 异常恢复

- Charles 启动前不可连接：不建立 VPN，保留手机正常网络。
- Charles 运行中断开：停止或明确关闭当前抓包状态，不允许界面继续显示正在抓包。
- VPN 服务异常退出：关闭 TUN，恢复目标 App 正常联网。
- 不允许在显示“正在抓包”时静默改为直连。
- App 被系统回收后应能在再次打开后显示真实状态，而不是使用过期状态。

## 6. Debug Trust SDK 需求

### 6.1 接入方式

Debug Trust SDK 最低支持 Android API 21。

目标应用仅在 Debug 构建中接入：

```kotlin
dependencies {
    debugImplementation("com.example.httpcapture:debug-trust:1.0.0")
}
```

SDK 应以 Android AAR 形式提供，不要求业务代码调用初始化方法。

### 6.2 SDK 职责

- Debug APK 保留 Android 系统 CA 信任。
- Debug APK 同时信任 Android 用户安装 CA。
- 使用 Android Network Security Configuration 的 `debug-overrides`。
- 不包含固定 Charles CA。
- 不保存或读取 Charles IP、端口。
- 不与 httpCapture APK 交换业务请求数据。
- 不设置系统代理，不创建 VPN。
- 不关闭主机名校验。
- 不使用接受任意证书的 TrustManager。
- 不采集、记录或上传数据。

### 6.3 Release 安全要求

- Release APK 不得依赖 Debug Trust SDK。
- Release APK 不得因为 SDK 而信任 Android 用户 CA。
- 提供验证最终 Manifest 和 Network Security Configuration 的说明或检查任务。
- 如果业务 App 已有 `networkSecurityConfig`，不得静默覆盖；必须提供明确的合并方法。

### 6.4 HTTPS 兼容范围

首版承诺支持遵循 Android Network Security Configuration 的网络客户端，例如：

- `HttpsURLConnection`
- 使用默认系统信任配置的 OkHttp
- 基于默认 OkHttp 的 Retrofit
- 遵循系统证书策略的 WebView 请求

首版不承诺支持：

- Certificate Pinning
- 自定义 `X509TrustManager`
- 使用独立 CA 证书库的网络客户端
- 不遵循 Android Network Security Configuration 的原生 TLS 库
- 第三方 Release APK

## 7. 桌面端 CLI 需求

### 7.1 技术选型

- CLI 使用 Go 开发。
- 保持纯 Go 实现，避免依赖 CGO 和本机 C/C++ 工具链。
- 配对协议保持语言无关，不与 Android 或 VPN 内核代码耦合。
- 发布单文件可执行程序，不要求目标电脑预装 Go 运行环境。
- 首版只发布 Linux `amd64` 单文件版本；Windows、macOS 和其他架构留作后续规划。
- 多平台产物应通过 CI 在对应平台执行基础测试；仅交叉编译成功不能替代目标系统验证。

选择 Go 的原因是 CLI 主要处理网络接口、X.509、公钥证书、二维码、临时 HTTP 服务和文件操作，这些能力可以保持纯 Go。Go 官方工具链适合生成独立命令行程序，并为未来扩展其他桌面平台保留空间。

### 7.2 核心命令

```bash
httpcapture-cli pair
```

允许手动覆盖自动检测结果：

```bash
httpcapture-cli pair \
  --name "公司电脑" \
  --host 192.168.3.114 \
  --port 8888 \
  --cert ./charles-ca.cer
```

### 7.3 CLI 职责

1. 获取或提示选择手机可以访问的本机 IP。
2. 读取或接收 Charles HTTP Proxy 端口。
3. 检查 Charles 端口是否处于监听状态。
4. 调用 Charles 官方导出能力，或读取用户指定的 CA 公钥文件。
5. 解析 CA 并计算 SHA-256 指纹。
6. 生成安全随机的一次性会话令牌，在选定 IPv4 上启动临时 HTTP 服务，仅提供本次配置包。
7. 将临时下载地址和整个配置包的 SHA-256 编码进短二维码，CA 公钥证书不直接写入二维码。
8. 在终端显示二维码，并可选输出二维码 PNG 文件。
9. 收到 App 成功确认后立即关闭服务；未完成时默认 3 分钟过期，CLI 退出时立即失效。

临时服务不得监听未指定的所有网络接口，不传输 CA 私钥，不允许 HTTP 跳转。会话令牌必须由密码学安全随机源生成；App 必须先下载、校验并保存配置，才能发送成功确认。配置包是公开 CA 和开发代理地址，不要求 TLS，但完整性必须由二维码中的 SHA-256 保证。

终端二维码必须使用紧凑字符渲染并检测当前终端宽高。只有二维码可以完整显示时才自动输出；空间不足或标准输出不是交互式终端时不得输出会换行、截断的二维码，应保留高清 PNG 并提示用户打开文件。CLI 同时提供强制显示和禁用终端二维码的参数。

### 7.4 建议参数

| 参数 | 说明 |
| --- | --- |
| `--name` | 电脑配置名称 |
| `--host` | 手机可访问的本机 IPv4，同时作为 Charles 地址和临时服务监听地址 |
| `--port` | Charles 代理端口 |
| `--cert` | 手动指定 CA 公钥证书 |
| `--serve-port` | 临时配对服务端口，`0` 表示自动选择 |
| `--timeout` | 临时配对服务有效时间，默认 `3m` |
| `--out` | 输出二维码图片路径 |
| `--terminal-qr` | 终端二维码显示模式：`auto`、`always` 或 `never` |
| `--uri-out` | 将配对 URI 写入文件，仅用于自动化测试 |

### 7.5 Charles 记录控制与会话导出

CLI 应通过 Charles Web Interface 控制 Charles，不自行实现抓包或 HTTP/HTTPS 解析引擎。

Charles 侧前置条件：

- Charles 已启动。
- Charles Web Interface 已启用。
- CLI 可以通过本机 Charles Proxy 访问 `http://control.charles/`。
- 如果 Web Interface 配置了身份验证，CLI 应通过安全配置或交互式输入获取凭据，不在命令行历史和普通日志中输出密码。

CLI 应提供以下命令：

```bash
# 查看 Charles 及记录状态
httpcapture-cli charles status

# 开始一次抓包记录，并保存开始时间和应用标签
httpcapture-cli record start \
  --app com.example.app \
  --client-ip 192.168.3.120

# 停止记录并导出原始会话和通用格式
httpcapture-cli record stop \
  --output-dir ./captures \
  --format chls,har

# 从已有导出数据中按时间和手机客户端 IP 筛选
httpcapture-cli export \
  --input ./captures/session.xml \
  --from-ms 1789192800000 \
  --to-ms 1789193400000 \
  --client-ip 192.168.3.120 \
  --format har \
  --output ./captures/filtered.har
```

CLI 应支持调用 Charles 的以下能力：

- 开始记录
- 停止记录
- 查询当前记录状态
- 导出 XML、JSON 或 HAR
- 下载并保存 Charles 原生 `.chls` 会话
- 在用户明确要求时清空当前会话

`.chls` 用于完整保留和重新打开原始 Charles 会话；XML、JSON 或 HAR 用于程序筛选、分析和交换。

### 7.6 抓包元数据与过滤

每次由 CLI 发起的记录至少保存以下元数据：

```json
{
  "captureId": "<UUID>",
  "packages": ["com.example.app"],
  "startTimeMillis": 1789192800000,
  "endTimeMillis": 1789193400000,
  "clientIp": "192.168.3.120",
  "charlesProfile": "公司电脑"
}
```

时间字段统一使用 Unix 毫秒时间戳。CLI 可以依据 Charles 导出数据中的 `startTimeMillis`、`responseTimeMillis`、`endTimeMillis` 和 `clientAddress`，按时间段及手机客户端 IP 过滤。

Charles 会话本身不包含 Android 包名，因此首版按 App 处理遵循以下规则：

- 单 App 抓包：CLI 可以将该时间段和手机 IP 对应的会话按该包名归档。
- 多 App 抓包：CLI 只记录本次选择的包名集合，不声称能够判断每条请求具体来自哪个 App。
- `--app` 参数是抓包标签和归档依据，不是从 Charles 会话中识别出的字段。
- 如果同一时间段内电脑或其他设备也通过 Charles 发送流量，CLI 应优先使用手机客户端 IP 过滤。

精确到“多 App 同时抓包时每条请求所属包名”不属于首版。后续实现需要 VPN 层建立 Android UID、原始连接和 Charles 客户端连接之间的映射，再将映射数据同步给 CLI。

### 7.7 会话清理安全要求

CLI 默认不得清空 Charles 当前会话，因为当前会话可能包含尚未保存的用户数据。

只有显式执行以下操作时才允许清空：

```bash
httpcapture-cli record start --clear
```

使用 `--clear` 时应先将当前会话自动备份为 `.chls`；备份失败时不得继续清空，除非用户再次明确指定强制清空。

### 7.8 CLI 安全要求

- 证书相关功能只处理 CA 公钥证书。
- 不导出、不读取、不保存 CA 私钥。
- 不接受 `.p12` 或 `.pfx`。
- 不将配对数据上传到云端。
- 二维码不得包含密码、Token、Cookie 或其他业务数据。
- 临时文件在命令结束后清理。
- 导出的 Charles 会话可能包含请求体、响应体、Cookie、Token 和个人数据，CLI 不得将这些内容自动上传或输出到普通终端日志。
- 抓包文件和元数据默认只允许当前系统用户读取；跨平台实现应采用各平台可用的最严格合理文件权限。
- Web Interface 密码不得写入二维码、抓包元数据或普通日志。

## 8. 二维码协议要求

二维码的逻辑数据至少包含：

```json
{
  "version": 1,
  "name": "公司电脑",
  "host": "192.168.3.114",
  "port": 8888,
  "certificateDer": "<CA 公钥证书>",
  "certificateSha256": "<SHA-256 指纹>"
}
```

实际编码应使用适合二维码容量的紧凑格式，例如压缩后二进制数据加 Base64URL。协议必须带版本号，以支持后续升级。

二维码本身不包含 CA 私钥。App 必须基于实际证书内容重新计算指纹，不能只相信二维码中的字符串字段。

## 9. 抓包能力边界

| 目标应用 | HTTP | HTTPS |
| --- | --- | --- |
| 已接入 Debug Trust SDK 的自研 Debug APK | 支持 | 支持，前提是 Charles CA 已安装且没有额外 Pinning |
| 未接入 SDK、但自身信任用户 CA 的 App | 尽力支持 | 可能支持，不作保证 |
| 不信任用户 CA 的第三方 App | 尽力支持 | 不保证解密，通常只能建立或观察 CONNECT/TCP 连接 |
| 使用 Certificate Pinning 的 App | 尽力支持 | 不支持绕过 Pinning |

## 10. 安全与隐私要求

- httpCapture 只负责转发，不在本地保存请求体、响应体、Cookie 或 Token。
- CLI 可以根据用户命令保存 Charles 导出的抓包文件；这些文件属于敏感调试数据，必须仅保存在用户指定的本地目录。
- App 明确显示 VPN 正在运行及当前 Charles 目标。
- 不允许后台静默开始抓包。
- CA 指纹、电脑名称和连接配置可以本地保存。
- CA 私钥禁止进入 CLI、二维码、Android App、SDK 或代码仓库。
- Debug Trust SDK 不得进入生产 Release 构建。
- 用户切换 Charles 配置前应能看到目标 IP、端口和证书指纹。

## 11. 开源与构建签名要求

### 11.1 开源仓库范围

- 项目源代码、构建脚本、公开协议说明和不含敏感信息的示例配置可以进入公开仓库。
- 正式开源前必须确定项目许可证，并核对 `tun2proxy` 等第三方源码、二进制和依赖的许可证及署名要求。
- 仓库不得包含真实抓包会话、配对二维码、开发机地址、内部服务地址、账号、密码、Token、Cookie 或其他业务数据。
- Charles CA 公钥虽然不是私钥，也属于开发环境配置；除专门制作且明确标注的测试夹具外，不提交真实 Charles CA 或配对产物。
- 发布前应执行密钥与敏感信息扫描，并检查 Git 历史；仅从最新文件中删除敏感信息不能消除历史泄漏。

### 11.2 Android 构建签名

- Release 签名私钥不得进入 Git，包括 `.jks`、`.keystore`、`.p12`、`.pfx`、`.pkcs12` 和独立私钥文件。
- 签名密码、Key Alias 及包含这些内容的本地属性文件不得写入源码、Gradle 脚本、示例文件、日志或构建产物。
- 本地 Release 构建应从仓库外的密钥路径和被 `.gitignore` 排除的本地属性文件，或从环境变量读取签名配置。
- CI/CD 只能从平台加密 Secret 注入签名材料；任务结束后清理临时密钥，不上传为普通构建产物，不在日志中打印路径以外的敏感内容。
- 仓库不得提供可用于正式发布的“默认签名密钥”。Debug 构建使用 Android 工具链在用户目录生成的默认 Debug Keystore，不复制到项目目录。
- 正式发布后应安全备份并持续使用同一发布证书；更换或丢失签名证书会影响已有用户升级。若使用应用商店托管签名，应另行记录密钥托管与轮换流程。
- `.gitignore` 只是最后一道防护。新增签名配置后，提交前仍必须确认待提交文件列表并执行敏感信息扫描。

### 11.3 抓包与本地产物

- CLI 导出的 `.chls`、HAR、XML、JSON、元数据文件及本地 `captures/` 目录默认不应提交。
- 文档和测试只能使用人工构造、脱敏且不含有效凭据的数据。
- 二维码截图或文本可能包含电脑地址和 CA，应作为本地产物处理，不进入公开仓库。

## 12. 首版验收标准

### 12.1 配对与证书

- CLI 能根据输入的 IP、端口和 CA 生成二维码。
- CLI 默认生成不含完整 CA 的 v2 短二维码，并保持运行等待 App 导入确认。
- App 能自动下载配置包并校验整个响应的 SHA-256；数据被修改时必须拒绝导入。
- App 确认成功后 CLI 立即关闭临时服务，未确认的服务在超时或 CLI 退出后失效。
- App 扫码后无需再次手动输入 IP 和端口。
- 手机锁定竖屏时，进入和退出扫码页面始终保持竖屏。
- App 能解析 CA、计算并展示 SHA-256 指纹。
- CA 未安装时，Android 8–10 能调起系统证书安装器；Android 11+ 能把证书写入 Downloads 并打开系统安全设置。
- 重复扫描相同 CA 时不重复要求安装。
- IP 或端口变化但 CA 不变时，只更新连接信息。
- CA 变化时能识别并要求安装新证书。
- App 能同时保存至少两台 Charles 电脑配置。
- App 可以修改当前 Charles 的 IP/Host 和端口，保存后 CA、名称和配置 ID 保持不变。
- 终端空间足够时，CLI 使用紧凑字符完整显示二维码且不发生自动换行。
- 终端空间不足或标准输出被重定向时，CLI 不输出残缺二维码，并明确提示用户打开已生成的 PNG。

### 12.2 App 选择与 VPN

- 用户可以搜索并选择一个或多个 App。
- 点击应用选择后立即显示 Loading；大量应用枚举期间界面仍可响应取消操作。
- 仅选中 App 的目标 TCP 流量进入 VPN。
- 未选中 App 的联网不受影响。
- 第一次启动时能完成 Android VPN 授权。
- 后续能够通过主界面一键开始和停止。
- 用户将快捷磁贴添加到控制中心后，可以使用上次配置直接开始和停止抓包。
- 配置不完整时点击快捷磁贴会进入主界面，不会启动无效 VPN。
- 快捷磁贴显示状态与实际 VPN 运行状态一致。
- Charles 不可连接时不会建立一个导致目标 App 断网的无效 VPN。
- 停止抓包后目标 App 恢复正常网络。

### 12.3 HTTP 与 HTTPS

- 选中的测试 App 发起 HTTP 请求时，Charles 可以看到请求。
- 示例 Debug APK 接入 SDK 并安装对应 Charles CA 后，HTTPS 请求正常完成。
- Charles 可以查看该 HTTPS 请求的 URL、请求头、请求体和响应内容。
- 未接入 SDK 的第三方 App 不作为 HTTPS 解密验收对象。
- 示例 Release APK 不信任用户 CA。

### 12.4 Charles 记录与过滤

- CLI 可以检测 Charles Web Interface 是否启用以及当前记录状态。
- CLI 可以调用 Charles 开始和停止记录，并记录准确的开始、结束 Unix 毫秒时间戳。
- CLI 可以下载并保存原始 `.chls` 会话。
- CLI 可以导出至少一种便于筛选的结构化格式，以及 HAR 格式。
- CLI 可以按开始时间、结束时间和手机客户端 IP 过滤会话。
- 单 App 记录能够使用 Android 包名命名和归档。
- 多 App 记录必须保存完整包名集合，并明确标注“不支持逐请求 App 归属”。
- 默认开始记录不会清空 Charles 现有会话。
- 使用 `--clear` 时会先备份现有 `.chls`；备份失败时不清空。
- 导出文件不会被自动上传，普通日志不会输出请求体、Cookie 或 Token。

### 12.5 开源发布

- Git 待提交内容中不存在签名私钥、签名密码、本地签名属性文件或正式 Charles 配置。
- 使用仓库外的本地签名材料可以生成 Release APK；没有本地签名材料时不应意外使用仓库内的公共正式密钥。
- CI 日志和公开构建产物不包含签名 Secret、私钥、真实抓包内容或配对数据。
- 第三方依赖许可证与署名清单已经核对，项目许可证已经确定并随源码发布。
- Release 构建只产生可发布的 `armeabi-v7a` 和 `arm64-v8a` 两个独立签名 APK；不产生 Universal 或 `x86_64` Release APK。

## 13. 待确认项

- 项目最终采用的开源许可证尚待确定。
- 首版二维码同时支持摄像头扫描和从相册识别。

## 14. 技术依据

- [Android VpnService](https://developer.android.com/reference/android/net/VpnService)
- [Android VPN 按应用分流](https://developer.android.com/develop/connectivity/vpn)
- [Charles SSL Certificates](https://www.charlesproxy.com/documentation/using-charles/ssl-certificates/)
- [Charles Command-line Tools](https://www.charlesproxy.com/documentation/tools/command-line-tools/)
- [Charles Web Interface](https://www.charlesproxy.com/documentation/using-charles/web-interface/)
- [Charles XML Session DTD](https://www.charlesproxy.com/dtd/charles-session-1_0.dtd)
- [Reqable Android Certificate Trust](https://github.com/reqable/android-certificate-trust)
- [tun2proxy](https://github.com/tun2proxy/tun2proxy)
- [Go 支持的 GOOS 和 GOARCH](https://go.dev/doc/install/source#environment)
