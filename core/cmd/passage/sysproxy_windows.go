//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
)

type registryProxy struct{}

func newSystemProxy() systemProxy { return registryProxy{} }

func (registryProxy) Read() (proxyState, error) {
	var st proxyState
	var sv proxyService
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return st, err
	}
	defer k.Close()
	sv.AutoConfigURL, _, _ = k.GetStringValue("AutoConfigURL")
	sv.AutoConfigOn = sv.AutoConfigURL != ""
	if n, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		sv.ProxyEnable = n != 0
	} else if v, _, err := k.GetStringValue("ProxyEnable"); err == nil {
		sv.ProxyEnable = v != "" && v != "0"
	}
	sv.ProxyServer, _, _ = k.GetStringValue("ProxyServer")
	if n, _, err := k.GetIntegerValue("AutoDetect"); err == nil {
		sv.AutoDetect = n != 0
	}
	st.Services = []proxyService{sv}
	st.Policy = policyPresent()
	return st, nil
}

// policyPresent returns the name of the first policy value that manages the
// proxy, or "". The decision is policyBlocks; this only reads the values.
func policyPresent() string {
	roots := []struct {
		name string
		key  registry.Key
	}{{"HKCU", registry.CURRENT_USER}, {"HKLM", registry.LOCAL_MACHINE}}
	for _, root := range roots {
		for _, path := range []string{policyInternetSettings, policyControlPanel} {
			k, err := registry.OpenKey(root.key, path, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			names, _ := k.ReadValueNames(-1)
			for _, name := range names {
				n, _, err := k.GetIntegerValue(name)
				if policyBlocks(root.name, path, name, uint32(n), err == nil) {
					k.Close()
					return name
				}
			}
			k.Close()
		}
	}
	return ""
}

// SetAutoConfig: Windows has one service and no separate on/off for the
// address, so on is ignored.
func (registryProxy) SetAutoConfig(_, url string, _ bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	if url == "" {
		err = k.DeleteValue("AutoConfigURL")
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
	} else {
		err = k.SetStringValue("AutoConfigURL", url)
	}
	k.Close()
	if err != nil {
		return err
	}
	notifyProxyChange()
	return nil
}

// notifyProxyChange tells running programs to read the settings again.
func notifyProxyChange() {
	proc := windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")
	for _, opt := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		proc.Call(0, opt, 0, 0)
	}
}
