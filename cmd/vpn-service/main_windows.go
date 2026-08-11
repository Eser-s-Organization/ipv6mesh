//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/Eser-s-Organization/ipv6mesh/internal/identity"
	"github.com/Eser-s-Organization/ipv6mesh/internal/service"
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
	localService := service.New(service.Options{Identity: identityStore})
	if err := service.ServeWindows(context.Background(), localService, `\\.\pipe\ipv6mesh`, localPipeAuthorizer{}); err != nil {
		log.Fatal(err)
	}
}
