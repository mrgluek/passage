package tunnel

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/getlantern/soar"
	links "github.com/mrgluek/passage-link/link"
)

// myPriority puts the user's own proxy level with the first route a link
// usually carries, and ties go to My; a link route below it still goes first.
const myPriority = 10

var errMyAddress = errors.New("my route: expected host:port of a SOCKS5 proxy")

// parseMy reads [user:pass@]host:port, also as a socks://, socks5:// or
// socks5h:// URL, or a t.me/socks or tg://socks proxy link.
func parseMy(s string) (string, links.SocksAuth, error) {
	var auth links.SocksAuth
	if !strings.Contains(s, "://") {
		s = "socks5://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", auth, errMyAddress
	}
	hostport := u.Host
	switch scheme := strings.ToLower(u.Scheme); {
	case scheme == "socks" || scheme == "socks5" || scheme == "socks5h":
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
			return "", auth, errMyAddress
		}
		if u.User != nil {
			auth.Username = u.User.Username()
			auth.Password, _ = u.User.Password()
		}
	case scheme == "tg" && strings.EqualFold(u.Host, "socks"),
		(scheme == "https" || scheme == "http") && (strings.EqualFold(u.Host, "t.me") || strings.EqualFold(u.Host, "telegram.me")) && strings.TrimSuffix(u.Path, "/") == "/socks":
		q := u.Query()
		hostport = net.JoinHostPort(q.Get("server"), q.Get("port"))
		auth.Username, auth.Password = q.Get("user"), q.Get("pass")
	default:
		return "", auth, errMyAddress
	}
	host, port, err := net.SplitHostPort(hostport)
	var n uint16
	if err != nil || host == "" {
		return "", auth, errMyAddress
	}
	if _, err = parsePort(port, &n); err != nil {
		return "", auth, errors.New("my route: invalid port")
	}
	if auth.Username == "" && auth.Password != "" || len(auth.Username) > 255 || len(auth.Password) > 255 {
		return "", auth, errors.New("my route: invalid username or password")
	}
	return net.JoinHostPort(host, port), auth, nil
}

// normalizeMy writes a My route in the stored form, [user:pass@]host:port.
func normalizeMy(s string) (string, error) {
	addr, auth, err := parseMy(s)
	if err != nil || auth.Username == "" {
		return addr, err
	}
	return url.UserPassword(auth.Username, auth.Password).String() + "@" + addr, nil
}

// myClient carries connections through the user's own SOCKS5 proxy, which
// resolves hostnames itself.
type myClient struct {
	addr  string
	auth  links.SocksAuth
	ready atomic.Bool
}

func newMyClient(s string) (*myClient, error) {
	addr, auth, err := parseMy(s)
	if err != nil {
		return nil, err
	}
	return &myClient{addr: addr, auth: auth}, nil
}

func (c *myClient) DialContext(ctx context.Context, _, target string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		c.ready.Store(false)
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	err = socks(conn, target, c.auth)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		conn.Close()
		if !errors.Is(err, errDestinationRefused) {
			c.ready.Store(false)
		}
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	c.ready.Store(true)
	return conn, nil
}

func (c *myClient) Established() bool          { return c.ready.Load() }
func (c *myClient) PathStats() []soar.PathStat { return nil }
func (c *myClient) Close() error               { return nil }

// pointsAt reports whether the My proxy is listen itself, which would loop.
func pointsAt(my string, listen net.Addr) bool {
	addr, _, err := parseMy(my)
	if err != nil {
		return false
	}
	host, port, _ := net.SplitHostPort(addr)
	l, ok := listen.(*net.TCPAddr)
	if !ok || port != strconv.Itoa(l.Port) {
		return false
	}
	ip := net.ParseIP(host)
	if host == "localhost" || ip != nil && ip.IsLoopback() {
		return l.IP.IsLoopback() || l.IP.IsUnspecified()
	}
	return ip != nil && (ip.Equal(l.IP) || l.IP.IsUnspecified())
}
