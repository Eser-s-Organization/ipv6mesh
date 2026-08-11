//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestExecuteServiceStopsServeOnStopRequest(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	serve := func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	requests := make(chan svc.ChangeRequest, 1)
	changes := make(chan svc.Status, 4)
	result := make(chan struct {
		accepted bool
		code     uint32
	}, 1)
	go func() {
		accepted, code := executeService(requests, changes, serve)
		result <- struct {
			accepted bool
			code     uint32
		}{accepted: accepted, code: code}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("service runner did not start")
	}
	if status := <-changes; status.State != svc.StartPending {
		t.Fatalf("first status = %v, want StartPending", status.State)
	}
	if status := <-changes; status.State != svc.Running {
		t.Fatalf("second status = %v, want Running", status.State)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("service runner did not stop after Stop request")
	}
	select {
	case value := <-result:
		if value.accepted || value.code != 0 {
			t.Fatalf("service result = %+v, want clean stop", value)
		}
	case <-time.After(time.Second):
		t.Fatal("service runner did not return")
	}
}

func TestExecuteServiceRejectsNilServeFunction(t *testing.T) {
	requests := make(chan svc.ChangeRequest)
	changes := make(chan svc.Status, 1)
	accepted, code := executeService(requests, changes, nil)
	if accepted || code == 0 {
		t.Fatalf("nil serve result = (%t, %d), want failure", accepted, code)
	}
}
