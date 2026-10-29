package tunnel

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// SOCKS5 reply codes (RFC 1928 §6).
const (
	repSucceeded       = 0x00
	repGeneralFailure  = 0x01
	repHostUnreachable = 0x04
	repCmdUnsupported  = 0x07
	repAtypUnsupported = 0x08
)

// socksHandshake reads a SOCKS5 greeting and CONNECT request from c and
// returns the requested host:port. With a non-empty user it requires
// username/password auth (RFC 1929), so other apps on the device can't use
// the proxy. The caller sends the final reply with socksReply.
func socksHandshake(c io.ReadWriter, user, pass string) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return "", err
	}
	if hdr[0] != 5 {
		return "", fmt.Errorf("not SOCKS5 (version %d)", hdr[0])
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	want := byte(0x00)
	if user != "" {
		want = 0x02
	}
	offered := false
	for _, m := range methods {
		if m == want {
			offered = true
		}
	}
	if !offered {
		c.Write([]byte{5, 0xff})
		return "", errors.New("client offers no acceptable auth method")
	}
	if _, err := c.Write([]byte{5, want}); err != nil {
		return "", err
	}
	if want == 0x02 {
		if err := socksAuth(c, user, pass); err != nil {
			return "", err
		}
	}

	var req [4]byte // VER CMD RSV ATYP
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", err
	}
	if req[1] != 1 {
		socksReply(c, repCmdUnsupported)
		return "", fmt.Errorf("unsupported command %d", req[1])
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3: // domain name, resolved at the far end
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return "", err
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		socksReply(c, repAtypUnsupported)
		return "", fmt.Errorf("unsupported address type %d", req[3])
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:])))), nil
}

func socksAuth(c io.ReadWriter, user, pass string) error {
	var ver [1]byte
	if _, err := io.ReadFull(c, ver[:]); err != nil {
		return err
	}
	if ver[0] != 1 {
		return fmt.Errorf("bad auth version %d", ver[0])
	}
	u, err := readLenPrefixed(c)
	if err != nil {
		return err
	}
	p, err := readLenPrefixed(c)
	if err != nil {
		return err
	}
	ok := subtle.ConstantTimeCompare(u, []byte(user)) == 1 && subtle.ConstantTimeCompare(p, []byte(pass)) == 1
	if !ok {
		c.Write([]byte{1, 1})
		return errors.New("bad SOCKS credentials")
	}
	_, err = c.Write([]byte{1, 0})
	return err
}

func readLenPrefixed(r io.Reader) ([]byte, error) {
	var n [1]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	b := make([]byte, n[0])
	_, err := io.ReadFull(r, b)
	return b, err
}

// socksReply sends a CONNECT reply with a zero IPv4 bind address.
func socksReply(w io.Writer, code byte) error {
	_, err := w.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}
