//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// networksetupProxy changes the auto-config address of every network
// service with networksetup, which an admin user may run without a
// password. networksetup cannot remove an address, so turning off leaves
// ours in place, unused.
type networksetupProxy struct{}

func newSystemProxy() systemProxy { return networksetupProxy{} }

func networksetup(args ...string) (string, error) {
	out, err := exec.Command("/usr/sbin/networksetup", args...).CombinedOutput()
	text := string(out)
	if err == nil && strings.Contains(text, "** Error") {
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
	names := proxyServices(parseServiceOrder(out))
	st.Services = make([]proxyService, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.Services[i], errs[i] = readService(name)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return proxyState{}, err
	}
	st.Policy = managedProxyPolicy()
	return st, nil
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
	if out, err := exec.Command("/usr/bin/profiles", "show", "-type", "configuration", "-output", "stdout-xml").Output(); err == nil {
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
			if data, err := os.ReadFile(f); err == nil {
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
