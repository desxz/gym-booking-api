package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validYAML = `
http:
  addr: ":8080"
  read_header_timeout: 3s
  read_timeout: 5s
  write_timeout: 10s
  idle_timeout: 60s
  request_timeout: 5s
  shutdown_timeout: 15s
  shutdown_drain_delay: 0s
  max_body_bytes: 1048576
database:
  host: "localhost"
  port: 5432
  name: "gym_booking"
  user: "gym_app"
  ssl_mode: "disable"
  max_conns: 15
  min_conns: 2
  connect_timeout: 5s
  statement_timeout: 3s
  lock_timeout: 2s
  max_conn_lifetime: 30m
  max_conn_idle_time: 5m
  rollback_timeout: 2s
  ready_ping_timeout: 2s
auth:
  jwt_issuer: "gym-booking-api"
  access_token_ttl: 15m
  refresh_token_ttl: 168h
  bcrypt_cost: 12
`

var configEnvKeys = []string{
	"CONFIG_PATH", "HTTP_ADDR", "HTTP_SHUTDOWN_DRAIN_DELAY", "DB_HOST", "DB_PORT",
	"DB_NAME", "DB_USER", "DB_SSL_MODE", "DB_PASSWORD", "JWT_SECRET",
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range configEnvKeys {
		t.Setenv(key, "")
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func validConfig() Config {
	return Config{
		HTTP: HTTP{
			Addr:               ":8080",
			ReadHeaderTimeout:  3 * time.Second,
			ReadTimeout:        5 * time.Second,
			WriteTimeout:       10 * time.Second,
			IdleTimeout:        60 * time.Second,
			RequestTimeout:     5 * time.Second,
			ShutdownTimeout:    15 * time.Second,
			ShutdownDrainDelay: 0,
			MaxBodyBytes:       1 << 20,
		},
		Database: Database{
			Host:             "localhost",
			Port:             5432,
			Name:             "gym_booking",
			User:             "gym_app",
			SSLMode:          "disable",
			MaxConns:         15,
			MinConns:         2,
			ConnectTimeout:   5 * time.Second,
			StatementTimeout: 3 * time.Second,
			LockTimeout:      2 * time.Second,
			MaxConnLifetime:  30 * time.Minute,
			MaxConnIdleTime:  5 * time.Minute,
			RollbackTimeout:  2 * time.Second,
			ReadyPingTimeout: 2 * time.Second,
			Password:         "db-password",
		},
		Auth: Auth{
			JWTIssuer:       "gym-booking-api",
			AccessTokenTTL:  15 * time.Minute,
			RefreshTokenTTL: 7 * 24 * time.Hour,
			BcryptCost:      12,
			JWTSecret:       "01234567890123456789012345678901",
		},
	}
}

func TestLoad(t *testing.T) {
	t.Run("loads a strict YAML file and environment secrets", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("CONFIG_PATH", writeConfig(t, validYAML))
		t.Setenv("DB_PASSWORD", "secret")
		t.Setenv("JWT_SECRET", "01234567890123456789012345678901")

		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, int64(1<<20), cfg.HTTP.MaxBodyBytes)
		assert.Equal(t, 12, cfg.Auth.BcryptCost)
		assert.Equal(t, "secret", cfg.Database.Password.Expose())
		assert.Equal(t, "01234567890123456789012345678901", cfg.Auth.JWTSecret.Expose())
	})

	t.Run("missing file", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("CONFIG_PATH", filepath.Join(t.TempDir(), "absent.yaml"))
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "open config")
	})

	t.Run("default path is used when CONFIG_PATH is unset", func(t *testing.T) {
		clearEnvironment(t)
		_, err := Load()
		require.Error(t, err)
	})

	t.Run("unknown YAML field fails fast", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("CONFIG_PATH", writeConfig(t, validYAML+"\n  surprise: true\n"))
		t.Setenv("DB_PASSWORD", "secret")
		t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode config")
	})

	t.Run("invalid DB_PORT", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("CONFIG_PATH", writeConfig(t, validYAML))
		t.Setenv("DB_PASSWORD", "secret")
		t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
		t.Setenv("DB_PORT", "not-a-port")
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DB_PORT")
	})

	t.Run("invalid drain delay", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("CONFIG_PATH", writeConfig(t, validYAML))
		t.Setenv("DB_PASSWORD", "secret")
		t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
		t.Setenv("HTTP_SHUTDOWN_DRAIN_DELAY", "soon")
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP_SHUTDOWN_DRAIN_DELAY")
	})

	t.Run("validation failure", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("CONFIG_PATH", writeConfig(t, validYAML))
		t.Setenv("DB_PASSWORD", "secret")
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JWT_SECRET")
	})

	t.Run("environment overrides", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("CONFIG_PATH", writeConfig(t, validYAML))
		t.Setenv("DB_PASSWORD", "secret")
		t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
		t.Setenv("HTTP_ADDR", ":9090")
		t.Setenv("HTTP_SHUTDOWN_DRAIN_DELAY", "7s")
		t.Setenv("DB_HOST", "db.internal")
		t.Setenv("DB_PORT", "6543")
		t.Setenv("DB_NAME", "other_db")
		t.Setenv("DB_USER", "other_user")
		t.Setenv("DB_SSL_MODE", "require")

		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, ":9090", cfg.HTTP.Addr)
		assert.Equal(t, 7*time.Second, cfg.HTTP.ShutdownDrainDelay)
		assert.Equal(t, "db.internal", cfg.Database.Host)
		assert.Equal(t, 6543, cfg.Database.Port)
		assert.Equal(t, "other_db", cfg.Database.Name)
		assert.Equal(t, "other_user", cfg.Database.User)
		assert.Equal(t, "require", cfg.Database.SSLMode)
	})
}

func TestValidate(t *testing.T) {
	require.NoError(t, validConfig().Validate())

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"missing DB password", func(c *Config) { c.Database.Password = "" }, "DB_PASSWORD"},
		{"short JWT secret", func(c *Config) { c.Auth.JWTSecret = "short" }, "JWT_SECRET"},
		{"empty addr", func(c *Config) { c.HTTP.Addr = "" }, "http.addr"},
		{"zero max body", func(c *Config) { c.HTTP.MaxBodyBytes = 0 }, "http.max_body_bytes"},
		{"negative drain delay", func(c *Config) { c.HTTP.ShutdownDrainDelay = -time.Second }, "http.shutdown_drain_delay"},
		{"zero read header timeout", func(c *Config) { c.HTTP.ReadHeaderTimeout = 0 }, "http.read_header_timeout"},
		{"zero read timeout", func(c *Config) { c.HTTP.ReadTimeout = 0 }, "http.read_timeout"},
		{"zero idle timeout", func(c *Config) { c.HTTP.IdleTimeout = 0 }, "http.idle_timeout"},
		{"empty host", func(c *Config) { c.Database.Host = "" }, "database.host"},
		{"empty name", func(c *Config) { c.Database.Name = "" }, "database.name"},
		{"empty user", func(c *Config) { c.Database.User = "" }, "database.user"},
		{"port zero", func(c *Config) { c.Database.Port = 0 }, "database.port"},
		{"port too high", func(c *Config) { c.Database.Port = 65536 }, "database.port"},
		{"zero connect timeout", func(c *Config) { c.Database.ConnectTimeout = 0 }, "database.connect_timeout"},
		{"zero max conns", func(c *Config) { c.Database.MaxConns = 0 }, "database.max_conns"},
		{"negative min conns", func(c *Config) { c.Database.MinConns = -1 }, "database.min_conns"},
		{"min above max", func(c *Config) { c.Database.MinConns = c.Database.MaxConns + 1 }, "database.min_conns"},
		{"zero max conn lifetime", func(c *Config) { c.Database.MaxConnLifetime = 0 }, "database.max_conn_lifetime"},
		{"zero max conn idle", func(c *Config) { c.Database.MaxConnIdleTime = 0 }, "database.max_conn_idle_time"},
		{"zero rollback timeout", func(c *Config) { c.Database.RollbackTimeout = 0 }, "database.rollback_timeout"},
		{"zero ready ping timeout", func(c *Config) { c.Database.ReadyPingTimeout = 0 }, "database.ready_ping_timeout"},
		{"ready ping too long", func(c *Config) { c.Database.ReadyPingTimeout = c.HTTP.RequestTimeout }, "database.ready_ping_timeout"},
		{"empty issuer", func(c *Config) { c.Auth.JWTIssuer = "" }, "auth.jwt_issuer"},
		{"zero access TTL", func(c *Config) { c.Auth.AccessTokenTTL = 0 }, "auth.access_token_ttl"},
		{"refresh not longer than access", func(c *Config) { c.Auth.RefreshTokenTTL = c.Auth.AccessTokenTTL }, "auth.refresh_token_ttl"},
		{"bcrypt cost too low", func(c *Config) { c.Auth.BcryptCost = 9 }, "auth.bcrypt_cost"},
		{"bcrypt cost too high", func(c *Config) { c.Auth.BcryptCost = 15 }, "auth.bcrypt_cost"},
		{"lock timeout not shorter", func(c *Config) { c.Database.LockTimeout = c.Database.StatementTimeout }, "lock_timeout"},
		{"statement timeout not shorter", func(c *Config) { c.Database.StatementTimeout = c.HTTP.RequestTimeout }, "statement_timeout"},
		{"request timeout not shorter", func(c *Config) { c.HTTP.RequestTimeout = c.HTTP.WriteTimeout }, "request_timeout"},
		{"write timeout not shorter", func(c *Config) { c.HTTP.WriteTimeout = c.HTTP.ShutdownTimeout }, "write_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestSecretRedaction(t *testing.T) {
	secret := Secret("super-secret")

	assert.Equal(t, redacted, secret.String())
	assert.Equal(t, redacted, secret.GoString())
	assert.Equal(t, "super-secret", secret.Expose())

	encoded, err := json.Marshal(secret)
	require.NoError(t, err)
	assert.Equal(t, `"`+redacted+`"`, string(encoded))
	assert.Equal(t, redacted, secret.LogValue().String())
}
