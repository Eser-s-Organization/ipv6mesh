package main

import (
	"errors"
	"testing"
)

func TestLoadConfigFromReadsEnvironmentAndDefaultsListenAddress(t *testing.T) {
	environment := map[string]string{
		"CONTROL_BOOTSTRAP_TOKEN": "bootstrap-secret",
		"CONTROL_SESSION_TTL":     "15m",
		"CONTROL_INVITE_TTL":      "2h",
		"CONTROL_DB_DSN":          "postgres://control",
		"CONTROL_DB_ENDPOINT":     "db.internal:5432",
	}
	config, err := LoadConfigFrom(func(name string) string { return environment[name] })
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.ListenAddress != "127.0.0.1:8080" || config.BootstrapToken != "bootstrap-secret" || config.SessionTTL.String() != "15m0s" || config.InviteTTL.String() != "2h0m0s" {
		t.Fatalf("unexpected config: %+v", config)
	}
	if config.DatabaseDSN != "postgres://control" || config.DatabaseEndpoint != "db.internal:5432" || config.DBDSN != config.DatabaseDSN || config.DBEndpoint != config.DatabaseEndpoint {
		t.Fatalf("database settings not loaded: %+v", config)
	}
}

func TestLoadConfigRejectsMissingBootstrapAndInvalidTTLs(t *testing.T) {
	tests := []map[string]string{
		{"CONTROL_SESSION_TTL": "15m", "CONTROL_INVITE_TTL": "2h"},
		{"CONTROL_BOOTSTRAP_TOKEN": "secret", "CONTROL_SESSION_TTL": "0s", "CONTROL_INVITE_TTL": "2h"},
		{"CONTROL_BOOTSTRAP_TOKEN": "secret", "CONTROL_SESSION_TTL": "15m", "CONTROL_INVITE_TTL": "not-duration"},
	}
	for _, environment := range tests {
		if _, err := LoadConfigFrom(func(name string) string { return environment[name] }); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("environment %#v error = %v, want ErrInvalidConfig", environment, err)
		}
	}
}

func TestLoadConfigSelectsRepositoryMode(t *testing.T) {
	environment := map[string]string{
		"CONTROL_BOOTSTRAP_TOKEN": "bootstrap-secret",
		"CONTROL_SESSION_TTL":     "15m",
		"CONTROL_INVITE_TTL":      "2h",
	}
	config, err := LoadConfigFrom(func(name string) string { return environment[name] })
	if err != nil {
		t.Fatalf("load default repository mode: %v", err)
	}
	if config.RepositoryMode != "memory" {
		t.Fatalf("default repository mode = %q, want memory", config.RepositoryMode)
	}

	environment["CONTROL_DB_DSN"] = "postgres://control"
	config, err = LoadConfigFrom(func(name string) string { return environment[name] })
	if err != nil {
		t.Fatalf("load postgres repository mode: %v", err)
	}
	if config.RepositoryMode != "postgres" {
		t.Fatalf("DSN repository mode = %q, want postgres", config.RepositoryMode)
	}

	environment["CONTROL_REPOSITORY_MODE"] = "invalid"
	if _, err := LoadConfigFrom(func(name string) string { return environment[name] }); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid repository mode error = %v, want ErrInvalidConfig", err)
	}
}
