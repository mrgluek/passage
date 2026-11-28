package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Before a site's first reply, a connection can move to another way: a
// connection may be made and still get no reply.
var siteSwitchWait = 3 * time.Second // without a reply, try the next way

const siteReplayMax = 16 << 10 // client bytes kept for a replay

// siteStallWait: after the first reply, a site that sends nothing this long
// while a request of the browser waits has stalled on this way.
var siteStallWait = 10 * time.Second

// siteLastWayWait bounds the wait for a first reply on the last way.
var siteLastWayWait = 60 * time.Second

// A stall marks a way only early in a connection: later quiet periods are
// WebSockets and long polls doing their job.
var siteStallEarlyAge = 60 * time.Second

const siteStallEarlyBytes = 64 << 10

// siteWay is one way to a site: a route and, for smart, its strategy.
type siteWay struct {
	route    Route
	strategy string
}

func (w siteWay) String() string {
	if w.route == RouteSmart && w.strategy != "" {
		return "smart:" + w.strategy
	}
	return w.route.String()
}

// siteNextWays are the ways to try after first, in order: the smart
// strategies not failed lately (failed ones last), then the tunnel.
func (t *Tunnel) siteNextWays(entry *siteEntry, first siteWay) []siteWay {
	if first.route == RouteTunnel {
		return nil
	}
	t.mu.Lock()
	strategies := t.siteStrategiesLocked()
	tunnelOK := t.ways[RouteTunnel].dial != nil && entry.tunnel && t.mode != ModeOff
	smartOK := !(entry.tunnel && (t.mode == ModeAlways || tunnelOK && entry.tunnelOnlyNow(t.timeNow())))
	t.mu.Unlock()
	now := t.timeNow()
	var good, late []siteWay
	if smartOK {
		for _, s := range strategies {
			if first.route == RouteSmart && s.name == first.strategy {
				continue
			}
			w := siteWay{RouteSmart, s.name}
			if entry.strategyBadUntil(s.name).After(now) {
				late = append(late, w)
			} else {
				good = append(good, w)
			}
		}
	}
	if tunnelOK && entry.tunnel {
		// A tunnel site goes to the tunnel next, as at dial time: trying the
		// strategies first would keep it waiting several times over.
		return []siteWay{{route: RouteTunnel}}
	}
	return append(good, late...)
}

// dialWay dials one way of a chain.
func (t *Tunnel) dialWay(entry *siteEntry, w siteWay, addr string) (net.Conn, error) {
	t.mu.Lock()
	ways := t.ways
	strategies := t.siteStrategiesLocked()
	t.mu.Unlock()
	if w.route == RouteSmart {
		ways[RouteSmart] = way{}
		for _, s := range strategies {
			if s.name == w.strategy {
				ways[RouteSmart] = way{s.dial, directDialTimeout}
			}
		}
	}
	if ways[w.route].dial == nil {
		return nil, errNoWay
	}
	ctx, cancel := context.WithTimeout(t.ctx, ways[w.route].timeout)
	defer cancel()
	return t.siteDial(entry, w.route, ways, "")(ctx, "tcp", addr)
}

var errNoWay = &net.OpError{Op: "dial", Err: io.ErrUnexpectedEOF}

func (t *Tunnel) markWayBad(entry *siteEntry, w siteWay) {
	until := t.timeNow().Add(siteBadFor)
	if w.route == RouteSmart && w.strategy != "" {
		entry.markStrategyBad(w.strategy, until)
		return
	}
	entry.markBad(w.route, until)
}

// siteRelay carries one browser connection whose remote side may change
// until the first byte from the site.
type siteRelay struct {
	t       *Tunnel
	browser net.Conn

	mu        sync.Mutex
	cur       net.Conn
	way       siteWay
	gen       int
	buf       []byte // client bytes since the start, until the first reply
	overflow  bool   // more than siteReplayMax: no replay possible
	answered  bool
	switching bool
	wrote     chan struct{} // closed at the first client byte
	wroteOnce sync.Once
	events    chan relayEvent
	finished  chan struct{} // the answering remote's copy is done

	// After the first reply, for the stall check.
	started          time.Time
	lastUp, lastDown time.Time
	downTotal        int64
	requestWaiting   bool // a write of a request size since the site's last byte
}

// siteRequestMin: a smaller client write is taken for protocol upkeep (an
// acknowledgement or window update fits in one small record), not a request
// that waits for an answer; several of them do not add up to one.
const siteRequestMin = 64

type relayEvent struct {
	gen      int
	answered bool // else: the remote closed or failed before a reply
}

func (r *siteRelay) setRemote(c net.Conn, w siteWay) int {
	r.t.mu.Lock()
	if r.t.ctx.Err() != nil {
		c.Close() // the router is closing: Close has already dropped the others
	}
	if r.t.siteRemotes == nil {
		r.t.siteRemotes = map[net.Conn]struct{}{}
	}
	r.t.siteRemotes[c] = struct{}{}
	r.t.mu.Unlock()
	r.mu.Lock()
	r.cur, r.way = c, w
	r.gen++
	gen := r.gen
	r.mu.Unlock()
	go r.down(c, gen, w)
	return gen
}

func (r *siteRelay) dropRemote(c net.Conn) {
	c.Close()
	r.t.mu.Lock()
	delete(r.t.siteRemotes, c)
	r.t.mu.Unlock()
}

// up copies the browser to the current remote, keeping the bytes for a
// replay until the first reply.
func (r *siteRelay) up() {
	b := make([]byte, 32<<10)
	for {
		n, err := r.browser.Read(b)
		if n > 0 {
			r.wroteOnce.Do(func() { close(r.wrote) })
			r.mu.Lock()
			if !r.answered || r.switching { // a replay in progress still needs them
				if len(r.buf)+n <= siteReplayMax {
					r.buf = append(r.buf, b[:n]...)
				} else {
					r.overflow = true
				}
			}
			cur, way, hold := r.cur, r.way, r.switching
			if r.answered && n >= siteRequestMin {
				r.lastUp = time.Now()
				r.requestWaiting = true
			}
			r.mu.Unlock()
			if !hold {
				if m, _ := cur.Write(b[:n]); m > 0 {
					r.t.upBytes[way.route].Add(int64(m))
				}
			}
		}
		if err != nil {
			r.mu.Lock()
			cur := r.cur
			r.mu.Unlock()
			if cw, ok := cur.(interface{ CloseWrite() error }); ok {
				cw.CloseWrite()
			} else {
				cur.Close()
			}
			return
		}
	}
}

// down copies one remote to the browser once it is the current one and
// has answered; a remote replaced before its first byte writes nothing.
func (r *siteRelay) down(c net.Conn, gen int, w siteWay) {
	b := make([]byte, 32<<10)
	n, err := c.Read(b)
	r.mu.Lock()
	if gen != r.gen {
		r.mu.Unlock()
		return
	}
	if n == 0 {
		r.mu.Unlock()
		r.events <- relayEvent{gen: gen}
		return
	}
	r.answered = true
	if !r.switching {
		r.buf = nil // else replay drops it when done
	}
	r.lastDown, r.downTotal = time.Now(), int64(n)
	r.mu.Unlock()
	r.events <- relayEvent{gen: gen, answered: true}
	r.browser.Write(b[:n])
	r.t.downBytes[w.route].Add(int64(n))
	for err == nil {
		n, err = c.Read(b)
		if n > 0 {
			r.mu.Lock()
			r.lastDown = time.Now()
			r.downTotal += int64(n)
			r.requestWaiting = false
			r.mu.Unlock()
			if _, werr := r.browser.Write(b[:n]); werr != nil {
				break
			}
			r.t.downBytes[w.route].Add(int64(n))
		}
	}
	if cw, ok := r.browser.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		r.browser.Close()
	}
	close(r.finished)
}

func (r *siteRelay) current() (net.Conn, siteWay, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur, r.way, r.gen
}

// replay writes the kept client bytes to the new remote, also those that
// arrive meanwhile, then lets up() write to it directly.
func (r *siteRelay) replay(c net.Conn, w siteWay, upEnded bool) bool {
	written := 0
	for {
		r.mu.Lock()
		if r.overflow {
			r.mu.Unlock()
			return false // bytes were lost while switching: no faithful replay
		}
		if written >= len(r.buf) {
			r.switching = false
			if r.answered {
				r.buf = nil
			}
			r.mu.Unlock()
			break
		}
		d := r.buf[written:]
		written = len(r.buf)
		r.mu.Unlock()
		if m, _ := c.Write(d); m > 0 {
			r.t.upBytes[w.route].Add(int64(m))
		}
	}
	if upEnded {
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}
	return true
}

// connectSite carries one browser connection. It keeps its own counters and
// per-site memory; it never reaches the groups, probes or stall statistics.
// Until the site's first byte it may move to the next way (see siteSwitchWait).
func (t *Tunnel) connectSite(c bufferedConn, entry *siteEntry, host, addr string) {
	started := t.timeNow()
	remote, route, strat, tried, err := t.dialSiteWay(entry, addr)
	if err != nil {
		t.siteFailed(entry, host, siteFailStage(err), tried, t.timeNow().Sub(started))
		socksReply(c, repHostUnreachable)
		t.siteSocksError(repHostUnreachable, "host unreachable")
		return
	}
	if t.ctx.Err() != nil || socksReply(c, repSucceeded) != nil {
		remote.Close()
		return
	}
	t.siteSucceeded()
	t.siteTotal.Add(1)
	t.siteOpen.Add(1)
	defer t.siteOpen.Add(-1)

	r := &siteRelay{t: t, browser: c, started: time.Now(), wrote: make(chan struct{}), events: make(chan relayEvent, 16), finished: make(chan struct{})}
	first := siteWay{route, strat}
	r.setRemote(remote, first)
	defer func() {
		cur, _, _ := r.current()
		r.dropRemote(cur)
		c.Close()
	}()
	upDone := make(chan struct{})
	go func() { r.up(); close(upDone) }()
	names := make([]string, 0, len(tried)+4)
	for _, x := range tried {
		if x != route {
			names = append(names, x.String())
		}
	}
	names = append(names, first.String())
	next := t.siteNextWays(entry, first)

	select {
	case <-r.wrote:
	case <-upDone:
		return // the browser left before sending anything
	}
	wait := siteSwitchWait
	if entry.tunnel {
		wait = min(wait, siteTunnelDelay)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	timerC := timer.C
	var lastWait <-chan time.Time // the bound on waiting when there is nowhere to switch
	upEnded := false
	for {
		closed := false
		select {
		case ev := <-r.events:
			if _, _, gen := r.current(); ev.gen != gen {
				continue
			}
			if ev.answered {
				_, w, _ := r.current()
				entry.clear(w.route)
				exit := ""
				if w.route == RouteTunnel {
					cur, _, _ := r.current()
					exit = routeIDOf(cur)
				}
				entry.remember(w.route, exit, t.timeNow(), w.strategy)
				t.siteLastOK.Store(t.timeNow().UnixNano())
				t.wg.Add(1)
				go r.watchStall(entry, w)
				if !upEnded {
					<-upDone
				}
				<-r.finished
				return
			}
			closed = true // closed or failed before a reply: the next way at once
		case <-timerC:
		case <-upDone:
			r.mu.Lock()
			answered := r.answered
			r.mu.Unlock()
			if !answered {
				return // the browser left before any reply: nobody to answer, nothing to mark
			}
			upDone, upEnded = nil, true
			continue
		case <-lastWait:
			t.siteFailed(entry, host, siteNoReply, nil, t.timeNow().Sub(started), names...)
			return
		case <-t.ctx.Done():
			return
		}
		r.mu.Lock()
		if r.answered {
			r.mu.Unlock()
			continue // its event is on the way
		}
		if r.overflow || len(next) == 0 {
			r.mu.Unlock()
			if closed {
				t.siteFailed(entry, host, siteNoReply, nil, t.timeNow().Sub(started), names...)
				return
			}
			// Nowhere to switch to, or too much sent to replay: a slow site
			// is still a site, so keep waiting on this way, for a while.
			timerC = nil
			if lastWait == nil {
				lastWait = time.After(siteLastWayWait)
			}
			continue
		}
		r.switching = true
		r.gen++ // the old remote can no longer answer into this connection
		cur, w := r.cur, r.way
		r.mu.Unlock()
		t.markWayBad(entry, w)
		r.dropRemote(cur)
		var nc net.Conn
		for len(next) > 0 && nc == nil {
			nw := next[0]
			next = next[1:]
			names = append(names, nw.String())
			conn, err := t.dialWay(entry, nw, addr)
			if err != nil {
				t.siteDialFailed(entry, nw.route, err, t.timeNow())
				if nw.route == RouteSmart && !errors.Is(err, errSiteDoT) && !errors.Is(err, errDestinationRefused) {
					entry.markStrategyBad(nw.strategy, t.timeNow().Add(siteBadFor))
				}
				continue
			}
			nc = conn
			r.setRemote(nc, nw)
			if !r.replay(nc, nw, upEnded) {
				return
			}
		}
		if nc == nil {
			t.siteFailed(entry, host, siteNoReply, nil, t.timeNow().Sub(started), names...)
			return
		}
		timer.Reset(wait)
		timerC = timer.C
	}
}

// watchStall marks a direct or smart way bad for the site once, when early
// in the connection a request of the browser has waited siteStallWait
// without a byte back. A quiet connection
// with nothing asked (a WebSocket at rest, a kept-alive socket) is never a
// stall, and the connection itself is left alone.
func (r *siteRelay) watchStall(entry *siteEntry, w siteWay) {
	defer r.t.wg.Done()
	wait := siteStallWait
	tick := time.NewTicker(max(wait/4, 10*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-r.finished:
			return
		case <-r.t.ctx.Done():
			return
		case <-tick.C:
		}
		r.mu.Lock()
		waiting := r.requestWaiting
		stalled := waiting && time.Since(r.lastUp) >= wait
		got := r.downTotal
		r.mu.Unlock()
		if stalled {
			// The tunnel is the last resort of a tunnel site: a stall there
			// is noted only, or the next connections would go back to the
			// blocked ways.
			early := got < siteStallEarlyBytes && time.Since(r.started) < siteStallEarlyAge
			now := r.t.timeNow()
			if w.route != RouteTunnel && early {
				r.t.markWayBad(entry, w)
				r.t.siteNote(entry, "stall", "%s stalled on %s after %d KB", entry.label(), w, got/1024)
				// One stall can be a slow reply; a second one soon after is not.
				if entry.tunnel && entry.noteEarlyStall(now) && entry.useTunnelOnly(now) {
					r.t.log.add("%s uses the tunnel for 24 h after a stall on %s", entry.label(), w)
				}
			} else {
				r.t.siteNote(entry, "stall", "%s stalled on %s after %d KB (not marked)", entry.label(), w, got/1024)
			}
			return
		}
	}
}
