package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const mitmStateFile = "mitmproxy.json"

type mitmState struct {
	PID        int    `json:"pid"`
	StartToken string `json:"startToken"`
	Executable string `json:"executable"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	StartedMS  int64  `json:"startedMs"`
	LogPath    string `json:"logPath"`
}

func proxyCommand(args []string) error {
	if len(args) < 2 || args[0] != "mitm" {
		return errors.New("用法: httpcapture proxy mitm start|stop|status")
	}
	switch args[1] {
	case "start":
		return mitmStart(args[2:])
	case "stop":
		return mitmStop(args[2:])
	case "status":
		return mitmStatus(args[2:])
	default:
		return errors.New("用法: httpcapture proxy mitm start|stop|status")
	}
}

func mitmStart(args []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("当前版本仅支持在 Linux 上管理 mitmdump 进程")
	}
	fs := flag.NewFlagSet("proxy mitm start", flag.ContinueOnError)
	host := fs.String("host", "0.0.0.0", "mitmdump 监听地址")
	port := fs.Int("port", 8080, "mitmdump 监听端口")
	binary := fs.String("bin", "mitmdump", "mitmdump 可执行文件或路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("proxy mitm start 不接受位置参数")
	}
	*host = strings.TrimSpace(*host)
	if err := validateListenAddress(*host, *port); err != nil {
		return err
	}

	if previous, err := readMitmState(); err == nil && managedProcessMatches(previous.PID, previous.StartToken) {
		return fmt.Errorf("mitmdump 已由 httpcapture 启动（PID %d，%s）", previous.PID, net.JoinHostPort(previous.Host, strconv.Itoa(previous.Port)))
	}
	executable, err := exec.LookPath(*binary)
	if err != nil {
		return errors.New("未找到 mitmdump；请先安装 mitmproxy，或使用 --bin 指定 mitmdump 路径")
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("解析 mitmdump 路径: %w", err)
	}
	directory, err := configDirectory()
	if err != nil {
		return err
	}
	logPath := filepath.Join(directory, "mitmdump.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开 mitmdump 日志: %w", err)
	}
	if err := os.Chmod(logPath, 0o600); err != nil {
		_ = logFile.Close()
		return err
	}
	command := exec.Command(executable, "--listen-host", *host, "--listen-port", strconv.Itoa(*port))
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	configureManagedProcess(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("启动 mitmdump: %w", err)
	}
	pid := command.Process.Pid
	startToken, err := managedProcessStartToken(pid)
	if err != nil {
		_ = terminateManagedProcess(pid)
		_ = logFile.Close()
		return fmt.Errorf("读取 mitmdump 进程状态: %w", err)
	}
	time.Sleep(300 * time.Millisecond)
	if !managedProcessMatches(pid, startToken) {
		_ = logFile.Close()
		return fmt.Errorf("mitmdump 启动后立即退出，请查看日志 %s", logPath)
	}
	state := mitmState{
		PID: pid, StartToken: startToken, Executable: executable,
		Host: *host, Port: *port, StartedMS: time.Now().UnixMilli(), LogPath: logPath,
	}
	if err := writeMitmState(state); err != nil {
		_ = terminateManagedProcess(pid)
		_ = logFile.Close()
		return err
	}
	_ = command.Process.Release()
	_ = logFile.Close()

	fmt.Printf("mitmdump 已启动，PID=%d\n监听: %s\n日志: %s\n", pid, net.JoinHostPort(*host, strconv.Itoa(*port)), logPath)
	if caPath := waitForMitmproxyCA(2 * time.Second); caPath != "" {
		fmt.Println("CA:", caPath)
	} else {
		fmt.Println("CA 尚未生成；请稍后检查 ~/.mitmproxy/mitmproxy-ca-cert.cer")
	}
	if *host == "0.0.0.0" || *host == "::" {
		fmt.Println("配对时请使用手机可访问的局域网 IP，例如: httpcapture pair --engine mitmproxy")
	}
	return nil
}

func waitForMitmproxyCA(timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if path := findMitmproxyCA(); path != "" {
			return path
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func mitmStop(args []string) error {
	if len(args) != 0 {
		return errors.New("proxy mitm stop 不接受参数")
	}
	state, err := readMitmState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("没有由 httpcapture 管理的 mitmdump 进程")
		}
		return err
	}
	if !managedProcessMatches(state.PID, state.StartToken) {
		return errors.New("记录的 mitmdump 已不在运行；为避免误杀其他进程，未发送停止信号")
	}
	if err := terminateManagedProcess(state.PID); err != nil {
		return fmt.Errorf("停止 mitmdump: %w", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for managedProcessMatches(state.PID, state.StartToken) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if managedProcessMatches(state.PID, state.StartToken) {
		return errors.New("mitmdump 在 5 秒内未退出；未强制终止，请检查进程")
	}
	if err := os.Remove(mitmStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Printf("mitmdump 已停止，PID=%d\n", state.PID)
	return nil
}

func mitmStatus(args []string) error {
	if len(args) != 0 {
		return errors.New("proxy mitm status 不接受参数")
	}
	state, err := readMitmState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("mitmdump 未由 httpcapture 启动")
			return nil
		}
		return err
	}
	if !managedProcessMatches(state.PID, state.StartToken) {
		fmt.Printf("mitmdump 未运行（旧记录 PID=%d）；未操作其他进程\n", state.PID)
		return nil
	}
	fmt.Printf("mitmdump 正在运行，PID=%d\n监听: %s\n启动时间(ms): %d\n日志: %s\n", state.PID, net.JoinHostPort(state.Host, strconv.Itoa(state.Port)), state.StartedMS, state.LogPath)
	if caPath := findMitmproxyCA(); caPath != "" {
		fmt.Println("CA:", caPath)
	}
	return nil
}

func validateListenAddress(host string, port int) error {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " /?#") || strings.Contains(host, "://") {
		return errors.New("--host 不是有效的监听 IP 或 Host")
	}
	if port < 1 || port > 65535 {
		return errors.New("--port 必须是 1..65535")
	}
	return nil
}

func mitmStatePath() string {
	directory, err := configDirectory()
	if err != nil {
		return ""
	}
	return filepath.Join(directory, mitmStateFile)
}

func writeMitmState(state mitmState) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := mitmStatePath()
	if path == "" {
		return errors.New("无法确定 httpcapture 配置目录")
	}
	return secureWrite(path, content)
}

func readMitmState() (mitmState, error) {
	path := mitmStatePath()
	if path == "" {
		return mitmState{}, errors.New("无法确定 httpcapture 配置目录")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return mitmState{}, err
	}
	var state mitmState
	if err := json.Unmarshal(content, &state); err != nil {
		return mitmState{}, fmt.Errorf("读取 mitmproxy 状态: %w", err)
	}
	if state.PID <= 0 || state.StartToken == "" {
		return mitmState{}, errors.New("mitmproxy 状态文件无效")
	}
	return state, nil
}
