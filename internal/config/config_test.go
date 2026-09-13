package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultsLoadAndValidationRejectsEmptySocketPath(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg.SocketPath != DefaultSocketPath {
		t.Fatalf("default socket path = %q, want %q", cfg.SocketPath, DefaultSocketPath)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Fatalf("default log level = %q, want %q", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.OTelServiceName != DefaultOTelServiceName {
		t.Fatalf("default OTel service name = %q, want %q", cfg.OTelServiceName, DefaultOTelServiceName)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config validation failed: %v", err)
	}

	tests := []struct {
		name string
		cfg  Config
		want error
	}{
		{
			name: "empty socket path",
			cfg:  Config{SocketPath: "", LogLevel: DefaultLogLevel, OTelServiceName: DefaultOTelServiceName},
			want: ErrEmptySocketPath,
		},
		{
			name: "whitespace socket path",
			cfg:  Config{SocketPath: "   ", LogLevel: DefaultLogLevel, OTelServiceName: DefaultOTelServiceName},
			want: ErrEmptySocketPath,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.cfg.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStoreRetainsLastValidSnapshotAfterInvalidYAML(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	validConfig := []byte("socket-path: /tmp/dockerdless-valid.sock\nlog-level: debug\n")
	if err := os.WriteFile(configPath, validConfig, 0o600); err != nil {
		t.Fatalf("write valid config: %v", err)
	}

	store := NewStore()
	store.SetConfigFile(configPath)
	errCh := make(chan error, 1)
	store.SetErrorHandler(func(err error) {
		select {
		case errCh <- err:
		default:
		}
	})
	if err := store.Load(); err != nil {
		t.Fatalf("load valid config: %v", err)
	}
	validSnapshot := store.Current()
	if validSnapshot.SocketPath != "/tmp/dockerdless-valid.sock" {
		t.Fatalf("valid snapshot socket path = %q", validSnapshot.SocketPath)
	}

	if err := store.WatchConfig(); err != nil {
		t.Fatalf("start config watch: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close config watch: %v", err)
		}
	})

	if err := os.WriteFile(configPath, []byte("socket-path: [\n"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("invalid config reported a nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for invalid config error")
	}

	if got := store.Current(); got != validSnapshot {
		t.Fatalf("invalid config changed snapshot: got %#v, want %#v", got, validSnapshot)
	}
}
