# httpcapture CLI

纯 Go、无 CGO 的桌面工具，目标平台为 Linux、Windows、macOS 的 amd64/arm64。

## 配对

```bash
httpcapture pair \
  --host 192.168.1.10 \
  --port 8888 \
  --cert charles-ca.cer \
  --name dev-mac \
  --out pair.png
```

只接受 DER/PEM X.509 CA 公钥证书，拒绝可能包含私钥的 `.p12/.pfx`。若省略 `--cert`，工具会尝试读取 Charles 默认 CA 位置；若省略 `--host`，工具会选择一个局域网 IPv4 地址。

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
