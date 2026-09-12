//go:build !linux

package main

import (
	"errors"
	"os/exec"
)

func configureManagedProcess(command *exec.Cmd) {}

func managedProcessStartToken(pid int) (string, error) {
	return "", errors.New("当前版本仅支持 Linux 进程管理")
}

func managedProcessMatches(pid int, startToken string) bool { return false }

func terminateManagedProcess(pid int) error {
	return errors.New("当前版本仅支持 Linux 进程管理")
}
