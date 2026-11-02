// Command wss-probe checks one private WSS route without using direct fallback.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	links "github.com/mrgluek/passage-link/link"
	"github.com/mrgluek/passage/core/tunnel"
)

type options struct {
	Target   string        `json:"target"`
	Protocol string        `json:"protocol"`
	Point    string        `json:"point"`
	Network  string        `json:"network"`
	Timeout  time.Duration `json:"timeout"`
	Dumbpipe string        `json:"dumbpipe,omitempty"`
	Path     string        `json:"path,omitempty"`
}
type request struct {
	Route   links.Route `json:"route"`
	Options options     `json:"options"`
}
type result struct {
	At           time.Time `json:"at"`
	Point        string    `json:"point"`
	Network      string    `json:"network"`
	Fingerprint  string    `json:"fingerprint"`
	Outcome      string    `json:"outcome"`
	Protocol     string    `json:"protocol"`
	Target       string    `json:"target,omitempty"`
	Milliseconds int64     `json:"milliseconds"`
	Bytes        int64     `json:"bytes,omitempty"`
	Error        string    `json:"error,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wss-probe:", err)
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("route-file", "", "private route JSON file; otherwise read a catalog probe request from stdin")
	o := options{}
	flag.StringVar(&o.Target, "target", "", "approved host:port")
	flag.StringVar(&o.Protocol, "protocol", "tls", "tls, mtproto, echo or download")
	flag.StringVar(&o.Point, "point", "local", "actual probe point")
	flag.StringVar(&o.Network, "network", "", "actual network label")
	flag.DurationVar(&o.Timeout, "timeout", 30*time.Second, "probe timeout")
	flag.StringVar(&o.Path, "path", "", "HTTPS path to fetch for download")
	flag.Parse()
	var r links.Route
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return errors.New("cannot open route file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, links.MaxBytes+1))
		if err != nil {
			return err
		}
		if err = links.JSON(data, &r); err != nil {
			return err
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, links.MaxBytes+1))
		if err != nil {
			return err
		}
		var req request
		if err = links.JSON(data, &req); err != nil {
			return err
		}
		r, o = req.Route, req.Options
	}
	if o.Target == "" || o.Point == "" || o.Network == "" || o.Timeout <= 0 || o.Timeout > 10*time.Minute || o.Protocol != "tls" && o.Protocol != "mtproto" && o.Protocol != "echo" && o.Protocol != "download" {
		return errors.New("set target, point, network, protocol and a timeout up to 10 minutes")
	}
	if (o.Protocol == "download") != strings.HasPrefix(o.Path, "/") {
		return errors.New("download needs a path starting with /, other protocols none")
	}
	if supported, err := links.ValidateRoute(r); err != nil || !supported || r.Type != "wss" {
		return errors.New("invalid WSS route")
	}
	params, err := links.Params[links.WSSParams](r)
	if err != nil {
		return err
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()
	var n int64
	if o.Protocol == "download" {
		n, err = tunnel.ProbeWSSDownload(ctx, params, o.Target, o.Path)
	} else {
		err = tunnel.ProbeWSS(ctx, params, o.Target, o.Protocol)
	}
	out := result{At: start.UTC(), Point: o.Point, Network: o.Network, Fingerprint: links.RouteHash(r), Outcome: "passed", Protocol: o.Protocol, Target: o.Target, Milliseconds: time.Since(start).Milliseconds(), Bytes: n}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wss-probe: detail:", err)
		out.Outcome = "failed"
		out.Error = "protocol or transport check failed"
		if ctx.Err() != nil {
			out.Error = "probe canceled or timed out"
		}
	}
	if e := json.NewEncoder(os.Stdout).Encode(out); e != nil {
		return e
	}
	if err != nil {
		return errors.New(out.Error)
	}
	return nil
}
