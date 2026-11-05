package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

var errIrohExited = errors.New("transport exited")
var errDestinationRefused = errors.New("destination connection refused")

// errDestinationUnreachable is a refusal the exit may not repeat: it could
// not connect, as opposed to a destination it does not allow.
var errDestinationUnreachable = fmt.Errorf("%w: unreachable", errDestinationRefused)

func startIroh(ctx context.Context, r links.Route, binaryPath string, privateDir string, dns []string) (dialFunc, func(), error) {
	if !filepath.IsAbs(binaryPath) {
		return nil, nil, errors.New("provide an absolute dumbpipe path")
	}
	p, _ := links.Params[links.IrohParams](r)
	dir, err := os.MkdirTemp(privateDir, "iroh-*")
	if err != nil {
		return nil, nil, err
	}
	network, address, listen, err := irohListen(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	if err = os.WriteFile(filepath.Join(dir, "iroh-secret.txt"), []byte(hex.EncodeToString(key)+"\n"), 0600); err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	// Loopback only: relay traffic needs no listening socket on the network.
	args := append(listen, "--relay", p.Relay, "--no-direct", "--ipv4-addr", "127.0.0.1:0", "--ipv6-addr", "[::1]:0", p.Ticket)
	var env []string
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "IROH_SECRET=") {
			env = append(env, v)
		}
	}
	for _, resolver := range dns {
		host, _, err := net.SplitHostPort(resolver)
		if err == nil && net.ParseIP(host) != nil {
			args = append(args, "--dns-server", host)
			break
		}
	}
	done, err := startHelper(childCtx, binaryPath, args, env, dir)
	if err != nil {
		cancel()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	cleanup := func() { cancel(); <-done; os.RemoveAll(dir) }
	dial := func(ctx context.Context, _, target string) (net.Conn, error) {
		for {
			attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
			conn, e := (&net.Dialer{}).DialContext(attempt, network, address)
			if e == nil {
				deadline, _ := attempt.Deadline()
				conn.SetDeadline(deadline)
				e = socks(conn, target, p.Socks)
				if e == nil {
					conn.SetDeadline(time.Time{})
					cancel()
					return conn, nil
				}
				conn.Close()
				if errors.Is(e, errDestinationRefused) {
					cancel()
					return nil, e
				}
			}
			cancel()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				return nil, errIrohExited
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	return dial, cleanup, nil
}

// irohListen picks where the helper listens: a socket in dir, or a free
// loopback port on Windows, where the helper has no socket mode.
func irohListen(dir string) (network, address string, args []string, err error) {
	if runtime.GOOS != "windows" {
		socket := filepath.Join(dir, "proxy.sock")
		return "unix", socket, []string{"connect-unix", "--socket-path", socket}, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}
	address = ln.Addr().String()
	ln.Close()
	return "tcp", address, []string{"connect-tcp", "--addr", address}, nil
}

// socks opens target through a SOCKS5 proxy on conn, with username and
// password when auth has a username.
func socks(conn net.Conn, target string, auth links.SocksAuth) error {
	method := byte(2)
	if auth.Username == "" {
		method = 0
	}
	if _, err := conn.Write([]byte{5, 1, method}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != method {
		return errors.New("SOCKS authentication not offered")
	}
	if method == 0 {
		return socksRequest(conn, target)
	}
	request := []byte{1, byte(len(auth.Username))}
	request = append(request, auth.Username...)
	request = append(request, byte(len(auth.Password)))
	request = append(request, auth.Password...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 1 || reply[1] != 0 {
		return errors.New("SOCKS authentication refused")
	}
	return socksRequest(conn, target)
}

func socksRequest(conn net.Conn, target string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	var n uint16
	if _, err := parsePort(port, &n); err != nil {
		return err
	}
	request := []byte{5, 1, 0}
	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		request = append(request, 1)
		request = append(request, ip4...)
	} else if ip != nil {
		request = append(request, 4)
		request = append(request, ip.To16()...)
	} else {
		if len(host) > 255 {
			return errors.New("hostname too long")
		}
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, n)
	if _, err = conn.Write(request); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err = io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[0] != 5 || head[2] != 0 {
		return errors.New("invalid SOCKS reply")
	}
	if head[1] == 2 {
		return errDestinationRefused
	}
	if head[1] >= 3 && head[1] <= 6 {
		return errDestinationUnreachable
	}
	if head[1] != 0 {
		return errors.New("SOCKS connection failed")
	}
	count := 0
	switch head[3] {
	case 1:
		count = 4
	case 4:
		count = 16
	case 3:
		one := []byte{0}
		if _, err = io.ReadFull(conn, one); err != nil {
			return err
		}
		count = int(one[0])
	default:
		return errors.New("invalid SOCKS address")
	}
	_, err = io.CopyN(io.Discard, conn, int64(count+2))
	return err
}
func parsePort(s string, out *uint16) (int, error) {
	var n uint64
	if s == "" {
		return 0, errors.New("missing port")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid port")
		}
		n = n*10 + uint64(c-'0')
		if n > 65535 {
			return 0, errors.New("invalid port")
		}
	}
	if n == 0 {
		return 0, errors.New("invalid port")
	}
	*out = uint16(n)
	return len(s), nil
}
