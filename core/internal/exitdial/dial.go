// Package exitdial races connections to already-checked destination addresses.
package exitdial

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

const fallbackDelay = 250 * time.Millisecond
const attemptTimeout = 3 * time.Second

type dialFunc func(context.Context, string, string) (net.Conn, error)

// Dial connects to IP literals without resolving the destination again.
func Dial(ctx context.Context, addresses []netip.Addr, port uint16) (net.Conn, error) {
	return dial(ctx, addresses, port, (&net.Dialer{}).DialContext)
}

func dial(ctx context.Context, addresses []netip.Addr, port uint16, connect dialFunc) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	addresses = alternate(addresses)
	if len(addresses) == 0 {
		return nil, errors.New("no destination addresses")
	}
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result)
	timer := time.NewTimer(fallbackDelay)
	defer timer.Stop()
	next, active := 0, 0
	start := func() {
		addr := addresses[next]
		next++
		active++
		timer.Reset(fallbackDelay)
		go func() {
			attempt, stop := context.WithTimeout(ctx, attemptTimeout)
			conn, err := connect(attempt, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(int(port))))
			stop()
			select {
			case results <- result{conn, err}:
			case <-ctx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start()
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			active--
			if r.err == nil {
				return r.conn, nil
			}
			if r.conn != nil {
				r.conn.Close()
			}
			lastErr = r.err
			if next < len(addresses) {
				start()
			} else if active == 0 {
				return nil, lastErr
			}
		case <-timer.C:
			if next < len(addresses) {
				start()
			}
		}
	}
}

func alternate(addresses []netip.Addr) []netip.Addr {
	if len(addresses) == 0 {
		return nil
	}
	firstV4 := addresses[0].Unmap().Is4()
	var first, other []netip.Addr
	for _, addr := range addresses {
		if addr.Unmap().Is4() == firstV4 {
			first = append(first, addr)
		} else {
			other = append(other, addr)
		}
	}
	ordered := make([]netip.Addr, 0, len(addresses))
	for len(first) > 0 || len(other) > 0 {
		if len(first) > 0 {
			ordered = append(ordered, first[0])
			first = first[1:]
		}
		if len(other) > 0 {
			ordered = append(ordered, other[0])
			other = other[1:]
		}
	}
	return ordered
}
