package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionOperationLockSerializesMutations(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("当前阶段只在 Linux 上实现跨进程文件锁")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var active int32
	var maximum int32
	var workers sync.WaitGroup
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := withSessionOperationLock(func() error {
				now := atomic.AddInt32(&active, 1)
				for {
					old := atomic.LoadInt32(&maximum)
					if now <= old || atomic.CompareAndSwapInt32(&maximum, old, now) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return nil
			})
			if err != nil {
				t.Errorf("session operation lock: %v", err)
			}
		}()
	}
	workers.Wait()
	if maximum != 1 {
		t.Fatalf("concurrent session mutations = %d, want 1", maximum)
	}
}

func TestForegroundMonitorExitsAfterExternalStop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state := sessionState{
		CaptureID: "test-session", Engine: engineProxify,
		StartedMS: time.Now().UnixMilli(), Status: "recording",
		SessionDir: filepath.Join(t.TempDir(), "session"),
	}
	if err := writeState(state); err != nil {
		t.Fatal(err)
	}
	if err := clearState(); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	if err := waitForCaptureSession(state, nil, make(chan os.Signal), ticks); err != nil {
		t.Fatal(err)
	}
}

func TestForegroundMonitorDoesNotStopReplacementSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	started := sessionState{CaptureID: "old", Engine: engineProxify, StartedMS: time.Now().UnixMilli()}
	replacement := started
	replacement.CaptureID = "new"
	if err := writeState(replacement); err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	if err := waitForCaptureSession(started, nil, signals, make(chan time.Time)); err == nil {
		t.Fatal("replacement session must not be stopped")
	}
	active, err := readState()
	if err != nil || active.CaptureID != "new" {
		t.Fatalf("replacement session changed: state=%+v err=%v", active, err)
	}
	if err := clearState(); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestProxyFromRecordStartArgs(t *testing.T) {
	for _, example := range []struct {
		args []string
		want string
	}{
		{[]string{"--proxy", "127.0.0.1:8888"}, "127.0.0.1:8888"},
		{[]string{"--proxy=http://127.0.0.1:8888"}, "http://127.0.0.1:8888"},
		{[]string{"--engine", "charles"}, ""},
	} {
		if actual := proxyFromRecordStartArgs(example.args); actual != example.want {
			t.Fatalf("proxyFromRecordStartArgs(%v) = %q, want %q", example.args, actual, example.want)
		}
	}
}
