package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mrgluek/passage/core/internal/reporttext"
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

	mu        sync.Mutex
	lines     []string
	copied    map[historyKey]struct{}
	copyOrder []historyKey
	copyMu    sync.Mutex
}

func openLog(path, version string) *appLog {
	l := &appLog{path: path, version: version}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			fields := strings.SplitN(line, " ", 4)
			if len(fields) == 4 {
				fields[3] = neutralHistoryMessage(fields[3])
				line = strings.Join(fields, " ")
			}
			l.keep(line)
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

func redact(s string) string {
	return reporttext.Redact(s)
}

type historyKey struct {
	time    time.Time
	message string
}

func neutralHistoryMessage(message string) string {
	if rest, ok := strings.CutPrefix(message, "unblocked:"); ok {
		return "resumed:" + rest
	}
	if strings.HasPrefix(message, "stalled:") {
		return strings.Replace(message, "), blocked for ", "), paused for ", 1)
	}
	return message
}

func (l *appLog) copyStalls(events []tunnel.Event) {
	l.copyMu.Lock()
	defer l.copyMu.Unlock()
	for _, e := range events {
		message := neutralHistoryMessage(e.Msg)
		if !strings.HasPrefix(message, "stalled:") && !strings.HasPrefix(message, "resumed:") && !strings.HasPrefix(message, "disagree:") {
			continue
		}
		if e.Time.IsZero() {
			continue
		}
		key := historyKey{time: e.Time.UTC(), message: e.Msg}
		if _, ok := l.copied[key]; ok {
			continue
		}
		l.addAt(e.Time, message)
		if l.copied == nil {
			l.copied = make(map[historyKey]struct{})
		}
		l.copied[key] = struct{}{}
		l.copyOrder = append(l.copyOrder, key)
		if len(l.copyOrder) > logKeep {
			delete(l.copied, l.copyOrder[0])
			l.copyOrder = l.copyOrder[1:]
		}
	}
}
