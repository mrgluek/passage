//go:build darwin

package main

import (
	"syscall"
	"testing"
	"time"
)

func TestSignalRunsQuit(t *testing.T) {
	quit := make(chan struct{})
	stop := quitOnSignal(func() { close(quit) })
	defer stop()
	syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("SIGHUP did not quit")
	}
}
