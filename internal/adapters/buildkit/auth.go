package buildkit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	authutil "github.com/containerd/containerd/v2/core/remotes/docker/auth"
	dockerconfig "github.com/containerd/containerd/v2/core/remotes/docker/config"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/moby/api/pkg/authconfig"
	registrytypes "github.com/moby/moby/api/types/registry"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
)

// ErrInvalidAuth reports a malformed X-Registry-Auth header or an auth
// configuration that cannot be applied.
var ErrInvalidAuth = errors.New("invalid registry auth")

// RegistryAuth is the daemon's neutral representation of Docker registry
// credentials. String, GoString, and MarshalLogObject redact every secret so
// credentials can never leak through logging or error formatting.
type RegistryAuth struct {
	Username      string
	Password      string
	IdentityToken string
	RegistryToken string
	ServerAddress string
}

// String implements fmt.Stringer with all secrets redacted.
func (a RegistryAuth) String() string {
	return fmt.Sprintf("RegistryAuth{server_address=%q, username_set=%t, password_set=%t, identity_token_set=%t, registry_token_set=%t}",
		a.ServerAddress, a.Username != "", a.Password != "", a.IdentityToken != "", a.RegistryToken != "")
}

// GoString implements fmt.GoStringer with all secrets redacted.
func (a RegistryAuth) GoString() string { return a.String() }

// MarshalLogObject implements zapcore.ObjectMarshaler so structured logs record
// only credential presence, never credential values.
func (a RegistryAuth) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("server_address", a.ServerAddress)
	encoder.AddBool("username_set", a.Username != "")
	encoder.AddBool("password_set", a.Password != "")
	encoder.AddBool("identity_token_set", a.IdentityToken != "")
	encoder.AddBool("registry_token_set", a.RegistryToken != "")
	return nil
}

// ParseAuthHeader decodes the Docker X-Registry-Auth header value, a base64url
// encoded JSON AuthConfig. An empty header yields nil without error.
func ParseAuthHeader(header string) (*RegistryAuth, error) {
	if strings.TrimSpace(header) == "" {
		return nil, nil
	}
	config, err := authconfig.Decode(header)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAuth, err)
	}
	return FromAuthConfig(config), nil
}

// FromAuthConfig converts a moby AuthConfig into RegistryAuth, including the
// legacy base64 "auth" field.
func FromAuthConfig(config *registrytypes.AuthConfig) *RegistryAuth {
	if config == nil {
		return nil
	}
	auth := &RegistryAuth{
		Username:      config.Username,
		Password:      config.Password,
		IdentityToken: config.IdentityToken,
		RegistryToken: config.RegistryToken,
		ServerAddress: config.ServerAddress,
	}
	if auth.Username == "" && auth.Password == "" && config.Auth != "" {
		if decoded, err := base64.StdEncoding.DecodeString(config.Auth); err == nil {
			if username, password, ok := strings.Cut(string(decoded), ":"); ok {
				auth.Username = username
				auth.Password = password
			}
		}
	}
	if auth.Username == "" && auth.Password == "" && auth.IdentityToken == "" && auth.RegistryToken == "" {
		return nil
	}
	return auth
}

// usernameAndSecret resolves the credential pair used for basic and token
// authentication. Identity tokens are refresh secrets, so they ride the secret
// slot with an empty username.
func (a *RegistryAuth) usernameAndSecret() (string, string) {
	if a == nil {
		return "", ""
	}
	if a.IdentityToken != "" {
		return "", a.IdentityToken
	}
	return a.Username, a.Password
}

// matchesHost reports whether the auth configuration applies to host. An empty
// ServerAddress falls back to the pulled reference's host, and finally matches
// any host because the credentials accompany the request that named the image.
func (a *RegistryAuth) matchesHost(refHost, host string) bool {
	if a == nil {
		return false
	}
	configured := normalizeRegistryHost(a.ServerAddress)
	if configured == "" {
		configured = normalizeRegistryHost(refHost)
	}
	if configured == "" {
		return true
	}
	return registryHostAlias(configured) == registryHostAlias(normalizeRegistryHost(host))
}

// newResolver builds a containerd resolver that applies the credentials to
// matching hosts. A nil auth produces an anonymous resolver.
func newResolver(ctx context.Context, auth *RegistryAuth, ref string) remotes.Resolver {
	refHost := referenceHost(ref)

	options := dockerconfig.HostOptions{}
	if auth != nil {
		options.Credentials = func(host string) (string, string, error) {
			if !auth.matchesHost(refHost, host) {
				return "", "", nil
			}
			username, secret := auth.usernameAndSecret()
			return username, secret, nil
		}
	}

	hosts := dockerconfig.ConfigureHosts(ctx, options)
	if auth != nil && auth.RegistryToken != "" {
		base := hosts
		hosts = func(name string) ([]docker.RegistryHost, error) {
			resolved, err := base(name)
			if err != nil {
				return nil, err
			}
			for i := range resolved {
				resolved[i].Authorizer = &bearerAuthorizer{host: resolved[i].Host, bearer: auth.RegistryToken}
			}
			return resolved, nil
		}
	}
	return docker.NewResolver(docker.ResolverOptions{Hosts: hosts})
}

// bearerAuthorizer sends a statically configured registry bearer token.
type bearerAuthorizer struct {
	host   string
	bearer string
}

func (a *bearerAuthorizer) Authorize(_ context.Context, req *http.Request) error {
	if normalizeRegistryHost(req.Host) != normalizeRegistryHost(a.host) {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+a.bearer)
	return nil
}

// AddResponses returns ErrNotImplemented to prevent retrying a request whose
// static bearer token was rejected.
func (a *bearerAuthorizer) AddResponses(context.Context, []*http.Response) error {
	return cerrdefs.ErrNotImplemented
}

// sessionAuth serves the BuildKit session auth gRPC service from one
// RegistryAuth, so base image pulls inside a build use the caller's
// credentials. Secrets stay in memory and are never logged.
type sessionAuth struct {
	auth.UnimplementedAuthServer

	creds *RegistryAuth
}

// newSessionAuth returns a BuildKit session attachable for the given
// credentials. The platform argument is accepted so callers can later scope
// credentials per platform without changing the seam.
func newSessionAuth(creds *RegistryAuth, _ string) session.Attachable {
	return &sessionAuth{creds: creds}
}

// Register implements session.Attachable.
func (s *sessionAuth) Register(server *grpc.Server) {
	auth.RegisterAuthServer(server, s)
}

// Credentials answers BuildKit's credential lookup for one registry host.
func (s *sessionAuth) Credentials(_ context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	if s.creds == nil || !s.creds.matchesHost("", req.GetHost()) {
		return &auth.CredentialsResponse{}, nil
	}
	if s.creds.RegistryToken != "" {
		return &auth.CredentialsResponse{Secret: s.creds.RegistryToken}, nil
	}
	username, secret := s.creds.usernameAndSecret()
	if username == "" && secret == "" {
		return &auth.CredentialsResponse{}, nil
	}
	return &auth.CredentialsResponse{Username: username, Secret: secret}, nil
}

// FetchToken exchanges credentials (or anonymity) for a registry bearer token.
func (s *sessionAuth) FetchToken(ctx context.Context, req *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	if s.creds != nil && s.creds.RegistryToken != "" {
		return &auth.FetchTokenResponse{Token: s.creds.RegistryToken}, nil
	}

	options := authutil.TokenOptions{
		Realm:   req.GetRealm(),
		Service: req.GetService(),
		Scopes:  req.GetScopes(),
	}
	if s.creds != nil {
		options.Username, options.Secret = s.creds.usernameAndSecret()
	}

	client := &http.Client{Timeout: 30 * time.Second}
	if options.Username != "" || options.Secret != "" {
		response, err := authutil.FetchTokenWithOAuth(ctx, client, nil, "dockerdless", options)
		if err == nil {
			return &auth.FetchTokenResponse{
				Token:     response.AccessToken,
				ExpiresIn: int64(response.ExpiresInSeconds),
				IssuedAt:  response.IssuedAt.Unix(),
			}, nil
		}
		// Some registries reject the OAuth POST endpoint; fall through to the
		// GET endpoint, mirroring BuildKit's own auth provider.
	}

	response, err := authutil.FetchToken(ctx, client, nil, options)
	if err != nil {
		return nil, err
	}
	return &auth.FetchTokenResponse{
		Token:     response.Token,
		ExpiresIn: int64(response.ExpiresInSeconds),
		IssuedAt:  response.IssuedAt.Unix(),
	}, nil
}

// referenceHost extracts the registry host from an image reference without
// normalizing the reference itself.
func referenceHost(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if at := strings.LastIndexByte(ref, '@'); at >= 0 {
		ref = ref[:at]
	}
	if slash := strings.IndexByte(ref, '/'); slash >= 0 {
		first := ref[:slash]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			return first
		}
	}
	// The reference has no explicit registry, so it targets Docker Hub.
	return "docker.io"
}

// normalizeRegistryHost lowercases a host or authority and strips any scheme,
// path, or trailing slash so it can be compared.
func normalizeRegistryHost(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
			value = parsed.Host
		}
	}
	value = strings.TrimPrefix(value, "//")
	value = strings.TrimSuffix(value, "/")
	if slash := strings.IndexByte(value, '/'); slash >= 0 {
		value = value[:slash]
	}
	return value
}

// registryHostAlias maps Docker Hub's interchangeable hostnames to one
// canonical host so credentials configured for index.docker.io also apply to
// registry-1.docker.io.
func registryHostAlias(host string) string {
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io", "registry.docker.io":
		return "registry-1.docker.io"
	default:
		return host
	}
}
