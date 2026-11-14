package tunnel

import "strings"

func groupApp(kind, host string) string {
	if kind != "relay" {
		return ""
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, app := range []struct{ domain, name string }{{"whatsapp.net", "whatsapp"}, {"signal.org", "signal"}} {
		if host == app.domain || strings.HasSuffix(host, "."+app.domain) {
			return app.name
		}
	}
	return ""
}
