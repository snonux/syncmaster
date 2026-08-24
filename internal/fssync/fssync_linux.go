//go:build linux

package fssync

import "syscall"

// Sync calls the sync(2) system call on Linux.
func Sync() { syscall.Sync() }
