package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type release struct {
	key     ed25519.PublicKey
	offer   *Offer
	content []byte
	body    []byte
}

func newRelease(t *testing.T, version string, content []byte) release {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	file := File{Name: "app.apk", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
	body, err := Sign(Manifest{Version: version, Files: map[string]File{"arm64": file}}, private)
	if err != nil {
		t.Fatal(err)
	}
	return release{pub, &Offer{Version: version, File: file}, content, body}
}

func serve(t *testing.T, r release, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/update.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(r.body) })
	mux.HandleFunc("/app.apk", func(w http.ResponseWriter, req *http.Request) {
		http.ServeContent(w, req, "app.apk", time.Time{}, bytes.NewReader(r.content))
	})
	var h http.Handler = mux
	if wrap != nil {
		h = wrap(mux)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func TestParseRejectsTampering(t *testing.T) {
	r := newRelease(t, "1.2.0", []byte("x"))
	if _, err := Parse(r.body, r.key); err != nil {
		t.Fatal(err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if _, err := Parse(r.body, other); err == nil {
		t.Fatal("accepted another key")
	}
	if _, err := Parse(bytes.Replace(r.body, []byte(`"manifest":"e`), []byte(`"manifest":"f`), 1), r.key); err == nil {
		t.Fatal("accepted a changed manifest")
	}
}

func TestSignRejectsUnsafeName(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(nil)
	f := File{Name: "../app.apk", Size: 1, SHA256: strings.Repeat("0", 64)}
	if _, err := Sign(Manifest{Version: "1.0.0", Files: map[string]File{"arm64": f}}, private); err == nil {
		t.Fatal("accepted a path as a file name")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.10.0", "0.9.9", true}, {"0.6.0", "0.6.0", false}, {"0.6", "0.6.0", false},
		{"0.6.0.1", "0.6.0", true}, {"0.5.9", "0.6.0", false}, {"bad", "0.6.0", false}, {"1.0.0", "bad", false},
		{"0.6.0", "0.6.0-beta1", true}, {"0.5.0", "0.6.0-beta1", false}, {"0.7.0-beta1", "0.6.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestSources(t *testing.T) {
	ExtraSources = "https://b.example.org/repo/, ftp://c.example.org"
	defer func() { ExtraSources = "" }()
	got := Sources([]string{"https://a.example.org/", "https://b.example.org/repo"})
	want := "https://a.example.org https://b.example.org/repo " + DefaultSource
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v", got)
	}
}

func TestCheck(t *testing.T) {
	r := newRelease(t, "1.2.0", []byte("x"))
	dead := "http://127.0.0.1:1"
	c := &Client{Key: r.key, Sources: []string{dead, serve(t, r, nil)}}
	offer, err := c.Check(context.Background(), "1.1.0", "arm64")
	if err != nil || offer == nil || offer.Version != "1.2.0" || len(offer.Sources) != 1 {
		t.Fatalf("offer %+v, err %v", offer, err)
	}
	if offer, err := c.Check(context.Background(), "1.2.0", "arm64"); offer != nil || err != nil {
		t.Fatalf("same version: %+v, %v", offer, err)
	}
	if offer, err := c.Check(context.Background(), "1.1.0", "armv7"); offer != nil || err != nil {
		t.Fatalf("other target: %+v, %v", offer, err)
	}
	c.Sources = []string{dead}
	if _, err := c.Check(context.Background(), "1.1.0", "arm64"); err == nil {
		t.Fatal("no error with no source reached")
	}
}

func TestCheckRejectsForeignManifest(t *testing.T) {
	r := newRelease(t, "1.2.0", []byte("x"))
	other, _, _ := ed25519.GenerateKey(nil)
	c := &Client{Key: other, Sources: []string{serve(t, r, nil)}}
	if offer, err := c.Check(context.Background(), "1.1.0", "arm64"); err == nil || offer != nil {
		t.Fatalf("offer %+v, err %v", offer, err)
	}
}

func TestCheckUsesNextRoute(t *testing.T) {
	r := newRelease(t, "1.2.0", []byte("x"))
	var plain net.Dialer
	broken := func(context.Context, string, string) (net.Conn, error) { return nil, fmt.Errorf("blocked") }
	c := &Client{Key: r.key, Sources: []string{serve(t, r, nil)}, Dials: func() []Dial { return []Dial{broken, plain.DialContext} }}
	if offer, err := c.Check(context.Background(), "1.1.0", "arm64"); err != nil || offer == nil {
		t.Fatalf("offer %+v, err %v", offer, err)
	}
}

func content(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 31 >> 3)
	}
	return b
}

func TestDownloadSpansChunks(t *testing.T) {
	r := newRelease(t, "1.2.0", content(chunkSize*2+123))
	r.offer.Sources = []string{serve(t, r, nil)}
	dest := filepath.Join(t.TempDir(), "app.apk")
	var last int64
	c := &Client{Key: r.key}
	if err := c.Download(context.Background(), r.offer, dest, func(done, _ int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, r.content) || last != int64(len(r.content)) {
		t.Fatalf("got %d bytes, progress %d", len(got), last)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal("partial file left behind")
	}
}

// Every response breaks partway.
func TestDownloadResumesAfterCuts(t *testing.T) {
	r := newRelease(t, "1.2.0", content(200_000))
	var requests atomic.Int32
	r.offer.Sources = []string{serve(t, r, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			requests.Add(1)
			next.ServeHTTP(&cutWriter{w, 50_000}, req)
		})
	})}
	dest := filepath.Join(t.TempDir(), "app.apk")
	if err := (&Client{Key: r.key}).Download(context.Background(), r.offer, dest, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, r.content) {
		t.Fatal("wrong content")
	}
	if requests.Load() < 4 {
		t.Fatalf("%d requests", requests.Load())
	}
}

type cutWriter struct {
	http.ResponseWriter
	left int
}

func (w *cutWriter) Write(p []byte) (int, error) {
	if w.left <= 0 {
		panic(http.ErrAbortHandler)
	}
	if len(p) > w.left {
		p = p[:w.left]
	}
	w.left -= len(p)
	n, err := w.ResponseWriter.Write(p)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

func TestDownloadContinuesPartialFile(t *testing.T) {
	r := newRelease(t, "1.2.0", content(100_000))
	var firstRange atomic.Value
	r.offer.Sources = []string{serve(t, r, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			firstRange.CompareAndSwap(nil, req.Header.Get("Range"))
			next.ServeHTTP(w, req)
		})
	})}
	dest := filepath.Join(t.TempDir(), "app.apk")
	if err := os.WriteFile(dest+".part", r.content[:40_000], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Client{Key: r.key}).Download(context.Background(), r.offer, dest, nil); err != nil {
		t.Fatal(err)
	}
	if got := firstRange.Load(); got != "bytes=40000-99999" {
		t.Fatalf("range %v", got)
	}
}

func TestDownloadRejectsWrongContent(t *testing.T) {
	r := newRelease(t, "1.2.0", content(1000))
	bad := r
	bad.content = content(1000)
	bad.content[10]++
	r.offer.Sources = []string{serve(t, bad, nil)}
	dest := filepath.Join(t.TempDir(), "app.apk")
	if err := (&Client{Key: r.key}).Download(context.Background(), r.offer, dest, nil); err == nil {
		t.Fatal("accepted wrong content")
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s stayed", p)
		}
	}
}

func TestDownloadMovesToNextSource(t *testing.T) {
	r := newRelease(t, "1.2.0", content(1000))
	r.offer.Sources = []string{"http://127.0.0.1:1", serve(t, r, nil)}
	dest := filepath.Join(t.TempDir(), "app.apk")
	if err := (&Client{Key: r.key}).Download(context.Background(), r.offer, dest, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadGivesUp(t *testing.T) {
	r := newRelease(t, "1.2.0", content(1000))
	r.offer.Sources = []string{"http://127.0.0.1:1"}
	dest := filepath.Join(t.TempDir(), "app.apk")
	if err := (&Client{Key: r.key}).Download(context.Background(), r.offer, dest, nil); err == nil {
		t.Fatal("no error")
	}
}

func TestDownloadCancel(t *testing.T) {
	r := newRelease(t, "1.2.0", content(1000))
	r.offer.Sources = []string{serve(t, r, nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "app.apk")
	if err := (&Client{Key: r.key}).Download(ctx, r.offer, dest, nil); err == nil {
		t.Fatal("no error")
	}
}

func TestCheckPicksNewestSource(t *testing.T) {
	sum := sha256.Sum256([]byte("y"))
	file := File{Name: "app.apk", Size: 1, SHA256: hex.EncodeToString(sum[:])}
	pub, private, _ := ed25519.GenerateKey(nil)
	sign := func(version string) release {
		body, err := Sign(Manifest{Version: version, Files: map[string]File{"arm64": file}}, private)
		if err != nil {
			t.Fatal(err)
		}
		return release{key: pub, body: body}
	}
	c := &Client{Key: pub, Sources: []string{serve(t, sign("1.1.0"), nil), serve(t, sign("1.3.0"), nil)}}
	offer, err := c.Check(context.Background(), "1.0.0", "arm64")
	if err != nil || offer == nil || offer.Version != "1.3.0" {
		t.Fatalf("offer %+v, err %v", offer, err)
	}
}
