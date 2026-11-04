// Package update finds, downloads and verifies new app releases. A release
// publishes a signed manifest next to its files, on any number of sources.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Set by the release build.
var (
	// PublicKey is the manifest key, base64url without padding.
	PublicKey string
	// ExtraSources lists more sources, comma-separated.
	ExtraSources string
)

const (
	// DefaultSource is tried with ExtraSources and the link's sources.
	DefaultSource = "https://passage.gluek.info/fdroid/repo"
	// ManifestName is the manifest's file name on a source.
	ManifestName = "update.json"
	signContext  = "passage-update-v1\n"
	maxManifest  = 64 << 10
	maxSources   = 8
	chunkSize    = 1 << 20
	stallTimeout = 15 * time.Second
	// A chunk that stopped short still counts when it brought data.
	maxIdleRounds = 2
)

var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// File is one downloadable file of a release.
type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest describes a release; Files is keyed by target, such as "arm64".
type Manifest struct {
	Version string          `json:"version"`
	Files   map[string]File `json:"files"`
}

type envelope struct {
	Manifest  string `json:"manifest"`
	Signature string `json:"signature"`
}

// Sign encodes m and its signature as the manifest file.
func Sign(m Manifest, key ed25519.PrivateKey) ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{
		Manifest:  base64.StdEncoding.EncodeToString(body),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(signContext), body...))),
	})
}

// Parse returns the manifest in raw if its signature matches key.
func Parse(raw []byte, key ed25519.PublicKey) (Manifest, error) {
	var m Manifest
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return m, errors.New("invalid manifest")
	}
	body, err := base64.StdEncoding.DecodeString(e.Manifest)
	if err != nil {
		return m, errors.New("invalid manifest")
	}
	signature, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || !ed25519.Verify(key, append([]byte(signContext), body...), signature) {
		return m, errors.New("invalid manifest signature")
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, errors.New("invalid manifest")
	}
	return m, m.validate()
}

func (m Manifest) validate() error {
	if _, err := parseVersion(m.Version); err != nil {
		return err
	}
	for target, f := range m.Files {
		sum, err := hex.DecodeString(f.SHA256)
		if !fileName.MatchString(f.Name) || f.Size <= 0 || err != nil || len(sum) != sha256.Size {
			return fmt.Errorf("invalid file for %s", target)
		}
	}
	return nil
}

func parseVersion(s string) ([]int, error) {
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid version %q", s)
		}
		out[i] = n
	}
	return out, nil
}

// Newer reports whether release a is later than installed version b, which
// may be a pre-release such as 0.6.0-beta1. Invalid versions are not.
func Newer(a, b string) bool {
	x, err := parseVersion(a)
	if err != nil {
		return false
	}
	base, pre, _ := strings.Cut(b, "-")
	y, err := parseVersion(base)
	if err != nil {
		return false
	}
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p > q
		}
	}
	return pre != ""
}

// Sources merges link sources with the built-in ones: http(s) only, no
// duplicates, link sources first so a link can route around a dead host.
func Sources(link []string) []string {
	all := append([]string(nil), link...)
	if ExtraSources != "" {
		all = append(all, strings.Split(ExtraSources, ",")...)
	}
	all = append(all, DefaultSource)
	seen := map[string]bool{}
	var out []string
	for _, s := range all {
		s = strings.TrimRight(strings.TrimSpace(s), "/")
		if seen[s] || !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
			continue
		}
		seen[s] = true
		if out = append(out, s); len(out) == maxSources {
			break
		}
	}
	return out
}

// Dial opens a connection; a nil Dial is a plain one.
type Dial = func(ctx context.Context, network, addr string) (net.Conn, error)

// Client fetches through the routes Dials returns, in order. It is called
// again on every step, since the routes change while the proxy runs.
type Client struct {
	Key     ed25519.PublicKey
	Sources []string
	Dials   func() []Dial
}

// Offer is a newer release available for a target.
type Offer struct {
	Version string `json:"version"`
	File    File   `json:"file"`
	// Sources has the release, the quickest first.
	Sources []string `json:"-"`
}

// Check asks every source at once, since one may lag behind the others, and
// returns the newest release for target. A nil Offer means up to date.
func (c *Client) Check(ctx context.Context, current, target string) (*Offer, error) {
	type answer struct {
		source   string
		manifest Manifest
		err      error
	}
	answers := make(chan answer, len(c.Sources))
	var wg sync.WaitGroup
	for _, source := range c.Sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := c.manifest(ctx, source)
			answers <- answer{source, m, err}
		}()
	}
	wg.Wait()
	close(answers)

	var offer *Offer
	var failure error
	reached := false
	for a := range answers {
		if a.err != nil {
			failure = a.err
			continue
		}
		reached = true
		file, ok := a.manifest.Files[target]
		if !ok || !Newer(a.manifest.Version, current) {
			continue
		}
		switch {
		case offer == nil || Newer(a.manifest.Version, offer.Version):
			offer = &Offer{Version: a.manifest.Version, File: file, Sources: []string{a.source}}
		case a.manifest.Version == offer.Version && file == offer.File:
			offer.Sources = append(offer.Sources, a.source)
		}
	}
	if !reached && failure != nil {
		return nil, failure
	}
	return offer, nil
}

func (c *Client) manifest(ctx context.Context, source string) (Manifest, error) {
	var last error = errors.New("no route")
	for _, dial := range c.dials() {
		raw, err := fetchAll(ctx, dial, source+"/"+ManifestName)
		if err == nil {
			m, err := Parse(raw, c.Key)
			if err == nil {
				return m, nil
			}
			last = err
			continue
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return Manifest{}, last
}

func (c *Client) dials() []Dial {
	if c.Dials != nil {
		if d := c.Dials(); len(d) > 0 {
			return d
		}
	}
	return []Dial{nil}
}

// Download saves the offered file at dest. An earlier partial download is
// continued. The progress callback gets the bytes saved so far.
func (c *Client) Download(ctx context.Context, offer *Offer, dest string, progress func(done, total int64)) error {
	if len(offer.Sources) == 0 {
		return errors.New("no source")
	}
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	done, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	size := offer.File.Size
	if done > size {
		if err := f.Truncate(0); err != nil {
			return err
		}
		done = 0
	}
	if progress != nil {
		progress(done, size)
	}

	// (source, route) pairs in turn; a pair stays while it brings data.
	turn, idle := 0, 0
	var last error
	for done < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		dials := c.dials()
		pairs := len(offer.Sources) * len(dials)
		if idle >= maxIdleRounds*pairs {
			return fmt.Errorf("download failed: %w", last)
		}
		source := offer.Sources[turn%pairs%len(offer.Sources)]
		dial := dials[(turn%pairs)/len(offer.Sources)]
		n, err := fetchChunk(ctx, dial, source+"/"+offer.File.Name, f, done, size)
		done += n
		if progress != nil && n > 0 {
			progress(done, size)
		}
		if n > 0 {
			idle = 0
		}
		if err != nil {
			last = err
			if n == 0 {
				idle++
				turn++
			}
		}
	}

	if err := verify(f, offer.File); err != nil {
		f.Close()
		os.Remove(part)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(part, dest)
}

func verify(f *os.File, want File) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(want.SHA256) {
		return errors.New("downloaded file does not match the manifest")
	}
	return nil
}

// open starts a GET on its own connection. Stalled reads end the request:
// the returned reader fails when no data comes for stallTimeout.
func open(ctx context.Context, dial Dial, url, byteRange string) (*http.Response, io.Reader, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	stall := time.AfterFunc(stallTimeout, cancel)
	stop := func() { stall.Stop(); cancel() }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	transport := &http.Transport{DialContext: dial, DisableKeepAlives: true}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	stall.Reset(stallTimeout)
	body := watched{resp.Body, stall}
	return resp, body, func() { resp.Body.Close(); stop() }, nil
}

type watched struct {
	r     io.Reader
	stall *time.Timer
}

func (w watched) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.stall.Reset(stallTimeout)
	}
	return n, err
}

func fetchAll(ctx context.Context, dial Dial, url string) ([]byte, error) {
	resp, body, closeBody, err := open(ctx, dial, url, "")
	if err != nil {
		return nil, err
	}
	defer closeBody()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxManifest {
		return nil, errors.New("manifest too large")
	}
	return raw, nil
}

// fetchChunk appends up to chunkSize bytes at offset and returns how many it
// wrote, also when it fails partway.
func fetchChunk(ctx context.Context, dial Dial, url string, f *os.File, offset, size int64) (int64, error) {
	want := min(size-offset, chunkSize)
	resp, body, closeBody, err := open(ctx, dial, url, fmt.Sprintf("bytes=%d-%d", offset, offset+want-1))
	if err != nil {
		return 0, err
	}
	defer closeBody()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", offset)) {
			return 0, errors.New("unexpected range in response")
		}
	case http.StatusOK: // the server ignored Range
		if _, err := io.CopyN(io.Discard, body, offset); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("%s: %s", url, resp.Status)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(body, want))
	if err == nil && n < want {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
