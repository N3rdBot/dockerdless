package config

import (
	"testing"
	"time"
)

func TestDefaultsIncludeAllRuntimeFields(t *testing.T) {
	cfg := Defaults()

	if cfg.SocketPath != DefaultSocketPath {
		t.Fatalf("socket path = %q, want %q", cfg.SocketPath, DefaultSocketPath)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Fatalf("log level = %q, want %q", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.OTelServiceName != DefaultOTelServiceName {
		t.Fatalf("OTel service name = %q, want %q", cfg.OTelServiceName, DefaultOTelServiceName)
	}
	if cfg.OTelEndpoint != DefaultOTelEndpoint {
		t.Fatalf("OTel endpoint = %q, want %q", cfg.OTelEndpoint, DefaultOTelEndpoint)
	}
	if cfg.ContainerdNamespace != DefaultContainerdNamespace {
		t.Fatalf("containerd namespace = %q, want %q", cfg.ContainerdNamespace, DefaultContainerdNamespace)
	}
	if cfg.ContainerdSocket != DefaultContainerdSocket {
		t.Fatalf("containerd socket = %q, want %q", cfg.ContainerdSocket, DefaultContainerdSocket)
	}
	if cfg.CNIConfigDir != DefaultCNIConfigDir {
		t.Fatalf("CNI config dir = %q, want %q", cfg.CNIConfigDir, DefaultCNIConfigDir)
	}
	if cfg.CNIPluginDir != DefaultCNIPluginDir {
		t.Fatalf("CNI plugin dir = %q, want %q", cfg.CNIPluginDir, DefaultCNIPluginDir)
	}
	if cfg.BuildKitSocket != DefaultBuildKitSocket {
		t.Fatalf("BuildKit socket = %q, want %q", cfg.BuildKitSocket, DefaultBuildKitSocket)
	}
	if cfg.DefaultStopTimeout != DefaultStopTimeout {
		t.Fatalf("stop timeout = %s, want %s", cfg.DefaultStopTimeout, DefaultStopTimeout)
	}
	if cfg.RequestTimeout != DefaultRequestTimeout {
		t.Fatalf("request timeout = %s, want %s", cfg.RequestTimeout, DefaultRequestTimeout)
	}
	if cfg.EnableCRI != DefaultEnableCRI {
		t.Fatalf("enable CRI = %t, want %t", cfg.EnableCRI, DefaultEnableCRI)
	}
	if cfg.EnableRootless != DefaultEnableRootless {
		t.Fatalf("enable rootless = %t, want %t", cfg.EnableRootless, DefaultEnableRootless)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config validation failed: %v", err)
	}
}

func TestLoadReadsEnvironmentOverrides(t *testing.T) {
	t.Setenv("DOCKERDLESS_SOCKET_PATH", "/tmp/dockerdless-env.sock")
	t.Setenv("DOCKERDLESS_REQUEST_TIMEOUT", "7s")
	t.Setenv("DOCKERDLESS_ENABLE_ROOTLESS", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketPath != "/tmp/dockerdless-env.sock" {
		t.Fatalf("socket path = %q", cfg.SocketPath)
	}
	if cfg.RequestTimeout != 7*time.Second {
		t.Fatalf("request timeout = %s, want 7s", cfg.RequestTimeout)
	}
	if !cfg.EnableRootless {
		t.Fatal("enable rootless = false, want true")
	}
}

func TestConfigValidationRejectsRuntimeConfigurationViolations(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*Config)
	}{
		{name: "invalid log level", apply: func(cfg *Config) { cfg.LogLevel = "trace" }},
		{name: "empty OTel service name", apply: func(cfg *Config) { cfg.OTelServiceName = " " }},
		{name: "OTel endpoint whitespace", apply: func(cfg *Config) { cfg.OTelEndpoint = " http://otel:4317" }},
		{name: "empty containerd namespace", apply: func(cfg *Config) { cfg.ContainerdNamespace = "\t" }},
		{name: "relative containerd socket", apply: func(cfg *Config) { cfg.ContainerdSocket = "containerd.sock" }},
		{name: "relative CNI config directory", apply: func(cfg *Config) { cfg.CNIConfigDir = "cni" }},
		{name: "relative CNI plugin directory", apply: func(cfg *Config) { cfg.CNIPluginDir = "plugins" }},
		{name: "relative BuildKit socket", apply: func(cfg *Config) { cfg.BuildKitSocket = "buildkit.sock" }},
		{name: "zero stop timeout", apply: func(cfg *Config) { cfg.DefaultStopTimeout = 0 }},
		{name: "negative stop timeout", apply: func(cfg *Config) { cfg.DefaultStopTimeout = -time.Second }},
		{name: "zero request timeout", apply: func(cfg *Config) { cfg.RequestTimeout = 0 }},
		{name: "negative request timeout", apply: func(cfg *Config) { cfg.RequestTimeout = -time.Second }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Defaults()
			test.apply(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() returned nil for invalid configuration")
			}
		})
	}
}
