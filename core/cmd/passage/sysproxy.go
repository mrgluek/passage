package main

import "errors"

// systemProxy is the part of the system's proxy settings Passage may touch:
// the auto-config address. Windows keeps it in the registry; the state
// machine below is tested on every platform through a fake.
type systemProxy interface {
	// Read returns the current settings.
	Read() (proxyState, error)
	// SetAutoConfig sets the auto-config address; an empty url removes it.
	// It tells running programs that the settings changed.
	SetAutoConfig(url string) error
}

// proxyState is what a read finds. The other fields are read only: they are
// kept in the backup for diagnosis and are never written back.
type proxyState struct {
	AutoConfigURL string
	ProxyEnable   bool
	ProxyServer   string
	AutoDetect    bool
	// Policy is set when a group policy manages the proxy settings.
	Policy bool
}

// sysBackup is saved in passage.ini while the address is ours, so a crash
// can be undone at the next start.
type sysBackup struct {
	Active      bool
	Ours        string // the address Passage set
	PrevURL     string // AutoConfigURL before; empty when there was none
	ProxyEnable bool
	ProxyServer string
	AutoDetect  bool
}

var (
	errSysForeignPAC  = errors.New("another auto-config address is set")
	errSysManualProxy = errors.New("a manual proxy is on")
	errSysPolicy      = errors.New("the proxy settings are managed by a policy")
	errSysUnsupported = errors.New("not supported on this system")
)

// enableSystemPAC points the system at url. It refuses, without changing
// anything, when another address, a manual proxy or a policy is in place.
func enableSystemPAC(sp systemProxy, url string) (sysBackup, error) {
	if sp == nil {
		return sysBackup{}, errSysUnsupported
	}
	st, err := sp.Read()
	if err != nil {
		return sysBackup{}, err
	}
	switch {
	case st.Policy:
		return sysBackup{}, errSysPolicy
	case st.AutoConfigURL != "" && st.AutoConfigURL != url:
		return sysBackup{}, errSysForeignPAC
	case st.ProxyEnable:
		return sysBackup{}, errSysManualProxy
	}
	prev := st.AutoConfigURL
	if prev == url {
		prev = "" // left by a crashed run: the state before it had none
	}
	b := sysBackup{Active: true, Ours: url, PrevURL: prev, ProxyEnable: st.ProxyEnable, ProxyServer: st.ProxyServer, AutoDetect: st.AutoDetect}
	if st.AutoConfigURL != url {
		if err := sp.SetAutoConfig(url); err != nil {
			return sysBackup{}, err
		}
	}
	return b, nil
}

// restoreSystemPAC puts the old address back, only if the current one is
// still ours. It reports whether it changed anything.
func restoreSystemPAC(sp systemProxy, b sysBackup) (bool, error) {
	if sp == nil || !b.Active {
		return false, nil
	}
	st, err := sp.Read()
	if err != nil {
		return false, err
	}
	if st.AutoConfigURL != b.Ours {
		return false, nil
	}
	return true, sp.SetAutoConfig(b.PrevURL)
}
