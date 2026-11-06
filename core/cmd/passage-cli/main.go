// Command passage-cli runs the Passage proxy standalone (Linux, macOS, or
// Termux on a phone): the same router and link format as the app.
//
//	passage-cli -link-file link.txt        # link kept out of argv/history
//	passage-cli -link 'passage://tunnel?v=1&zone=…&key=…' -listen 127.0.0.1:18000
//
// Point a SOCKS5 client at 127.0.0.1:18000 (no auth unless -user is set).
// Like the app, it puts this machine's own DNS servers (from resolv.conf)
// first among the tunnel's resolver paths; -system-dns=false turns that off.
// iroh routes run the passage-iroh helper from next to this binary, or -iroh.
// -my adds your own SOCKS5 proxy as the "my" route, tried before the link's.
// -test runs the network test once, prints it as JSON and exits.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mrgluek/passage/core/internal/sysdns"
	"github.com/mrgluek/passage/core/tunnel"
)

// version is set by scripts/build-cli.sh.
var version = "dev"

func main() {
	link := flag.String("link", "", "Passage link (none: no tunnel)")
	canaries := flag.String("canaries", "", "comma-separated host:port canaries, replacing the link's or the defaults")
	linkFile := flag.String("link-file", "", "read the Passage link from this file")
	systemDNS := flag.Bool("system-dns", true, "add this machine's DNS servers (resolv.conf) as first resolver paths")
	operator := flag.String("operator", "", "SIM operator MCC+MNC (e.g. 25001) to add its known resolvers")
	mode := flag.String("mode", tunnel.ModeAuto, "auto, always, off, route:<id>, or a route type (my, http, wss, turn, iroh, soar)")
	my := flag.String("my", "", "your own SOCKS5 proxy as a route: [user:pass@]host:port, a socks5:// URL or a t.me/socks link")
	listen := flag.String("listen", "127.0.0.1:18000", "SOCKS5 listen address")
	user := flag.String("user", "", "SOCKS5 username (empty = no auth)")
	pass := flag.String("pass", "", "SOCKS5 password")
	every := flag.Duration("status", time.Minute, "print a status line this often (0 = never)")
	paths := flag.Bool("paths", false, "also print one line per resolver path with each status")
	iroh := flag.String("iroh", "", "iroh helper binary (default: passage-iroh next to passage-cli)")
	remove := flag.String("subscribe-remove", "", "remove a subscription by ID and exit")
	refresh := flag.Bool("subscribe-refresh", false, "refresh subscriptions now")
	test := flag.Bool("test", false, "run the network test once, print it as JSON and exit")
	flag.Parse()

	if *linkFile != "" {
		b, err := os.ReadFile(*linkFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "passage-cli:", err)
			os.Exit(2)
		}
		*link = strings.TrimSpace(string(b))
	}
	var cfg tunnel.Config // no link: direct and smart routes only
	var err error
	if *link != "" {
		result, importErr := tunnel.ImportLink(cfg, *link, false)
		if importErr == nil && result.Mode == "subscribe" {
			result, importErr = tunnel.SubscribeLink(cfg, *link, false)
		}
		err = importErr
		cfg = result.Config
		if err != nil {
			fmt.Fprintln(os.Stderr, "passage-cli:", err)
			os.Exit(2)
		}
	}
	if *canaries != "" {
		cfg.Canaries = strings.Split(*canaries, ",")
	}
	cfg.Mode, cfg.Listen, cfg.User, cfg.Pass = *mode, *listen, *user, *pass
	cfg.Operator = *operator
	cfg.My = *my
	cfg.IrohBinary = irohHelper(*iroh)
	cfg.PrivateDir = privateDir()
	// What the router learns per network, kept across runs.
	if os.MkdirAll(cfg.PrivateDir, 0o700) == nil {
		cfg.StateFile = filepath.Join(cfg.PrivateDir, "router-state.json")
	}
	statePath := filepath.Join(cfg.PrivateDir, "subscriptions.json")
	if restored, e := tunnel.RestoreSubscriptionState(cfg, statePath); e == nil {
		cfg = restored
	} else {
		fmt.Fprintln(os.Stderr, "passage-cli: subscription state not loaded")
	}
	if *remove != "" {
		cfg, err = tunnel.RemoveSubscription(cfg, *remove)
		if err == nil {
			err = tunnel.SaveSubscriptionState(statePath, cfg)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "passage-cli:", err)
			os.Exit(2)
		}
		return
	}
	for _, r := range cfg.Routes {
		if r.Type == "iroh" && cfg.IrohBinary == "" {
			fmt.Fprintln(os.Stderr, "passage-cli: no passage-iroh helper found, iroh routes will fail")
			break
		}
	}
	if *systemDNS {
		cfg.SystemResolvers = sysdns.Resolvers()
	}
	if cfg, err = cfg.Normalize(); err != nil {
		fmt.Fprintln(os.Stderr, "passage-cli:", err)
		os.Exit(2)
	}
	t, err := tunnel.Start(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "passage-cli:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Passage %s: SOCKS5 on %s, mode %s, %d resolvers (%d system, %d from the link)\n",
		version, t.Addr(), cfg.Mode, len(cfg.Effective()), len(cfg.SystemResolvers), len(cfg.Resolvers))

	if *test {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out := json.NewEncoder(os.Stdout)
		out.SetIndent("", "  ")
		err := out.Encode(t.Diagnose(ctx))
		t.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "passage-cli:", err)
			os.Exit(1)
		}
		return
	}

	if *refresh {
		t.RefreshSubscriptions(context.Background())
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	var tick <-chan time.Time
	if *every > 0 {
		tk := time.NewTicker(*every)
		defer tk.Stop()
		tick = tk.C
	}
	printed := time.Time{}
	for {
		select {
		case <-sig:
			t.Close()
			return
		case update := <-t.SubscriptionUpdates():
			if err := tunnel.SaveSubscriptionState(statePath, update.Config); err != nil {
				fmt.Fprintln(os.Stderr, "passage-cli: subscription state not saved")
			}
			if update.Changed {
				cfg = update.Config
				cfg.Listen = t.Addr()
				t.Close()
				t, err = tunnel.Start(cfg)
				if err != nil {
					fmt.Fprintln(os.Stderr, "passage-cli: subscription restart failed")
					return
				}
			}
		case <-tick:
			s := t.Status(50)
			for _, e := range s.Events {
				if e.Time.After(printed) {
					fmt.Fprintf(os.Stderr, "%s %s\n", e.Time.Local().Format("15:04:05"), e.Msg)
					printed = e.Time
				}
			}
			groups := map[string]any{}
			for name, g := range s.Groups {
				groups[name] = map[string]any{"route": g.Route, "state": g.State, "targets": g.Targets}
			}
			line, _ := json.Marshal(map[string]any{"route": s.Route, "state": s.State, "groups": groups, "active": s.Active, "stats": s.Stats})
			fmt.Fprintf(os.Stderr, "%s status %s session=%v\n", time.Now().Format("15:04:05"), line, s.Session)
			if *paths {
				for _, p := range s.Paths {
					fmt.Fprintf(os.Stderr, "  %-28s %-4s verified=%-5v susp=%-5v rtt=%-6v loss=%.2f win=%.1f name=%d ans=%d ok=%d\n",
						p.Resolver, p.Transport, p.Verified, p.Suspended, p.RTT.Round(time.Millisecond), p.Loss, p.Window, p.NameLen, p.AnswerLimit, p.Successes)
				}
			}
		}
	}
}

// irohHelper returns the helper's absolute path, or "" if there is none.
func irohHelper(path string) string {
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return ""
		}
		path = filepath.Join(filepath.Dir(exe), "passage-iroh"+exeSuffix)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return ""
	}
	return path
}

// privateDir is where iroh keeps its socket and session key. The user cache
// directory keeps the socket path short; macOS limits it to 104 bytes.
func privateDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "passage")
}
