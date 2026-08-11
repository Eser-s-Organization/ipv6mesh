package relay

import (
	"context"
	"errors"
	"os/exec"
	"sync"
)

var ErrUnsupportedPlatform = errors.New("Linux relay data plane is unsupported on this platform")

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type WireGuardManager struct {
	runner  CommandRunner
	tempDir string
	mu      sync.Mutex
}

func NewWireGuardManagerWithRunner(runner CommandRunner, tempDir string) *WireGuardManager {
	return &WireGuardManager{runner: runner, tempDir: tempDir}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
