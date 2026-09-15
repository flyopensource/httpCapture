package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// captureSessionManager is the single entry point for mutating a capture
// session. The on-disk lock also serializes separate CLI processes.
type captureSessionManager struct{}

var sessions captureSessionManager

func (captureSessionManager) start(args []string) (sessionState, error) {
	var started sessionState
	err := withSessionOperationLock(func() error {
		if err := recordStart(args); err != nil {
			return err
		}
		var err error
		started, err = readState()
		return err
	})
	return started, err
}

func (captureSessionManager) stop(args []string) error {
	return withSessionOperationLock(func() error { return recordStop(args) })
}

func (captureSessionManager) active() (sessionState, error) {
	return readState()
}

func markActiveSessionStatus(captureID, status string) error {
	return withSessionOperationLock(func() error {
		state, err := readState()
		if err != nil {
			return err
		}
		if state.CaptureID != captureID {
			return fmt.Errorf("活动会话是 %s，不是 %s", state.CaptureID, captureID)
		}
		state.Status = status
		if err := writeState(state); err != nil {
			return err
		}
		return writeSessionMetadata(state)
	})
}

func readSessionMetadata(directory string) (sessionState, error) {
	content, err := os.ReadFile(filepath.Join(directory, "meta.json"))
	if err != nil {
		return sessionState{}, err
	}
	var state sessionState
	if err := json.Unmarshal(content, &state); err != nil {
		return sessionState{}, err
	}
	return state, nil
}

func withSessionOperationLock(operation func() error) error {
	directory, err := configDirectory()
	if err != nil {
		return err
	}
	lockPath := filepath.Join(directory, "session-operation.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("打开会话操作锁: %w", err)
	}
	defer file.Close()
	if err := lockSessionFile(file); err != nil {
		return fmt.Errorf("锁定会话操作: %w", err)
	}
	defer unlockSessionFile(file)
	return operation()
}

func recordStartForeground(args []string) error {
	state, err := sessions.start(args)
	if err != nil {
		return err
	}
	stopArgs := []string{}
	if state.Engine == engineCharles {
		if proxy := proxyFromRecordStartArgs(args); proxy != "" {
			stopArgs = []string{"--proxy", proxy}
		}
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	return waitForCaptureSession(state, stopArgs, signals, ticker.C)
}

func waitForCaptureSession(
	started sessionState, stopArgs []string, signals <-chan os.Signal, ticks <-chan time.Time,
) error {
	fmt.Printf("正在抓包；引擎=%s，会话=%s，开始时间(ms)=%d\n", started.Engine, started.CaptureID, started.StartedMS)
	fmt.Printf("应用=%s；目录=%s\n", strings.Join(started.Packages, ", "), started.SessionDir)
	fmt.Println("请求数: 未知；按 Ctrl+C 可安全停止并归档；另一终端仍可执行 record stop。")
	for {
		select {
		case <-signals:
			active, err := sessions.active()
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("会话已在另一终端结束。")
				return nil
			}
			if err != nil {
				return err
			}
			if active.CaptureID != started.CaptureID {
				return fmt.Errorf("活动会话已变为 %s，不停止其他会话", active.CaptureID)
			}
			if err := sessions.stop(stopArgs); err != nil {
				return fmt.Errorf("停止会话失败，已落盘数据保留，可在另一终端执行 record stop 恢复: %w", err)
			}
			return nil
		case <-ticks:
			active, err := sessions.active()
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("会话已在另一终端停止并归档。")
				return nil
			}
			if err != nil {
				return err
			}
			if active.CaptureID != started.CaptureID {
				return fmt.Errorf("活动会话已变为 %s，原会话状态需检查", active.CaptureID)
			}
			elapsed := time.Since(time.UnixMilli(started.StartedMS)).Truncate(time.Second)
			fmt.Printf("仍在抓包；会话=%s，已运行=%s，请求数=未知\n", started.CaptureID, elapsed)
		}
	}
}

func proxyFromRecordStartArgs(args []string) string {
	for index, arg := range args {
		if arg == "--proxy" && index+1 < len(args) {
			return args[index+1]
		}
		if strings.HasPrefix(arg, "--proxy=") {
			return strings.TrimPrefix(arg, "--proxy=")
		}
	}
	return ""
}
