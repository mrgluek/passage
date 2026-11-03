//go:build !windows

package main

const exeSuffix = ""

// adapterResolvers is Windows only; elsewhere resolv.conf has the servers.
func adapterResolvers() []string { return nil }
