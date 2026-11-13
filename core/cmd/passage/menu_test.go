package main

import (
	"testing"
	"time"
)

func TestMenuListenerStopsWhenReplaced(t *testing.T) {
	done := make(chan struct{})
	clicks := make(chan struct{})
	handled := make(chan struct{}, 1)
	stopped := make(chan struct{})
	go func() { defer close(stopped); listenMenu(done, clicks, func() { handled <- struct{}{} }) }()
	clicks <- struct{}{}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("click was not handled")
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("replaced menu listener survived")
	}
}
