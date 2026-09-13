package config

import (
	"testing"
	"time"
)

func TestDefaultsIncludeAllRuntimeFields(t *testing.T) {
	cfg := Defaults()

	assertStringDefault(t, "socket path", cfg.SocketPath, DefaultSocketPath)
	assertStringDefault(t, "log level", cfg.LogLevel, DefaultLogLevel)
	assertStringDefault(t, "OTel service name", cfg.OTelServiceName, DefaultOTelServiceName)
	assertStringDefault(t, "OTel endpoint", cfg.OTelEndpoint, DefaultOTelEndpoint)
	assertStringDefault(t, "containerd namespace", cfg.ContainerdNamespace, DefaultContainerdNamespace)
	assertStringDefault(t, "containerd socket", cfg.ContainerdSocket, DefaultContainerdSocket)
	assertStringDefault(t, "CNI config dir", cfg.CNIConfigDir, DefaultCNIConfigDir)
	assertStringDefault(t, "CNI plugin dir", cfg.CNIPluginDir, DefaultCNIPluginDir)
	assertStringDefault(t, "BuildKit socket", cfg.BuildKitSocket, DefaultBuildKitSocket)
	assertDurationDefault(t, "stop timeout", cfg.DefaultStopTimeout, DefaultStopTimeout)
	assertDurationDefault(t, "request timeout", cfg.RequestTimeout, DefaultRequestTimeout)
	assertBoolDefault(t, "enable CRI", cfg.EnableCRI, DefaultEnableCRI)
	assertBoolDefault(t, "enable rootless", cfg.EnableRootless, DefaultEnableRootless)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config validation failed: %v", err)
	}
}

func assertStringDefault(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func assertDurationDefault(t *testing.T, name string, got, want time.Duration) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %s, want %s", name, got, want)
	}
}

func assertBoolDefault(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %t, want %t", name, got, want)
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
