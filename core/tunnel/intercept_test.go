package tunnel

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeDNS answers every A query with 203.0.113.7, like an interceptor would.
func fakeDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			resp := append([]byte(nil), q...)
			resp[2] |= 0x80                         // QR
			binary.BigEndian.PutUint16(resp[6:], 1) // ANCOUNT
			resp = append(resp, 0xC0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 203, 0, 113, 7)
			pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDetectInterception(t *testing.T) {
	saved := interceptProbeTargets
	defer func() { interceptProbeTargets = saved }()

	interceptProbeTargets = []string{fakeDNS(t)}
	if !detectInterception(context.Background(), time.Second) {
		t.Fatal("an answer from a non-DNS address means interception")
	}

	// A bound but silent UDP socket: queries time out, as on a normal network.
	silent, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer silent.Close()
	interceptProbeTargets = []string{silent.LocalAddr().String()}
	if detectInterception(context.Background(), 200*time.Millisecond) {
		t.Fatal("no answer must not count as interception")
	}
}

func TestWhoamiParsesTheARecord(t *testing.T) {
	ip, err := dnsWhoami(context.Background(), fakeDNS(t))
	if err != nil || ip.String() != "203.0.113.7" {
		t.Fatalf("got %v, %v", ip, err)
	}
}

func TestParseRejectsForeignIDs(t *testing.T) {
	q, _ := buildQueryA(0x1234, "example.com")
	q[2] |= 0x80
	if _, err := parseResponseA(q, 0x4321); err == nil {
		t.Fatal("a response to another query id must be ignored")
	}
	if _, err := parseResponseA(q, 0x1234); err != nil {
		t.Fatalf("an empty answer to our id is still an answer: %v", err)
	}
}

func TestDotFirst(t *testing.T) {
	in := []string{"77.88.8.8:53", "77.88.8.8:853", "8.8.8.8:53", "77.88.8.1:853"}
	if got := strings.Join(dotFirst(in), ","); got != "77.88.8.8:853,77.88.8.1:853,77.88.8.8:53,8.8.8.8:53" {
		t.Fatalf("got %s", got)
	}
}
