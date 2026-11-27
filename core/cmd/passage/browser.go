package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

// browserView is the settings window's Browsers section. It counts sites and
// never names them.
type browserView struct {
	Show   bool   `json:"show"`
	PAC    string `json:"pac"`
	Sites  int    `json:"sites"`
	System bool   `json:"system"` // this platform has the system switch
	On     bool   `json:"on"`     // the system address is ours now
	// Confirmed: the user set the address by hand.
	Confirmed bool   `json:"confirmed"`
	Notice    string `json:"notice,omitempty"` // why the switch was refused
}

// homeTabs lists the home screen's setup tabs, four at most. With sites the
// Browser tab replaces Other; its "other app (SOCKS5)" line holds those details.
func homeTabs(hasSites bool) []string {
	if hasSites {
		return []string{"tab_delta", "tab_tg", "whatsapp", "tab_browser"}
	}
	return []string{"tab_delta", "tab_tg", "whatsapp", "tab_other"}
}

// connectDone says whether the home screen's "connect an app" step is done:
// a chat app has used the proxy, or, when the subscription lists sites, the
// browser is set up (the system switch is on, or the user confirmed a manual
// address). Once it is done the screen stops asking and only offers
// "connect another app".
func connectDone(appSeen, hasSites, systemOn, confirmed bool) bool {
	return appSeen || hasSites && (systemOn || confirmed)
}

// confirmBrowser records that the user set the PAC address in the browser by hand.
func (a *App) confirmBrowser(on bool) {
	a.mu.Lock()
	a.settings.BrowserConfirmed = on
	a.saveSettings()
	a.mu.Unlock()
	a.changed()
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
	v.Confirmed = a.settings.BrowserConfirmed
	if a.sysNotice != "" {
		v.Notice = a.t.f(a.sysNotice)
		if a.sysNotice == "browser_refuse_error" || a.sysNotice == "browser_refuse_policy" || a.sysNotice == "browser_restore_error" {
			v.Notice = a.t.f(a.sysNotice, a.sysError)
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

// noticeArg is the detail a refusal text shows: the policy value's name, else the error.
func noticeArg(err error) string {
	var pe *policyError
	if errors.As(err, &pe) {
		return pe.Name
	}
	return err.Error()
}

// setSystemPAC turns the system switch on or off. A refusal leaves the
// system alone and is shown in the window; it is not an error of the call.
// The system calls can take seconds, so they run outside a.mu; sysMu keeps
// changes of the system settings in order.
func (a *App) setSystemPAC(on bool) error {
	a.sysMu.Lock()
	defer a.sysMu.Unlock()
	a.mu.Lock()
	sp := a.sysProxy
	if sp == nil {
		a.mu.Unlock()
		return errSysUnsupported
	}
	a.sysNotice, a.sysError = "", ""
	a.ensureBrowserPortLocked()
	url, b := a.pacURLLocked(), a.settings.SysBackup
	a.mu.Unlock()

	var nb sysBackup
	var res restoreResult
	var err error
	if on {
		nb, err = enableSystemPAC(sp, url)
	} else {
		res, err = restoreServices(sp, b)
	}

	a.mu.Lock()
	defer a.changedAfter()
	defer a.mu.Unlock()
	wanted := a.browserWantedLocked()
	switch {
	case on && err != nil:
		a.sysNotice, a.sysError = noticeKey(err), noticeArg(err)
		a.event("system proxy: not changed (%s)", a.sysNotice)
		return nil
	case on:
		a.settings.SystemPAC, a.settings.SysBackup = true, nb
		a.event("system proxy: auto-config set")
	case err != nil:
		// The switch shows what the system has: still ours. The wish is off,
		// so the next start tries the restore again.
		a.settings.SystemPAC = false
		a.sysNotice, a.sysError = "browser_restore_error", err.Error()
		a.event("system proxy: restore failed (off): %v", err)
	default:
		a.settings.SystemPAC = false
		a.noteRestoreLocked(res, "off")
		a.settings.SysBackup = sysBackup{}
	}
	a.saveSettings()
	if wanted != a.browserWantedLocked() {
		a.restartIfRunningLocked("browser listener changed")
	}
	return nil
}

func (a *App) noteRestoreLocked(res restoreResult, why string) {
	if res.changed {
		a.event("system proxy: restored (%s)", why)
	} else {
		a.event("system proxy: address changed by someone else, left alone (%s)", why)
	}
	if res.missing > 0 {
		a.event("system proxy: %d network service(s) no longer exist, dropped from the backup (%s)", res.missing, why)
	}
}

// restoreSystemPACLocked puts the old settings back if the address is still
// ours, and forgets the backup unless the restore failed.
func (a *App) restoreSystemPACLocked(why string) {
	b := a.settings.SysBackup
	if !b.Active {
		return
	}
	res, err := restoreServices(a.sysProxy, b)
	if err != nil {
		a.event("system proxy: restore failed (%s): %v", why, err)
		return // keep the backup: the next start tries again
	}
	a.noteRestoreLocked(res, why)
	a.settings.SysBackup = sysBackup{}
	a.saveSettings()
}

// restoreOnQuit restores outside a.mu and waits at most limit; a restore
// that has not finished by then stays in the backup for the next start.
func (a *App) restoreOnQuit(limit time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.sysMu.Lock()
		defer a.sysMu.Unlock()
		a.mu.Lock()
		sp, b := a.sysProxy, a.settings.SysBackup
		a.mu.Unlock()
		if sp == nil || !b.Active {
			return
		}
		res, err := restoreServices(sp, b)
		a.mu.Lock()
		defer a.mu.Unlock()
		if err != nil {
			a.event("system proxy: restore failed (quit): %v", err)
			return
		}
		a.noteRestoreLocked(res, "quit")
		a.settings.SysBackup = sysBackup{}
		a.saveSettings()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		a.mu.Lock()
		a.event("system proxy: restore did not finish in time (quit); the next start finishes it")
		a.mu.Unlock()
	}
}

// recoverSystemPACLocked runs at start. A backup left by a crashed run is
// either continued (the user still wants it) or undone; without a backup, a
// wish for the switch is applied again. extend asks the caller to run
// extendSystemPAC once a.mu is released.
func (a *App) recoverSystemPACLocked() (extend bool) {
	if a.sysProxy == nil {
		return false
	}
	s := &a.settings
	switch {
	case s.SysBackup.Active && !s.SystemPAC:
		a.restoreSystemPACLocked("crash recovery")
	case s.SysBackup.Active:
		st, err := a.sysProxy.Read()
		if err == nil && !st.anyOurs(s.SysBackup.Ours) {
			s.SysBackup = sysBackup{} // changed meanwhile: not ours to restore
			s.SystemPAC = false       // and the wish no longer matches the system
			a.saveSettings()
			return false
		}
		return true
	case s.SystemPAC:
		a.ensureBrowserPortLocked()
		b, err := enableSystemPAC(a.sysProxy, a.pacURLLocked())
		if err != nil {
			s.SystemPAC = false
			a.sysNotice, a.sysError = noticeKey(err), noticeArg(err)
			a.event("system proxy: not set at start (%s)", a.sysNotice)
		} else {
			s.SysBackup = b
		}
		a.saveSettings()
	}
	return false
}

// sweepSystemPAC runs at start, outside a.mu: see sweepLeftovers.
func (a *App) sweepSystemPAC() {
	a.sysMu.Lock()
	defer a.sysMu.Unlock()
	a.mu.Lock()
	sp, b, url := a.sysProxy, a.settings.SysBackup, ""
	if a.settings.BrowserPort != 0 {
		url = a.pacURLLocked()
	}
	a.mu.Unlock()
	n, err := sweepLeftovers(sp, url, b)
	a.mu.Lock()
	defer a.mu.Unlock()
	if n > 0 {
		a.event("system proxy: turned off the address on %d network service(s) left by an interrupted change", n)
	}
	if err != nil {
		a.event("system proxy: leftover check: %v", err)
	}
}

// extendSystemPAC points network services that appeared since the switch
// was turned on at the address too (macOS). It runs outside a.mu.
func (a *App) extendSystemPAC(why string) {
	a.sysMu.Lock()
	defer a.sysMu.Unlock()
	a.mu.Lock()
	sp, b := a.sysProxy, a.settings.SysBackup
	on := a.settings.SystemPAC && b.Active
	a.mu.Unlock()
	if sp == nil || !on {
		return
	}
	b.Services = slices.Clone(b.Services)
	added, skipped, err := extendSystemPAC(sp, &b)
	a.mu.Lock()
	defer a.mu.Unlock()
	if added > 0 && a.settings.SysBackup.Active && a.settings.SysBackup.Ours == b.Ours {
		a.settings.SysBackup = b
		a.saveSettings()
		a.event("system proxy: auto-config set on %d new network service(s) (%s)", added, why)
	}
	if skipped > 0 {
		a.event("system proxy: %d network service(s) left alone, they have their own proxy settings (%s)", skipped, why)
	}
	if err != nil {
		a.event("system proxy: %v (%s)", err, why)
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
