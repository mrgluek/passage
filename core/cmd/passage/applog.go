package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mrgluek/passage/core/tunnel"
)

const (
	logName   = "passage.log"
	logMax    = 256 << 10 // then passage.log becomes passage.log.1
	logKeep   = 128       // lines kept in memory for the window and the report
	logMsgMax = 1024
)

// appLog is the app's own history in passage.log, kept across restarts
// like the Android app's. Each line is "<UTC time> [pid] <version> <message>".
type appLog struct {
	path    string
	version string

	mu     sync.Mutex
	lines  []string
	copied time.Time
	copyMu sync.Mutex
}

func openLog(path, version string) *appLog {
	l := &appLog{path: path, version: version}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l.keep(sc.Text())
		}
		f.Close()
	}
	return l
}

func (l *appLog) keep(line string) {
	l.lines = append(l.lines, line)
	if len(l.lines) > logKeep {
		l.lines = l.lines[len(l.lines)-logKeep:]
	}
}

// add records one event. Links, keys and credentials are redacted first.
func (l *appLog) add(format string, args ...any) {
	l.addAt(time.Now(), fmt.Sprintf(format, args...))
}

func (l *appLog) addAt(stamp time.Time, message string) {
	msg := redact(message)
	if r := []rune(msg); len(r) > logMsgMax {
		msg = string(r[:logMsgMax])
	}
	msg = strings.ReplaceAll(msg, "\n", " ")
	line := fmt.Sprintf("%s [%d] %s %s", stamp.UTC().Format("2006-01-02T15:04:05.000Z"), os.Getpid(), l.version, msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keep(line)
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > logMax {
		os.Rename(l.path, l.path+".1")
	}
	if f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600); err == nil {
		f.WriteString(line + "\r\n")
		f.Close()
	}
}

// last is up to n recent lines, oldest first.
func (l *appLog) last(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines[max(0, len(l.lines)-n):]...)
}

// text is the recent lines for the window, with local times.
func (l *appLog) text(n int) string {
	var b strings.Builder
	for _, line := range l.last(n) {
		stamp, rest, _ := strings.Cut(line, " ")
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", stamp); err == nil {
			stamp = clock(t)
		}
		b.WriteString("  " + stamp + " " + rest + "\n")
	}
	return b.String()
}

var (
	redactURL    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)
	redactSecret = regexp.MustCompile(`(?i)("?(?:key|pass(?:word)?|user(?:name)?|ticket|zone|link)"?\s*[:=]\s*)(?:"[^"]*"|[^\s,}]+)`)
)

// redact hides what a link carries, as the Android app's Diagnostics.redact.
func redact(s string) string {
	s = redactURL.ReplaceAllString(s, "[link]")
	return redactSecret.ReplaceAllString(s, "${1}[redacted]")
}

func (l *appLog) copyStalls(events []tunnel.Event) {
	l.copyMu.Lock()
	defer l.copyMu.Unlock()
	for _, e := range events {
		if !strings.HasPrefix(e.Msg, "stalled:") && !strings.HasPrefix(e.Msg, "unblocked:") {
			continue
		}
		if !e.Time.After(l.copied) {
			continue
		}
		l.addAt(e.Time, e.Msg)
		l.copied = e.Time
	}
}
