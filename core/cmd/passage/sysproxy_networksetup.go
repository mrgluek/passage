package main

import "strings"

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

// managedProxyKeys are names that appear in a configuration profile or a
// managed preference file when it sets proxies. They are stored as plain
// text in both XML and binary property lists.
var managedProxyKeys = []string{
	"com.apple.proxy.http.global",
	"ProxyAutoConfigURLString",
	"ProxyAutoConfigEnable",
	"ProxyPACURL",
	"ProxyType",
	"ProxyServer",
	"HTTPProxy",
	"HTTPSProxy",
	"SOCKSProxy",
}

// managedProxy names the first proxy key found in data, or "". In XML
// only whole keys and strings count, so a longer name does not match.
func managedProxy(data []byte) string {
	text := string(data)
	xml := strings.HasPrefix(strings.TrimSpace(text), "<?xml")
	for _, k := range managedProxyKeys {
		needle := k
		if xml {
			needle = ">" + k + "<"
		}
		if strings.Contains(text, needle) {
			return k
		}
	}
	return ""
}
