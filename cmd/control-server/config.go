package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidConfig = errors.New("invalid control-server configuration")

// Config contains process configuration only. Loading it never opens a
// database connection.
type Config struct {
	ListenAddress    string
	BootstrapToken   string
	SessionTTL       time.Duration
	InviteTTL        time.Duration
	DatabaseDSN      string
	DatabaseEndpoint string
	DBDSN            string
	DBEndpoint       string
	MaxBodyBytes     int64
}

type configError struct {
	Field  string
	Reason string
}

func (err *configError) Error() string {
	return fmt.Sprintf("%s: %s", err.Field, err.Reason)
}

func (err *configError) Unwrap() error { return ErrInvalidConfig }

const defaultListenAddress = "127.0.0.1:8080"
const defaultMaxBodyBytes int64 = 1 << 20

// LoadConfig loads configuration from the process environment.
func LoadConfig() (Config, error) { return LoadConfigFromEnv() }

// LoadConfigFromEnv loads configuration from os.Environ without connecting to
// or validating a live database.
func LoadConfigFromEnv() (Config, error) { return LoadConfigFrom(os.Getenv) }

// LoadConfigFrom is the injectable form used by tests and embedding callers.
func LoadConfigFrom(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, &configError{Field: "environment", Reason: "lookup function is required"}
	}
	config := Config{
		ListenAddress:    firstEnv(getenv, "CONTROL_LISTEN_ADDRESS", "CONTROL_SERVER_LISTEN_ADDRESS", "LISTEN_ADDRESS"),
		BootstrapToken:   firstEnv(getenv, "CONTROL_BOOTSTRAP_TOKEN", "CONTROL_SERVER_BOOTSTRAP_TOKEN", "BOOTSTRAP_TOKEN"),
		DatabaseDSN:      firstEnv(getenv, "CONTROL_DB_DSN", "CONTROL_SERVER_DB_DSN", "DB_DSN", "DATABASE_URL"),
		DatabaseEndpoint: firstEnv(getenv, "CONTROL_DB_ENDPOINT", "CONTROL_SERVER_DB_ENDPOINT", "DB_ENDPOINT"),
		MaxBodyBytes:     defaultMaxBodyBytes,
	}
	config.DBDSN = config.DatabaseDSN
	config.DBEndpoint = config.DatabaseEndpoint
	if config.ListenAddress == "" {
		config.ListenAddress = defaultListenAddress
	}
	if strings.TrimSpace(config.BootstrapToken) == "" {
		return Config{}, &configError{Field: "bootstrap_token", Reason: "is required"}
	}
	var err error
	config.SessionTTL, err = parsePositiveDuration(getenv, "session_ttl", "CONTROL_SESSION_TTL", "SESSION_TTL")
	if err != nil {
		return Config{}, err
	}
	config.InviteTTL, err = parsePositiveDuration(getenv, "invite_ttl", "CONTROL_INVITE_TTL", "INVITE_TTL")
	if err != nil {
		return Config{}, err
	}
	if raw := firstEnv(getenv, "CONTROL_MAX_BODY_BYTES", "MAX_BODY_BYTES"); raw != "" {
		value, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || value <= 0 {
			return Config{}, &configError{Field: "max_body_bytes", Reason: "must be a positive integer"}
		}
		config.MaxBodyBytes = value
	}
	return config, nil
}

func firstEnv(getenv func(string) string, names ...string) string {
	for _, name := range names {
		if value := getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func parsePositiveDuration(getenv func(string) string, field string, names ...string) (time.Duration, error) {
	raw := firstEnv(getenv, names...)
	if raw == "" {
		return 0, &configError{Field: field, Reason: "is required"}
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, &configError{Field: field, Reason: "must be a positive duration"}
	}
	return duration, nil
}
