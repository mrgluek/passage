package tunnel

import (
	"errors"
	"net"
	"sync"
)

// BrowserPort is a browser listener that outlives a router: the app keeps
// it across restarts and gives it to each new router, so the port goes on
// accepting while one router stops and the next starts. Connections that
// arrive in between wait for the next router.
type BrowserPort struct {
	ln    net.Listener
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// ListenBrowserPort opens the port on addr, which must be 127.0.0.1:port.
func ListenBrowserPort(addr string) (*BrowserPort, error) {
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		return nil, errors.New("the address must be 127.0.0.1 and a port")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &BrowserPort{ln: ln, conns: make(chan net.Conn), done: make(chan struct{})}
	go p.accept()
	return p, nil
}

func (p *BrowserPort) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			select {
			case <-p.done:
				return
			default:
				continue
			}
		}
		select {
		case p.conns <- c:
		case <-p.done:
			c.Close()
			return
		}
	}
}

// Addr is the port's address.
func (p *BrowserPort) Addr() net.Addr { return p.ln.Addr() }

// Close closes the port for good.
func (p *BrowserPort) Close() error {
	p.once.Do(func() { close(p.done) })
	return p.ln.Close()
}

// view is one router's listener on the port; closing it leaves the port open.
func (p *BrowserPort) view() net.Listener {
	return &portView{p: p, done: make(chan struct{})}
}

type portView struct {
	p    *BrowserPort
	done chan struct{}
	once sync.Once
}

func (v *portView) Accept() (net.Conn, error) {
	select {
	case <-v.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case c := <-v.p.conns:
		return c, nil
	case <-v.done:
		return nil, net.ErrClosed
	case <-v.p.done:
		return nil, net.ErrClosed
	}
}

func (v *portView) Close() error {
	v.once.Do(func() { close(v.done) })
	return nil
}

func (v *portView) Addr() net.Addr { return v.p.ln.Addr() }
