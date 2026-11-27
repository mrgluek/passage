// Command passage-http-exit serves encrypted streams behind an HTTPS reverse proxy.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mrgluek/passage/core/internal/exitpolicy"
	"github.com/mrgluek/passage/core/tunnel"
)

type config struct {
	Key     string            `json:"key"`
	Path    string            `json:"path"`
	Allowed []exitpolicy.Rule `json:"allowed"`
}

func run() error {
	configFile := flag.String("config", "", "private JSON configuration file")
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address behind the reverse proxy")
	flag.Parse()
	data, err := os.ReadFile(*configFile)
	if err != nil {
		return errors.New("cannot read configuration")
	}
	var cfg config
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return errors.New("invalid configuration")
	}
	key, err := base64.StdEncoding.DecodeString(cfg.Key)
	if err != nil {
		return errors.New("invalid key")
	}
	dial, err := exitpolicy.New(cfg.Allowed)
	if err != nil {
		return err
	}
	exit, err := tunnel.NewHTTPExit(key, cfg.Path, dial)
	if err != nil {
		return err
	}
	defer exit.Close()
	server := &http.Server{Addr: *listen, Handler: exit, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); exit.Close(); server.Close() }()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "passage-http-exit:", err)
		os.Exit(1)
	}
}
