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
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unsafe"

	"github.com/mrgluek/passage/core/tunnel"
)

var (
	mu  sync.Mutex
	cur *tunnel.Tunnel
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

// versions returns {"soar": "<module version>"} for the About screen.
//
//export Java_info_gluek_passage_Native_versions
func Java_info_gluek_passage_Native_versions(env *C.JNIEnv, cls C.jclass) C.jbyteArray {
	return result(env, map[string]string{"soar": tunnel.SoarVersion()})
}

func main() {}
