package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/N3rdBot/dockerdless/internal/ports"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/storage"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const maxBuildContextBytes int64 = 1 << 30

func (h *handlers) imageInspect(w http.ResponseWriter, r *http.Request) {
	name := pathParameter(r, "/images/", "/json")
	detail, err := h.service.ImageInspect(r.Context(), name)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imageInspectResponse(detail))
}

func (h *handlers) imageRemove(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(pathParameter(r, "/images/", ""), "/")
	force := boolQuery(r, "force", false)
	result, err := h.service.ImageRemove(r.Context(), name, force)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	items := make([]image.DeleteResponse, 0, 1)
	switch {
	case result.Untagged != "":
		items = append(items, image.DeleteResponse{Untagged: result.Untagged})
	case result.Deleted != "":
		items = append(items, image.DeleteResponse{Deleted: result.Deleted})
	default:
		items = append(items, image.DeleteResponse{Deleted: string(result.ID)})
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *handlers) imageList(w http.ResponseWriter, r *http.Request) {
	details, err := h.service.ImageList(r.Context())
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	summaries := make([]image.Summary, 0, len(details))
	for _, detail := range details {
		summaries = append(summaries, imageSummaryResponse(detail))
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (h *handlers) imageCreate(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	reference := strings.TrimSpace(query.Get("fromImage"))
	if tag := strings.TrimSpace(query.Get("tag")); tag != "" {
		reference = withTag(reference, tag)
	}
	auth, err := decodeRegistryAuth(r.Header.Get("X-Registry-Auth"))
	if err != nil {
		WriteDockerError(w, err.(*DockerError))
		return
	}
	stream := newLazyStreamWriter(w, "application/json")
	pullErr := h.service.ImagePull(r.Context(), ports.PullRequest{
		Reference: reference,
		Platform:  strings.TrimSpace(query.Get("platform")),
		Auth:      auth,
	}, stream)
	finishStream(w, stream, pullErr)
}

func (h *handlers) build(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	auth, err := decodeRegistryAuth(r.Header.Get("X-Registry-Auth"))
	if err != nil {
		WriteDockerError(w, err.(*DockerError))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBuildContextBytes)
	stream := newLazyStreamWriter(w, "application/json")
	buildErr := h.service.ImageBuild(r.Context(), ports.BuildRequest{
		Context:    r.Body,
		Tag:        strings.TrimSpace(query.Get("t")),
		Dockerfile: strings.TrimSpace(query.Get("dockerfile")),
		Platform:   strings.TrimSpace(query.Get("platform")),
		Target:     strings.TrimSpace(query.Get("target")),
		NoCache:    boolQuery(r, "nocache", false),
		BuildArgs:  decodeBuildArgs(query.Get("buildargs")),
		Auth:       auth,
	}, stream)
	finishStream(w, stream, buildErr)
}

// finishStream writes the Docker error envelope when nothing has been streamed
// yet, and an in-stream errorDetail otherwise, matching Engine behavior.
func finishStream(w http.ResponseWriter, stream *lazyStreamWriter, err error) {
	if err != nil {
		if !stream.Started() {
			WriteServiceError(w, err)
			return
		}
		writeStreamError(stream, err)
		return
	}
	stream.ensureStarted()
}

func writeStreamError(w io.Writer, err error) {
	message := serviceMessage(err)
	raw, marshalErr := json.Marshal(map[string]any{
		"errorDetail": map[string]string{"message": message},
		"error":       message,
	})
	if marshalErr != nil {
		return
	}
	_, _ = w.Write(append(raw, '\n'))
}

func withTag(reference, tag string) string {
	if reference == "" || strings.Contains(reference, "@") {
		return reference
	}
	if strings.LastIndex(reference, ":") > strings.LastIndex(reference, "/") {
		return reference
	}
	return reference + ":" + tag
}

func decodeBuildArgs(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	args := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil
	}
	return args
}

func imageSummaryResponse(detail ports.ImageDetail) image.Summary {
	return image.Summary{
		Containers:  -1,
		Created:     detail.Created.Unix(),
		ID:          string(detail.ID),
		Labels:      detail.Labels,
		ParentID:    "",
		RepoDigests: append([]string(nil), detail.RepoDigests...),
		RepoTags:    append([]string(nil), detail.RepoTags...),
		SharedSize:  -1,
		Size:        detail.Size,
	}
}

func imageInspectResponse(detail ports.ImageDetail) image.InspectResponse {
	architecture, variant, osName := splitPlatform(detail.Platform)
	return image.InspectResponse{
		ID:           string(detail.ID),
		RepoTags:     append([]string(nil), detail.RepoTags...),
		RepoDigests:  append([]string(nil), detail.RepoDigests...),
		Created:      detail.Created.Format(time.RFC3339Nano),
		Architecture: architecture,
		Variant:      variant,
		Os:           osName,
		Size:         detail.Size,
		GraphDriver:  &storage.DriverData{Name: "overlayfs"},
		RootFS:       image.RootFS{Type: "layers"},
		Config:       imageConfigResponse(detail.Config),
	}
}

// imageConfigResponse renders the Docker inspect Config envelope. It is always
// non-nil because Docker clients (testcontainers-go among them) dereference
// Config.ExposedPorts without a nil check on every container create.
func imageConfigResponse(config *ports.ImageConfig) *dockerspec.DockerOCIImageConfig {
	response := &dockerspec.DockerOCIImageConfig{
		ImageConfig: ocispec.ImageConfig{
			ExposedPorts: map[string]struct{}{},
			Volumes:      map[string]struct{}{},
		},
	}
	if config == nil {
		return response
	}
	for _, raw := range config.ExposedPorts {
		if port, err := network.ParsePort(raw); err == nil {
			response.ExposedPorts[port.String()] = struct{}{}
		}
	}
	for _, volume := range config.Volumes {
		if strings.TrimSpace(volume) != "" {
			response.Volumes[volume] = struct{}{}
		}
	}
	response.User = config.User
	response.Env = append([]string(nil), config.Env...)
	response.Entrypoint = append([]string(nil), config.Entrypoint...)
	response.Cmd = append([]string(nil), config.Cmd...)
	response.WorkingDir = config.WorkingDir
	response.Labels = config.Labels
	response.StopSignal = config.StopSignal
	return response
}

func splitPlatform(platform string) (architecture, variant, osName string) {
	osName = "linux"
	parts := strings.Split(platform, "/")
	if len(parts) == 0 || platform == "" {
		return "", "", osName
	}
	osName = parts[0]
	if len(parts) > 1 {
		architecture = parts[1]
	}
	if len(parts) > 2 {
		variant = parts[2]
	}
	return architecture, variant, osName
}

// lazyStreamWriter defers the HTTP status line until the first streamed byte,
// so validation failures still return a Docker error envelope.
type lazyStreamWriter struct {
	writer      http.ResponseWriter
	contentType string
	started     bool
	control     *http.ResponseController
}

func newLazyStreamWriter(w http.ResponseWriter, contentType string) *lazyStreamWriter {
	return &lazyStreamWriter{writer: w, contentType: contentType, control: http.NewResponseController(w)}
}

func (s *lazyStreamWriter) Started() bool { return s.started }

func (s *lazyStreamWriter) ensureStarted() {
	if s.started {
		return
	}
	s.started = true
	s.writer.Header().Set("Content-Type", s.contentType)
	s.writer.WriteHeader(http.StatusOK)
	_ = s.control.Flush()
}

func (s *lazyStreamWriter) Write(p []byte) (int, error) {
	s.ensureStarted()
	written, err := s.writer.Write(p)
	_ = s.control.Flush()
	return written, err
}
