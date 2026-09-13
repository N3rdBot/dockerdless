package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/N3rdBot/dockerdless/internal/ports"
)

// handlers carries the dependencies shared by every Docker API handler.
type handlers struct {
	service Service
	logger  *zap.Logger
	limiter *streamLimiter
	maxBody int64
}

func newHandlers(deps Dependencies) *handlers {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &handlers{
		service: deps.Service,
		logger:  logger,
		limiter: newStreamLimiter(deps.StreamLimit),
		maxBody: deps.MaxBodyBytes,
	}
}

func (h *handlers) bodyLimit() int64 {
	if h.maxBody == 0 {
		return DefaultMaxBodyBytes
	}
	return h.maxBody
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		WriteDockerError(w, NewServerError(err.Error()))
		return
	}
	w.Header().Set("Content-Type", jsonMediaType)
	w.WriteHeader(status)
	_, _ = w.Write(buffer.Bytes())
}

// pathParameter extracts a route parameter from the unversioned request path.
func pathParameter(r *http.Request, prefix, suffix string) string {
	path := unversionedPath(r.URL.Path)
	return strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
}

func boolQuery(r *http.Request, key string, fallback bool) bool {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func tailQuery(r *http.Request) int {
	value := strings.TrimSpace(r.URL.Query().Get("tail"))
	if value == "" || value == "all" || value == "-1" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func timeQuery(r *http.Request, key string) time.Time {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return time.Time{}
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		whole := int64(seconds)
		return time.Unix(whole, int64((seconds-float64(whole))*float64(time.Second)))
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed
	}
	return time.Time{}
}

func timeoutQuery(r *http.Request) *time.Duration {
	for _, key := range []string{"t", "timeout"} {
		value := strings.TrimSpace(r.URL.Query().Get(key))
		if value == "" {
			continue
		}
		seconds, err := strconv.Atoi(value)
		if err != nil {
			return nil
		}
		duration := time.Duration(seconds) * time.Second
		return &duration
	}
	return nil
}

func apiVersionFromRequest(r *http.Request) string {
	version, versioned, err := versionFromPath(r.URL.Path)
	if err != nil || !versioned {
		return AdvertisedAPIVersion
	}
	return version
}

func envMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			out[entry] = ""
			continue
		}
		out[key] = value
	}
	return out
}

func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

type registryAuthPayload struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	Auth          string `json:"auth"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
	ServerAddress string `json:"serveraddress"`
}

// decodeRegistryAuth decodes Docker's X-Registry-Auth header into the
// transport-neutral credential shape.
func decodeRegistryAuth(header string) (*ports.RegistryAuth, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}
	raw, err := decodeBase64Any(header)
	if err != nil {
		return nil, NewInvalidParameter("invalid X-Registry-Auth header")
	}
	var payload registryAuthPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, NewInvalidParameter("invalid X-Registry-Auth header")
	}
	auth := &ports.RegistryAuth{
		Username:      payload.Username,
		Password:      payload.Password,
		IdentityToken: payload.IdentityToken,
		RegistryToken: payload.RegistryToken,
		ServerAddress: payload.ServerAddress,
	}
	if auth.Username == "" && auth.Password == "" && payload.Auth != "" {
		if decoded, err := decodeBase64Any(payload.Auth); err == nil {
			if username, password, ok := strings.Cut(string(decoded), ":"); ok {
				auth.Username, auth.Password = username, password
			}
		}
	}
	if auth.Username == "" && auth.Password == "" && auth.IdentityToken == "" && auth.RegistryToken == "" {
		return nil, nil
	}
	return auth, nil
}

func decodeBase64Any(value string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.URLEncoding,
		base64.RawURLEncoding,
		base64.StdEncoding,
		base64.RawStdEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 payload")
}
