package main

import (
	"cmp"
	"errors"
	"slices"
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
	// Unread counts services that could not be read; they are left alone.
	Unread int
}

// serviceReader reads named services only. missing lists names the system
// no longer knows; any other error means the read should be tried again.
type serviceReader interface {
	ReadServices(names []string) (found []proxyService, missing []string, err error)
}

// errServiceGone is a read of a service the system no longer knows.
var errServiceGone = errors.New("not a known network service")

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
	errSysUnreadable  = errors.New("the network services could not be read")
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
	if len(st.Services) == 0 && st.Unread > 0 {
		return sysBackup{}, errSysUnreadable
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
	r, err := restoreServices(sp, b)
	return r.changed, err
}

// restoreResult says what a restore did.
type restoreResult struct {
	changed bool
	missing int             // backed-up services the system no longer knows
	left    []serviceBackup // not restored yet (unreadable or a failed write)
}

func restoreServices(sp systemProxy, b sysBackup) (restoreResult, error) {
	var res restoreResult
	var readErr error
	if sp == nil || !b.Active {
		return res, nil
	}
	var services []proxyService
	if sr, ok := sp.(serviceReader); ok {
		names := make([]string, 0, len(b.Services))
		for _, sb := range b.Services {
			names = append(names, sb.Name)
		}
		found, missing, err := sr.ReadServices(names)
		if err != nil && len(found) == 0 {
			return res, err
		}
		services, res.missing = found, len(missing)
		// One service that cannot be read does not hold up the others: they
		// are restored, it stays in the backup.
		for _, sb := range b.Services {
			if !slices.ContainsFunc(found, func(s proxyService) bool { return s.Name == sb.Name }) && !slices.Contains(missing, sb.Name) {
				res.left = append(res.left, sb)
			}
		}
		readErr = err
	} else {
		st, err := sp.Read()
		if err != nil {
			return res, err
		}
		services = st.Services
	}
	var first error
	for _, sb := range b.Services {
		for _, s := range services {
			if s.Name != sb.Name || !s.ours(b.Ours) {
				continue
			}
			if err := sp.SetAutoConfig(s.Name, sb.PrevURL, false); err != nil {
				if first == nil {
					first = err
				}
				res.left = append(res.left, sb)
				continue
			}
			res.changed = true
		}
	}
	return res, cmp.Or(first, readErr)
}

// sweepLeftovers turns off url on services that use it but are not in b: a
// crash between setting a service and saving the backup leaves such a
// service behind. What it had before is unknown, so it gets none.
func sweepLeftovers(sp systemProxy, url string, b sysBackup) (int, error) {
	if sp == nil || url == "" {
		return 0, nil
	}
	st, err := sp.Read()
	if err != nil {
		return 0, err
	}
	n := 0
	var first error
	for _, s := range st.Services {
		if !s.ours(url) || b.Active && b.Ours == url && b.has(s.Name) {
			continue
		}
		if err := sp.SetAutoConfig(s.Name, "", false); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		n++
	}
	return n, first
}

// reapplySystemPAC points every readable service at b.Ours again: ours are
// counted as already set, free ones are set (and added to b if new), and
// services with their own settings are left alone and dropped from b.
// refusal is the first such reason.
func reapplySystemPAC(sp systemProxy, b *sysBackup) (set, already int, refusal, err error) {
	st, err := sp.Read()
	if err != nil {
		return 0, 0, nil, err
	}
	if st.Policy != "" {
		return 0, 0, &policyError{st.Policy}, nil
	}
	refused := map[string]bool{}
	for _, s := range st.Services {
		switch r := s.refusal(b.Ours); {
		case s.ours(b.Ours):
			already++
		case r != nil:
			refused[s.Name] = true
			if refusal == nil {
				refusal = r
			}
		default:
			if err := sp.SetAutoConfig(s.Name, b.Ours, true); err != nil {
				return set, already, refusal, err
			}
			if !b.has(s.Name) {
				prev := s.AutoConfigURL
				if prev == b.Ours {
					prev = ""
				}
				b.Services = append(b.Services, serviceBackup{Name: s.Name, PrevURL: prev, ProxyEnable: s.ProxyEnable, ProxyServer: s.ProxyServer, AutoDetect: s.AutoDetect})
			}
			set++
		}
	}
	b.Services = slices.DeleteFunc(b.Services, func(sb serviceBackup) bool { return refused[sb.Name] })
	return set, already, refusal, nil
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
