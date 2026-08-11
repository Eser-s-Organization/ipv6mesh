//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Eser-s-Organization/ipv6mesh/internal/relay"
)

func main() {
	runRelayAgent()
}

func runRelayAgent() {
	configPath := flag.String("config", "/etc/ipv6mesh/relay.json", "path to the relay configuration")
	flag.Parse()
	if err := runRelayAgentWithPath(context.Background(), *configPath); err != nil {
		log.Fatal(err)
	}
}

func runRelayAgentWithPath(parent context.Context, configPath string) error {
	if parent == nil {
		return fmt.Errorf("relay parent context is required")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read relay configuration: %w", err)
	}
	config, err := relay.ParseConfig(data)
	if err != nil {
		return err
	}
	runtime, err := relay.NewRuntime(relay.NewWireGuardManager(), relay.NewRouteManager())
	if err != nil {
		return err
	}
	if err := runtime.Apply(parent, config); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if err := runtime.Clear(context.Background()); err != nil {
		return fmt.Errorf("clear relay runtime: %w", err)
	}
	return nil
}
