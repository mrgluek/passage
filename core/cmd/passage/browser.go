package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/mrgluek/passage/core/tunnel"
)

// browserView is the settings window's Browsers section. It counts sites and
// never names them.
type browserView struct {
	Show   bool   `json:"show"`
	PAC    string `json:"pac"`
	Sites  int    `json:"sites"`
	System bool   `json:"system"`           // this platform has the system switch
	On     bool   `json:"on"`               // the system address is ours now
	Notice string `json:"notice,omitempty"` // why the switch was refused
}

func (a *App) siteCountLocked() int {
	n := 0
	for _, g := range a.linkCfg.SiteGroups() {
		n += len(g.Hosts)
	}
	return n
}

// browserWantedLocked: the listener runs when there are sites to serve or
// the system points at its address.
func (a *App) browserWantedLocked() bool {
	return a.siteCountLocked() > 0 || a.settings.SystemPAC
}

func (a *App) pacURLLocked() string {
	return fmt.Sprintf("http://%s:%d/proxy.pac", host, a.settings.BrowserPort)
}

// ensureBrowserPortLocked keeps a port of its own, different from the proxy's
// and the WhatsApp ports, and free now.
func (a *App) ensureBrowserPortLocked() {
	s := &a.settings
	if p := s.BrowserPort; p >= minPort && p < maxPort && p != s.Port && p != s.WhatsAppChatPort && p != s.WhatsAppMediaPort {
		return
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(maxPort-minPort))
	free := a.portFree
	if free == nil {
		free = localPortBindable
	}
	port, err := chooseWhatsAppPort(int(n.Int64()), []int{s.Port, s.WhatsAppChatPort, s.WhatsAppMediaPort}, free)
	if err != nil {
		a.event("browser port: %v", err)
		return
	}
	s.BrowserPort = port
	a.saveSettings()
}

func (a *App) browserViewLocked() browserView {
	v := browserView{Sites: a.siteCountLocked(), System: a.sysProxy != nil}
	v.Show = v.Sites > 0 || a.settings.SystemPAC
	if v.Show {
		a.ensureBrowserPortLocked()
		v.PAC = a.pacURLLocked()
	}
	v.On = a.settings.SysBackup.Active
	if a.sysNotice != "" {
		v.Notice = a.t.f(a.sysNotice)
		if a.sysNotice == "browser_refuse_error" {
			v.Notice = a.t.f("browser_refuse_error", a.sysError)
		}
	}
	return v
}

// noticeKey names the window text for a refusal.
func noticeKey(err error) string {
	switch {
	case errors.Is(err, errSysForeignPAC):
		return "browser_refuse_pac"
	case errors.Is(err, errSysManualProxy):
		return "browser_refuse_manual"
	case errors.Is(err, errSysPolicy):
		return "browser_refuse_policy"
	}
	return "browser_refuse_error"
}

// setSystemPAC turns the system switch on or off. A refusal leaves the
// system alone and is shown in the window; it is not an error of the call.
func (a *App) setSystemPAC(on bool) error {
	a.mu.Lock()
	defer a.changedAfter()
	defer a.mu.Unlock()
	if a.sysProxy == nil {
		return errSysUnsupported
	}
	a.sysNotice, a.sysError = "", ""
	wanted := a.browserWantedLocked()
	if on {
		a.ensureBrowserPortLocked()
		b, err := enableSystemPAC(a.sysProxy, a.pacURLLocked())
		if err != nil {
			a.sysNotice, a.sysError = noticeKey(err), err.Error()
			a.event("system proxy: not changed (%s)", a.sysNotice)
			return nil
		}
		a.settings.SystemPAC, a.settings.SysBackup = true, b
		a.event("system proxy: auto-config set")
	} else {
		a.restoreSystemPACLocked("off")
		a.settings.SystemPAC = false
	}
	a.saveSettings()
	if wanted != a.browserWantedLocked() {
		a.restartIfRunningLocked("browser listener changed")
	}
	return nil
}

// restoreSystemPACLocked puts the old settings back if the address is still
// ours, and forgets the backup either way.
func (a *App) restoreSystemPACLocked(why string) {
	b := a.settings.SysBackup
	if !b.Active {
		return
	}
	changed, err := restoreSystemPAC(a.sysProxy, b)
	switch {
	case err != nil:
		a.event("system proxy: restore failed (%s): %v", why, err)
		return // keep the backup: the next start tries again
	case changed:
		a.event("system proxy: restored (%s)", why)
	default:
		a.event("system proxy: address changed by someone else, left alone (%s)", why)
	}
	a.settings.SysBackup = sysBackup{}
	a.saveSettings()
}

// recoverSystemPACLocked runs at start. A backup left by a crashed run is
// either continued (the user still wants it) or undone; without a backup, a
// wish for the switch is applied again.
func (a *App) recoverSystemPACLocked() {
	if a.sysProxy == nil {
		return
	}
	s := &a.settings
	switch {
	case s.SysBackup.Active && !s.SystemPAC:
		a.restoreSystemPACLocked("crash recovery")
	case s.SysBackup.Active:
		st, err := a.sysProxy.Read()
		if err == nil && st.AutoConfigURL != s.SysBackup.Ours {
			s.SysBackup = sysBackup{} // changed meanwhile: not ours to restore
			s.SystemPAC = false       // and the wish no longer matches the system
			a.saveSettings()
		}
	case s.SystemPAC:
		a.ensureBrowserPortLocked()
		b, err := enableSystemPAC(a.sysProxy, a.pacURLLocked())
		if err != nil {
			s.SystemPAC = false
			a.sysNotice, a.sysError = noticeKey(err), err.Error()
			a.event("system proxy: not set at start (%s)", a.sysNotice)
		} else {
			s.SysBackup = b
		}
		a.saveSettings()
	}
}

// browserConfigLocked adds the browser listener to the router's configuration.
func (a *App) browserConfigLocked(cfg *tunnel.Config) {
	if !a.browserWantedLocked() {
		return
	}
	a.ensureBrowserPortLocked()
	cfg.BrowserListen = fmt.Sprintf("%s:%d", host, a.settings.BrowserPort)
}
