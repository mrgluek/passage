package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestIrohRestartFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		results  []error
		restarts int
	}{
		{"timeouts", []error{context.DeadlineExceeded, context.DeadlineExceeded, context.DeadlineExceeded}, 1},
		{"transport", []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}, 1},
		{"refusals", []error{errDestinationRefused, errDestinationUnreachable, errDestinationRefused}, 0},
		{"refusal preserves count", []error{context.DeadlineExceeded, errDestinationRefused, context.DeadlineExceeded, errDestinationUnreachable, context.DeadlineExceeded}, 1},
		{"success clears count", []error{context.DeadlineExceeded, context.DeadlineExceeded, nil, context.DeadlineExceeded, context.DeadlineExceeded}, 0},
		{"canceled", []error{context.Canceled, context.Canceled, context.Canceled}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			log := newEventLog(10)
			starts, stops, index := 0, 0, 0
			c := &irohClient{dir: t.TempDir(), now: func() time.Time { return now }, onRestart: func(at time.Time) {
				log.addAt(at, "%s", historyLine(historyTransition{prefix: "restarted", unit: "iroh helper", reason: "no answer"}))
			}}
			defer c.Close()
			c.start = func(life context.Context) (dialFunc, func(), error) {
				starts++
				done := make(chan struct{})
				go func() { defer close(done); <-life.Done() }()
				return func(ctx context.Context, _, _ string) (net.Conn, error) {
					err := tc.results[index]
					index++
					if errors.Is(err, context.DeadlineExceeded) {
						<-ctx.Done()
					}
					if err != nil {
						return nil, err
					}
					a, b := net.Pipe()
					b.Close()
					return a, nil
				}, func() { <-done; stops++ }, nil
			}
			for range tc.results {
				ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
				conn, _ := c.DialContext(ctx, "tcp", "example.org:443")
				cancel()
				if conn != nil {
					conn.Close()
				}
			}
			events := log.last(10)
			if stops != tc.restarts || len(events) != tc.restarts {
				t.Fatalf("stops=%d history=%v", stops, events)
			}
			for _, event := range events {
				if event.Msg != "restarted: iroh helper (no answer)" || !event.Time.Equal(now) {
					t.Fatalf("event=%+v", event)
				}
			}
			c.Close()
			c.Close()
			if starts != stops {
				t.Fatalf("helpers started=%d stopped=%d", starts, stops)
			}
		})
	}
}

func TestIrohRestartLimitAndRecovery(t *testing.T) {
	now := time.Unix(1000, 0)
	starts, stops := 0, 0
	fail := true
	c := &irohClient{dir: t.TempDir(), now: func() time.Time { return now }}
	c.start = func(context.Context) (dialFunc, func(), error) {
		starts++
		return func(context.Context, string, string) (net.Conn, error) {
			if fail {
				return nil, context.DeadlineExceeded
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		}, func() { stops++ }, nil
	}
	defer c.Close()
	dial := func(n int) {
		t.Helper()
		for range n {
			c.DialContext(context.Background(), "tcp", "example.org:443")
		}
	}
	dial(3)
	if starts != 1 || stops != 1 {
		t.Fatalf("first restart: starts=%d stops=%d", starts, stops)
	}
	dial(6)
	now = now.Add(5*time.Minute - time.Nanosecond)
	dial(1)
	if starts != 2 || stops != 1 {
		t.Fatalf("rate limit: starts=%d stops=%d", starts, stops)
	}
	now = now.Add(time.Nanosecond)
	dial(1)
	if stops != 2 {
		t.Fatalf("second restart: stops=%d", stops)
	}
	fail = false
	conn, err := c.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if starts != 3 || !c.Established() || c.failures != 0 {
		t.Fatalf("recovery: starts=%d ready=%v failures=%d", starts, c.Established(), c.failures)
	}
	c.Close()
	if starts != stops {
		t.Fatalf("helpers started=%d stopped=%d", starts, stops)
	}
}

func TestIrohOldGenerationCannotRestartHelper(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	starts, stops := 0, 0
	c := &irohClient{dir: t.TempDir()}
	c.start = func(context.Context) (dialFunc, func(), error) {
		starts++
		generation := starts
		return func(_ context.Context, _, address string) (net.Conn, error) {
			if generation == 1 && address == "old.example.org:443" {
				close(entered)
				<-release
				return nil, errIrohExited
			}
			if generation == 1 {
				return nil, context.DeadlineExceeded
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		}, func() { stops++ }, nil
	}
	defer c.Close()
	go func() { defer close(done); c.DialContext(context.Background(), "tcp", "old.example.org:443") }()
	<-entered
	for range 3 {
		c.DialContext(context.Background(), "tcp", "example.org:443")
	}
	conn, err := c.DialContext(context.Background(), "tcp", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	close(release)
	<-done
	if starts != 2 || stops != 1 || !c.Established() {
		t.Fatalf("old generation changed helper: starts=%d stops=%d ready=%v", starts, stops, c.Established())
	}
	c.Close()
	if stops != starts {
		t.Fatalf("helpers started=%d stopped=%d", starts, stops)
	}
}
