# httpcapture CLI

纯 Go、无 CGO 的桌面工具。当前只发布 Linux AMD64 单文件版本；Proxify 已编入同一个可执行文件，不需要安装 Go、Python 或外部 Proxify。

## 安装

```bash
chmod +x httpcapture-linux-amd64
sudo install -m 0755 httpcapture-linux-amd64 /usr/local/bin/httpcapture
httpcapture version
```

不希望安装到系统目录时，也可以直接执行 `./httpcapture-linux-amd64`。

## 内嵌 Proxify

启动、查询和停止默认代理：

```bash
httpcapture proxy proxify start
httpcapture proxy proxify status
httpcapture proxy proxify stop
```

默认监听 `0.0.0.0:8888`，每个请求或响应最多保存 4 MiB Body。可以调整监听地址、端口和上限：

```bash
httpcapture proxy proxify start \
  --host 0.0.0.0 \
  --port 8888 \
  --max-body-bytes 8388608
```

CA、私钥、进程状态、运行日志和实时 JSONL 保存在当前用户的 httpCapture 配置目录，敏感文件权限为 `0600`。`stop` 只结束由当前 CLI 启动且进程身份一致的 Proxify。

## 管理备用 mitmproxy

CLI 不内置 mitmproxy。先在系统中安装可执行文件 `mitmdump`，再运行：

```bash
httpcapture proxy mitm start
httpcapture proxy mitm status
httpcapture proxy mitm stop
```

默认监听 `0.0.0.0:8080`。也可指定监听地址、端口或二进制路径：

```bash
httpcapture proxy mitm start --host 0.0.0.0 --port 8080 --bin /usr/local/bin/mitmdump
```

CLI 将状态和 `mitmdump.log` 保存在当前用户配置目录，文件权限为 `0600`。状态记录包含 PID 和 Linux 进程启动标识；`stop` 只会停止由 httpcapture 启动且标识一致的进程，不会按名称批量结束用户自行启动的 mitmproxy。

## 配对

最简用法：

```bash
httpcapture pair
```

不指定 `--engine` 时使用 Proxify：CLI 自动选择局域网 IPv4、使用端口 `8888`，并读取内嵌 Proxify 生成的 CA。首次配对前先执行：

```bash
httpcapture proxy proxify start
httpcapture pair
```

Charles 用法需要指定 `--engine charles`，CLI 会查找：

- `~/.charles/ca/charles-proxy-ssl-proxying-certificate.cer`
- `~/.charles/ca/charles-proxy-ssl-proxying-certificate.pem`

需要覆盖自动结果时：

```bash
httpcapture pair \
  --engine charles \
  --host 192.168.1.10 \
  --port 8888 \
  --cert charles-ca.cer \
  --name dev-mac \
  --out pair.png
```

只接受 DER/PEM X.509 CA 公钥证书，拒绝可能包含私钥的 `.p12/.pfx`。

mitmproxy 用法：

```bash
httpcapture proxy mitm start
httpcapture pair --engine mitmproxy --host 192.168.1.10
```

mitmproxy 默认端口为 `8080`，默认 CA 为 `~/.mitmproxy/mitmproxy-ca-cert.cer`，也会尝试 `.pem`。如果 CA 尚不存在，先启动一次 `mitmdump` 让它生成证书。

自定义 HTTP Proxy 使用 `--engine custom`，并且必须显式提供 `--cert`。

### 配对过程

1. CLI 读取 CA，启动一次性局域网 HTTP 服务并显示短二维码。
2. 保持 CLI 运行，在 HTTP Capture APK 中点击“扫码导入”。
3. APK 自动下载、校验并保存代理类型、IP、端口和 CA；用户不需要打开浏览器或手动下载证书。
4. APK 回传成功确认后，CLI 显示“手机已确认导入，临时服务已关闭”并自动退出。

`pair` 在二维码生成后继续运行不是卡住。默认等待时间为 3 分钟；超时、按 `Ctrl+C` 或 CLI 进程退出后，二维码立即失效。每个二维码只允许下载一次，失败后应重新执行 `pair`。

终端二维码默认使用 `--terminal-qr auto`：CLI 会使用紧凑字符渲染并检测终端宽高，只有二维码能够完整显示时才输出；空间不足或输出被重定向时只保留 PNG，避免二维码换行损坏。可使用 `--terminal-qr always` 强制显示，或使用 `--terminal-qr never` 始终关闭终端二维码。PNG 保持清晰输出，不受终端尺寸影响。

### 配对参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--engine` | `proxify` | `proxify`、`charles`、`mitmproxy` 或 `custom` |
| `--host` | 自动检测 | 手机可以访问的本机 IPv4；同时作为代理地址和临时服务监听地址 |
| `--port` | 按引擎 | Proxify/Charles/custom 为 `8888`，mitmproxy 为 `8080` |
| `--cert` | 按引擎查找 | Proxify、Charles 或 mitmproxy CA；custom 必须指定，只支持 DER/PEM |
| `--name` | CA 名称 | APK 中显示的电脑配置名称 |
| `--out` | `httpcapture-pair.png` | 二维码 PNG 输出路径 |
| `--serve-port` | `0` | 临时服务端口；`0` 表示自动选择空闲端口 |
| `--timeout` | `3m` | 等待 APK 导入的最长时间，例如 `30s`、`5m` |
| `--terminal-qr` | `auto` | `auto`、`always` 或 `never` |
| `--uri-out` | 空 | 将配对 URI 写入文件，主要用于自动化测试 |

如果 Linux 防火墙阻止手机访问随机端口，可固定端口后只允许局域网访问，例如：

```bash
httpcapture pair --host 192.168.1.10 --serve-port 39001
```

常见失败：

- `无法自动确定局域网 IP`：使用 `--host` 指定手机能访问的电脑 IPv4。
- `启动临时配对服务 ... address already in use`：更换 `--serve-port`，或恢复默认值 `0`。
- APK 提示无法连接：确认手机和电脑在同一可互访网络，并检查防火墙或访客 Wi-Fi 隔离。
- APK 提示配置校验失败：二维码对应的会话已不可用，应重新运行 `pair` 并扫码。
- CLI 提示二维码过期：APK 未在超时前确认导入，重新运行即可。

普通 `httpcapture pair` 仍用于只导入代理和 CA 的 v3 配置，不具备 APK 控制 CLI 会话能力。需要一键联动记录时，请使用下文的 `httpcapture serve --control-host ... --pair` 生成 v4 配置。开发阶段不迁移旧的本地代理配置，本机已有旧配置时请重新扫码。

### 构建 Linux AMD64

```bash
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o bin/httpcapture-linux-amd64 .
```

## Proxify 抓包会话

Proxify 启动后，可以标记一次抓包的开始和结束：

```bash
httpcapture record start \
  --package com.example.demo \
  --device Pixel-5 \
  --client-ip 192.168.1.20

httpcapture record status
httpcapture record stop
```

会话默认保存在 `~/httpcapture-sessions/<captureId>/`：

- `meta.json`：引擎、包名集合、设备、IP 和毫秒时间。
- `traffic.jsonl`：httpCapture 自己生成的流式原始数据。
- `session.har`：停止抓包后生成的 HAR 1.2 文件。

`--client-ip` 不为空时，只归档该手机 IP 的记录；多 App 只保存包名集合，不声明每条请求来自哪个包。即使只指定 `--formats har`，仍会保留作为主数据的 `traffic.jsonl`。

当前限制：HTTPS MITM 可用，但客户端 HTTP/2 会降级为 HTTP/1.1；不绕过 Certificate Pinning；SSE/长响应可能被缓冲，WebSocket 实测不能可靠透传；单个 Body 默认最多保存 4 MiB，超出部分继续转发但在记录中标记为截断。

`record start` 成功后默认保持前台，每 5 秒显示一次会话时长；同一终端按 `Ctrl+C` 会正常停止并归档。也可以从另一终端执行 `record stop`，原前台命令会检测到会话结束并退出。停止或导出失败时返回错误并保留原始数据，不能把失败显示为成功。当前请求数量无法可靠取得时显示“未知”，状态行不会打印 URL、Header 或 Body。

## 本地 Web 查看器

停止抓包后直接启动：

```bash
httpcapture web
```

默认打开 `http://127.0.0.1:9080/`，读取 `~/httpcapture-sessions`。页面和 API 都嵌入同一个 CLI 文件，不依赖 Proxify 正在运行、外部 CDN 或云服务。端口被占用时可以改用：

```bash
httpcapture web --port 9081
httpcapture web --sessions /path/to/httpcapture-sessions --no-open
```

当前能力：

- 会话按时间倒序，并可按引擎、包名、设备和时间范围筛选。
- 请求可全文搜索 URL、Host、Path、Method、状态、Header 和文本 Body。
- 支持 Method、状态码或状态组（如 `2xx`）、Content-Type、耗时、大小以及时间正倒序组合过滤。
- 详情展示 Query、请求/响应 Headers、文本 Body 和基础时序。
- 二进制 Body 不直接展开，超大文本只显示有限预览；均可下载已保存的内容。
- 可下载会话 HAR、调用系统文件管理器打开会话目录。
- “移到回收目录”只把会话移动到会话根目录下的 `.trash`，不会永久删除。
- 支持结构化 ZIP 批量导出：所有 Proxify 会话、当前会话、当前已生效查询条件的全部命中项，以及跨分页手选的多个请求或会话。导出不受页面分页数量限制。
- ZIP 内有 `manifest.json` 和按会话分组的 `traffic.jsonl`；逐请求保留已有毫秒请求时间、总耗时、请求体和响应体。实测响应完成时间尚未采集，不会用推算值冒充。
- 单条损坏记录、采集时截断或读取失败会在清单中标为 `partial` 并记录告警；已采集 Body 导出不受 Web 文本预览上限影响。
- 活动 `traffic.jsonl` 完整写入一条事务后，页面通过本机 SSE 自动刷新；“暂停页面更新/继续”只影响页面，不停止代理或录制。连接中断会显示提示，重连先对齐当前查询结果。

索引文件为会话根目录下的 `.httpcapture-index.sqlite`，权限为 `0600`。它保存可搜索文本副本，原始 `traffic.jsonl` 仍是主数据；删除索引后再次运行 `httpcapture web` 会自动重建。为了避免抓包内容暴露，`--host` 只接受 `127.0.0.1`、`::1` 或 `localhost`，不提供局域网监听兼容选项。

活动 JSONL 的已完成行位置和文件身份保存在 SQLite；文件末尾半行不提前展示。文件截断、替换或位置失效时重建受影响会话的索引，不扫描其他会话的完整历史；索引重置时清除可能误指向别的请求的多选和详情。页面只接收请求摘要，Headers/Body 仍在打开详情时按需读取。当前实时页面首版已通过 1000 条新增记录、半行、损坏、截断、替换及重连的合成数据测试；真实长时间录制和高负载回归仍待补充。

## 前台 serve 与 APK 联动

```bash
httpcapture serve
httpcapture serve --web-port 0 --no-open
httpcapture serve \
  --control-host 192.168.1.10 \
  --control-port 39000 \
  --pair
```

`serve` 在前台持续显示服务和会话状态，同时启动只监听 `127.0.0.1` 的本地 Web。单次会话停止后服务继续运行；按 `Ctrl+C` 时会检查活动会话并走正常停止/归档流程。

默认情况下 `serve` 不开放手机控制端口。需要 APK 主按钮/快捷磁贴直接控制 CLI 记录时，显式传入 `--control-host`：

- 本地 Web 仍只监听 `127.0.0.1`，不会暴露到局域网。
- 手机控制接口使用独立 HTTPS 身份，二维码 v4 会携带控制服务证书 SHA-256，APK 下载配置和后续控制请求都会校验该指纹。
- 控制接口只提供状态、开始会话、VPN 已启动确认、停止/放弃会话，不提供 Web 数据查看、HAR 下载或任意命令执行。
- `--pair` 会生成一次性 v4 配对二维码，配置包内包含代理 CA 公钥证书、控制服务地址和本设备 token；代理 CA 私钥不会进入二维码或配置包。
- Proxify 模式下，`serve --control-host ... --pair` 会确保受管 Proxify 已运行；停止抓包会话不会停止 Proxify 进程。
- APK 回到前台会查询 CLI 状态并提示不一致；联动开始失败时不会静默启动 VPN，用户必须显式选择“仅启动 VPN”。

常用参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--control-host` | 空 | 手机可访问的局域网 IP；为空则只运行本机 Web |
| `--control-port` | `39000` | 手机控制 HTTPS 端口；`0` 表示自动选择 |
| `--pair` | `false` | 打印并写出 v4 配对二维码 |
| `--engine` | `proxify` | 联动抓包引擎：当前支持 `proxify`、`charles` |
| `--proxy-port` | 按引擎 | 手机实际连接的代理端口 |
| `--cert` | 自动查找 | 代理 CA 公钥证书，自动查找失败时需要手动指定 |
| `--name` | CA 名称 | APK 中显示的电脑配置名称 |
| `--out` | `httpcapture-control-pair.png` | v4 二维码 PNG 输出路径 |
| `--uri-out` | 空 | 将 v4 配对 URI 写入文件，主要用于自动化测试 |
| `--terminal-qr` | `auto` | `auto`、`always` 或 `never` |

## Charles 录制

先在 Charles 的 `Proxy Settings > Web Interface` 启用 Web Interface。

若 Web Interface 开启了身份验证，通过环境变量提供凭据，避免密码进入命令行历史：

```bash
export HTTPCAPTURE_CHARLES_USERNAME=dev
export HTTPCAPTURE_CHARLES_PASSWORD='...'
```

```bash
httpcapture charles status
httpcapture record start --engine charles --package com.example.demo --client-ip 192.168.1.20
httpcapture record stop --out ./captures/demo --formats chls,xml,json,har
```

`record start` 默认保留 Charles 当前会话。只有显式传入 `--clear` 才会清空，且 CLI 必须先成功下载 `.chls` 备份；备份失败时不会清空。

```bash
httpcapture export \
  --input captures/demo/session.xml \
  --output captures/demo/filtered.xml \
  --from-ms 1789180000000 \
  --to-ms 1789180300000 \
  --client-ip 192.168.1.20
```

XML 支持 Unix 毫秒时间和 `clientAddress` 过滤；HAR 支持时间过滤，但没有可靠的客户端 IP 字段。会话文件可能包含 Cookie、Token 和请求体，默认以 `0600` 权限写入且不会自动上传。
