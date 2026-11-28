//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// networksetupProxy changes the auto-config address of every network
// service with networksetup, which an admin user may run without a
// password. networksetup cannot remove an address, so turning off leaves
// ours in place, unused.
type networksetupProxy struct{}

func newSystemProxy() systemProxy { return networksetupProxy{} }

// macCallTimeout bounds one networksetup or profiles call: they can hang
// while the system rebuilds its network services.
const macCallTimeout = 5 * time.Second

func networksetup(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), macCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/networksetup", args...)
	cmd.WaitDelay = time.Second // a child holding the pipe does not extend the wait
	out, err := cmd.CombinedOutput()
	text := string(out)
	if ctx.Err() != nil {
		err = errors.New("timed out")
	} else if err == nil && strings.Contains(text, "** Error") {
		err = errors.New(strings.TrimSpace(text))
	}
	if err != nil {
		return text, errors.New("networksetup -" + strings.TrimPrefix(args[0], "-") + ": " + strings.TrimSpace(err.Error()))
	}
	return text, nil
}

func (networksetupProxy) Read() (proxyState, error) {
	var st proxyState
	out, err := networksetup("-listnetworkserviceorder")
	if err != nil {
		return st, err
	}
	found, errs := readServices(proxyServices(parseServiceOrder(out)))
	st.Services = found
	st.Unread = len(errs) // left alone: it may be going away
	st.Policy = managedProxyPolicy()
	return st, nil
}

// ReadServices reads the named services, whatever their state now, so a
// restore does not depend on the other services.
func (networksetupProxy) ReadServices(names []string) ([]proxyService, []string, error) {
	out, err := networksetup("-listallnetworkservices")
	if err != nil {
		return nil, nil, err
	}
	known := map[string]bool{}
	for _, line := range strings.Split(out, "\n")[1:] {
		known[strings.TrimPrefix(strings.TrimRight(line, "\r"), "*")] = true
	}
	var present, missing []string
	for _, n := range names {
		if known[n] {
			present = append(present, n)
		} else {
			missing = append(missing, n)
		}
	}
	found, errs := readServices(present)
	return found, missing, errors.Join(errs...)
}

// readServices reads services in parallel; a service that fails is left out.
func readServices(names []string) ([]proxyService, []error) {
	services := make([]proxyService, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			services[i], errs[i] = readService(name)
		}()
	}
	wg.Wait()
	var found []proxyService
	var failed []error
	for i := range names {
		if errs[i] != nil {
			failed = append(failed, errs[i])
		} else {
			found = append(found, services[i])
		}
	}
	return found, failed
}

func readService(name string) (proxyService, error) {
	s := proxyService{Name: name}
	out, err := networksetup("-getautoproxyurl", name)
	if err != nil {
		return s, err
	}
	s.AutoConfigURL, s.AutoConfigOn = parseAutoProxy(out)
	for _, get := range []string{"-getwebproxy", "-getsecurewebproxy", "-getsocksfirewallproxy"} {
		out, err := networksetup(get, name)
		if err != nil {
			return s, err
		}
		if on, server := parseManualProxy(out); on {
			s.ProxyEnable, s.ProxyServer = true, server
		}
	}
	if out, err := networksetup("-getproxyautodiscovery", name); err == nil {
		s.AutoDetect = strings.Contains(out, ": On")
	}
	return s, nil
}

// managedProxyPolicy looks for proxy settings in the user's configuration
// profiles and in managed preferences. Both are readable without a password.
func managedProxyPolicy() string {
	ctx, cancel := context.WithTimeout(context.Background(), macCallTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/profiles", "show", "-type", "configuration", "-output", "stdout-xml").Output(); err == nil {
		if k := managedProxy(out); k != "" {
			return "profile: " + k
		}
	}
	dirs := []string{"/Library/Managed Preferences"}
	if u := os.Getenv("USER"); u != "" {
		dirs = append(dirs, filepath.Join("/Library/Managed Preferences", u))
	}
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.plist"))
		for _, f := range files {
			if data, err := plistXML(f); err == nil {
				if k := managedProxy(data); k != "" {
					return "managed preferences: " + k
				}
			}
		}
	}
	return ""
}

func (networksetupProxy) SetAutoConfig(service, url string, on bool) error {
	if url != "" {
		if _, err := networksetup("-setautoproxyurl", service, url); err != nil {
			return err
		}
	}
	if !on {
		if _, err := networksetup("-setautoproxystate", service, "off"); err != nil {
			return err
		}
	}
	return nil
}

// plistXML reads a property list as XML, so only whole keys and strings
// match: plutil converts a binary one without a password.
func plistXML(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), macCallTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", path).Output()
}
