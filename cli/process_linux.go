//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureManagedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func managedProcessStartToken(pid int) (string, error) {
	token, _, err := managedProcessIdentity(pid)
	return token, err
}

func managedProcessIdentity(pid int) (string, string, error) {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", "", err
	}
	closing := strings.LastIndex(string(content), ") ")
	if closing < 0 {
		return "", "", errors.New("无法解析 /proc 进程信息")
	}
	fields := strings.Fields(string(content)[closing+2:])
	if len(fields) <= 19 {
		return "", "", errors.New("/proc 进程信息字段不足")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", "", errors.New("/proc 进程启动标识无效")
	}
	return fields[19], fields[0], nil
}

func managedProcessMatches(pid int, startToken string) bool {
	actual, state, err := managedProcessIdentity(pid)
	return err == nil && state != "Z" && startToken != "" && actual == startToken
}

func terminateManagedProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
