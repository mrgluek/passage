package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordSender logs what sendParts does, in order.
type recordSender struct {
	mu     sync.Mutex
	events []string
	sent   []byte
}

func (r *recordSender) add(e string) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }
func (r *recordSender) write(b []byte) (int, error) {
	r.add("write " + itoa(len(b)))
	r.mu.Lock()
	r.sent = append(r.sent, b...)
	r.mu.Unlock()
	return len(b), nil
}
func (r *recordSender) writeOOB(b []byte) (int, error) {
	r.add("oob " + itoa(len(b)))
	r.mu.Lock()
	r.sent = append(r.sent, b...)
	r.mu.Unlock()
	return len(b), nil
}
func (r *recordSender) lowTTL() (func(), error) {
	r.add("ttl 1")
	return func() { r.add("ttl back") }, nil
}
func (r *recordSender) waitSent() { r.add("sent") }

// The TTL comes back only after the low-TTL part has left the socket.
func TestLowTTLIsRestoredAfterThePartLeft(t *testing.T) {
	r := &recordSender{}
	sendParts(r, []part{{b: []byte("a"), lowTTL: true}, {b: []byte("bcd")}})
	want := "ttl 1,write 1,sent,ttl back,write 3,sent"
	if got := strings.Join(r.events, ","); got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}

// goHello is the first record of a Go TLS client for name.
func goHello(t *testing.T, name string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go tls.Client(a, &tls.Config{ServerName: name}).HandshakeContext(context.Background())
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(b, hdr); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, int(hdr[3])<<8|int(hdr[4]))
	if _, err := io.ReadFull(b, body); err != nil {
		t.Fatal(err)
	}
	return append(hdr, body...)
}

func TestSNICutsOnARealClientHello(t *testing.T) {
	for _, name := range []string{"www.example.org", "example.org", "a.b.example.co.uk"} {
		rec := goHello(t, name)
		start, end, ok := helloName(rec)
		if !ok || string(rec[start:end]) != name {
			t.Fatalf("%s: %v %q", name, ok, rec[start:end])
		}
		cuts, _ := sniCuts(rec)
		mid := cuts[2]
		sld := name[mid-start-1 : mid-start+1]
		if cuts[0] != 1 || cuts[1] != start || cuts[3] != end-1 || mid <= start || mid >= end-1 {
			t.Fatalf("%s: cuts %v (name %d-%d)", name, cuts, start, end)
		}
		if !strings.Contains(name, sld) {
			t.Fatal(sld)
		}
		for name, plan := range map[string]func([]byte) []part{"split": splitSNIPlan, "disorder": disorderSNIPlan, "oob": oobSNIPlan} {
			parts := plan(rec)
			var joined []byte
			for _, p := range parts {
				joined = append(joined, p.b...)
			}
			if !bytes.Equal(joined, rec) || len(parts) < 3 || len(parts) > 8 {
				t.Fatalf("%s: %d parts, same bytes %v", name, len(parts), bytes.Equal(joined, rec))
			}
		}
	}
}

func TestMidOfTheSecondLevelLabel(t *testing.T) {
	for name, want := range map[string]int{"example.org": 3, "www.example.org": 7, "ab.io": 1, "localhost": 4} {
		if got := midSLD(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
}

// loopback returns a connected TCP pair.
func loopback(t *testing.T) (*net.TCPConn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); acc <- c }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s := <-acc
	t.Cleanup(func() { c.Close(); s.Close() })
	return c.(*net.TCPConn), s
}

// The record is collected over several writes and then sent by plan.
func TestPartConnCollectsTheWholeFirstRecord(t *testing.T) {
	c, srv := loopback(t)
	rec := goHello(t, "www.example.org")
	r := &recordSender{}
	pc := &partConn{TCPConn: c, plan: splitSNIPlan, sender: r}
	pc.Write(rec[:3])
	pc.Write(rec[3:200])
	if len(r.events) != 0 {
		t.Fatal("sent before the record was whole")
	}
	pc.Write(append(append([]byte{}, rec[200:]...), "tail"...))
	if !bytes.Equal(r.sent, append(append([]byte{}, rec...), "tail"...)) || strings.Count(strings.Join(r.events, ","), "write") < 4 {
		t.Fatalf("%v", r.events)
	}
	_ = srv
}

// Anything but a ClientHello passes unchanged in one write.
func TestPartConnLeavesOtherDataAlone(t *testing.T) {
	for _, data := range [][]byte{[]byte("GET / HTTP/1.1\r\n\r\n"), {0x17, 3, 3, 0, 2, 1, 2}} {
		c, srv := loopback(t)
		r := &recordSender{}
		pc := &partConn{TCPConn: c, plan: splitSNIPlan, sender: r}
		if _, err := pc.Write(data); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(data))
		srv.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := io.ReadFull(srv, got); err != nil || !bytes.Equal(got, data) || len(r.events) != 0 {
			t.Fatalf("%q %v %v", got, err, r.events)
		}
	}
}

// A first record that never completes goes out after a short wait.
func TestPartConnSendsAnIncompleteRecordAfterAWait(t *testing.T) {
	old := firstRecordWait
	firstRecordWait = 20 * time.Millisecond
	t.Cleanup(func() { firstRecordWait = old })
	c, srv := loopback(t)
	pc := &partConn{TCPConn: c, plan: splitSNIPlan, sender: &recordSender{}}
	rec := goHello(t, "example.org")
	pc.Write(rec[:100])
	got := make([]byte, 100)
	srv.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(srv, got); err != nil || !bytes.Equal(got, rec[:100]) {
		t.Fatalf("held back: %v", err)
	}
}

func TestRecordLength(t *testing.T) {
	for _, tc := range []struct {
		p    []byte
		want int
	}{{[]byte{0x16, 3, 1, 5, 0xe0}, 5 + 0x5e0}, {[]byte{0x16, 3, 1, 0, 3}, 8}, {[]byte{0x16, 3}, 5}, {[]byte("GET"), 0}, {[]byte{0x16, 3, 1, 0xff, 0xff}, maxRecord}} {
		if got := recordLen(tc.p); got != tc.want {
			t.Errorf("% x: %d, want %d", tc.p, got, tc.want)
		}
	}
}

// The apps' search is what it was before the name cuts: same strategies,
// same order, so a search that finds nothing takes as long as before.
func TestAppStrategyOrder(t *testing.T) {
	var names []string
	for _, s := range appStrategies(smartStrategies()) {
		names = append(names, s.name)
	}
	if got := strings.Join(names, ","); got != "tlsfrag:1,split:2,disorder:1" {
		t.Fatal(got)
	}
}
