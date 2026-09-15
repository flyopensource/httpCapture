//go:build !linux

package main

import "os"

func sourceDeviceInode(os.FileInfo) (uint64, uint64, bool) { return 0, 0, false }
