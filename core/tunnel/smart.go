package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.getoutline.org/sdk/transport"
	"golang.getoutline.org/sdk/transport/split"
	"golang.getoutline.org/sdk/transport/tlsfrag"
)

// knownStrategies remembers, per network, the strategy that worked last and
// the groups that had to use the tunnel, in memory and, when
// Config.StateFile is set, in that file, so it survives the app being
// stopped. Networks and groups are kept as hashes; at most maxKnownNetworks.
var knownStrategies = struct {
	sync.Mutex
	m       map[string]knownStrategy
	blocked map[string]map[string]time.Time // network -> group -> when
	clean   map[string]map[string]time.Time
	holds   map[string]map[string]savedHold
	last    lastNetwork
	loaded  map[string]bool // state files already read
	writers map[string]*knownStateWriter
}{m: map[string]knownStrategy{}, blocked: map[string]map[string]time.Time{}, clean: map[string]map[string]time.Time{}, loaded: map[string]bool{}, writers: map[string]*knownStateWriter{}}

// blockedMemory is how long a group stays known as filtered on a network
// without being seen filtered again.
const blockedMemory = 14 * 24 * time.Hour

type knownStrategy struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

const maxKnownNetworks = 32

// networkKey identifies the network by what the app passes in at start.
func networkKey(c Config) string {
	resolvers := slices.Clone(c.SystemResolvers)
	slices.Sort(resolvers)
	resolvers = slices.Compact(resolvers)
	sum := sha256.Sum256([]byte(strings.Join(resolvers, ",") + "|" + c.Operator))
	return hex.EncodeToString(sum[:12])
}

// knownStrategyName returns the strategy remembered for c's network.
func knownStrategyName(c Config) string {
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	return knownStrategies.m[networkKey(c)].Name
}

// rememberStrategy records name for c's network ("" forgets it) and saves.
func rememberStrategy(c Config, name string) {
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	k := networkKey(c)
	if old, ok := knownStrategies.m[k]; (!ok && name == "") || (ok && old.Name == name && time.Since(old.At) < time.Hour) {
		return // unchanged and recent: no write
	}
	if name == "" {
		delete(knownStrategies.m, k)
	} else {
		knownStrategies.m[k] = knownStrategy{name, time.Now().UTC()}
	}
	for len(knownStrategies.m) > maxKnownNetworks {
		oldest := ""
		for key, v := range knownStrategies.m {
			if oldest == "" || v.At.Before(knownStrategies.m[oldest].At) {
				oldest = key
			}
		}
		delete(knownStrategies.m, oldest)
	}
	dirtyKnownLocked(c.StateFile)
}

func groupKey(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:8])
}

type lastNetwork struct {
	Network string          `json:"network"`
	Groups  map[string]bool `json:"groups"`
	At      time.Time       `json:"at"`
}

const previousNetworkMemory = 24 * time.Hour

// knownBlocked reports whether group had to use the tunnel on c's network.
func knownBlocked(c Config, group string) bool {
	blocked, _ := knownNetworkGroup(c, group, time.Now())
	return blocked
}

func knownNetworkGroup(c Config, group string, now time.Time) (blocked, clean bool) {
	if c.StateFile == "" {
		return false, false
	}
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	k, g := networkKey(c), groupKey(group)
	b, bok := knownStrategies.blocked[k][g]
	d, dok := knownStrategies.clean[k][g]
	return bok && now.Sub(b) < blockedMemory && (!dok || !d.After(b)),
		dok && now.Sub(d) < blockedMemory && (!bok || d.After(b))
}

func previousNetwork(c Config) lastNetwork {
	if c.StateFile == "" {
		return lastNetwork{}
	}
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	last := knownStrategies.last
	last.Groups = make(map[string]bool, len(knownStrategies.last.Groups))
	for g, tunnel := range knownStrategies.last.Groups {
		last.Groups[g] = tunnel
	}
	return last
}

func rememberTunnelLocked(c Config, group string, tunnel bool, now time.Time) bool {
	last := &knownStrategies.last
	changed := last.Network != networkKey(c) || last.Groups[groupKey(group)] != tunnel || now.Sub(last.At) >= time.Hour
	if !changed {
		return false
	}
	if last.Network != networkKey(c) {
		*last = lastNetwork{Network: networkKey(c), Groups: map[string]bool{}}
	}
	if last.Groups == nil {
		last.Groups = map[string]bool{}
	}
	if tunnel {
		last.Groups[groupKey(group)] = true
	} else {
		delete(last.Groups, groupKey(group))
	}
	last.At = now.UTC()
	return changed
}

// rememberTunnel records a route without assigning a network verdict.
func rememberTunnel(c Config, group string, tunnel bool, now time.Time) {
	if c.StateFile == "" {
		return
	}
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	if rememberTunnelLocked(c, group, tunnel, now) {
		dirtyKnownLocked(c.StateFile)
	}
}

func rememberBlocked(c Config, group string, blocked bool) {
	rememberBlockedAt(c, group, blocked, time.Now())
}

// rememberBlockedAt records the verdict and current route together.
func rememberBlockedAt(c Config, group string, blocked bool, now time.Time) {
	if c.StateFile == "" {
		return
	}
	knownStrategies.Lock()
	defer knownStrategies.Unlock()
	loadKnownLocked(c.StateFile)
	k, g := networkKey(c), groupKey(group)
	memory, opposite := knownStrategies.blocked, knownStrategies.clean
	if !blocked {
		memory, opposite = opposite, memory
	}
	at, ok := memory[k][g]
	if ok && now.Sub(at) < time.Hour && knownStrategies.last.Network == k && knownStrategies.last.Groups[g] == blocked {
		return
	}
	rememberTunnelLocked(c, group, blocked, now)
	delete(opposite[k], g)
	if len(opposite[k]) == 0 {
		delete(opposite, k)
	}
	if memory[k] == nil {
		memory[k] = map[string]time.Time{}
	}
	memory[k][g] = now.UTC()
	pruneNetworkMemoryLocked(now)
	dirtyKnownLocked(c.StateFile)
}

// Both verdicts share the network limit.
func pruneNetworkMemoryLocked(now time.Time) {
	latest := map[string]time.Time{}
	for _, memory := range []map[string]map[string]time.Time{knownStrategies.blocked, knownStrategies.clean} {
		for k, groups := range memory {
			for g, at := range groups {
				if now.Sub(at) >= blockedMemory {
					delete(groups, g)
				} else if at.After(latest[k]) {
					latest[k] = at
				}
			}
			if len(groups) == 0 {
				delete(memory, k)
			}
		}
	}
	for len(latest) > maxKnownNetworks {
		oldest := ""
		for k, at := range latest {
			if oldest == "" || at.Before(latest[oldest]) {
				oldest = k
			}
		}
		delete(latest, oldest)
		delete(knownStrategies.blocked, oldest)
		delete(knownStrategies.clean, oldest)
	}
}

func loadKnownLocked(path string) {
	if path == "" || knownStrategies.loaded[path] {
		return
	}
	knownStrategies.loaded[path] = true
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f struct {
		Strategies map[string]knownStrategy        `json:"strategies"`
		Blocked    map[string]map[string]time.Time `json:"blocked"`
		Clean      map[string]map[string]time.Time `json:"clean"`
		Holds      json.RawMessage                 `json:"holds"`
		Last       lastNetwork                     `json:"last"`
	}
	if json.Unmarshal(b, &f) != nil {
		return
	}
	loadHoldsLocked(f.Holds)
	for k, v := range f.Strategies {
		if cur, ok := knownStrategies.m[k]; !ok || v.At.After(cur.At) {
			knownStrategies.m[k] = v
		}
	}
	for _, pair := range []struct {
		dst, src map[string]map[string]time.Time
	}{
		{knownStrategies.blocked, f.Blocked}, {knownStrategies.clean, f.Clean},
	} {
		for k, groups := range pair.src {
			if pair.dst[k] == nil {
				pair.dst[k] = map[string]time.Time{}
			}
			for g, at := range groups {
				if at.After(pair.dst[k][g]) {
					pair.dst[k][g] = at
				}
			}
		}
	}
	if f.Last.At.After(knownStrategies.last.At) {
		knownStrategies.last = f.Last
	}
}

type knownStateWriter struct {
	dirty bool
	done  chan struct{}
}

// dirtyKnownLocked keeps at most one writer running for each state file.
func dirtyKnownLocked(path string) {
	if path == "" {
		return
	}
	if w := knownStrategies.writers[path]; w != nil {
		w.dirty = true
		return
	}
	w := &knownStateWriter{dirty: true, done: make(chan struct{})}
	knownStrategies.writers[path] = w
	go writeKnownState(path, w)
}

func writeKnownState(path string, w *knownStateWriter) {
	for {
		knownStrategies.Lock()
		if !w.dirty {
			delete(knownStrategies.writers, path)
			close(w.done)
			knownStrategies.Unlock()
			return
		}
		w.dirty = false
		b, err := json.Marshal(map[string]any{"strategies": knownStrategies.m, "blocked": knownStrategies.blocked, "clean": knownStrategies.clean, "last": knownStrategies.last, "holds": knownStrategies.holds})
		knownStrategies.Unlock()
		if err == nil {
			tmp := path + ".tmp"
			if os.WriteFile(tmp, b, 0o600) == nil {
				os.Rename(tmp, path)
			}
		}
	}
}

// flushKnownState waits for pending writes without holding the router lock.
func flushKnownState(path string) {
	knownStrategies.Lock()
	w := knownStrategies.writers[path]
	knownStrategies.Unlock()
	if w != nil {
		<-w.done
	}
}

// useKnownStrategy sets up the smart route with the strategy that worked on
// this network before, if any. Called at start, before the monitor runs.
func (t *Tunnel) useKnownStrategy() {
	name := knownStrategyName(t.cfg)
	for _, s := range t.strategies {
		if s.name == name {
			t.strategy = &s
			t.ways[RouteSmart] = way{s.dial, directDialTimeout}
			t.log.add("smart route: %s worked on this network before", name)
			return
		}
	}
}

// strategy is one way to send a connection's first bytes on the smart route.
type strategy struct {
	name string
	dial dialFunc
}

// appStrategies are the strategies the apps' search tries: those that cut
// the server name are for sites only. They leave other data as it is, and
// each one would add a probe timeout to every search that finds nothing.
func appStrategies(all []strategy) []strategy {
	var out []strategy
	for _, s := range all {
		if !strings.HasSuffix(s.name, ":sni") {
			out = append(out, s)
		}
	}
	return out
}

// oobUsable: macOS sends the urgent byte so that servers reject the hello,
// so the urgent-byte strategy is not offered there.
func oobUsable(goos string) bool { return goos != "darwin" }

// smartStrategies lists the strategies in the order sites try them.
func smartStrategies() []strategy { return smartStrategiesFor(runtime.GOOS) }

func smartStrategiesFor(goos string) []strategy {
	base := &transport.TCPDialer{Dialer: net.Dialer{Timeout: directDialTimeout}}
	out := []strategy{}
	if d, err := tlsfrag.NewFixedLenStreamDialer(base, 1); err == nil {
		out = append(out, strategy{"tlsfrag:1", streamDial(d)})
	}
	out = append(out, strategy{"split:sni", partDial(splitSNIPlan)})
	if oobUsable(goos) {
		out = append(out, strategy{"oob:sni", partDial(oobSNIPlan)})
	}
	out = append(out, strategy{"disorder:sni", partDial(disorderSNIPlan)})
	if d, err := split.NewStreamDialer(base, split.NewFixedSplitIterator(2)); err == nil {
		out = append(out, strategy{"split:2", streamDial(d)})
	}
	out = append(out, strategy{"disorder:1", disorderDial(1)})
	return out
}

func streamDial(d transport.StreamDialer) dialFunc {
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialStream(ctx, addr)
	}
}

// disorderDial sends the first n bytes with TTL 1, then the rest.
func disorderDial(n int) dialFunc {
	return partDial(func(rec []byte) []part {
		if len(rec) <= n {
			return nil
		}
		return []part{{b: rec[:n], lowTTL: true}, {b: rec[n:]}}
	})
}

// partDial dials TCP and sends the first record by plan.
func partDial(plan func([]byte) []part) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: directDialTimeout}
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tc, ok := c.(*net.TCPConn)
		if !ok {
			return c, nil
		}
		return newPartConn(tc, plan), nil
	}
}

// findStrategy returns the first of list that completes a TLS handshake
// with every host, or nil. Each try gets probeTimeout.
func (t *Tunnel) findStrategy(list []strategy, hosts []string) *strategy {
	for _, s := range list {
		ok := true
		for _, hp := range hosts {
			host, _, _ := net.SplitHostPort(hp)
			ctx, cancel := context.WithTimeout(t.ctx, probeTimeout)
			probe := t.smartProbe
			if probe == nil {
				probe = probeTLSVia
			}
			err := probe(ctx, s.dial, hp, host)
			cancel()
			if !probeReachableFor(err, host) {
				ok = false
				break
			}
		}
		if ok {
			return &s
		}
		if t.ctx.Err() != nil {
			return nil
		}
	}
	return nil
}

// smartCheck runs while the probe hosts fail directly: it keeps the current
// strategy if it still works, otherwise looks for one. It reports whether
// the smart route works for these hosts and sets it up if so. A failure
// leaves the strategy alone: the caller tears it down with smartDown once no
// group in the pass has a working one.
func (t *Tunnel) smartCheck(hosts []string) bool {
	t.mu.Lock()
	cur := t.strategy
	t.mu.Unlock()
	if cur != nil && t.findStrategy([]strategy{*cur}, hosts) != nil {
		rememberStrategy(t.cfg, cur.name)
		return true
	}
	start := time.Now()
	found := t.findStrategy(appStrategies(t.strategies), hosts)
	t.mu.Lock()
	defer t.mu.Unlock()
	if found == nil {
		return false
	}
	if t.strategy == nil || t.strategy.name != found.name {
		t.log.add("smart route: %s works (search took %v)", found.name, time.Since(start).Round(100*time.Millisecond))
	}
	t.strategy = found
	t.ways[RouteSmart] = way{found.dial, directDialTimeout}
	rememberStrategy(t.cfg, found.name)
	return true
}

// smartDown forgets the strategy when no failed group could use it.
func (t *Tunnel) smartDown() {
	t.mu.Lock()
	defer t.mu.Unlock()
	rememberStrategy(t.cfg, "")
	if t.strategy != nil {
		t.log.add("smart route: %s stopped working, none found", t.strategy.name)
	}
	t.strategy = nil
	t.ways[RouteSmart] = way{}
}
