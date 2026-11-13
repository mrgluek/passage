package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func registerPassageScheme() error {
	exe, err := programPath()
	if err != nil {
		return err
	}
	for dir := filepath.Dir(exe); ; dir = filepath.Dir(dir) {
		if strings.HasPrefix(strings.ToLower(filepath.Base(dir)), ".passage-update-") {
			return nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if insideArchive(filepath.Dir(exe), os.TempDir()) {
		return nil
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\passage`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err = k.SetStringValue("", "URL:Passage"); err != nil {
		return err
	}
	if err = k.SetStringValue("URL Protocol", ""); err != nil {
		return err
	}
	command, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\passage\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer command.Close()
	return command.SetStringValue("", fmt.Sprintf(`"%s" "%%1"`, exe))
}

func linkPipeName() *uint16 {
	name := `\\.\pipe\PassageImport-`
	if token, err := windows.OpenCurrentProcessToken(); err == nil {
		defer token.Close()
		if user, err := token.GetTokenUser(); err == nil {
			name += user.User.Sid.String()
		}
	}
	p, _ := windows.UTF16PtrFromString(name)
	return p
}

func sendLaunchLink(text string) error {
	var h windows.Handle
	var err error
	for range 40 {
		h, err = windows.CreateFile(linkPipeName(), windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), "Passage import")
	defer f.Close()
	data := make([]byte, 4+len(text))
	binary.LittleEndian.PutUint32(data, uint32(len(text)))
	copy(data[4:], text)
	_, err = f.Write(data)
	return err
}

func receiveLaunchLinks(ctx context.Context, receive func(string)) {
	for ctx.Err() == nil {
		h, err := windows.CreateNamedPipe(linkPipeName(), windows.PIPE_ACCESS_INBOUND, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 0, maxLaunchLink+4, 0, nil)
		if err != nil {
			return
		}
		f := os.NewFile(uintptr(h), "Passage import")
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				windows.CancelIoEx(h, nil)
				f.Close()
			case <-done:
			}
		}()
		err = windows.ConnectNamedPipe(h, nil)
		if err == nil || err == windows.ERROR_PIPE_CONNECTED {
			var size [4]byte
			if _, err = io.ReadFull(f, size[:]); err == nil {
				n := binary.LittleEndian.Uint32(size[:])
				if n <= maxLaunchLink {
					data := make([]byte, n)
					if _, err = io.ReadFull(f, data); err == nil {
						receive(launchImport([]string{string(data)}))
					}
				}
			}
		}
		close(done)
		f.Close()
	}
}
