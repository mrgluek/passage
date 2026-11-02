package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	links "github.com/mrgluek/passage-link/link"
)

// ProbeWSS checks an application exchange through a single WSS route.
func ProbeWSS(ctx context.Context, params links.WSSParams, target, protocol string) error {
	client, err := NewWSSClient(params)
	if err != nil {
		return err
	}
	defer client.Close()
	if protocol == "mtproto" {
		return probeMTProtoVia(ctx, client.DialContext, target)
	}
	if protocol != "tls" && protocol != "echo" {
		return errors.New("unknown probe protocol")
	}
	conn, err := client.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if protocol == "tls" {
		host, _, err := net.SplitHostPort(target)
		if err != nil {
			return err
		}
		secure := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		defer secure.Close()
		return secure.HandshakeContext(ctx)
	}
	data := make([]byte, 256*1024)
	if _, err = rand.Read(data); err != nil {
		return err
	}
	sent := make(chan error, 1)
	go func() {
		_, err := conn.Write(data)
		if err == nil {
			err = conn.(interface{ CloseWrite() error }).CloseWrite()
		}
		sent <- err
	}()
	received := make([]byte, len(data))
	_, err = io.ReadFull(conn, received)
	if err != nil {
		conn.Close()
		<-sent
		return err
	}
	if err = <-sent; err != nil {
		return err
	}
	if !bytes.Equal(data, received) {
		return errors.New("echo payload mismatch")
	}
	// The echo fixture must finish after write EOF.
	var tail [1]byte
	if n, err := conn.Read(tail[:]); n != 0 || err != io.EOF {
		return errors.New("echo did not finish after write EOF")
	}
	return nil
}

// ProbeWSSDownload fetches one HTTPS path through a single WSS route and
// returns the body bytes received, also when the transfer breaks off.
func ProbeWSSDownload(ctx context.Context, params links.WSSParams, target, path string) (int64, error) {
	client, err := NewWSSClient(params)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return 0, err
	}
	conn, err := client.DialContext(ctx, "tcp", target)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	secure := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	defer secure.Close()
	if _, err = fmt.Fprintf(secure, "GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: curl/8.7.1\r\nAccept: */*\r\nConnection: close\r\n\r\n", path, host); err != nil {
		return 0, err
	}
	response, err := http.ReadResponse(bufio.NewReader(secure), nil)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download: HTTP %d", response.StatusCode)
	}
	n, err := io.Copy(io.Discard, response.Body)
	if err == nil && response.ContentLength >= 0 && n != response.ContentLength {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
