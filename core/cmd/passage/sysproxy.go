package main

import (
	"errors"
	"strings"
)

// systemProxy is the part of the system's proxy settings Passage may touch:
// the auto-config address of each network service. Windows has one, kept in
// the registry; macOS has one per network service. The state machine below
// is tested on every platform through a fake.
type systemProxy interface {
	// Read returns the current settings.
	Read() (proxyState, error)
	// SetAutoConfig sets one service's auto-config address. With on it is
	// turned on; without, url is the address to leave behind unused, and an
	// empty url removes it where the system can (Windows), else only turns
	// auto-config off (macOS keeps the address). It tells running programs
	// that the settings changed.
	SetAutoConfig(service, url string, on bool) error
}

// proxyState is what a read finds.
type proxyState struct {
	Services []proxyService
	// Policy names the policy value that manages the proxy settings; empty when none does.
	Policy string
}

// proxyService is one service's settings. Only the auto-config address is
// ever written; the other fields are read to decide and kept for diagnosis.
type proxyService struct {
	Name          string // empty on Windows
	AutoConfigURL string
	AutoConfigOn  bool
	ProxyEnable   bool // a manual proxy is on
	ProxyServer   string
	AutoDetect    bool
}

// ours: the service uses url now.
func (s proxyService) ours(url string) bool { return s.AutoConfigOn && s.AutoConfigURL == url }

// refusal says why Passage must leave this service alone, or nil.
func (s proxyService) refusal(url string) error {
	switch {
	case s.AutoConfigOn && s.AutoConfigURL != url:
		return errSysForeignPAC
	case s.ProxyEnable:
		return errSysManualProxy
	}
	return nil
}

// sysBackup is saved in passage.ini while the address is ours, so a crash
// can be undone at the next start.
type sysBackup struct {
	Active   bool
	Ours     string // the address Passage set
	Services []serviceBackup
}

// serviceBackup is one service Passage changed.
type serviceBackup struct {
	Name        string
	PrevURL     string // the address before; empty when there was none
	ProxyEnable bool
	ProxyServer string
	AutoDetect  bool
}

func (b sysBackup) has(name string) bool {
	for _, s := range b.Services {
		if s.Name == name {
			return true
		}
	}
	return false
}

var (
	errSysForeignPAC  = errors.New("another auto-config address is set")
	errSysManualProxy = errors.New("a manual proxy is on")
	errSysPolicy      = errors.New("the proxy settings are managed by a policy")
	errSysUnsupported = errors.New("not supported on this system")
)

// enableSystemPAC points every service at url. It refuses, without changing
// anything, when a service has another address or a manual proxy, or a
// policy manages the settings.
func enableSystemPAC(sp systemProxy, url string) (sysBackup, error) {
	if sp == nil {
		return sysBackup{}, errSysUnsupported
	}
	st, err := sp.Read()
	if err != nil {
		return sysBackup{}, err
	}
	if st.Policy != "" {
		return sysBackup{}, &policyError{st.Policy}
	}
	for _, s := range st.Services {
		if err := s.refusal(url); err != nil {
			return sysBackup{}, err
		}
	}
	b := sysBackup{Active: true, Ours: url}
	if err := addServices(sp, st, &b); err != nil {
		restoreSystemPAC(sp, b)
		return sysBackup{}, err
	}
	return b, nil
}

// extendSystemPAC points services that appeared since b was made at its
// address, skipping those Passage must leave alone. It reports how many it
// added and how many it skipped.
func extendSystemPAC(sp systemProxy, b *sysBackup) (added, skipped int, err error) {
	if sp == nil || !b.Active {
		return 0, 0, nil
	}
	st, err := sp.Read()
	if err != nil || st.Policy != "" {
		return 0, 0, err
	}
	var fresh []proxyService
	for _, s := range st.Services {
		switch {
		case b.has(s.Name):
		case s.refusal(b.Ours) != nil:
			skipped++
		default:
			fresh = append(fresh, s)
		}
	}
	n := len(b.Services)
	err = addServices(sp, proxyState{Services: fresh}, b)
	return len(b.Services) - n, skipped, err
}

// addServices sets url on each service in st that does not use it yet and
// records the previous state in b, also for services already pointing at it.
func addServices(sp systemProxy, st proxyState, b *sysBackup) error {
	for _, s := range st.Services {
		prev := s.AutoConfigURL
		if prev == b.Ours {
			prev = "" // left by an earlier run: the state before it had none
		}
		if !s.ours(b.Ours) {
			if err := sp.SetAutoConfig(s.Name, b.Ours, true); err != nil {
				return err
			}
		}
		b.Services = append(b.Services, serviceBackup{Name: s.Name, PrevURL: prev, ProxyEnable: s.ProxyEnable, ProxyServer: s.ProxyServer, AutoDetect: s.AutoDetect})
	}
	return nil
}

// restoreSystemPAC puts the old address back on each service that still
// uses ours. It reports whether it changed anything; after an error the
// rest is still tried, and a later call finishes the job.
func restoreSystemPAC(sp systemProxy, b sysBackup) (bool, error) {
	if sp == nil || !b.Active {
		return false, nil
	}
	st, err := sp.Read()
	if err != nil {
		return false, err
	}
	changed := false
	var first error
	for _, sb := range b.Services {
		for _, s := range st.Services {
			if s.Name != sb.Name || !s.ours(b.Ours) {
				continue
			}
			if err := sp.SetAutoConfig(s.Name, sb.PrevURL, false); err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			changed = true
		}
	}
	return changed, first
}

// anyOurs: some service uses url now.
func (st proxyState) anyOurs(url string) bool {
	for _, s := range st.Services {
		if s.ours(url) {
			return true
		}
	}
	return false
}

// policyError is errSysPolicy with the name of the value that blocks.
type policyError struct{ Name string }

func (e *policyError) Error() string { return errSysPolicy.Error() + " (" + e.Name + ")" }
func (e *policyError) Unwrap() error { return errSysPolicy }

// Policy keys, below HKCU and HKLM.
const (
	policyInternetSettings = `Software\Policies\Microsoft\Windows\CurrentVersion\Internet Settings`
	policyControlPanel     = `Software\Policies\Microsoft\Internet Explorer\Control Panel`
)

// policyBlocks decides whether one policy value takes the proxy settings away
// from the user. root is "HKCU" or "HKLM"; dword is meaningful when isDWORD.
// Everything not listed here (CallLegacyWCMPolicies, SecureProtocols, ...)
// has nothing to do with proxies.
func policyBlocks(root, path, name string, dword uint32, isDWORD bool) bool {
	switch {
	case strings.EqualFold(path, policyInternetSettings):
		switch strings.ToLower(name) {
		case "autoconfigurl", "proxyenable", "proxyserver", "proxyoverride", "autodetect":
			return true
		case "proxysettingsperuser":
			// 0 makes the proxy machine-wide; 1 is the normal per-user setting.
			return root == "HKLM" && isDWORD && dword == 0
		}
	case strings.EqualFold(path, policyControlPanel):
		switch strings.ToLower(name) {
		case "proxy", "autoconfig":
			return isDWORD && dword != 0 // locks the proxy settings page
		}
	}
	return false
}
