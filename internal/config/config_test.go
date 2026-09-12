package config

import (
	"errors"
	"testing"
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
			if err := test.cfg.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}
