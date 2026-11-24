package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func registerPassageScheme() error {
	exe, err := programPath()
	if err != nil {
		return err
	}
	launch := stableProgramPath(exe)
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
	return command.SetStringValue("", fmt.Sprintf(`"%s" "%%1"`, launch))
}

func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func sendLaunchLink(text string) error {
	sid, err := currentUserSID()
	if err != nil || !validSID(sid.String()) {
		return fmt.Errorf("import pipe: no user identity")
	}
	name, err := windows.UTF16PtrFromString(linkPipePath(sid.String()))
	if err != nil {
		return err
	}
	var h windows.Handle
	for range 40 {
		// Anonymous impersonation: the server cannot act as this user.
		h, err = windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.SECURITY_SQOS_PRESENT|windows.SECURITY_ANONYMOUS, 0)
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
	// Never hand the link to a pipe another account created.
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, sid) {
		return errors.New("import pipe belongs to another account")
	}
	data := make([]byte, 4+len(text))
	binary.LittleEndian.PutUint32(data, uint32(len(text)))
	copy(data[4:], text)
	_, err = f.Write(data)
	return err
}

// createLinkPipe makes the import pipe: the first instance of the name,
// owned by and open to the current user only.
func createLinkPipe() (windows.Handle, error) {
	sid, err := currentUserSID()
	if err != nil || !validSID(sid.String()) {
		return 0, fmt.Errorf("no user identity: %v", err)
	}
	sd, err := windows.SecurityDescriptorFromString(linkPipeSDDL(sid.String()))
	if err != nil {
		return 0, err
	}
	attrs := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(linkPipePath(sid.String()))
	if err != nil {
		return 0, err
	}
	return windows.CreateNamedPipe(name, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 0, maxLaunchLink+4, 0, attrs)
}

func receiveLaunchLinks(ctx context.Context, receive func(string), logf func(string, ...any)) {
	for ctx.Err() == nil {
		h, err := createLinkPipe()
		if err != nil {
			if ctx.Err() == nil {
				logf("import pipe not created, links from a second start are lost: %v", err)
			}
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
