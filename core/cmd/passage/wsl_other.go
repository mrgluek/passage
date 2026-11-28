//go:build !windows

package main

// wslInstalled: the WSL note is for Windows only.
func wslInstalled() bool { return false }
