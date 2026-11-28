//go:build darwin

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// quitOnSignal runs quit, the menu's Quit, on SIGTERM, SIGHUP or SIGINT, so
// a kill or a logout restores the system proxy settings like Quit does.
func quitOnSignal(quit func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			signal.Stop(ch) // a second signal ends the process the usual way
			quit()
		case <-done:
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
