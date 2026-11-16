package tunnel

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// quietFixture is a server that greets, so the connection has had a reply,
// and then stays silent.
func quietFixture(t *testing.T) (net.Conn, func()) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer b.Close()
		b.Write([]byte("hello"))
		io.Copy(io.Discard, b)
	}()
	return a, func() { a.Close(); b.Close(); <-done }
}

func TestLateQuietKeepsTheConnectionOpen(t *testing.T) {
	shortStalls(t)
	a, closeAll := quietFixture(t)
	defer closeAll()
	var asked, stalled atomic.Int32
	c := watchResponsesKeep(a, nil, func(bool) { stalled.Add(1) }, func() bool { asked.Add(1); return true })
	if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := c.Write([]byte("ack")); err != nil {
			t.Fatalf("write %d on a kept connection: %v", i, err)
		}
		time.Sleep(3 * responseStallTimeout)
		if int(asked.Load()) != i {
			t.Fatalf("after write %d keep was asked %d times", i, asked.Load())
		}
	}
	if stalled.Load() != 0 {
		t.Fatal("a kept connection was reported as stalled")
	}
	if _, err := c.Write([]byte("still open")); err != nil {
		t.Fatal("connection was closed:", err)
	}
}

func TestLateQuietClosesWhenKeepSaysNo(t *testing.T) {
	shortStalls(t)
	a, closeAll := quietFixture(t)
	defer closeAll()
	stalled := make(chan bool, 1)
	c := watchResponsesKeep(a, nil, func(first bool) { stalled <- first }, func() bool { return false })
	if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("x"))
	select {
	case first := <-stalled:
		if first {
			t.Fatal("a later stall was reported as the first")
		}
	case <-time.After(10 * responseStallTimeout):
		t.Fatal("stall was not reported")
	}
}

func TestFirstReplyStallStillClosesWhenLateOnesAreKept(t *testing.T) {
	shortStalls(t)
	a, b := net.Pipe()
	defer b.Close()
	go io.Copy(io.Discard, b)
	stalled := make(chan bool, 1)
	var asked atomic.Int32
	c := watchResponsesKeep(a, nil, func(first bool) { stalled <- first }, func() bool { asked.Add(1); return true })
	c.Write([]byte("x"))
	select {
	case first := <-stalled:
		if !first {
			t.Fatal("expected a first-reply stall")
		}
	case <-time.After(10 * firstResponseTimeout):
		t.Fatal("first-reply stall was not reported")
	}
	if asked.Load() != 0 {
		t.Fatal("keep was asked about a first-reply stall")
	}
}

func TestEarlyFreezeIsYoungAndSmall(t *testing.T) {
	for _, c := range []struct {
		age      time.Duration
		received int64
		want     bool
	}{
		{20 * time.Second, 247, true},
		{50 * time.Second, 13589, true},
		{21 * time.Second, 32 << 10, true},
		{21 * time.Second, 33029, false},
		{21 * time.Second, 65797, false},
		{time.Minute, 100, true},
		{time.Minute + time.Second, 100, false},
		{110 * time.Second, 266, false},
	} {
		if got := earlyFreeze(c.age, c.received); got != c.want {
			t.Errorf("age %v, received %d: early freeze = %v, want %v", c.age, c.received, got, c.want)
		}
	}
}

func TestWatcherReportsTheLastReplyAndWrite(t *testing.T) {
	a, closeAll := quietFixture(t)
	defer closeAll()
	c := watchResponses(a, nil, func(bool) {}).(*responseWatch)
	if read, write := c.quiet(); read != 0 || write != 0 {
		t.Fatalf("fresh connection: read %v write %v, want zeros", read, write)
	}
	if _, err := io.ReadFull(c, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	c.Write([]byte("x"))
	read, write := c.quiet()
	// The clock can be coarse (Windows), so the write may read as zero.
	if read < 30*time.Millisecond || write < 0 || write >= read {
		t.Fatalf("read %v write %v: the write must be newer than the reply", read, write)
	}
}
