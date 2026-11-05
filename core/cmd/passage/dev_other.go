//go:build !windows && (!darwin || !cgo)

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
)

const exeSuffix = ""

// runDesktop serves the window on loopback, for development: the tray and
// the native window are Windows only.
func runDesktop(dir string, _ bool) {
	a := newApp(dir, version, irohHelper(), language(os.Getenv("LANG")), devPlatform{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go a.run(ctx)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	token := randomHex(16)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, strings.Replace(indexHTML, "<head>", `<head><script>window.passageToken="`+token+`"</script>`, 1))
	})
	mux.HandleFunc("POST /call", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token  string          `json:"token"`
			Method string          `json:"method"`
			Arg    json.RawMessage `json:"arg"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Token != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		res, err := a.call(in.Method, in.Arg)
		out := map[string]any{"result": res}
		if err != nil {
			out = map[string]any{"error": err.Error()}
		}
		json.NewEncoder(w).Encode(out)
	})
	fmt.Fprintf(os.Stderr, "Passage %s: settings in %s\nwindow: http://%s/?t=%s\n", version, dir, ln.Addr(), token)
	srv := &http.Server{Handler: mux}
	go func() { <-ctx.Done(); srv.Close() }()
	srv.Serve(ln)
}

func showError(string) {}

type devPlatform struct{}

func (devPlatform) copyText(text string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("copy is not supported here")
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func (devPlatform) open(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, target).Start()
}

func (devPlatform) canOpen(string) bool { return false }

func (devPlatform) device() map[string]any {
	return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version()}
}

func (devPlatform) autostart() bool         { return false }
func (devPlatform) setAutostart(bool) error { return errors.New("start at sign-in is Windows only") }

func (devPlatform) updateTarget() string             { return "" }
func (devPlatform) applyUpdate(string, string) error { return nil }
