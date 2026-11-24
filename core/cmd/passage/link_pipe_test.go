package main

import "testing"

func TestLinkPipeNaming(t *testing.T) {
	sid := "S-1-5-21-1004336348-1177238915-682003330-1001"
	if got := linkPipePath(sid); got != `\\.\pipe\PassageImport-`+sid {
		t.Fatal(got)
	}
	if got := linkPipeSDDL(sid); got != "O:"+sid+"D:P(A;;GA;;;"+sid+")" {
		t.Fatal(got)
	}
}

func TestValidSID(t *testing.T) {
	for sid, want := range map[string]bool{
		"S-1-5-21-1004336348-1177238915-682003330-1001": true,
		"S-1-5-18":           true,
		"":                   false,
		"S-1-":               false,
		"S-1-5-)(A;;GA;;;WD": false,
		"S-1-5-1 ":           false,
		"X-1-5-18":           false,
		"S-1-5-18-":          false,
	} {
		if validSID(sid) != want {
			t.Errorf("validSID(%q) != %v", sid, want)
		}
	}
}
