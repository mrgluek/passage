package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type socksHandshakeResult struct {
	addr string
	err  error
}

func startSocksHandshake(t *testing.T, user, pass string) (net.Conn, <-chan socksHandshakeResult) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	deadline := time.Now().Add(3 * time.Second)
	server.SetDeadline(deadline)
	client.SetDeadline(deadline)
	done := make(chan socksHandshakeResult, 1)
	go func() {
		defer server.Close()
		addr, err := socksHandshake(server, user, pass)
		done <- socksHandshakeResult{addr, err}
	}()
	return client, done
}

func writeSocksBytes(t *testing.T, c net.Conn, b []byte) {
	t.Helper()
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

func readSocksBytes(t *testing.T, c net.Conn, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reply = %x, want %x", got, want)
	}
}

func awaitSocksHandshake(t *testing.T, done <-chan socksHandshakeResult) socksHandshakeResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("handshake did not finish")
		return socksHandshakeResult{}
	}
}

func TestSocksMethodNegotiation(t *testing.T) {
	for _, passwordOn := range []bool{false, true} {
		for _, offer := range []struct {
			methods []byte
			off, on byte
		}{
			{[]byte{0x00}, 0x00, 0xff},
			{[]byte{0x02}, 0x02, 0x02},
			{[]byte{0x00, 0x02}, 0x00, 0x02},
			{[]byte{0x02, 0x00}, 0x00, 0x02},
			{[]byte{0x01}, 0xff, 0xff},
			{[]byte{}, 0xff, 0xff},
		} {
			for _, credentials := range []struct {
				name, user, pass string
			}{
				{"correct", "u", "p"},
				{"wrong_user", "other", "p"},
				{"wrong_password", "u", "other"},
				{"empty", "", ""},
				{"max_length", string(bytes.Repeat([]byte{'u'}, 255)), string(bytes.Repeat([]byte{'p'}, 255))},
			} {
				t.Run(fmt.Sprintf("password_%t/methods_%x/%s", passwordOn, offer.methods, credentials.name), func(t *testing.T) {
					user, pass, wantMethod := "", "unused", offer.off
					if passwordOn {
						user, pass, wantMethod = "u", "p", offer.on
					}
					client, done := startSocksHandshake(t, user, pass)
					writeSocksBytes(t, client, append([]byte{5, byte(len(offer.methods))}, offer.methods...))
					readSocksBytes(t, client, []byte{5, wantMethod})
					wantError := wantMethod == 0xff
					if wantMethod == 0x02 {
						auth := append([]byte{1, byte(len(credentials.user))}, credentials.user...)
						auth = append(auth, byte(len(credentials.pass)))
						auth = append(auth, credentials.pass...)
						writeSocksBytes(t, client, auth)
						status := byte(0)
						if passwordOn && credentials.name != "correct" {
							status, wantError = 1, true
						}
						readSocksBytes(t, client, []byte{1, status})
					}
					if !wantError {
						request := append([]byte{5, 1, 0, 3, byte(len("example.org"))}, "example.org"...)
						writeSocksBytes(t, client, append(request, 0x01, 0xbb))
					}
					result := awaitSocksHandshake(t, done)
					if (result.err != nil) != wantError {
						t.Fatalf("handshake error = %v, want error %v", result.err, wantError)
					}
					if !wantError && result.addr != "example.org:443" {
						t.Fatalf("address = %q", result.addr)
					}
				})
			}
		}
	}
}

func TestSocksAuthTruncated(t *testing.T) {
	auth := []byte{1, 2, 'u', 'u', 2, 'p', 'p'}
	for _, user := range []string{"", "uu"} {
		for n := 0; n < len(auth); n++ {
			t.Run(fmt.Sprintf("password_%t/prefix_%d", user != "", n), func(t *testing.T) {
				client, done := startSocksHandshake(t, user, "pp")
				writeSocksBytes(t, client, []byte{5, 1, 2})
				readSocksBytes(t, client, []byte{5, 2})
				if n > 0 {
					writeSocksBytes(t, client, auth[:n])
				}
				client.Close()
				result := awaitSocksHandshake(t, done)
				if !errors.Is(result.err, io.EOF) && !errors.Is(result.err, io.ErrUnexpectedEOF) {
					t.Fatalf("truncated auth error = %v", result.err)
				}
			})
		}
	}
}

func TestSocksAuthBadVersion(t *testing.T) {
	for _, user := range []string{"", "u"} {
		t.Run(fmt.Sprintf("password_%t", user != ""), func(t *testing.T) {
			client, done := startSocksHandshake(t, user, "p")
			writeSocksBytes(t, client, []byte{5, 1, 2})
			readSocksBytes(t, client, []byte{5, 2})
			writeSocksBytes(t, client, []byte{2})
			if result := awaitSocksHandshake(t, done); result.err == nil {
				t.Fatal("invalid auth version accepted")
			}
		})
	}
}
