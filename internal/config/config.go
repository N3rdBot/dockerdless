// Package config defines daemon configuration and its validation boundary.
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

const (
	// DefaultSocketPath is the default Docker-compatible Unix socket path.
	DefaultSocketPath = "/var/run/dockerdless.sock"
	// DefaultLogLevel is the default structured logging level.
	DefaultLogLevel = "info"
	// DefaultOTelServiceName is the default OpenTelemetry service name.
	DefaultOTelServiceName = "dockerdless"
)

// ErrEmptySocketPath indicates that the daemon cannot bind without a socket.
var ErrEmptySocketPath = errors.New("socket path must not be empty")

// Config contains daemon bootstrap settings.
type Config struct {
	SocketPath      string `mapstructure:"socket-path"`
	LogLevel        string `mapstructure:"log-level"`
	OTelServiceName string `mapstructure:"otel-service-name"`
	OTelEndpoint    string `mapstructure:"otel-endpoint"`
}

// Defaults returns the safe baseline daemon configuration.
func Defaults() Config {
	return Config{
		SocketPath:      DefaultSocketPath,
		LogLevel:        DefaultLogLevel,
		OTelServiceName: DefaultOTelServiceName,
	}
}

// Load reads environment-backed configuration over the default values.
func Load() (Config, error) {
	cfg := Defaults()
	v := viper.New()
	v.SetEnvPrefix("DOCKERDLESS")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()
	v.SetDefault("socket-path", cfg.SocketPath)
	v.SetDefault("log-level", cfg.LogLevel)
	v.SetDefault("otel-service-name", cfg.OTelServiceName)
	v.SetDefault("otel-endpoint", cfg.OTelEndpoint)

	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration invariants required by daemon startup.
func (c Config) Validate() error {
	if strings.TrimSpace(c.SocketPath) == "" {
		return ErrEmptySocketPath
	}
	if strings.TrimSpace(c.LogLevel) == "" {
		return errors.New("log level must not be empty")
	}
	if strings.TrimSpace(c.OTelServiceName) == "" {
		return errors.New("OpenTelemetry service name must not be empty")
	}
	return nil
}
