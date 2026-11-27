//go:build ios

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unsafe"

	"github.com/mrgluek/passage/core/tunnel"
	"github.com/mrgluek/passage/core/update"
)

var (
	mu  sync.Mutex
	cur *tunnel.Tunnel
)

func goString(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

func result(v any) *C.char {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"error":"marshal failed"}`)
	}
	return C.CString(string(b))
}

func fail(err error) *C.char {
	return result(map[string]string{"error": err.Error()})
}

//export passage_parseLink
func passage_parseLink(link *C.char) *C.char {
	cfg, err := tunnel.ParseLink(goString(link))
	if err != nil {
		return fail(err)
	}
	return result(cfg)
}

//export passage_importLink
func passage_importLink(request *C.char) *C.char {
	var in struct {
		Existing  tunnel.Config `json:"existing"`
		Link      string        `json:"link"`
		Replace   bool          `json:"replace"`
		Subscribe bool          `json:"subscribe"`
	}
	if err := json.Unmarshal([]byte(goString(request)), &in); err != nil {
		return fail(errors.New("invalid import request"))
	}
	out, err := tunnel.ImportLink(in.Existing, in.Link, in.Replace)
	if err == nil && in.Subscribe && out.Mode == "subscribe" {
		out, err = tunnel.SubscribeLink(in.Existing, in.Link, in.Replace)
	}
	if err != nil {
		return fail(err)
	}
	return result(out)
}

//export passage_removeSubscription
func passage_removeSubscription(request *C.char) *C.char {
	var in struct {
		Existing tunnel.Config `json:"existing"`
		ID       string        `json:"id"`
	}
	if err := json.Unmarshal([]byte(goString(request)), &in); err != nil {
		return fail(errors.New("invalid remove request"))
	}
	cfg, err := tunnel.RemoveSubscription(in.Existing, in.ID)
	if err != nil {
		return fail(err)
	}
	return result(struct {
		Config tunnel.Config `json:"config"`
	}{cfg})
}

//export passage_refreshSubscriptions
func passage_refreshSubscriptions(request *C.char) *C.char {
	var in struct {
		Existing tunnel.Config `json:"existing"`
		Initial  bool          `json:"initial"`
	}
	if err := json.Unmarshal([]byte(goString(request)), &in); err != nil {
		return fail(errors.New("invalid refresh request"))
	}
	mu.Lock()
	t := cur
	mu.Unlock()
	if t != nil {
		if in.Initial {
			return result(t.RefreshInitialSubscriptions(context.Background()))
		}
		return result(t.RefreshSubscriptions(context.Background()))
	}
	var ref tunnel.SubscriptionRefresher
	return result(ref.Refresh(context.Background(), in.Existing, nil))
}

//export passage_start
func passage_start(cfgJSON *C.char) *C.char {
	var cfg tunnel.Config
	if err := json.Unmarshal([]byte(goString(cfgJSON)), &cfg); err != nil {
		return fail(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.Close()
		cur = nil
	}
	t, err := tunnel.Start(cfg)
	if err != nil {
		return fail(err)
	}
	cur = t
	return result(map[string]string{"addr": t.Addr()})
}

//export passage_stop
func passage_stop() {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.Close()
		cur = nil
	}
}

//export passage_status
func passage_status(events C.int) *C.char {
	mu.Lock()
	defer mu.Unlock()
	if cur == nil {
		return result(map[string]bool{"running": false})
	}
	return result(struct {
		Running bool `json:"running"`
		tunnel.Status
	}{true, cur.Status(int(events))})
}

//export passage_setMode
func passage_setMode(mode *C.char) *C.char {
	mu.Lock()
	defer mu.Unlock()
	if cur == nil {
		return fail(errors.New("tunnel is not running"))
	}
	if err := cur.SetMode(goString(mode)); err != nil {
		return fail(err)
	}
	return result(map[string]bool{"ok": true})
}

//export passage_networkChanged
func passage_networkChanged() {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.NetworkChanged()
	}
}

//export passage_recheck
func passage_recheck() {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.Recheck()
	}
}

//export passage_diagnose
func passage_diagnose() *C.char {
	mu.Lock()
	t := cur
	mu.Unlock()
	if t == nil {
		return fail(errors.New("proxy is not running"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return result(t.Diagnose(ctx))
}

//export passage_versions
func passage_versions() *C.char {
	return result(map[string]string{"soar": tunnel.SoarVersion(), "updates": update.PublicKey})
}

//export passage_free
func passage_free(s *C.char) {
	C.free(unsafe.Pointer(s))
}

func main() {}
