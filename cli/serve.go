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

// serve is deliberately loopback-only until pairing v4 and authenticated App
// control are complete. It already owns the local Web viewer and session
// lifecycle, without creating an unauthenticated LAN endpoint.
func serveCommand(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := flags.Int("web-port", 9080, "本地 Web 查看器端口；0 表示自动选择")
	sessionsRoot := flags.String("sessions", "", "抓包会话根目录")
	noOpen := flags.Bool("no-open", false, "启动后不自动打开浏览器")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *port < 0 || *port > 65535 {
		return errors.New("serve 用法: [--web-port 0..65535] [--sessions DIR] [--no-open]")
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
	fmt.Println("当前仅监听本机回环地址；APK 局域网控制需待配对 v4 和认证完成后启用。")
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
			return server.Shutdown(ctx)
		case err := <-serverErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("本地 Web 服务中断: %w", err)
		}
	}
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
