// Command passage-turn-exit serves encrypted streams over UDP.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mrgluek/passage/core/internal/exitpolicy"
	"github.com/mrgluek/passage/core/tunnel"
)

type config struct {
	Key     string            `json:"key"`
	Allowed []exitpolicy.Rule `json:"allowed"`
}

func run() error {
	configFile := flag.String("config", "", "private JSON configuration file")
	listen := flag.String("listen", "0.0.0.0:34780", "UDP listen address")
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
	exit, err := tunnel.NewTURNExit(key, dial)
	if err != nil {
		return err
	}
	defer exit.Close()
	conn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); exit.Close(); conn.Close() }()
	err = exit.Serve(conn)
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "passage-turn-exit:", err)
		os.Exit(1)
	}
}
