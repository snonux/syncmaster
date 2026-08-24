// Package fssync flushes filesystem buffers to disk. On Linux it calls
// sync(2); elsewhere it is a no-op.
//go:build !linux

package fssync

// Sync flushes pending writes to disk. The Linux implementation lives in a
// build-tagged file.
func Sync() {}
