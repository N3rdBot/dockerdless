package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	"go.uber.org/zap"
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
	s.attachImageConfig(ctx, &detail)
	return detail, nil
}

// attachImageConfig merges the resolved OCI image configuration into the
// inspect projection. A read failure leaves Config unset instead of failing
// the inspect: the HTTP layer always renders a non-nil Docker Config envelope.
func (s *Service) attachImageConfig(ctx context.Context, detail *ports.ImageDetail) {
	if s.imageConfigs == nil || detail == nil {
		return
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	imageID := string(detail.ID)
	config, err := s.imageConfigs.ImageConfig(ctx, imageID)
	if err != nil {
		s.logger.Warn("failed to resolve image config",
			zap.String("image_id", imageID),
			zap.Error(err))
		return
	}
	detail.Config = &config
}

// ImageRemove implements DELETE /images/{name}. A running container's image is
// never removable; a stopped container's image needs force. Missing images are
// Docker 404s.
func (s *Service) ImageRemove(ctx context.Context, ref string, force bool) (ports.ImageRemoveResult, error) {
	ctx, cancel := s.requestContext(ctx)
	defer cancel()

	ref = strings.TrimPrefix(strings.TrimSpace(ref), "/")
	if ref == "" {
		return ports.ImageRemoveResult{}, invalidError("image reference must not be empty")
	}
	detail, err := s.images.Inspect(ctx, ref)
	if err != nil {
		mapped := translateError(err)
		if errors.Is(mapped, ports.ErrNotFound) {
			return ports.ImageRemoveResult{}, newDockerError(ports.ErrNotFound, fmt.Sprintf("No such image: %s", ref), err)
		}
		return ports.ImageRemoveResult{}, mapped
	}
	if err := s.checkImageInUse(detail, force); err != nil {
		return ports.ImageRemoveResult{}, err
	}
	if err := s.images.Remove(ctx, domain.ImageID(ref)); err != nil {
		return ports.ImageRemoveResult{}, translateError(err)
	}
	result := ports.ImageRemoveResult{ID: detail.ID}
	if tag, ok := matchingImageTag(ref, detail.RepoTags); ok {
		result.Untagged = tag
	} else {
		result.Deleted = string(detail.ID)
	}
	return result, nil
}

func (s *Service) checkImageInUse(detail ports.ImageDetail, force bool) error {
	for _, container := range s.registry.List() {
		if !containerUsesImage(container, detail) {
			continue
		}
		imageID := shortImageID(string(detail.ID))
		containerID := shortImageID(string(container.ID))
		switch {
		case container.State == domain.ContainerStateRunning:
			return conflictError(
				"conflict: unable to delete %s (cannot be forced) - image is being used by running container %s",
				imageID, containerID)
		case !force:
			return conflictError(
				"conflict: unable to delete %s (must be forced) - image is being used by stopped container %s",
				imageID, containerID)
		}
	}
	return nil
}

func containerUsesImage(container domain.Container, detail ports.ImageDetail) bool {
	if digestsMatch(string(container.ImageDigest), string(detail.ID)) {
		return true
	}
	reference := string(container.ImageReference)
	if strings.TrimSpace(reference) == "" {
		return false
	}
	for _, candidate := range append(append([]string(nil), detail.RepoTags...), detail.RepoDigests...) {
		if sameImageReference(reference, candidate) {
			return true
		}
	}
	return false
}

// sameImageReference compares two Docker references after canonicalizing
// familiar forms such as "alpine:3.20" against "docker.io/library/alpine:3.20".
func sameImageReference(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	normalizedLeft, leftErr := buildkit.NormalizeReference(left)
	normalizedRight, rightErr := buildkit.NormalizeReference(right)
	if leftErr == nil && rightErr == nil {
		return normalizedLeft == normalizedRight
	}
	return strings.TrimPrefix(left, "docker.io/library/") == strings.TrimPrefix(right, "docker.io/library/")
}

// digestsMatch accepts equal digests and unambiguous prefixes in either
// direction, so a full config digest matches a stored 12-character ID.
func digestsMatch(candidate, wanted string) bool {
	candidate = strings.TrimPrefix(candidate, "sha256:")
	wanted = strings.TrimPrefix(wanted, "sha256:")
	if candidate == "" || wanted == "" {
		return false
	}
	return candidate == wanted || strings.HasPrefix(candidate, wanted) || strings.HasPrefix(wanted, candidate)
}

// matchingImageTag returns the stored familiar tag for ref when the request
// named a tag rather than an ID.
func matchingImageTag(ref string, tags []string) (string, bool) {
	for _, tag := range tags {
		if sameImageReference(tag, ref) || strings.TrimPrefix(tag, "docker.io/library/") == ref {
			return tag, true
		}
	}
	return "", false
}

func shortImageID(id string) string {
	value := strings.TrimPrefix(id, "sha256:")
	if len(value) > 12 {
		return value[:12]
	}
	return value
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
