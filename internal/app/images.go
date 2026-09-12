package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// ImageInspect implements GET /images/{name}/json.
func (s *Service) ImageInspect(ctx context.Context, ref string) (ports.ImageDetail, error) {
	detail, err := s.images.Inspect(ctx, ref)
	if err != nil {
		mapped := translateError(err)
		if errors.Is(mapped, ports.ErrNotFound) {
			return ports.ImageDetail{}, newDockerError(ports.ErrNotFound, fmt.Sprintf("No such image: %s", ref), err)
		}
		return ports.ImageDetail{}, mapped
	}
	return detail, nil
}

// ImageList implements GET /images/json.
func (s *Service) ImageList(ctx context.Context) ([]ports.ImageDetail, error) {
	details, err := s.images.List(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return details, nil
}

// ImagePull implements POST /images/create. Progress is written as Docker JSON
// stream lines; failures are returned so the HTTP layer can decide between an
// error envelope and an in-stream errorDetail.
func (s *Service) ImagePull(ctx context.Context, request ports.PullRequest, out io.Writer) error {
	reference := strings.TrimSpace(request.Reference)
	if reference == "" {
		return invalidError("fromImage is required")
	}
	if _, err := buildkit.NormalizeReference(reference); err != nil {
		return translateError(err)
	}
	stream := dockerJSONWriter{out: out}
	_ = stream.write(map[string]string{"status": "Pulling from " + reference})
	if _, err := s.images.PullImage(ctx, reference, request); err != nil {
		return translateError(err)
	}
	if detail, err := s.images.Inspect(ctx, reference); err == nil {
		for _, digest := range detail.RepoDigests {
			_ = stream.write(map[string]string{"status": "Digest: " + digest})
		}
	}
	_ = stream.write(map[string]string{"status": "Status: Downloaded newer image for " + reference})
	return nil
}

// ImageBuild implements POST /build.
func (s *Service) ImageBuild(ctx context.Context, request ports.BuildRequest, out io.Writer) error {
	if request.Context == nil {
		return invalidError("build context is required")
	}
	if err := s.images.Build(ctx, request, out); err != nil {
		var buildErr *buildkit.BuildError
		if errors.As(err, &buildErr) {
			return newDockerError(kindForStatus(buildErr.StatusCode()), buildErr.Message, err)
		}
		return translateError(err)
	}
	return nil
}

// dockerJSONWriter emits one JSON object per line, Docker's progress stream
// framing.
type dockerJSONWriter struct {
	out io.Writer
}

func (w dockerJSONWriter) write(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w.out, "%s\n", raw)
	return err
}
