//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/identity"
	"github.com/Eser-s-Organization/ipv6mesh/internal/service"
	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

func main() {
	runService()
}

type localPipeAuthorizer struct{}

func (localPipeAuthorizer) Authorize(context.Context) error { return nil }

func runService() {
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		root = `C:\ProgramData`
	}
	identityStore := identity.NewStore(filepath.Join(root, "IPv6Mesh", "identity.json"))
	loaded, err := identityStore.LoadOrCreate()
	if err != nil {
		log.Fatal(err)
	}
	var privateKey wgnt.Key
	if err := loaded.WithPrivateKey(func(value []byte) error {
		copy(privateKey[:], value)
		return nil
	}); err != nil {
		log.Fatal(err)
	}
	controlURL := os.Getenv("IPV6MESH_CONTROL_URL")
	controlClient, err := control.NewClient(controlURL)
	if err != nil {
		log.Fatal(err)
	}
	controlBridge := service.NewHTTPControlClient(controlClient, "", "windows", "0.1.0-dev")
	dataPlane, err := service.NewWindowsDataPlane(privateKey, "IPv6Mesh", 51820)
	if err != nil {
		log.Fatal(err)
	}
	localService := service.New(service.Options{Identity: identityStore, Control: controlBridge, Adapter: dataPlane, Reconciler: dataPlane.Applier})
	if err := service.ServeWindows(context.Background(), localService, `\\.\pipe\ipv6mesh`, localPipeAuthorizer{}); err != nil {
		log.Fatal(err)
	}
}
