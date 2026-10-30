// Command passage-cli runs the Passage proxy standalone (Linux, macOS, or
// Termux on a phone): the same router and link format as the app.
//
//	passage-cli -link-file link.txt        # link kept out of argv/history
//	passage-cli -link 'passage://tunnel?v=1&zone=…&key=…' -listen 127.0.0.1:18000
//
// Point a SOCKS5 client at 127.0.0.1:18000 (no auth unless -user is set).
// Like the app, it puts this machine's own DNS servers (from resolv.conf)
// first among the tunnel's resolver paths; -system-dns=false turns that off.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

func main() {
	link := flag.String("link", "", "passage://tunnel link (none: no tunnel)")
	canaries := flag.String("canaries", "", "comma-separated host:port canaries, replacing the link's or the defaults")
	linkFile := flag.String("link-file", "", "read the passage://tunnel link from this file")
	systemDNS := flag.Bool("system-dns", true, "add this machine's DNS servers (resolv.conf) as first resolver paths")
	operator := flag.String("operator", "", "SIM operator MCC+MNC (e.g. 25001) to add its known resolvers")
	mode := flag.String("mode", tunnel.ModeAuto, "auto, always or off")
	listen := flag.String("listen", "127.0.0.1:18000", "SOCKS5 listen address")
	user := flag.String("user", "", "SOCKS5 username (empty = no auth)")
	pass := flag.String("pass", "", "SOCKS5 password")
	every := flag.Duration("status", time.Minute, "print a status line this often (0 = never)")
	paths := flag.Bool("paths", false, "also print one line per resolver path with each status")
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
		if cfg, err = tunnel.ParseLink(*link); err != nil {
			fmt.Fprintln(os.Stderr, "passage-cli:", err)
			os.Exit(2)
		}
	}
	if *canaries != "" {
		cfg.Canaries = strings.Split(*canaries, ",")
	}
	cfg.Mode, cfg.Listen, cfg.User, cfg.Pass = *mode, *listen, *user, *pass
	cfg.Operator = *operator
	if *systemDNS {
		cfg.SystemResolvers = systemResolvers()
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
	fmt.Fprintf(os.Stderr, "SOCKS5 on %s, mode %s, %d resolvers (%d system, %d from the link)\n",
		t.Addr(), cfg.Mode, len(cfg.Effective()), len(cfg.SystemResolvers), len(cfg.Resolvers))

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
		case <-tick:
			s := t.Status(50)
			for _, e := range s.Events {
				if e.Time.After(printed) {
					fmt.Fprintf(os.Stderr, "%s %s\n", e.Time.Local().Format("15:04:05"), e.Msg)
					printed = e.Time
				}
			}
			line, _ := json.Marshal(map[string]any{"route": s.Route, "state": s.State, "active": s.Active, "stats": s.Stats})
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

// systemResolvers reads this machine's DNS servers. With systemd-resolved,
// /etc/resolv.conf only names the 127.0.0.53 stub, so the real upstream list
// comes from /run/systemd/resolve/resolv.conf. Loopback entries are skipped:
// a local stub can't forward to the tunnel zone any better than its upstreams.
func systemResolvers() []string {
	for _, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var out []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 || fields[0] != "nameserver" {
				continue
			}
			ip := net.ParseIP(strings.SplitN(fields[1], "%", 2)[0])
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, net.JoinHostPort(ip.String(), "53"))
		}
		f.Close()
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
