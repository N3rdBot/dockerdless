package buildkit

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/moby/buildkit/session/auth"
	"github.com/moby/moby/api/pkg/authconfig"
	registrytypes "github.com/moby/moby/api/types/registry"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestParseAuthHeaderDecodesBase64URLJSON(t *testing.T) {
	config := registrytypes.AuthConfig{
		Username:      "octocat",
		Password:      "s3cr3t",
		ServerAddress: "https://index.docker.io/v1/",
	}
	encoded, err := authconfig.Encode(config)
	if err != nil {
		t.Fatalf("encode auth config: %v", err)
	}

	auth, err := ParseAuthHeader(encoded)
	if err != nil {
		t.Fatalf("ParseAuthHeader() error = %v", err)
	}
	if auth == nil {
		t.Fatal("ParseAuthHeader() = nil, want credentials")
	}
	if auth.Username != "octocat" || auth.Password != "s3cr3t" {
		t.Fatalf("credentials = %q/%q, want octocat/s3cr3t", auth.Username, auth.Password)
	}
	if auth.ServerAddress != "https://index.docker.io/v1/" {
		t.Fatalf("ServerAddress = %q", auth.ServerAddress)
	}
}

func TestParseAuthHeaderIdentityToken(t *testing.T) {
	payload := `{"identitytoken":"refresh-token-value","serveraddress":"registry.example.com"}`
	encoded := base64.URLEncoding.EncodeToString([]byte(payload))

	auth, err := ParseAuthHeader(encoded)
	if err != nil {
		t.Fatalf("ParseAuthHeader() error = %v", err)
	}
	if auth.IdentityToken != "refresh-token-value" {
		t.Fatalf("IdentityToken = %q", auth.IdentityToken)
	}
}

func TestParseAuthHeaderEmptyIsNil(t *testing.T) {
	for _, header := range []string{"", "   "} {
		auth, err := ParseAuthHeader(header)
		if err != nil {
			t.Fatalf("ParseAuthHeader(%q) error = %v", header, err)
		}
		if auth != nil {
			t.Fatalf("ParseAuthHeader(%q) = %+v, want nil", header, auth)
		}
	}
}

func TestParseAuthHeaderInvalidBase64Fails(t *testing.T) {
	_, err := ParseAuthHeader("!!!not-base64!!!")
	if !errors.Is(err, ErrInvalidAuth) {
		t.Fatalf("ParseAuthHeader() error = %v, want ErrInvalidAuth", err)
	}
}

func TestFromAuthConfigLegacyAuthField(t *testing.T) {
	credential := base64.StdEncoding.EncodeToString([]byte("legacy:password"))
	auth := FromAuthConfig(&registrytypes.AuthConfig{Auth: credential})
	if auth == nil {
		t.Fatal("FromAuthConfig() = nil, want credentials")
	}
	if auth.Username != "legacy" || auth.Password != "password" {
		t.Fatalf("credentials = %q/%q, want legacy/password", auth.Username, auth.Password)
	}
}

func TestRegistryAuthRedactsSecretsInFormatting(t *testing.T) {
	auth := RegistryAuth{
		Username:      "user",
		Password:      "hunter2-password",
		IdentityToken: "identity-token-value",
		RegistryToken: "registry-token-value",
		ServerAddress: "registry.example.com",
	}
	rendered := []string{
		fmt.Sprintf("%v", auth),
		fmt.Sprintf("%s", auth),
		fmt.Sprintf("%#v", auth),
		fmt.Sprint(&auth),
		fmt.Sprintf("%+v", &auth),
	}
	for _, text := range rendered {
		for _, secret := range []string{"hunter2-password", "identity-token-value", "registry-token-value"} {
			if strings.Contains(text, secret) {
				t.Fatalf("rendered auth %q leaks secret %q", text, secret)
			}
		}
	}
}

func TestRegistryAuthMarshalLogObjectRedactsSecrets(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)

	auth := RegistryAuth{
		Username:      "user",
		Password:      "hunter2-password",
		IdentityToken: "identity-token-value",
		RegistryToken: "registry-token-value",
	}
	logger.Info("auth test", zap.Any("auth", auth), zap.Any("auth_ptr", &auth))

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("observed %d log entries, want 1", len(entries))
	}
	raw, err := json.Marshal(entries[0].ContextMap())
	if err != nil {
		t.Fatalf("marshal log context: %v", err)
	}
	for _, secret := range []string{"hunter2-password", "identity-token-value", "registry-token-value"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("log context %s leaks secret %q", raw, secret)
		}
	}
	if !strings.Contains(string(raw), "registry.example.com") && !strings.Contains(string(raw), "server_address") {
		t.Fatalf("log context %s lost the non-secret server address", raw)
	}
}

func TestRegistryAuthMatchesHost(t *testing.T) {
	tests := []struct {
		name      string
		auth      RegistryAuth
		refHost   string
		host      string
		wantMatch bool
	}{
		{
			name:      "docker hub aliases match",
			auth:      RegistryAuth{ServerAddress: "https://index.docker.io/v1/"},
			host:      "registry-1.docker.io",
			wantMatch: true,
		},
		{
			name:      "docker hub alias from bare docker.io",
			auth:      RegistryAuth{ServerAddress: "docker.io"},
			host:      "registry-1.docker.io",
			wantMatch: true,
		},
		{
			name:      "custom registry matches",
			auth:      RegistryAuth{ServerAddress: "registry.example.com"},
			host:      "registry.example.com",
			wantMatch: true,
		},
		{
			name:      "custom registry with port matches",
			auth:      RegistryAuth{ServerAddress: "https://registry.example.com:5000/v2"},
			host:      "registry.example.com:5000",
			wantMatch: true,
		},
		{
			name:      "different registry does not match",
			auth:      RegistryAuth{ServerAddress: "registry.example.com"},
			host:      "other.example.com",
			wantMatch: false,
		},
		{
			name:      "empty server address falls back to ref host",
			auth:      RegistryAuth{Username: "u"},
			refHost:   "ghcr.io",
			host:      "ghcr.io",
			wantMatch: true,
		},
		{
			name:      "empty server address and ref host matches any host",
			auth:      RegistryAuth{Username: "u"},
			host:      "anything.example.com",
			wantMatch: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.auth.matchesHost(test.refHost, test.host); got != test.wantMatch {
				t.Fatalf("matchesHost(%q, %q) = %t, want %t", test.refHost, test.host, got, test.wantMatch)
			}
		})
	}
}

func TestReferenceHost(t *testing.T) {
	tests := map[string]string{
		"alpine":                                     "docker.io",
		"library/alpine:latest":                      "docker.io",
		"docker.io/library/alpine:latest":            "docker.io",
		"ghcr.io/owner/image:tag":                    "ghcr.io",
		"registry.example.com:5000/team/image:tag":   "registry.example.com:5000",
		"registry.example.com/team/image@sha256:abc": "registry.example.com",
		"localhost:5000/image":                       "localhost:5000",
	}
	for ref, want := range tests {
		if got := referenceHost(ref); got != want {
			t.Fatalf("referenceHost(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestRegistryAuthUsernameAndSecretPrefersIdentityToken(t *testing.T) {
	auth := RegistryAuth{Username: "user", Password: "pass", IdentityToken: "identity"}
	username, secret := auth.usernameAndSecret()
	if username != "" || secret != "identity" {
		t.Fatalf("usernameAndSecret() = %q/%q, want empty/identity", username, secret)
	}

	basic := RegistryAuth{Username: "user", Password: "pass"}
	username, secret = basic.usernameAndSecret()
	if username != "user" || secret != "pass" {
		t.Fatalf("usernameAndSecret() = %q/%q, want user/pass", username, secret)
	}
}

func TestSessionAuthCredentialsAppliesToMatchingHostOnly(t *testing.T) {
	credentials := &RegistryAuth{Username: "user", Password: "pass", ServerAddress: "registry.example.com"}
	server := &sessionAuth{creds: credentials}

	tests := []struct {
		host       string
		wantUser   string
		wantSecret string
	}{
		{host: "registry.example.com", wantUser: "user", wantSecret: "pass"},
		{host: "other.example.com"},
		{host: "docker.io"},
	}
	for _, test := range tests {
		response, err := server.Credentials(t.Context(), &auth.CredentialsRequest{Host: test.host})
		if err != nil {
			t.Fatalf("Credentials(%q) error = %v", test.host, err)
		}
		if response.GetUsername() != test.wantUser || response.GetSecret() != test.wantSecret {
			t.Fatalf("Credentials(%q) = %q/%q, want %q/%q",
				test.host, response.GetUsername(), response.GetSecret(), test.wantUser, test.wantSecret)
		}
	}
}

func TestSessionAuthRegistryTokenIsReturnedAsBearerSecret(t *testing.T) {
	server := &sessionAuth{creds: &RegistryAuth{RegistryToken: "registry-token"}}
	response, err := server.Credentials(t.Context(), &auth.CredentialsRequest{Host: "registry.example.com"})
	if err != nil {
		t.Fatalf("Credentials() error = %v", err)
	}
	if response.GetUsername() != "" || response.GetSecret() != "registry-token" {
		t.Fatalf("Credentials() = %q/%q, want empty/registry-token", response.GetUsername(), response.GetSecret())
	}
}
