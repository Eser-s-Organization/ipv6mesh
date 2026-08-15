package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(parent context.Context) error {
	config, err := LoadConfig()
	if err != nil {
		return err
	}
	repository, closeRepository, err := openRepository(config)
	if err != nil {
		return err
	}
	defer closeRepository()
	server := newHTTPServer(config, repository)
	if server == nil {
		return ErrRepositoryUnavailable
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveError := make(chan error, 1)
	go func() {
		log.Printf("control-server listening on %s using %s repository", config.ListenAddress, config.RepositoryMode)
		serveError <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownError := server.Shutdown(context.Background())
		if shutdownError != nil {
			return shutdownError
		}
		return nil
	case err := <-serveError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
