//go:build !windows && !darwin

package main

// newSystemProxy: only Windows and macOS have a system switch; elsewhere the window
// shows the address to paste.
func newSystemProxy() systemProxy { return nil }
