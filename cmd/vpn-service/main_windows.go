//go:build windows

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/identity"
	"github.com/Eser-s-Organization/ipv6mesh/internal/service"
	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
	"golang.org/x/sys/windows/svc"
)

func main() {
	if err := runWindowsService(); err != nil {
		log.Fatal(err)
	}
}

type localPipeAuthorizer struct{}

func (localPipeAuthorizer) Authorize(context.Context) error { return nil }

func runWindowsService() error {
	serve := func(ctx context.Context) error { return runService(ctx) }
	interactive, err := svc.IsAnInteractiveSession()
	if err != nil {
		return err
	}
	if interactive {
		return serve(context.Background())
	}
	return svc.Run(windowsServiceName, serviceRunner{serve: serve})
}

type serviceRunner struct {
	serve serviceServeFunc
}

func (runner serviceRunner) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	return executeService(requests, changes, runner.serve)
}

func runService(ctx context.Context) error {
	identityStore := identity.NewStore(filepath.Join(serviceDataDirectory(), "identity.json"))
	loaded, err := identityStore.LoadOrCreate()
	if err != nil {
		return err
	}
	var privateKey wgnt.Key
	if err := loaded.WithPrivateKey(func(value []byte) error {
		copy(privateKey[:], value)
		return nil
	}); err != nil {
		return err
	}
	controlURL := os.Getenv("IPV6MESH_CONTROL_URL")
	controlClient, err := control.NewClient(controlURL)
	if err != nil {
		return err
	}
	controlBridge := service.NewHTTPControlClient(controlClient, "", "windows", "0.1.0-dev")
	dataPlane, err := service.NewWindowsDataPlane(privateKey, "IPv6Mesh", 51820)
	if err != nil {
		return err
	}
	localService := service.New(localServiceOptions(identityStore, controlBridge, dataPlane, dataPlane.Applier, controlURL))
	if err := service.ServeWindows(ctx, localService, `\\.\pipe\ipv6mesh`, localPipeAuthorizer{}); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func localServiceOptions(identityStore service.IdentityStore, controlBridge service.ControlClient, adapter service.Adapter, reconciler service.SnapshotApplier, controlURL string) service.Options {
	return service.Options{
		Identity:   identityStore,
		Control:    controlBridge,
		ControlURL: controlURL,
		Adapter:    adapter,
		Reconciler: reconciler,
	}
}

func serviceDataDirectory() string {
	if value := strings.TrimSpace(os.Getenv("IPV6MESH_DATA_DIR")); value != "" {
		return filepath.Clean(value)
	}
	if value := strings.TrimSpace(os.Getenv("ProgramData")); value != "" {
		return filepath.Join(value, "IPv6Mesh")
	}
	return `C:\ProgramData\IPv6Mesh`
}
