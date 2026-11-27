//go:build !windows

package main

// newSystemProxy: only Windows has a system switch; elsewhere the window
// shows the address to paste.
func newSystemProxy() systemProxy { return nil }
