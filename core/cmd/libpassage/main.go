//go:build android

// Command libpassage is the native side of the Passage Android app: the
// router exported as JNI methods of info.gluek.passage.Native, so the app
// needs no C glue. Strings cross as UTF-8 byte arrays (NewStringUTF wants
// modified UTF-8).
package main

/*
#include <jni.h>
#include <stdlib.h>

static jbyteArray newBytes(JNIEnv *env, const void *p, jsize n) {
	jbyteArray a = (*env)->NewByteArray(env, n);
	if (a != NULL && n > 0) (*env)->SetByteArrayRegion(env, a, 0, n, (const jbyte *)p);
	return a;
}
static jsize bytesLen(JNIEnv *env, jbyteArray a) { return (*env)->GetArrayLength(env, a); }
static void bytesCopy(JNIEnv *env, jbyteArray a, jsize n, void *dst) {
	(*env)->GetByteArrayRegion(env, a, 0, n, (jbyte *)dst);
}
*/
import "C"

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/mrgluek/passage/core/tunnel"
	"github.com/mrgluek/passage/core/update"
)

var (
	mu  sync.Mutex
	cur *tunnel.Tunnel

	updateMu     sync.Mutex
	offer        *update.Offer
	stopDownload context.CancelFunc
	updateDone   atomic.Int64
	updateTotal  atomic.Int64
)

// cgo maps JNI object types to uintptr, so a null array is 0.
func goString(env *C.JNIEnv, a C.jbyteArray) string {
	if a == 0 {
		return ""
	}
	n := C.bytesLen(env, a)
	if n <= 0 {
		return ""
	}
	b := make([]byte, int(n))
	C.bytesCopy(env, a, n, unsafe.Pointer(&b[0]))
	return string(b)
}

func result(env *C.JNIEnv, v any) C.jbyteArray {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"error":"marshal failed"}`)
	}
	p := C.CBytes(b)
	defer C.free(p)
	return C.newBytes(env, p, C.jsize(len(b)))
}

func fail(env *C.JNIEnv, err error) C.jbyteArray {
	return result(env, map[string]string{"error": err.Error()})
}

// parseLink validates a passage://tunnel link and returns its config.
//
//export Java_info_gluek_passage_Native_parseLink
func Java_info_gluek_passage_Native_parseLink(env *C.JNIEnv, cls C.jclass, link C.jbyteArray) C.jbyteArray {
	cfg, err := tunnel.ParseLink(goString(env, link))
	if err != nil {
		return fail(env, err)
	}
	return result(env, cfg)
}

//export Java_info_gluek_passage_Native_importLink
func Java_info_gluek_passage_Native_importLink(env *C.JNIEnv, cls C.jclass, request C.jbyteArray) C.jbyteArray {
	var in struct {
		Existing  tunnel.Config `json:"existing"`
		Link      string        `json:"link"`
		Replace   bool          `json:"replace"`
		Subscribe bool          `json:"subscribe"`
	}
	if err := json.Unmarshal([]byte(goString(env, request)), &in); err != nil {
		return fail(env, errors.New("invalid import request"))
	}
	out, err := tunnel.ImportLink(in.Existing, in.Link, in.Replace)
	if err == nil && in.Subscribe && out.Mode == "subscribe" {
		out, err = tunnel.SubscribeLink(in.Existing, in.Link, in.Replace)
	}
	if err != nil {
		return fail(env, err)
	}
	return result(env, out)
}

//export Java_info_gluek_passage_Native_removeSubscription
func Java_info_gluek_passage_Native_removeSubscription(env *C.JNIEnv, cls C.jclass, request C.jbyteArray) C.jbyteArray {
	var in struct {
		Existing tunnel.Config `json:"existing"`
		ID       string        `json:"id"`
	}
	if err := json.Unmarshal([]byte(goString(env, request)), &in); err != nil {
		return fail(env, errors.New("invalid remove request"))
	}
	cfg, err := tunnel.RemoveSubscription(in.Existing, in.ID)
	if err != nil {
		return fail(env, err)
	}
	return result(env, struct {
		Config tunnel.Config `json:"config"`
	}{cfg})
}

//export Java_info_gluek_passage_Native_refreshSubscriptions
func Java_info_gluek_passage_Native_refreshSubscriptions(env *C.JNIEnv, cls C.jclass, request C.jbyteArray) C.jbyteArray {
	var in struct {
		Existing tunnel.Config `json:"existing"`
	}
	if err := json.Unmarshal([]byte(goString(env, request)), &in); err != nil {
		return fail(env, errors.New("invalid refresh request"))
	}
	mu.Lock()
	t := cur
	mu.Unlock()
	if t != nil {
		return result(env, t.RefreshSubscriptions(context.Background()))
	}
	var ref tunnel.SubscriptionRefresher
	return result(env, ref.Refresh(context.Background(), in.Existing, nil))
}

// start (re)starts the proxy from a config JSON and returns {"addr": "…"}.
//
//export Java_info_gluek_passage_Native_start
func Java_info_gluek_passage_Native_start(env *C.JNIEnv, cls C.jclass, cfgJSON C.jbyteArray) C.jbyteArray {
	var cfg tunnel.Config
	if err := json.Unmarshal([]byte(goString(env, cfgJSON)), &cfg); err != nil {
		return fail(env, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.Close()
		cur = nil
	}
	t, err := tunnel.Start(cfg)
	if err != nil {
		return fail(env, err)
	}
	cur = t
	return result(env, map[string]string{"addr": t.Addr()})
}

//export Java_info_gluek_passage_Native_stop
func Java_info_gluek_passage_Native_stop(env *C.JNIEnv, cls C.jclass) {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.Close()
		cur = nil
	}
}

// status returns the snapshot with up to `events` events, or {"running": false}.
//
//export Java_info_gluek_passage_Native_status
func Java_info_gluek_passage_Native_status(env *C.JNIEnv, cls C.jclass, events C.jint) C.jbyteArray {
	mu.Lock()
	defer mu.Unlock()
	if cur == nil {
		return result(env, map[string]bool{"running": false})
	}
	return result(env, struct {
		Running bool `json:"running"`
		tunnel.Status
	}{true, cur.Status(int(events))})
}

//export Java_info_gluek_passage_Native_setMode
func Java_info_gluek_passage_Native_setMode(env *C.JNIEnv, cls C.jclass, mode C.jbyteArray) C.jbyteArray {
	mu.Lock()
	defer mu.Unlock()
	if cur == nil {
		return fail(env, errors.New("tunnel is not running"))
	}
	if err := cur.SetMode(goString(env, mode)); err != nil {
		return fail(env, err)
	}
	return result(env, map[string]bool{"ok": true})
}

//export Java_info_gluek_passage_Native_networkChanged
func Java_info_gluek_passage_Native_networkChanged(env *C.JNIEnv, cls C.jclass) {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.NetworkChanged()
	}
}

//export Java_info_gluek_passage_Native_recheck
func Java_info_gluek_passage_Native_recheck(env *C.JNIEnv, cls C.jclass) {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		cur.Recheck()
	}
}

// diagnose runs the network test (Tunnel.Diagnose) and returns it as JSON.
// It blocks for up to about 20 seconds; the app calls it off the UI thread.
//
//export Java_info_gluek_passage_Native_diagnose
func Java_info_gluek_passage_Native_diagnose(env *C.JNIEnv, cls C.jclass) C.jbyteArray {
	mu.Lock()
	t := cur
	mu.Unlock()
	if t == nil {
		return fail(env, errors.New("proxy is not running"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return result(env, t.Diagnose(ctx))
}

// versions returns {"soar": "<module version>", "updates": "<key>"} for the
// About screen; a build without the update key has none.
//
//export Java_info_gluek_passage_Native_versions
func Java_info_gluek_passage_Native_versions(env *C.JNIEnv, cls C.jclass) C.jbyteArray {
	return result(env, map[string]string{"soar": tunnel.SoarVersion(), "updates": update.PublicKey})
}

func updateClient(sources []string) (*update.Client, error) {
	key, err := base64.RawURLEncoding.DecodeString(update.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("this build has no update key")
	}
	return &update.Client{
		Key:     ed25519.PublicKey(key),
		Sources: update.Sources(sources),
		Dials: func() []update.Dial {
			mu.Lock()
			t := cur
			mu.Unlock()
			if t == nil {
				return nil
			}
			return t.Dials()
		},
	}, nil
}

// updateCheck takes {"version", "target", "sources"} and returns the newer
// release as {"version", "file"}, or {} when there is none. It blocks for up
// to about 45 seconds; the app calls it off the UI thread.
//
//export Java_info_gluek_passage_Native_updateCheck
func Java_info_gluek_passage_Native_updateCheck(env *C.JNIEnv, cls C.jclass, request C.jbyteArray) C.jbyteArray {
	var in struct {
		Version string   `json:"version"`
		Target  string   `json:"target"`
		Sources []string `json:"sources"`
	}
	if err := json.Unmarshal([]byte(goString(env, request)), &in); err != nil {
		return fail(env, errors.New("invalid update request"))
	}
	client, err := updateClient(in.Sources)
	if err != nil {
		return fail(env, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	found, err := client.Check(ctx, in.Version, in.Target)
	if err != nil {
		return fail(env, err)
	}
	updateMu.Lock()
	defer updateMu.Unlock()
	if stopDownload == nil {
		offer = found
	}
	if found == nil {
		return result(env, map[string]string{})
	}
	return result(env, found)
}

// updateDownload saves the last offer at the path in dest. It blocks until
// the file is complete, or updateCancel stops it.
//
//export Java_info_gluek_passage_Native_updateDownload
func Java_info_gluek_passage_Native_updateDownload(env *C.JNIEnv, cls C.jclass, dest C.jbyteArray) C.jbyteArray {
	client, err := updateClient(nil)
	if err != nil {
		return fail(env, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updateMu.Lock()
	if offer == nil || stopDownload != nil {
		updateMu.Unlock()
		return fail(env, errors.New("nothing to download"))
	}
	current := offer
	stopDownload = cancel
	updateMu.Unlock()
	defer func() {
		updateMu.Lock()
		stopDownload = nil
		updateMu.Unlock()
	}()
	updateDone.Store(0)
	updateTotal.Store(current.File.Size)
	path := goString(env, dest)
	if err := client.Download(ctx, current, path, func(done, _ int64) { updateDone.Store(done) }); err != nil {
		return fail(env, err)
	}
	return result(env, map[string]string{"path": path})
}

// updateProgress returns {"done", "total"} in bytes.
//
//export Java_info_gluek_passage_Native_updateProgress
func Java_info_gluek_passage_Native_updateProgress(env *C.JNIEnv, cls C.jclass) C.jbyteArray {
	return result(env, map[string]int64{"done": updateDone.Load(), "total": updateTotal.Load()})
}

//export Java_info_gluek_passage_Native_updateCancel
func Java_info_gluek_passage_Native_updateCancel(env *C.JNIEnv, cls C.jclass) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if stopDownload != nil {
		stopDownload()
	}
}

func main() {}
