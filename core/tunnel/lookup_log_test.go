package tunnel

import (
	"errors"
	"fmt"
	"net"
	"testing"
)

func TestLookupFailureKind(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing name", &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, "no such name"},
		{"no answer", fmt.Errorf("dial: %w", &net.DNSError{Err: "i/o timeout", IsTimeout: true}), "no answer"},
		{"other lookup error", &net.DNSError{Err: "server misbehaving"}, "error"},
		{"not a lookup", errors.New("connection refused"), ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got := lookupFailureKind(c.err); got != c.want {
			t.Errorf("%s: kind = %q, want %q", c.name, got, c.want)
		}
	}
}
