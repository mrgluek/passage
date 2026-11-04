//go:build darwin && cgo

package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type macInstance struct {
	lock      *os.File
	listener  net.Listener
	socketDir string
	open      chan struct{}
	done      chan struct{}
	once      sync.Once
}

// claimMacInstance locks the settings folder before anything can write to it.
// A second process asks the owner to show its window and returns nil.
func claimMacInstance(dir string) (*macInstance, error) {
	lock, err := os.OpenFile(filepath.Join(dir, "passage.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		defer lock.Close()
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		return nil, notifyMacInstance(lock)
	}

	m := &macInstance{lock: lock, open: make(chan struct{}, 1), done: make(chan struct{})}
	// Keep the socket path below Darwin's limit, even with a long settings path.
	m.socketDir, err = os.MkdirTemp("/tmp", "passage-")
	if err == nil {
		m.listener, err = net.Listen("unix", filepath.Join(m.socketDir, "open.sock"))
	}
	if err == nil {
		endpoint := []byte(m.listener.Addr().String())
		_, err = lock.WriteAt(endpoint, 0)
		if err == nil {
			err = lock.Truncate(int64(len(endpoint)))
		}
	}
	if err != nil {
		if m.listener != nil {
			m.listener.Close()
		}
		if m.socketDir != "" {
			os.RemoveAll(m.socketDir)
		}
		lock.Close()
		return nil, err
	}
	go m.listen()
	return m, nil
}

func notifyMacInstance(lock *os.File) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var endpoint [256]byte
		n, _ := lock.ReadAt(endpoint[:], 0)
		if path := strings.TrimSpace(string(endpoint[:n])); path != "" {
			conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
			if err == nil {
				conn.SetDeadline(time.Now().Add(time.Second))
				_, err = conn.Write([]byte{1})
				var reply [1]byte
				if err == nil {
					_, err = io.ReadFull(conn, reply[:])
				}
				conn.Close()
				if err == nil && reply[0] == 1 {
					return nil
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("could not contact the running Passage instance")
}

func (m *macInstance) listen() {
	defer close(m.done)
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		var request [1]byte
		if _, err := io.ReadFull(conn, request[:]); err == nil && request[0] == 1 {
			select {
			case m.open <- struct{}{}:
			default:
			}
			conn.Write([]byte{1})
		}
		conn.Close()
	}
}

func (m *macInstance) close() {
	m.once.Do(func() {
		m.listener.Close()
		<-m.done
		os.RemoveAll(m.socketDir)
		m.lock.Close()
	})
}
