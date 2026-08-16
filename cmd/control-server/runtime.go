package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/db"
	_ "github.com/lib/pq"
)

var ErrRepositoryUnavailable = errors.New("control-server repository is unavailable")

func openRepository(config Config) (control.TransactionalRepository, func() error, error) {
	switch config.RepositoryMode {
	case "", "memory":
		repository := db.NewMemoryRepository()
		return repository, func() error { return nil }, nil
	case "postgres":
		if config.DatabaseDSN == "" {
			return nil, nil, errors.Join(ErrRepositoryUnavailable, errors.New("postgres repository requires CONTROL_DB_DSN"))
		}
		database, err := sql.Open("postgres", config.DatabaseDSN)
		if err != nil {
			return nil, nil, errors.Join(ErrRepositoryUnavailable, err)
		}
		pingContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := database.PingContext(pingContext); err != nil {
			_ = database.Close()
			return nil, nil, errors.Join(ErrRepositoryUnavailable, err)
		}
		return db.NewPostgresRepository(database), database.Close, nil
	default:
		return nil, nil, errors.Join(ErrRepositoryUnavailable, errors.New("unknown repository mode"))
	}
}

func newHTTPServer(config Config, repository control.TransactionalRepository) *http.Server {
	if repository == nil {
		return nil
	}
	handler := control.NewHandler(repository, control.HandlerOptions{
		BootstrapToken: config.BootstrapToken,
		SessionTTL:     config.SessionTTL,
		InviteTTL:      config.InviteTTL,
		MaxBodyBytes:   config.MaxBodyBytes,
		RoomMode:       config.RoomMode,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.Handle("/", handler)
	return &http.Server{
		Addr:              config.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}
