package main

import (
	"encoding/binary"
	"syscall"
	"testing"
)

func TestRouteMessagesWithFourByteAlignment(t *testing.T) {
	size := syscall.SizeofRtMsghdr
	if size%8 == 0 {
		size += 4
	}
	message := make([]byte, size)
	binary.NativeEndian.PutUint16(message, uint16(size))
	message[2], message[3] = syscall.RTM_VERSION, syscall.RTM_GET
	rib := append(append([]byte(nil), message...), message...)
	messages, err := parseRouteMessages(rib)
	if err != nil || len(messages) != 2 {
		t.Fatalf("decoded %d messages: %v", len(messages), err)
	}
	if _, err := parseRouteMessages(rib[:len(rib)-1]); err == nil {
		t.Fatal("truncated message was accepted")
	}
}
