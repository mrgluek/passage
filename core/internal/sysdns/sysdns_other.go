//go:build !windows

package sysdns

// adapterResolvers is Windows only; elsewhere resolv.conf has the servers.
func adapterResolvers() []string { return nil }
