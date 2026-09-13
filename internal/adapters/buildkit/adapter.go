// Package buildkit provides the BuildKit image adapter: image pull, inspect,
// list, and removal against containerd's image store, plus Dockerfile builds
// through the BuildKit Go client with a Docker-compatible JSON progress stream.
package buildkit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// ErrNoStore reports that an adapter was built without an image store.
var ErrNoStore = errors.New("image store is not configured")

// Options configures an Adapter.
type Options struct {
	// Logger receives adapter lifecycle logs. Nil uses a no-op logger.
	Logger *zap.Logger
	// TempRoot is the parent directory for materialized build contexts. Empty
	// uses the process temp directory.
	TempRoot string
}

// Adapter implements image pull/inspect/list/remove on the narrow ImageStore
// seam and Dockerfile builds on the narrow Solver seam.
type Adapter struct {
	store    ImageStore
	solver   Solver
	logger   *zap.Logger
	tempRoot string
}

// New constructs an Adapter from an image store and a BuildKit solver. Either
// seam may be nil when the corresponding operations are not wired yet.
func New(store ImageStore, solver Solver, opts Options) *Adapter {
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Adapter{
		store:    store,
		solver:   solver,
		logger:   logger,
		tempRoot: opts.TempRoot,
	}
}

// ImageDetail is the Docker-facing projection of one stored image.
type ImageDetail struct {
	// ID is the Docker image ID (the image config digest).
	ID domain.ImageID
	// RepoTags are the familiar references of every record sharing the image.
	RepoTags []string
	// RepoDigests are the familiar digested references of the image.
	RepoDigests []string
	// Platform is the image platform such as "linux/amd64".
	Platform string
	// Size is the packed image size in bytes.
	Size int64
	// Created is the image creation time.
	Created time.Time
	// Labels are the image labels.
	Labels map[string]string
}

// Pull resolves and prepares an image reference through the store and returns
// the stable Docker image ID. It satisfies the frozen ports.Image contract;
// callers that need registry auth or a platform must use PullImage.
func (a *Adapter) Pull(ctx context.Context, ref string) (domain.ImageID, error) {
	return a.PullImage(ctx, ref, PullOptions{})
}

// PullImage pulls with explicit platform and registry-auth options.
func (a *Adapter) PullImage(ctx context.Context, ref string, opts PullOptions) (domain.ImageID, error) {
	if a.store == nil {
		return "", ErrNoStore
	}
	record, err := a.store.Pull(ctx, ref, opts)
	if err != nil {
		return "", fmt.Errorf("pull image %q: %w", ref, err)
	}
	id := record.ConfigDigest
	if id == "" {
		id = record.TargetDigest
	}
	if id == "" {
		return "", fmt.Errorf("pull image %q: store returned an empty image id", ref)
	}
	// Only non-secret fields are logged; RegistryAuth redacts itself.
	a.logger.Info("image pulled",
		zap.String("reference", record.Name),
		zap.String("image_id", id),
		zap.Int64("size", record.Size))
	return domain.ImageID(id), nil
}

// Inspect resolves a reference, image ID, or ID prefix and returns the Docker
// inspect projection. Missing images yield ErrNotFound.
func (a *Adapter) Inspect(ctx context.Context, refOrID string) (ImageDetail, error) {
	if a.store == nil {
		return ImageDetail{}, ErrNoStore
	}
	record, err := a.store.Get(ctx, refOrID)
	if err != nil {
		return ImageDetail{}, fmt.Errorf("inspect image %q: %w", refOrID, err)
	}
	return detailFromRecord(record), nil
}

// List returns every stored image record, grouped by config digest so one
// multi-tag image yields one row per Docker's image list semantics.
func (a *Adapter) List(ctx context.Context) ([]ImageDetail, error) {
	if a.store == nil {
		return nil, ErrNoStore
	}
	records, err := a.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}

	grouped := map[string]ImageDetail{}
	order := make([]string, 0, len(records))
	for _, record := range records {
		detail := detailFromRecord(record)
		key := string(detail.ID)
		if key == "" {
			key = record.Name
		}
		existing, ok := grouped[key]
		if !ok {
			order = append(order, key)
			grouped[key] = detail
			continue
		}
		existing.RepoTags = appendUnique(existing.RepoTags, detail.RepoTags...)
		existing.RepoDigests = appendUnique(existing.RepoDigests, detail.RepoDigests...)
		grouped[key] = existing
	}

	details := make([]ImageDetail, 0, len(grouped))
	for _, key := range order {
		details = append(details, grouped[key])
	}
	return details, nil
}

// Remove deletes one image reference. A reference argument untags exactly that
// reference. An image ID or ID prefix removes one reference per call, so an
// image with several tags is reference-counted down; ErrNotFound is returned
// once no matching reference remains.
func (a *Adapter) Remove(ctx context.Context, id domain.ImageID) error {
	if a.store == nil {
		return ErrNoStore
	}
	ref := strings.TrimSpace(string(id))
	if ref == "" {
		return domain.ErrEmptyIdentity
	}
	if isImageID(ref) {
		return a.removeByID(ctx, ref)
	}
	if err := a.store.Delete(ctx, ref); err != nil {
		return fmt.Errorf("remove image %q: %w", ref, err)
	}
	a.logger.Info("image reference removed", zap.String("reference", ref))
	return nil
}

// removeByID resolves an image ID to its stored references and removes one of
// them. The containerd store keeps one record per tag, so deleting that record
// is the reference decrement; unreferenced content is reclaimed by containerd.
func (a *Adapter) removeByID(ctx context.Context, id string) error {
	records, err := a.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list images to remove %q: %w", id, err)
	}
	matches := make([]ImageRecord, 0, len(records))
	for _, record := range records {
		if recordMatchesID(record, id) {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })

	if err := a.store.Delete(ctx, matches[0].Name); err != nil {
		return fmt.Errorf("remove image %q: %w", matches[0].Name, err)
	}
	a.logger.Info("image reference removed",
		zap.String("image_id", id),
		zap.String("reference", matches[0].Name),
		zap.Int("remaining_references", len(matches)-1))
	return nil
}

func detailFromRecord(record ImageRecord) ImageDetail {
	id := record.ConfigDigest
	if id == "" {
		id = record.TargetDigest
	}
	return ImageDetail{
		ID:          domain.ImageID(id),
		RepoTags:    append([]string(nil), record.RepoTags...),
		RepoDigests: append([]string(nil), record.RepoDigests...),
		Platform:    formatPlatform(record.Platform),
		Size:        record.Size,
		Created:     record.CreatedAt,
		Labels:      record.Labels,
	}
}

func formatPlatform(platform ocispec.Platform) string {
	if platform.OS == "" || platform.Architecture == "" {
		return ""
	}
	return platforms.Format(platform)
}

// isImageID reports whether a removal target is a digest or a Docker ID
// prefix rather than a named reference.
func isImageID(ref string) bool {
	if strings.HasPrefix(ref, "sha256:") {
		return true
	}
	if len(ref) < 12 || len(ref) > 64 {
		return false
	}
	for _, r := range ref {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

var _ ports.Image = (*Adapter)(nil)
