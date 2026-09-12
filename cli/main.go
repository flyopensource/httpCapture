package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const version = "0.2.0"

const (
	engineCharles   = "charles"
	engineMitmproxy = "mitmproxy"
	engineCustom    = "custom"
)

type pairing struct {
	Version           int    `json:"v"`
	Engine            string `json:"engine"`
	Name              string `json:"name"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	CertificateDER    string `json:"certificateDer"`
	CertificateSHA256 string `json:"certificateSha256"`
}

type sessionState struct {
	CaptureID string   `json:"captureId"`
	Apps      []string `json:"apps"`
	ClientIP  string   `json:"clientIp,omitempty"`
	StartedMS int64    `json:"startedMs"`
	StoppedMS int64    `json:"stoppedMs,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "pair":
		err = pairCommand(os.Args[2:])
	case "proxy":
		err = proxyCommand(os.Args[2:])
	case "charles":
		err = charlesCommand(os.Args[2:])
	case "record":
		err = recordCommand(os.Args[2:])
	case "export":
		err = exportCommand(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("httpcapture", version)
		return
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("未知命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`httpcapture - Android 抓包代理接入工具

用法:
  httpcapture pair [--engine charles|mitmproxy|custom] [--host IP] [--port PORT] [--cert FILE] [--name NAME] [--out pair.png] [--serve-port 0] [--timeout 3m] [--terminal-qr auto|always|never]
  httpcapture proxy mitm start [--host 0.0.0.0] [--port 8080] [--bin mitmdump]
  httpcapture proxy mitm stop
  httpcapture proxy mitm status
  httpcapture charles status [--proxy 127.0.0.1:8888]
  httpcapture record start [--app PACKAGE ...] [--client-ip IP] [--clear]
  httpcapture record stop [--out DIR] [--formats chls,xml,json,har]
  httpcapture export --input session.xml|session.har --output filtered.xml|filtered.har [--from-ms N] [--to-ms N] [--client-ip IP]

record start 默认不会清空 Charles 当前会话。--clear 会先备份 .chls，备份失败则不清空。
`)
	os.Exit(0)
}

type repeated []string

func (r *repeated) String() string { return strings.Join(*r, ",") }
func (r *repeated) Set(value string) error {
	*r = append(*r, value)
	return nil
}

type optionalPort struct {
	value int
	set   bool
}

func (p *optionalPort) String() string {
	if !p.set {
		return ""
	}
	return strconv.Itoa(p.value)
}

func (p *optionalPort) Set(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return errors.New("端口必须是整数")
	}
	p.value = parsed
	p.set = true
	return nil
}

func pairCommand(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	engine := fs.String("engine", engineCharles, "代理引擎：charles、mitmproxy 或 custom")
	host := fs.String("host", "", "手机能访问的电脑 IP")
	var port optionalPort
	fs.Var(&port, "port", "HTTP proxy 端口；Charles 默认 8888，mitmproxy 默认 8080")
	certPath := fs.String("cert", "", "代理 CA 证书（DER 或 PEM）")
	name := fs.String("name", "", "此代理配置名称")
	out := fs.String("out", "httpcapture-pair.png", "二维码 PNG 路径")
	uriOut := fs.String("uri-out", "", "可选：同时写出配对 URI，便于模拟器自动化")
	terminalQR := fs.String("terminal-qr", "auto", "终端二维码显示方式：auto、always 或 never")
	servePort := fs.Int("serve-port", 0, "临时配对服务端口，0 表示自动选择")
	timeout := fs.Duration("timeout", 3*time.Minute, "临时配对服务有效时间")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("pair 不接受位置参数")
	}
	*engine = strings.ToLower(strings.TrimSpace(*engine))
	if !validProxyEngine(*engine) {
		return errors.New("--engine 仅支持 charles、mitmproxy 或 custom")
	}
	if !port.set {
		port.value = defaultProxyPort(*engine)
	}
	if err := validateTerminalQRMode(*terminalQR); err != nil {
		return err
	}
	if *host == "" {
		*host = discoverIP()
	}
	if *host == "" {
		return errors.New("无法自动确定局域网 IP，请使用 --host")
	}
	if port.value < 1 || port.value > 65535 {
		return errors.New("--port 必须是 1..65535")
	}
	if *servePort < 0 || *servePort > 65535 {
		return errors.New("--serve-port 必须是 0..65535")
	}
	if *timeout <= 0 {
		return errors.New("--timeout 必须大于 0")
	}
	if *certPath == "" {
		*certPath = findProxyCA(*engine)
	}
	if *certPath == "" {
		if *engine == engineMitmproxy {
			return errors.New("未找到 mitmproxy CA；请先运行 `httpcapture proxy mitm start` 生成证书，或使用 --cert 指定 ~/.mitmproxy/mitmproxy-ca-cert.cer")
		}
		if *engine == engineCharles {
			return errors.New("未找到 Charles CA，请先从 Charles 导出并使用 --cert 指定；不接受 .p12/.pfx")
		}
		return errors.New("custom 代理必须使用 --cert 指定 DER/PEM CA 证书")
	}
	ext := strings.ToLower(filepath.Ext(*certPath))
	if ext == ".p12" || ext == ".pfx" {
		return errors.New("拒绝包含私钥的 .p12/.pfx，请导出 Certificate (.cer/.pem)")
	}
	der, subject, err := readCertificate(*certPath)
	if err != nil {
		return err
	}
	if *name == "" {
		*name = subject
		if *name == "" {
			*name = net.JoinHostPort(*host, strconv.Itoa(port.value))
		}
	}
	if certificate, parseErr := x509.ParseCertificate(der); parseErr == nil {
		now := time.Now()
		if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
			fmt.Fprintf(os.Stderr, "警告: %s CA 当前无效（有效期 %s 至 %s），HTTP 仍可转发，但 HTTPS 解密会失败；请更新代理 CA 后再次配对。\n", *engine, certificate.NotBefore.Format(time.RFC3339), certificate.NotAfter.Format(time.RFC3339))
		}
	}
	digest := sha256.Sum256(der)
	payload := pairing{
		Version: 3, Engine: *engine, Name: *name, Host: *host, Port: port.value,
		CertificateDER:    base64.StdEncoding.EncodeToString(der),
		CertificateSHA256: strings.ToUpper(hex.EncodeToString(digest[:])),
	}
	bundle, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	server, err := startPairingServer(*host, *servePort, bundle)
	if err != nil {
		return err
	}
	defer server.Close()
	uri, err := encodePairingReference(server.URL(), bundle)
	if err != nil {
		return err
	}
	code, err := qrcode.New(uri, qrcode.Low)
	if err != nil {
		return fmt.Errorf("生成二维码: %w", err)
	}
	if err := code.WriteFile(512, *out); err != nil {
		return fmt.Errorf("写入二维码: %w", err)
	}
	if *uriOut != "" {
		if err := secureWrite(*uriOut, []byte(uri+"\n")); err != nil {
			return fmt.Errorf("写入配对 URI: %w", err)
		}
	}
	fmt.Printf("配置: %s\n类型: %s\n代理: %s\nCA SHA-256: %s\n二维码: %s\n", *name, *engine, net.JoinHostPort(*host, strconv.Itoa(port.value)), payload.CertificateSHA256, *out)
	fmt.Printf("临时服务: %s（%s 后失效）\n", server.URL(), timeout.String())
	fmt.Println()
	if err := printTerminalQRCode(os.Stdout, code, *terminalQR); err != nil {
		return err
	}
	fmt.Println("等待手机扫码并完成导入；按 Ctrl+C 可取消。")
	if err := server.Wait(*timeout); err != nil {
		return err
	}
	fmt.Println("手机已确认导入，临时服务已关闭。")
	return nil
}

func encodePairingReference(downloadURL string, bundle []byte) (string, error) {
	endpoint, err := url.Parse(downloadURL)
	if err != nil {
		return "", err
	}
	port := endpoint.Port()
	token := strings.TrimPrefix(endpoint.EscapedPath(), "/p/")
	if endpoint.Scheme != "http" || endpoint.Hostname() == "" || port == "" || token == endpoint.EscapedPath() || token == "" {
		return "", errors.New("临时配对地址无效")
	}
	digest := sha256.Sum256(bundle)
	return fmt.Sprintf(
		"httpcapture://p/3/%s/%s/%s/%s",
		endpoint.Hostname(), port, token, base64.RawURLEncoding.EncodeToString(digest[:]),
	), nil
}

func readCertificate(path string) ([]byte, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("读取证书: %w", err)
	}
	der := raw
	if block, _ := pem.Decode(raw); block != nil {
		if block.Type != "CERTIFICATE" {
			return nil, "", fmt.Errorf("PEM 类型是 %q，不是 CERTIFICATE", block.Type)
		}
		der = block.Bytes
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", fmt.Errorf("解析 X.509 证书: %w", err)
	}
	if !certificate.IsCA {
		return nil, "", errors.New("证书不是 CA 证书")
	}
	return der, certificate.Subject.CommonName, nil
}

func discoverIP() string {
	connection, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer connection.Close()
		if address, ok := connection.LocalAddr().(*net.UDPAddr); ok && suitableIP(address.IP) {
			return address.IP.String()
		}
	}
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, _ := net.ParseCIDR(address.String())
			if suitableIP(ip) {
				return ip.String()
			}
		}
	}
	return ""
}

func suitableIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.To4() == nil {
		return false
	}
	v4 := ip.To4()
	return v4[0] == 10 || v4[0] == 192 && v4[1] == 168 || v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31
}

func findCharlesCA() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".charles", "ca", "charles-proxy-ssl-proxying-certificate.cer"),
		filepath.Join(home, ".charles", "ca", "charles-proxy-ssl-proxying-certificate.pem"),
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, filepath.Join(home, "Library", "Application Support", "Charles", "ca", "charles-proxy-ssl-proxying-certificate.cer"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func validProxyEngine(engine string) bool {
	return engine == engineCharles || engine == engineMitmproxy || engine == engineCustom
}

func defaultProxyPort(engine string) int {
	if engine == engineMitmproxy {
		return 8080
	}
	return 8888
}

func findProxyCA(engine string) string {
	switch engine {
	case engineCharles:
		return findCharlesCA()
	case engineMitmproxy:
		return findMitmproxyCA()
	default:
		return ""
	}
}

func findMitmproxyCA() string {
	home, _ := os.UserHomeDir()
	for _, name := range []string{"mitmproxy-ca-cert.cer", "mitmproxy-ca-cert.pem"} {
		candidate := filepath.Join(home, ".mitmproxy", name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

type charlesClient struct {
	client   *http.Client
	username string
	password string
}

func newCharlesClient(proxyAddress string) (*charlesClient, error) {
	if !strings.Contains(proxyAddress, "://") {
		proxyAddress = "http://" + proxyAddress
	}
	proxyURL, err := url.Parse(proxyAddress)
	if err != nil {
		return nil, err
	}
	return &charlesClient{client: &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}, username: os.Getenv("HTTPCAPTURE_CHARLES_USERNAME"), password: os.Getenv("HTTPCAPTURE_CHARLES_PASSWORD")}, nil
}

func (c *charlesClient) get(path string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, "http://control.charles"+path, nil)
	if err != nil {
		return nil, err
	}
	if c.username != "" || c.password != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Charles 返回 HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if bytes.Contains(bytes.ToLower(body), []byte("web interface is disabled")) {
		return nil, errors.New("Charles Web Interface 未启用；请在 Proxy Settings > Web Interface 中启用")
	}
	return body, nil
}

func charlesCommand(args []string) error {
	if len(args) == 0 || args[0] != "status" {
		return errors.New("用法: httpcapture charles status")
	}
	fs := flag.NewFlagSet("charles status", flag.ContinueOnError)
	proxy := fs.String("proxy", "127.0.0.1:8888", "Charles 代理地址")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	client, err := newCharlesClient(*proxy)
	if err != nil {
		return err
	}
	_, err = client.get("/")
	if err != nil {
		return err
	}
	fmt.Println("Charles Web Interface 可访问:", *proxy)
	return nil
}

func recordCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: httpcapture record start|stop")
	}
	switch args[0] {
	case "start":
		return recordStart(args[1:])
	case "stop":
		return recordStop(args[1:])
	default:
		return errors.New("用法: httpcapture record start|stop")
	}
}

func recordStart(args []string) error {
	fs := flag.NewFlagSet("record start", flag.ContinueOnError)
	proxy := fs.String("proxy", "127.0.0.1:8888", "Charles 代理地址")
	clientIP := fs.String("client-ip", "", "手机在 Charles 中显示的客户端 IP")
	clear := fs.Bool("clear", false, "先备份再清空当前 Charles 会话")
	var apps repeated
	fs.Var(&apps, "app", "本次抓包的 Android 包名，可重复")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := newCharlesClient(*proxy)
	if err != nil {
		return err
	}
	state := sessionState{
		CaptureID: time.Now().Format("20060102-150405"), Apps: apps,
		ClientIP: *clientIP, StartedMS: time.Now().UnixMilli(),
	}
	if *clear {
		backupDir, err := sessionDirectory(state.CaptureID + "-pre-clear")
		if err != nil {
			return err
		}
		content, err := client.get("/session/download")
		if err != nil {
			return fmt.Errorf("清空前备份失败，当前会话未清空: %w", err)
		}
		if err := secureWrite(filepath.Join(backupDir, "backup.chls"), content); err != nil {
			return fmt.Errorf("清空前备份失败，当前会话未清空: %w", err)
		}
		if _, err := client.get("/session/clear"); err != nil {
			return err
		}
		fmt.Println("原会话已备份:", backupDir)
	}
	if _, err := client.get("/recording/start"); err != nil {
		return err
	}
	if err := writeState(state); err != nil {
		_, _ = client.get("/recording/stop")
		return err
	}
	fmt.Printf("Charles 已开始记录，captureId=%s，开始时间(ms)=%d\n", state.CaptureID, state.StartedMS)
	if len(apps) > 1 {
		fmt.Println("提示: Charles 数据不能精确归属到 Android 包；这些包名仅作为本次会话标签。")
	}
	return nil
}

func recordStop(args []string) error {
	fs := flag.NewFlagSet("record stop", flag.ContinueOnError)
	proxy := fs.String("proxy", "127.0.0.1:8888", "Charles 代理地址")
	out := fs.String("out", "", "输出目录")
	formats := fs.String("formats", "chls,xml,json,har", "逗号分隔的导出格式")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := readState()
	if err != nil {
		return err
	}
	client, err := newCharlesClient(*proxy)
	if err != nil {
		return err
	}
	if _, err := client.get("/recording/stop"); err != nil {
		return err
	}
	state.StoppedMS = time.Now().UnixMilli()
	outputDir := *out
	if outputDir == "" {
		outputDir, err = sessionDirectory(state.CaptureID)
		if err != nil {
			return err
		}
	} else if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return err
	}
	endpoints := map[string]string{
		"chls": "/session/download", "xml": "/session/export-xml",
		"json": "/session/export-json", "har": "/session/export-har", "csv": "/session/export-csv",
	}
	for _, format := range strings.Split(*formats, ",") {
		format = strings.TrimSpace(strings.ToLower(format))
		endpoint, ok := endpoints[format]
		if !ok {
			return fmt.Errorf("不支持的格式 %q", format)
		}
		content, err := client.get(endpoint)
		if err != nil {
			return fmt.Errorf("导出 %s: %w", format, err)
		}
		if err := secureWrite(filepath.Join(outputDir, "session."+format), content); err != nil {
			return err
		}
	}
	metadata, _ := json.MarshalIndent(state, "", "  ")
	if err := secureWrite(filepath.Join(outputDir, "capture.json"), metadata); err != nil {
		return err
	}
	if err := writeState(state); err != nil {
		return err
	}
	fmt.Printf("Charles 已停止记录，结束时间(ms)=%d\n导出目录: %s\n", state.StoppedMS, outputDir)
	return nil
}

func configDirectory() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "httpcapture")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func sessionDirectory(name string) (string, error) {
	root, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "httpcapture-sessions", name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func writeState(state sessionState) error {
	directory, err := configDirectory()
	if err != nil {
		return err
	}
	content, _ := json.MarshalIndent(state, "", "  ")
	return secureWrite(filepath.Join(directory, "active-session.json"), content)
}

func readState() (sessionState, error) {
	directory, err := configDirectory()
	if err != nil {
		return sessionState{}, err
	}
	content, err := os.ReadFile(filepath.Join(directory, "active-session.json"))
	if err != nil {
		return sessionState{}, errors.New("没有活动的抓包会话，请先执行 record start")
	}
	var state sessionState
	if err := json.Unmarshal(content, &state); err != nil {
		return sessionState{}, err
	}
	return state, nil
}

func secureWrite(path string, content []byte) error {
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
