package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"

	"github.com/mrgluek/passage/core/tunnel"
)

func chooseWhatsAppPort(start int, excluded []int, bindable func(int) bool) (int, error) {
	count := maxPort - minPort
	start = (start%count + count) % count
	for i := 0; i < count; i++ {
		port := minPort + (start+i)%count
		skip := false
		for _, n := range excluded {
			if n == port {
				skip = true
			}
		}
		if !skip && bindable(port) {
			return port, nil
		}
	}
	return 0, errors.New("no local ports available")
}

func localPortBindable(port int) bool {
	ln, err := net.Listen("tcp4", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func (s *Settings) ensureWhatsAppPorts(bindable func(int) bool) error {
	valid := func(p int) bool { return p >= minPort && p < maxPort && p != s.Port && p != s.BrowserPort }
	chat, media := s.WhatsAppChatPort, s.WhatsAppMediaPort
	if !valid(chat) {
		chat = 0
	}
	if !valid(media) || media == chat {
		media = 0
	}
	pick := func(excluded []int) (int, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(maxPort-minPort))
		if err != nil {
			return 0, err
		}
		return chooseWhatsAppPort(int(n.Int64()), excluded, bindable)
	}
	var err error
	if chat == 0 {
		chat, err = pick([]int{s.Port, s.BrowserPort, media})
		if err != nil {
			return err
		}
	}
	if media == 0 {
		media, err = pick([]int{s.Port, s.BrowserPort, chat})
		if err != nil {
			return err
		}
	}
	s.WhatsAppChatPort, s.WhatsAppMediaPort = chat, media
	return nil
}

func whatsappForwards(s Settings) []tunnel.Forward {
	if !s.WhatsApp {
		return nil
	}
	return []tunnel.Forward{
		{Name: "whatsapp-chat", Listen: fmt.Sprintf("%s:%d", host, s.WhatsAppChatPort), Target: "g.whatsapp.net:5222"},
		{Name: "whatsapp-media", Listen: fmt.Sprintf("%s:%d", host, s.WhatsAppMediaPort), Target: "whatsapp.net:443"},
	}
}

func (a *App) ensureWhatsAppPortsLocked() {
	if a.whatsappPortsTried {
		return
	}
	a.whatsappPortsTried = true
	previous := a.settings
	if err := a.settings.ensureWhatsAppPorts(localPortBindable); err != nil {
		a.whatsappError = a.t.f("whatsapp_ports_busy")
		a.settings.WhatsApp = false
	} else {
		a.whatsappError = ""
	}
	if previous.WhatsApp != a.settings.WhatsApp || previous.WhatsAppChatPort != a.settings.WhatsAppChatPort || previous.WhatsAppMediaPort != a.settings.WhatsAppMediaPort {
		a.saveSettings()
	}
}

func (a *App) setWhatsApp(on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if on {
		a.whatsappPortsTried = false
		a.ensureWhatsAppPortsLocked()
		if a.whatsappError != "" {
			return errors.New(a.whatsappError)
		}
	}
	if a.settings.WhatsApp == on {
		return nil
	}
	a.settings.WhatsApp = on
	a.saveSettings()
	a.restartIfRunningLocked("settings changed")
	return nil
}

type whatsappStatus struct {
	Lines     []string `json:"lines"`
	Warnings  []string `json:"warnings"`
	Connected bool     `json:"connected"`
	Hint      bool     `json:"hint"`
}

func formatWhatsAppStatus(t tr, forwards []tunnel.ForwardStatus) whatsappStatus {
	var result whatsappStatus
	for _, kind := range []string{"chat", "media"} {
		label, text := t.f("whatsapp_"+kind), t.f("whatsapp_waiting")
		for _, f := range forwards {
			if f.Name != "whatsapp-"+kind {
				continue
			}
			if f.LastConn != nil && !f.LastConn.IsZero() {
				result.Connected = true
				text = t.f("whatsapp_last", f.LastConn.Local().Format("15:04"))
			}
			if !f.Up {
				warning := label + ": " + t.f("whatsapp_down")
				detail := strings.SplitN(f.Error, "\n", 2)[0]
				if len([]rune(detail)) > 120 {
					detail = string([]rune(detail)[:120])
				}
				if detail != "" {
					warning += " (" + detail + ")"
				}
				result.Warnings = append(result.Warnings, warning)
			}
		}
		result.Lines = append(result.Lines, label+": "+text)
	}
	return result
}
