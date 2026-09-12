# httpcapture CLI

纯 Go、无 CGO 的桌面工具。首版只发布 Linux AMD64 单文件版本。

## 安装

```bash
chmod +x httpcapture-linux-amd64
sudo install -m 0755 httpcapture-linux-amd64 /usr/local/bin/httpcapture
httpcapture version
```

不希望安装到系统目录时，也可以直接执行 `./httpcapture-linux-amd64`。

## 配对

最简用法：

```bash
httpcapture pair
```

CLI 会自动选择一个局域网 IPv4、使用 Charles 默认代理端口 `8888`，并尝试读取 Linux 默认 CA 文件：

- `~/.charles/ca/charles-proxy-ssl-proxying-certificate.cer`
- `~/.charles/ca/charles-proxy-ssl-proxying-certificate.pem`

需要覆盖自动结果时：

```bash
httpcapture pair \
  --host 192.168.1.10 \
  --port 8888 \
  --cert charles-ca.cer \
  --name dev-mac \
  --out pair.png
```

只接受 DER/PEM X.509 CA 公钥证书，拒绝可能包含私钥的 `.p12/.pfx`。

### 配对过程

1. CLI 读取 CA，启动一次性局域网 HTTP 服务并显示短二维码。
2. 保持 CLI 运行，在 HTTP Capture APK 中点击“扫码导入”。
3. APK 自动下载、校验并保存 Charles IP、端口和 CA；用户不需要打开浏览器或手动下载证书。
4. APK 回传成功确认后，CLI 显示“手机已确认导入，临时服务已关闭”并自动退出。

`pair` 在二维码生成后继续运行不是卡住。默认等待时间为 3 分钟；超时、按 `Ctrl+C` 或 CLI 进程退出后，二维码立即失效。每个二维码只允许下载一次，失败后应重新执行 `pair`。

终端二维码默认使用 `--terminal-qr auto`：CLI 会使用紧凑字符渲染并检测终端宽高，只有二维码能够完整显示时才输出；空间不足或输出被重定向时只保留 PNG，避免二维码换行损坏。可使用 `--terminal-qr always` 强制显示，或使用 `--terminal-qr never` 始终关闭终端二维码。PNG 保持清晰输出，不受终端尺寸影响。

### 配对参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--host` | 自动检测 | 手机可以访问的本机 IPv4；同时作为 Charles 地址和临时服务监听地址 |
| `--port` | `8888` | Charles HTTP Proxy 端口 |
| `--cert` | 自动查找 | Charles CA 公钥证书路径，只支持 DER/PEM |
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

### 构建 Linux AMD64

```bash
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o bin/httpcapture-linux-amd64 .
```

## Charles 录制

先在 Charles 的 `Proxy Settings > Web Interface` 启用 Web Interface。

若 Web Interface 开启了身份验证，通过环境变量提供凭据，避免密码进入命令行历史：

```bash
export HTTPCAPTURE_CHARLES_USERNAME=dev
export HTTPCAPTURE_CHARLES_PASSWORD='...'
```

```bash
httpcapture charles status
httpcapture record start --app com.example.demo --client-ip 192.168.1.20
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
