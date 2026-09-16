package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func serveCommand(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := flags.Int("web-port", 9080, "本地 Web 查看器端口；0 表示自动选择")
	sessionsRoot := flags.String("sessions", "", "抓包会话根目录")
	noOpen := flags.Bool("no-open", false, "启动后不自动打开浏览器")
	noPair := flags.Bool("no-pair", false, "仅启动本机 Web，不开放手机控制接口")
	controlHost := flags.String("control-host", "", "手机可访问的局域网 IP；默认自动识别")
	controlPort := flags.Int("control-port", 39000, "手机控制接口 HTTPS 端口；0 表示自动选择")
	controlPair := flags.Bool("pair", true, "打印一次性 v4 配对二维码")
	controlEngine := flags.String("engine", engineProxify, "手机联动使用的代理引擎：proxify 或 charles")
	var proxyPort optionalPort
	flags.Var(&proxyPort, "proxy-port", "手机代理端口；Proxify/Charles 默认 8888")
	certPath := flags.String("cert", "", "代理 CA 证书（DER 或 PEM）；默认按引擎自动查找")
	profileName := flags.String("name", "", "配对配置名称")
	deviceName := flags.String("device-name", "Android", "本次配对的设备名称")
	qrOut := flags.String("out", "httpcapture-control-pair.png", "v4 配对二维码 PNG 路径")
	uriOut := flags.String("uri-out", "", "可选：同时写出 v4 配对 URI，便于模拟器自动化")
	terminalQR := flags.String("terminal-qr", "auto", "终端二维码显示方式：auto、always 或 never")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *port < 0 || *port > 65535 || *controlPort < 0 || *controlPort > 65535 {
		return errors.New("serve 用法: [--web-port 0..65535] [--sessions DIR] [--no-open] [--no-pair] [--control-host IP] [--control-port 39000]")
	}
	if *noPair {
		if strings.TrimSpace(*controlHost) != "" {
			return errors.New("--no-pair 与 --control-host 不能同时使用")
		}
		*controlPair = false
	}
	*controlEngine = strings.ToLower(strings.TrimSpace(*controlEngine))
	if !proxyPort.set {
		proxyPort.value = defaultProxyPort(*controlEngine)
	}
	if err := validateTerminalQRMode(*terminalQR); err != nil {
		return err
	}
	if *sessionsRoot == "" {
		var err error
		*sessionsRoot, err = defaultSessionsRoot()
		if err != nil {
			return err
		}
	}
	index, err := openWebIndex(*sessionsRoot)
	if err != nil {
		return err
	}
	defer index.close()
	if err := index.rescan(context.Background()); err != nil {
		return err
	}
	token, err := randomWebToken()
	if err != nil {
		return err
	}
	ui, err := fs.Sub(embeddedWebUI, "webui")
	if err != nil {
		return err
	}
	app := &webApplication{index: index, token: token, ui: ui}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		return fmt.Errorf("启动本地 Web 查看器: %w", err)
	}
	defer listener.Close()
	server := &http.Server{
		Handler: app, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	webURL := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	fmt.Println("httpcapture serve 已启动；本地 Web:", webURL)
	fmt.Println("会话目录:", index.root)
	var controlServer *http.Server
	resolvedControlHost, controlEnabled, err := resolveServeControlHost(*controlHost, *controlPair)
	if err != nil {
		return err
	}
	if controlEnabled && *controlEngine != engineProxify && *controlEngine != engineCharles {
		return errors.New("--engine 仅支持 proxify 或 charles 联动")
	}
	if !controlEnabled {
		fmt.Println("当前仅监听本机回环地址；如需 APK 联动，直接运行 `httpcapture serve`。")
	} else {
		if *controlEngine == engineProxify {
			if err := ensureManagedProxify(proxyPort.value); err != nil {
				return err
			}
		}
		if *certPath == "" {
			*certPath = findProxyCA(*controlEngine)
		}
		if *certPath == "" {
			return errors.New("未找到代理 CA；请先启动代理或使用 --cert 指定 CA 公钥证书")
		}
		identity, err := loadOrCreateControlIdentity(resolvedControlHost)
		if err != nil {
			return err
		}
		controlListener, controlURL, err := listenControlAddress(resolvedControlHost, *controlPort)
		if err != nil {
			return err
		}
		defer controlListener.Close()
		pairToken, err := randomIdentifier(22)
		if err != nil {
			return err
		}
		device, token, err := createControlDevice(*deviceName, *controlEngine)
		if err != nil {
			return err
		}
		bundle, proxyCA, err := buildControlPairingBundle(*controlEngine, *profileName, resolvedControlHost, proxyPort.value, *certPath, controlURL, identity.SHA256, device, token)
		if err != nil {
			return err
		}
		app := newControlApplication(bundle, pairToken)
		controlServer = serveControlListener(controlListener, app, identity)
		fmt.Println("手机控制服务:", controlURL)
		fmt.Println("控制服务 TLS SHA-256:", identity.SHA256)
		fmt.Println("代理:", net.JoinHostPort(resolvedControlHost, strconv.Itoa(proxyPort.value)))
		fmt.Println("代理 CA SHA-256:", proxyCA)
		if *controlHost == "" {
			fmt.Println("已自动选择局域网 IP:", resolvedControlHost, "；如不正确，请使用 --control-host 覆盖。")
		}
		if *controlPair {
			uri, err := encodeControlPairingReference(controlURL+"p/"+pairToken, identity.SHA256, bundle)
			if err != nil {
				return err
			}
			if *uriOut != "" {
				if err := secureWrite(*uriOut, []byte(uri+"\n")); err != nil {
					return fmt.Errorf("写入 v4 配对 URI: %w", err)
				}
			}
			fmt.Println("v4 配对二维码:", *qrOut)
			if err := printControlPairing(uri, *qrOut, *terminalQR); err != nil {
				return err
			}
			fmt.Println("等待 APK 扫码导入；serve 会继续保持运行。")
		} else {
			fmt.Println("提示: 当前未生成新二维码；需要给 APK 配对时，直接运行 `httpcapture serve`。")
		}
	}
	if !*noOpen {
		if err := openExternal(webURL); err != nil {
			fmt.Fprintln(os.Stderr, "提示: 请手动打开本地 Web 地址:", err)
		}
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			printServeStatus()
		case <-signals:
			fmt.Println("收到停止信号，正在关闭服务并检查活动会话…")
			if err := stopActiveSessionForServe(); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if controlServer != nil {
				_ = controlServer.Shutdown(ctx)
			}
			return server.Shutdown(ctx)
		case err := <-serverErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("本地 Web 服务中断: %w", err)
		}
	}
}

func resolveServeControlHost(controlHost string, pair bool) (string, bool, error) {
	return resolveServeControlHostWithDiscover(controlHost, pair, discoverIP)
}

func resolveServeControlHostWithDiscover(controlHost string, pair bool, discover func() string) (string, bool, error) {
	controlHost = strings.TrimSpace(controlHost)
	if controlHost != "" {
		return controlHost, true, nil
	}
	if !pair {
		return "", false, nil
	}
	discovered := discover()
	if discovered == "" {
		return "", false, errors.New("无法自动确定局域网 IP，请使用 --control-host 指定手机可访问的电脑 IP")
	}
	return discovered, true, nil
}

func printServeStatus() {
	state, err := sessions.active()
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("服务运行中；当前无活动抓包会话。")
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取活动会话状态失败:", err)
		return
	}
	elapsed := time.Since(time.UnixMilli(state.StartedMS)).Truncate(time.Second)
	fmt.Printf("服务运行中；会话=%s，引擎=%s，已运行=%s，应用=%s，请求数=未知\n",
		state.CaptureID, state.Engine, elapsed, strings.Join(state.Packages, ", "))
}

func stopActiveSessionForServe() error {
	state, err := sessions.active()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stopArgs := []string{}
	if state.Engine == engineCharles && state.ProxyHost != "" && state.ProxyPort > 0 {
		stopArgs = []string{"--proxy", net.JoinHostPort(state.ProxyHost, strconv.Itoa(state.ProxyPort))}
	}
	if err := sessions.stop(stopArgs); err != nil {
		return fmt.Errorf("关闭服务时未能归档会话 %s；原始数据保留，请使用 record stop 恢复: %w", state.CaptureID, err)
	}
	return nil
}
