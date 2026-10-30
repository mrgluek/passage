package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

// telegramRanges: https://core.telegram.org/resources/cidr.txt (copied 2026-10-30
// without a fresh fetch; re-check against the file).
var telegramRanges = mustPrefixes(
	"91.108.56.0/22",
	"91.108.4.0/22",
	"91.108.8.0/22",
	"91.108.16.0/22",
	"91.108.12.0/22",
	"149.154.160.0/20",
	"91.105.192.0/23",
	"91.108.20.0/22",
	"185.76.151.0/24",
	"2001:b28:f23d::/48",
	"2001:b28:f23f::/48",
	"2001:67c:4e8::/48",
	"2001:b28:f23c::/48",
	"2a0a:f280::/32",
)

// telegramDCs are production data centers 1-5, as in Telegram Desktop's
// built-in list.
var telegramDCs = []string{
	"149.154.175.53:443",
	"149.154.167.41:443",
	"149.154.175.100:443",
	"149.154.167.91:443",
	"91.108.56.191:443",
}

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// isTelegram reports whether host is an IP literal in Telegram's ranges.
func isTelegram(host string) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, p := range telegramRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// MTProto constructors and transport bytes.
const (
	ctorReqPQMulti   = 0xbe7e8ef1
	ctorResPQ        = 0x05162463
	abridgedTag      = 0xef
	maxMTProtoAnswer = 4096
)

// probeMTProtoVia sends an unencrypted req_pq_multi in the abridged
// transport and waits for a resPQ carrying the same nonce.
func probeMTProtoVia(ctx context.Context, dial dialFunc, hostport string) error {
	c, err := dial(ctx, "tcp", hostport)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}

	var nonce [16]byte
	rand.Read(nonce[:])
	// auth_key_id, message_id, message_data_length, then the body.
	msg := make([]byte, 0, 2+40)
	msg = append(msg, abridgedTag, 40/4)
	msg = binary.LittleEndian.AppendUint64(msg, 0)
	msg = binary.LittleEndian.AppendUint64(msg, messageID(time.Now()))
	msg = binary.LittleEndian.AppendUint32(msg, 20)
	msg = binary.LittleEndian.AppendUint32(msg, ctorReqPQMulti)
	msg = append(msg, nonce[:]...)
	if _, err := c.Write(msg); err != nil {
		return err
	}

	body, err := readAbridged(c)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return checkResPQ(body, nonce)
}

// messageID is a client message_id: unixtime*2^32, divisible by 4.
func messageID(now time.Time) uint64 {
	frac := uint64(now.Nanosecond()) << 32 / 1e9
	return (uint64(now.Unix())<<32 | frac) &^ 3
}

// readAbridged reads one abridged packet.
func readAbridged(r io.Reader) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:1]); err != nil {
		return nil, err
	}
	n := int(h[0])
	if n >= 0x7f {
		if _, err := io.ReadFull(r, h[:3]); err != nil {
			return nil, err
		}
		h[3] = 0
		n = int(binary.LittleEndian.Uint32(h[:]))
	}
	n *= 4
	if n == 0 || n > maxMTProtoAnswer {
		return nil, fmt.Errorf("mtproto: packet of %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

func checkResPQ(b []byte, nonce [16]byte) error {
	if len(b) == 4 {
		return fmt.Errorf("mtproto: error %d", int32(binary.LittleEndian.Uint32(b)))
	}
	if len(b) < 20+4+16 {
		return errors.New("mtproto: short answer")
	}
	if binary.LittleEndian.Uint64(b) != 0 {
		return errors.New("mtproto: answer is not unencrypted")
	}
	if binary.LittleEndian.Uint64(b[8:])%4 != 1 {
		return errors.New("mtproto: not a response message_id")
	}
	data := b[20:]
	if n := binary.LittleEndian.Uint32(b[16:]); int(n) > len(data) || n < 20 {
		return errors.New("mtproto: bad message length")
	}
	if binary.LittleEndian.Uint32(data) != ctorResPQ {
		return errors.New("mtproto: not resPQ")
	}
	if [16]byte(data[4:20]) != nonce {
		return errors.New("mtproto: nonce mismatch")
	}
	return nil
}

func probeMTProto(ctx context.Context, hostport string) error {
	d := &net.Dialer{Timeout: probeTimeout}
	return probeMTProtoVia(ctx, d.DialContext, hostport)
}
