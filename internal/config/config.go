package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath = "configs/config.yaml"
	minJWTSecretBytes = 32
	minBcryptCost     = 10
	maxBcryptCost     = 14
)

// Config is the fully validated runtime configuration. Secrets are populated
// from the environment only; config files cannot set them.
type Config struct {
	HTTP     HTTP     `yaml:"http"`
	Database Database `yaml:"database"`
	Auth     Auth     `yaml:"auth"`
}

type HTTP struct {
	Addr               string        `yaml:"addr"`
	ReadHeaderTimeout  time.Duration `yaml:"read_header_timeout"`
	ReadTimeout        time.Duration `yaml:"read_timeout"`
	WriteTimeout       time.Duration `yaml:"write_timeout"`
	IdleTimeout        time.Duration `yaml:"idle_timeout"`
	RequestTimeout     time.Duration `yaml:"request_timeout"`
	ShutdownTimeout    time.Duration `yaml:"shutdown_timeout"`
	ShutdownDrainDelay time.Duration `yaml:"shutdown_drain_delay"`
	MaxBodyBytes       int64         `yaml:"max_body_bytes"`
}

type Database struct {
	Host             string        `yaml:"host"`
	Port             int           `yaml:"port"`
	Name             string        `yaml:"name"`
	User             string        `yaml:"user"`
	SSLMode          string        `yaml:"ssl_mode"`
	MaxConns         int32         `yaml:"max_conns"`
	MinConns         int32         `yaml:"min_conns"`
	ConnectTimeout   time.Duration `yaml:"connect_timeout"`
	StatementTimeout time.Duration `yaml:"statement_timeout"`
	LockTimeout      time.Duration `yaml:"lock_timeout"`
	MaxConnLifetime  time.Duration `yaml:"max_conn_lifetime"`
	MaxConnIdleTime  time.Duration `yaml:"max_conn_idle_time"`
	RollbackTimeout  time.Duration `yaml:"rollback_timeout"`
	ReadyPingTimeout time.Duration `yaml:"ready_ping_timeout"`
	Password         Secret        `yaml:"-"`
}

type Auth struct {
	JWTIssuer       string        `yaml:"jwt_issuer"`
	AccessTokenTTL  time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl"`
	BcryptCost      int           `yaml:"bcrypt_cost"`
	JWTSecret       Secret        `yaml:"-"`
}

// Load reads CONFIG_PATH (or configs/config.yaml), applies environment
// overrides, and fails fast on invalid or unsafe settings.
func Load() (Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = defaultConfigPath
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	var cfg Config
	dec := yaml.NewDecoder(file)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}

	if err := applyEnvironment(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnvironment(cfg *Config) error {
	override(&cfg.HTTP.Addr, "HTTP_ADDR")
	override(&cfg.Database.Host, "DB_HOST")
	override(&cfg.Database.Name, "DB_NAME")
	override(&cfg.Database.User, "DB_USER")
	override(&cfg.Database.SSLMode, "DB_SSL_MODE")

	if raw := os.Getenv("DB_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("DB_PORT must be a number: %w", err)
		}
		cfg.Database.Port = port
	}

	if raw := os.Getenv("HTTP_SHUTDOWN_DRAIN_DELAY"); raw != "" {
		delay, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("HTTP_SHUTDOWN_DRAIN_DELAY must be a duration: %w", err)
		}
		cfg.HTTP.ShutdownDrainDelay = delay
	}

	cfg.Database.Password = Secret(os.Getenv("DB_PASSWORD"))
	cfg.Auth.JWTSecret = Secret(os.Getenv("JWT_SECRET"))
	return nil
}

func override(target *string, envKey string) {
	if value := os.Getenv(envKey); value != "" {
		*target = value
	}
}

func (c Config) Validate() error {
	if c.Database.Password.Expose() == "" {
		return errors.New("invalid config: DB_PASSWORD is required")
	}
	if len(c.Auth.JWTSecret.Expose()) < minJWTSecretBytes {
		return fmt.Errorf("invalid config: JWT_SECRET must be at least %d bytes", minJWTSecretBytes)
	}

	checks := []struct {
		name string
		ok   bool
	}{
		{"http.addr must not be empty", c.HTTP.Addr != ""},
		{"http.read_header_timeout must be positive", c.HTTP.ReadHeaderTimeout > 0},
		{"http.read_timeout must be positive", c.HTTP.ReadTimeout > 0},
		{"http.idle_timeout must be positive", c.HTTP.IdleTimeout > 0},
		{"http.max_body_bytes must be positive", c.HTTP.MaxBodyBytes > 0},
		{"http.shutdown_drain_delay must not be negative", c.HTTP.ShutdownDrainDelay >= 0},
		{"database.host must not be empty", c.Database.Host != ""},
		{"database.name must not be empty", c.Database.Name != ""},
		{"database.user must not be empty", c.Database.User != ""},
		{"database.port must be between 1 and 65535", c.Database.Port > 0 && c.Database.Port <= 65535},
		{"database.connect_timeout must be positive", c.Database.ConnectTimeout > 0},
		{"database.max_conns must be positive", c.Database.MaxConns > 0},
		{"database.min_conns must be between 0 and max_conns", c.Database.MinConns >= 0 && c.Database.MinConns <= c.Database.MaxConns},
		{"database.max_conn_lifetime must be positive", c.Database.MaxConnLifetime > 0},
		{"database.max_conn_idle_time must be positive", c.Database.MaxConnIdleTime > 0},
		{"database.rollback_timeout must be positive", c.Database.RollbackTimeout > 0},
		{"database.ready_ping_timeout must be positive", c.Database.ReadyPingTimeout > 0},
		{"database.ready_ping_timeout must be shorter than http.request_timeout", c.Database.ReadyPingTimeout < c.HTTP.RequestTimeout},
		{"auth.jwt_issuer must not be empty", c.Auth.JWTIssuer != ""},
		{"auth.access_token_ttl must be positive", c.Auth.AccessTokenTTL > 0},
		{"auth.refresh_token_ttl must be longer than access_token_ttl", c.Auth.RefreshTokenTTL > c.Auth.AccessTokenTTL},
		{"auth.bcrypt_cost must be between 10 and 14", c.Auth.BcryptCost >= minBcryptCost && c.Auth.BcryptCost <= maxBcryptCost},
		{"database.lock_timeout must be shorter than database.statement_timeout", c.Database.LockTimeout > 0 && c.Database.LockTimeout < c.Database.StatementTimeout},
		{"database.statement_timeout must be shorter than http.request_timeout", c.Database.StatementTimeout < c.HTTP.RequestTimeout},
		{"http.request_timeout must be shorter than http.write_timeout", c.HTTP.RequestTimeout < c.HTTP.WriteTimeout},
		{"http.write_timeout must be shorter than http.shutdown_timeout", c.HTTP.WriteTimeout < c.HTTP.ShutdownTimeout},
	}
	for _, check := range checks {
		if !check.ok {
			return fmt.Errorf("invalid config: %s", check.name)
		}
	}
	return nil
}
