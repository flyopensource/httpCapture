//go:build !linux

package main

import (
	"errors"
	"os"
)

func lockSessionFile(file *os.File) error {
	return errors.New("当前阶段会话并发锁仅支持 Linux")
}

func unlockSessionFile(file *os.File) {}
