package flowtui

import (
	"context"
	"testing"
	"time"
)

func TestFlowAfterFirstViewWaitsForInitialFrame(t *testing.T) {
	called := make(chan struct{})
	model := NewPicker(Actions{AfterFirstView: func(context.Context) { close(called) }})
	command := model.runAfterFirstView()
	done := make(chan struct{})
	go func() {
		command()
		close(done)
	}()

	select {
	case <-called:
		t.Fatal("AfterFirstView ran before View computed a frame")
	case <-time.After(50 * time.Millisecond):
	}
	view := model.View()
	if view == "" {
		t.Fatal("initial picker frame is empty")
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("AfterFirstView did not run after the first View")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("AfterFirstView command did not return")
	}
}
