// Package config defines daemon configuration and its validation boundary.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	// DefaultSocketPath is the default Docker-compatible Unix socket path.
	DefaultSocketPath = "/var/run/dockerdless.sock"
	// DefaultLogLevel is the default structured logging level.
	DefaultLogLevel = "info"
	// DefaultOTelServiceName is the default OpenTelemetry service name.
	DefaultOTelServiceName = "dockerdless"
	// DefaultOTelEndpoint disables an OpenTelemetry exporter by default.
	DefaultOTelEndpoint = ""
	// DefaultContainerdNamespace is the namespace used for daemon containers.
	DefaultContainerdNamespace = "moby"
	// DefaultContainerdSocket is the rootful containerd Unix socket path.
	DefaultContainerdSocket = "/run/containerd/containerd.sock"
	// DefaultCNIConfigDir is the system CNI network configuration directory.
	DefaultCNIConfigDir = "/etc/cni/net.d"
	// DefaultCNIPluginDir is the system CNI plugin directory.
	DefaultCNIPluginDir = "/opt/cni/bin"
	// DefaultBuildKitSocket is the rootful BuildKit Unix socket path.
	DefaultBuildKitSocket = "/run/buildkit/buildkitd.sock"
	// DefaultStopTimeout is the timeout used when stopping a container.
	DefaultStopTimeout = 10 * time.Second
	// DefaultRequestTimeout bounds an individual daemon request.
	DefaultRequestTimeout = 30 * time.Second
	// DefaultEnableCRI keeps the optional CRI adapter disabled.
	DefaultEnableCRI = false
	// DefaultEnableRootless keeps the Linux rootful runtime as the default.
	DefaultEnableRootless = false
)

// ErrEmptySocketPath indicates that the daemon cannot bind without a socket.
var ErrEmptySocketPath = errors.New("socket path must not be empty")

// Config contains daemon bootstrap settings.
type Config struct {
	SocketPath          string        `mapstructure:"socket-path"`
	LogLevel            string        `mapstructure:"log-level"`
	OTelServiceName     string        `mapstructure:"otel-service-name"`
	OTelEndpoint        string        `mapstructure:"otel-endpoint"`
	ContainerdNamespace string        `mapstructure:"containerd-namespace"`
	ContainerdSocket    string        `mapstructure:"containerd-socket"`
	CNIConfigDir        string        `mapstructure:"cni-config-dir"`
	CNIPluginDir        string        `mapstructure:"cni-plugin-dir"`
	BuildKitSocket      string        `mapstructure:"buildkit-socket"`
	DefaultStopTimeout  time.Duration `mapstructure:"default-stop-timeout"`
	RequestTimeout      time.Duration `mapstructure:"request-timeout"`
	EnableCRI           bool          `mapstructure:"enable-cri"`
	EnableRootless      bool          `mapstructure:"enable-rootless"`
}

// Defaults returns the safe baseline daemon configuration.
func Defaults() Config {
	return Config{
		SocketPath:          DefaultSocketPath,
		LogLevel:            DefaultLogLevel,
		OTelServiceName:     DefaultOTelServiceName,
		OTelEndpoint:        DefaultOTelEndpoint,
		ContainerdNamespace: DefaultContainerdNamespace,
		ContainerdSocket:    DefaultContainerdSocket,
		CNIConfigDir:        DefaultCNIConfigDir,
		CNIPluginDir:        DefaultCNIPluginDir,
		BuildKitSocket:      DefaultBuildKitSocket,
		DefaultStopTimeout:  DefaultStopTimeout,
		RequestTimeout:      DefaultRequestTimeout,
		EnableCRI:           DefaultEnableCRI,
		EnableRootless:      DefaultEnableRootless,
	}
}

// Load reads environment-backed configuration over the default values.
func Load() (Config, error) {
	v, err := newViper()
	if err != nil {
		return Config{}, err
	}
	return decodeConfig(v)
}

// Validate checks configuration invariants required by daemon startup.
func (c Config) Validate() error {
	if strings.TrimSpace(c.SocketPath) == "" {
		return ErrEmptySocketPath
	}
	if err := validateAbsolutePath("socket path", c.SocketPath); err != nil {
		return err
	}

	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug", "info", "warn", "error", "dpanic", "panic", "fatal":
	default:
		return errors.New("log level must be one of debug, info, warn, error, dpanic, panic, or fatal")
	}
	if strings.TrimSpace(c.OTelServiceName) == "" {
		return errors.New("OpenTelemetry service name must not be empty")
	}
	if strings.TrimSpace(c.OTelEndpoint) != c.OTelEndpoint {
		return errors.New("OpenTelemetry endpoint must not have leading or trailing whitespace")
	}
	if err := validateNonEmpty("containerd namespace", c.ContainerdNamespace); err != nil {
		return err
	}
	if err := validateAbsolutePath("containerd socket", c.ContainerdSocket); err != nil {
		return err
	}
	if err := validateAbsolutePath("CNI config directory", c.CNIConfigDir); err != nil {
		return err
	}
	if err := validateAbsolutePath("CNI plugin directory", c.CNIPluginDir); err != nil {
		return err
	}
	if err := validateAbsolutePath("BuildKit socket", c.BuildKitSocket); err != nil {
		return err
	}
	if c.DefaultStopTimeout <= 0 {
		return errors.New("default stop timeout must be greater than zero")
	}
	if c.RequestTimeout <= 0 {
		return errors.New("request timeout must be greater than zero")
	}
	return nil
}

func newViper() (*viper.Viper, error) {
	v := viper.New()
	v.SetEnvPrefix("DOCKERDLESS")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	defaults := Defaults()
	defaultValues := map[string]any{
		"socket-path":          defaults.SocketPath,
		"log-level":            defaults.LogLevel,
		"otel-service-name":    defaults.OTelServiceName,
		"otel-endpoint":        defaults.OTelEndpoint,
		"containerd-namespace": defaults.ContainerdNamespace,
		"containerd-socket":    defaults.ContainerdSocket,
		"cni-config-dir":       defaults.CNIConfigDir,
		"cni-plugin-dir":       defaults.CNIPluginDir,
		"buildkit-socket":      defaults.BuildKitSocket,
		"default-stop-timeout": defaults.DefaultStopTimeout,
		"request-timeout":      defaults.RequestTimeout,
		"enable-cri":           defaults.EnableCRI,
		"enable-rootless":      defaults.EnableRootless,
	}
	for key, value := range defaultValues {
		v.SetDefault(key, value)
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("bind environment variable for %s: %w", key, err)
		}
	}
	return v, nil
}

func decodeConfig(v *viper.Viper) (Config, error) {
	cfg := Defaults()
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}
	return cfg, nil
}

func validateNonEmpty(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return nil
}

func validateAbsolutePath(name, value string) error {
	if err := validateNonEmpty(name, value); err != nil {
		return err
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s must be absolute: %q", name, value)
	}
	return nil
}
