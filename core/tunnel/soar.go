package tunnel

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/getlantern/soar"
)

type soarClient interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	Established() bool
	PathStats() []soar.PathStat
	Close() error
}

// lazySoar starts the transport on demand and releases it after an idle period.
type lazySoar struct {
	newClient func() (soarClient, error)
	idle      time.Duration
	mu        sync.Mutex
	client    soarClient
	users     int
	timer     *time.Timer
	idleGen   uint64
	closed    bool
}

func newLazySoar(cfg soar.Config) *lazySoar {
	return &lazySoar{
		newClient: func() (soarClient, error) { return soar.NewClient(cfg) },
		idle:      90 * time.Second,
	}
}

func (c *lazySoar) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if c.client == nil {
		client, err := c.newClient()
		if err != nil {
			c.mu.Unlock()
			return nil, err
		}
		c.client = client
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.idleGen++
	c.users++
	client := c.client
	c.mu.Unlock()
	conn, err := client.DialContext(ctx, network, addr)
	if err != nil {
		c.release()
		return nil, err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		conn.Close()
		c.release()
		return nil, net.ErrClosed
	}
	return &soarConn{Conn: conn, release: c.release}, nil
}

func (c *lazySoar) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users--
	if !c.closed && c.users == 0 {
		c.idleGen++
		gen := c.idleGen
		c.timer = time.AfterFunc(c.idle, func() { c.closeIdle(gen) })
	}
}

func (c *lazySoar) closeIdle(gen uint64) {
	c.mu.Lock()
	if c.closed || c.idleGen != gen || c.users != 0 || c.client == nil {
		c.mu.Unlock()
		return
	}
	client := c.client
	c.client = nil
	c.timer = nil
	c.mu.Unlock()
	client.Close()
}

func (c *lazySoar) Established() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil && c.client.Established()
}

func (c *lazySoar) PathStats() []soar.PathStat {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	return c.client.PathStats()
}

func (c *lazySoar) Close() error {
	c.mu.Lock()
	c.closed = true
	c.idleGen++
	if c.timer != nil {
		c.timer.Stop()
	}
	client := c.client
	c.client = nil
	c.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

type soarConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *soarConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (c *soarConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return c.Close()
}
