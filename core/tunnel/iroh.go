package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	links "github.com/mrgluek/passage-link/link"
)

var errIrohExited = errors.New("transport exited")

func startIroh(ctx context.Context, r links.Route, binaryPath string, privateDir string, dns []string) (dialFunc, func(), error) {
	if !filepath.IsAbs(binaryPath) {
		return nil, nil, errors.New("provide an absolute dumbpipe path")
	}
	p, _ := links.Params[links.IrohParams](r)
	dir, err := os.MkdirTemp(privateDir, "iroh-*")
	if err != nil {
		return nil, nil, err
	}
	socket := filepath.Join(dir, "proxy.sock")
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
	cmd := exec.CommandContext(childCtx, binaryPath, "connect-unix", "--socket-path", socket, "--relay", p.Relay, "--no-direct", p.Ticket)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "IROH_SECRET=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	for _, resolver := range dns {
		host, _, err := net.SplitHostPort(resolver)
		if err == nil && net.ParseIP(host) != nil {
			cmd.Args = append(cmd.Args, "--dns-server", host)
			break
		}
	}
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	cleanup := func() { cancel(); <-done; os.RemoveAll(dir) }
	dial := func(ctx context.Context, _, target string) (net.Conn, error) {
		for {
			attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
			conn, e := (&net.Dialer{}).DialContext(attempt, "unix", socket)
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
func socks(conn net.Conn, target string, auth links.SocksAuth) error {
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 2 {
		return errors.New("SOCKS authentication not offered")
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
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	var n uint16
	if _, err := parsePort(port, &n); err != nil {
		return err
	}
	request = []byte{5, 1, 0}
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
	if head[0] != 5 || head[1] != 0 || head[2] != 0 {
		return errors.New("SOCKS connection refused")
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
