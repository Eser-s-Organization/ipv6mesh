//go:build windows

package ipc

import (
	"context"
	"testing"
	"time"
)

func TestClientCallDeadlineSelection(t *testing.T) {
	client := NewClient(`\\.\pipe\ipv6mesh-deadline-test`)
	now := time.Unix(100, 0)

	tests := []struct {
		name    string
		command Command
		ctx     context.Context
		want    time.Time
	}{
		{name: "local budget", command: CommandStatus, ctx: context.Background(), want: now.Add(5 * time.Second)},
		{name: "network budget", command: CommandRoomMembers, ctx: context.Background(), want: now.Add(45 * time.Second)},
		{name: "caller deadline wins", command: CommandRoomMembers, ctx: deadlineContext(now.Add(2 * time.Second)), want: now.Add(2 * time.Second)},
		{name: "budget wins over later caller", command: CommandStatus, ctx: deadlineContext(now.Add(30 * time.Second)), want: now.Add(5 * time.Second)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := client.callDeadline(test.ctx, test.command, now)
			if !ok {
				t.Fatal("callDeadline reported no deadline")
			}
			if !got.Equal(test.want) {
				t.Fatalf("deadline = %s, want %s", got, test.want)
			}
		})
	}
}

func deadlineContext(deadline time.Time) context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	// The test only needs the deadline value; keep the context alive until the
	// test process exits rather than canceling it before callDeadline inspects it.
	_ = cancel
	return ctx
}
