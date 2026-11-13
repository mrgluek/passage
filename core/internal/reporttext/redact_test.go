package reporttext

import "testing"

func TestSecretsPreservesFollowingFields(t *testing.T) {
	for _, s := range []string{
		`{"token":abc,"status":"ok"}`,
		`{"token":"two words","status":"ok"}`,
		`{"token":'two words',"status":"ok"}`,
	} {
		if got := Secrets(s); got != `{"token":[redacted],"status":"ok"}` {
			t.Errorf("got %s", got)
		}
	}
	if got := Secrets("password: two words\nstatus: ok"); got != "password: [redacted]\nstatus: ok" {
		t.Fatal(got)
	}
}
