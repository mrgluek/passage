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
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return st, err
	}
	defer k.Close()
	st.AutoConfigURL, _, _ = k.GetStringValue("AutoConfigURL")
	if n, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		st.ProxyEnable = n != 0
	} else if v, _, err := k.GetStringValue("ProxyEnable"); err == nil {
		st.ProxyEnable = v != "" && v != "0"
	}
	st.ProxyServer, _, _ = k.GetStringValue("ProxyServer")
	if n, _, err := k.GetIntegerValue("AutoDetect"); err == nil {
		st.AutoDetect = n != 0
	}
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

func (registryProxy) SetAutoConfig(url string) error {
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
