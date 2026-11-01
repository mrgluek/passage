package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getlantern/soar"
)

type testSoarClient struct {
	dial   dialFunc
	closed chan struct{}
	once   sync.Once
}

func (c *testSoarClient) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return c.dial(ctx, network, addr)
}

func (c *testSoarClient) Established() bool          { return true }
func (c *testSoarClient) PathStats() []soar.PathStat { return []soar.PathStat{{}} }
func (c *testSoarClient) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestSoarStaysStoppedUntilNeeded(t *testing.T) {
	var starts atomic.Int32
	c := &lazySoar{newClient: func() (soarClient, error) {
		starts.Add(1)
		return nil, errors.New("not needed")
	}, idle: time.Second}
	if c.Established() || c.PathStats() != nil {
		t.Fatal("unused transport must have no session or paths")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.DialContext(ctx, "tcp", "relay.example.org:993"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled dial: %v", err)
	}
	c.Close()
	if _, err := c.DialContext(context.Background(), "tcp", "relay.example.org:993"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("dial after close: %v", err)
	}
	if starts.Load() != 0 {
		t.Fatal("status, cancelled dials and close must not start the transport")
	}
}

func TestSoarConcurrentDialsShareClient(t *testing.T) {
	var starts, dials atomic.Int32
	backend := &testSoarClient{dial: echoDialer("T:", &dials), closed: make(chan struct{})}
	c := &lazySoar{newClient: func() (soarClient, error) {
		starts.Add(1)
		return backend, nil
	}, idle: time.Hour}
	t.Cleanup(func() { c.Close() })
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := c.DialContext(context.Background(), "tcp", "relay.example.org:993")
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		}()
	}
	wg.Wait()
	if starts.Load() != 1 || dials.Load() != 8 || !c.Established() || len(c.PathStats()) != 1 {
		t.Fatalf("starts=%d dials=%d", starts.Load(), dials.Load())
	}
}

func TestDirectTrafficDoesNotStartSoar(t *testing.T) {
	var starts, direct, tunnel atomic.Int32
	var blocked atomic.Bool
	works, fails := echoDialer("D:", &direct), failDialer(&direct)
	tt := newTestTunnel(t, ModeAuto, func(ctx context.Context, network, addr string) (net.Conn, error) {
		if blocked.Load() {
			return fails(ctx, network, addr)
		}
		return works(ctx, network, addr)
	}, nil)
	tt.client = &lazySoar{idle: time.Hour, newClient: func() (soarClient, error) {
		starts.Add(1)
		return &testSoarClient{dial: echoDialer("T:", &tunnel), closed: make(chan struct{})}, nil
	}}
	tt.ways[RouteTunnel] = way{tt.client.DialContext, time.Second}
	tt.probeTLS = func(context.Context, string, string) error { return nil }
	for _, want := range []string{"D:hi", "T:hi"} {
		conn, reply := socksConnect(t, tt.Addr(), "u", "p", "relay.example.org:993")
		if reply != repSucceeded {
			conn.Close()
			t.Fatalf("SOCKS reply %d", reply)
		}
		got := roundTrip(t, conn)
		conn.Close()
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		if !blocked.Load() {
			tt.check()
			if s := tt.Status(0); s.Session || len(s.Paths) != 0 || starts.Load() != 0 {
				t.Fatal("direct traffic and healthy probes started the tunnel")
			}
		}
		blocked.Store(true)
	}
	if starts.Load() != 1 || tunnel.Load() != 1 {
		t.Fatalf("fallback starts=%d dials=%d", starts.Load(), tunnel.Load())
	}
}

type halfCloseConn struct {
	net.Conn
	writesClosed atomic.Int32
}

func (c *halfCloseConn) CloseWrite() error { c.writesClosed.Add(1); return nil }

func TestSoarReleasesIdleClientAndRestarts(t *testing.T) {
	var starts atomic.Int32
	created := make(chan *testSoarClient, 2)
	c := &lazySoar{idle: 20 * time.Millisecond, newClient: func() (soarClient, error) {
		starts.Add(1)
		backend := &testSoarClient{closed: make(chan struct{}), dial: func(context.Context, string, string) (net.Conn, error) {
			a, b := net.Pipe()
			t.Cleanup(func() { b.Close() })
			return &halfCloseConn{Conn: a}, nil
		}}
		created <- backend
		return backend, nil
	}}
	t.Cleanup(func() { c.Close() })
	conn, err := c.DialContext(context.Background(), "tcp", "relay.example.org:993")
	if err != nil {
		t.Fatal(err)
	}
	backend := <-created
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.closed:
		t.Fatal("an open stream must keep its transport")
	case <-time.After(2 * c.idle):
	}
	if got := conn.(*soarConn).Conn.(*halfCloseConn).writesClosed.Load(); got != 1 {
		t.Fatalf("CloseWrite not forwarded: %d", got)
	}
	conn.Close()
	conn.Close()
	select {
	case <-backend.closed:
	case <-time.After(time.Second):
		t.Fatal("unused transport did not stop")
	}
	if c.Established() || c.PathStats() != nil {
		t.Fatal("retired transport still reported active")
	}
	conn, err = c.DialContext(context.Background(), "tcp", "relay.example.org:993")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if starts.Load() != 2 {
		t.Fatalf("transport did not restart: %d", starts.Load())
	}
}

func TestSoarFailedDialReleasesClient(t *testing.T) {
	backend := &testSoarClient{closed: make(chan struct{}), dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unreachable")
	}}
	c := &lazySoar{idle: time.Millisecond, newClient: func() (soarClient, error) { return backend, nil }}
	t.Cleanup(func() { c.Close() })
	if _, err := c.DialContext(context.Background(), "tcp", "relay.example.org:993"); err == nil {
		t.Fatal("failed dial must return its error")
	}
	select {
	case <-backend.closed:
	case <-time.After(time.Second):
		t.Fatal("failed dial kept the transport running")
	}
}

func TestSoarDoesNotRetirePendingDial(t *testing.T) {
	entered := make(chan struct{})
	finish := make(chan struct{})
	backend := &testSoarClient{closed: make(chan struct{}), dial: func(context.Context, string, string) (net.Conn, error) {
		close(entered)
		<-finish
		return nil, errors.New("unreachable")
	}}
	c := &lazySoar{idle: time.Millisecond, newClient: func() (soarClient, error) { return backend, nil }}
	t.Cleanup(func() { c.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.DialContext(context.Background(), "tcp", "relay.example.org:993")
	}()
	<-entered
	select {
	case <-backend.closed:
		t.Fatal("pending dial lost its transport")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	<-done
	select {
	case <-backend.closed:
	case <-time.After(time.Second):
		t.Fatal("finished dial kept the transport running")
	}
}
