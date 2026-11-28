package main

import (
	"encoding/xml"
	"strings"
)

// macService is one entry of `networksetup -listnetworkserviceorder`.
type macService struct {
	Name     string
	Disabled bool
	Device   string
}

// parseServiceOrder reads `networksetup -listnetworkserviceorder`: pairs of
// "(1) Name" or "(*) Name" and "(Hardware Port: ..., Device: en0)".
func parseServiceOrder(out string) []macService {
	var list []macService
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(line, "(Hardware Port: "); ok {
			if len(list) > 0 {
				if i := strings.LastIndex(rest, ", Device: "); i >= 0 {
					if d := strings.TrimSuffix(rest[i+len(", Device: "):], ")"); d != "(null)" {
						list[len(list)-1].Device = d
					}
				}
			}
			continue
		}
		if !strings.HasPrefix(line, "(") {
			continue
		}
		num, name, ok := strings.Cut(line[1:], ") ")
		if !ok || name == "" || (num != "*" && strings.Trim(num, "0123456789") != "") {
			continue
		}
		list = append(list, macService{Name: name, Disabled: num == "*"})
	}
	return list
}

// proxyServices keeps the services Passage may touch: enabled ones on a
// network device. Those without a device are VPN and other app-managed
// services; their settings belong to that app.
func proxyServices(list []macService) []string {
	var names []string
	for _, s := range list {
		if !s.Disabled && s.Device != "" {
			names = append(names, s.Name)
		}
	}
	return names
}

// parseFields reads networksetup's "Key: value" lines.
func parseFields(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), ": "); ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

// parseAutoProxy reads `networksetup -getautoproxyurl`.
func parseAutoProxy(out string) (url string, on bool) {
	f := parseFields(out)
	url = f["URL"]
	if url == "(null)" {
		url = ""
	}
	return url, f["Enabled"] == "Yes"
}

// parseManualProxy reads `-getwebproxy` and its kin: whether it is on, and the server.
func parseManualProxy(out string) (bool, string) {
	f := parseFields(out)
	server := f["Server"]
	if p := f["Port"]; server != "" && p != "" && p != "0" {
		server += ":" + p
	}
	return f["Enabled"] == "Yes", server
}

// managedProxy names the setting in a property list (XML) that manages
// proxies, or "": a ProxyType other than None (network payloads write None
// when they leave proxies alone), or the global proxy payload.
func managedProxy(data []byte) string {
	d := xml.NewDecoder(strings.NewReader(string(data)))
	d.Strict = false
	key := ""
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var text string
		if el.Name.Local == "key" || el.Name.Local == "string" {
			if d.DecodeElement(&text, &el) != nil {
				return ""
			}
			text = strings.TrimSpace(text)
		}
		switch {
		case el.Name.Local == "key":
			key = text
			continue
		case el.Name.Local == "string" && key == "ProxyType" && text != "" && !strings.EqualFold(text, "None"):
			return "ProxyType"
		case el.Name.Local == "string" && key == "PayloadType" && text == "com.apple.proxy.http.global":
			return text
		}
		key = ""
	}
}
