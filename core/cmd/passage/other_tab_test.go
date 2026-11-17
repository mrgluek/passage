package main

import (
	"strings"
	"testing"
)

func TestOtherTab(t *testing.T) {
	for _, want := range []string{
		`else renderOther(pane, v, para)`, `t("other_app_title")`, `t("other_apps_text")`, `t("other_servers")`,
		`row(t("proxy_host"), "127.0.0.1", "127.0.0.1")`, `t("proxy_pass")`, `if (!v.auth) pane.append(para(t("proxy_no_password")))`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("UI missing %q", want)
		}
	}
	for lang, table := range texts {
		for _, key := range []string{"tab_other", "other_app_title", "other_apps_text", "other_servers"} {
			if table[key] == "" {
				t.Errorf("%s: %s is missing", lang, key)
			}
		}
	}
	if !strings.Contains(texts["en"]["other_servers"], "Signal") || !strings.Contains(texts["en"]["other_servers"], "SimpleX") {
		t.Error("the note must name Signal and SimpleX")
	}
}
