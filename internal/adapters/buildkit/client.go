package buildkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	buildkitclient "github.com/moby/buildkit/client"
	exptypes "github.com/moby/buildkit/exporter/containerimage/exptypes"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/fsutil"
)

// ErrNotFound reports that an image is absent from the local image store. API
// layers map it to Docker's 404 response.
var ErrNotFound = errors.New("image not found")

// ErrInvalidReference reports a reference that cannot be parsed or normalized.
var ErrInvalidReference = errors.New("invalid image reference")

// ErrInvalidContext reports a build context that cannot be materialized (bad
// tar stream, archive traversal attempt, or a missing Dockerfile).
var ErrInvalidContext = errors.New("invalid build context")

// ErrNoSolver reports that Build was called on an adapter without a solver.
var ErrNoSolver = errors.New("buildkit solver is not configured")

// ImageStore is the narrow seam over a container image store. The concrete
// implementation talks to containerd; unit tests provide an in-memory fake.
// Implementations accept Docker references in any form and normalize them
// internally, so callers may pass "alpine" or
// "docker.io/library/alpine:latest" interchangeably.
type ImageStore interface {
	// Pull resolves ref, downloads and unpacks the image for the requested
	// platform, and returns the stored record.
	Pull(ctx context.Context, ref string, opts PullOptions) (ImageRecord, error)
	// Get resolves a reference, full ID, or ID prefix to one stored record.
	Get(ctx context.Context, refOrID string) (ImageRecord, error)
	// List returns every stored image record.
	List(ctx context.Context) ([]ImageRecord, error)
	// Delete removes one stored reference. It must not delete content that is
	// still referenced by another stored record.
	Delete(ctx context.Context, ref string) error
}

// PullOptions controls one image pull.
type PullOptions struct {
	// Platform is an OCI platform string such as "linux/amd64". Empty means
	// the store's configured default platform.
	Platform string
	// Auth carries Docker registry credentials. It is optional.
	Auth *RegistryAuth
}

// ImageRecord is a daemon-neutral projection of one stored image reference.
// A single image (same ConfigDigest) may have several records, one per tag.
type ImageRecord struct {
	// Name is the store's canonical reference, e.g.
	// "docker.io/library/alpine:latest".
	Name string
	// TargetDigest is the manifest or index digest ("sha256:...").
	TargetDigest string
	// ConfigDigest is the platform image config digest ("sha256:..."), which
	// Docker exposes as the image ID.
	ConfigDigest string
	// RepoTags and RepoDigests are the familiar references of every stored
	// record that shares TargetDigest.
	RepoTags    []string
	RepoDigests []string
	// Platform is the platform of the stored image.
	Platform ocispec.Platform
	// Size is the packed size of the image in bytes.
	Size int64
	// CreatedAt is the image creation time when known, otherwise the time the
	// store record was created.
	CreatedAt time.Time
	// Labels are the image record labels.
	Labels map[string]string
}

// Solver is the narrow seam over the BuildKit client. Solve must close
// statusCh before returning, mirroring client.Client.Solve. The channel is
// bidirectional to match the upstream client contract exactly.
type Solver interface {
	Solve(ctx context.Context, opts SolveOptions, statusCh chan *buildkitclient.SolveStatus) (SolveResult, error)
}

// SolveOptions controls one BuildKit solve against a local directory context.
type SolveOptions struct {
	// ContextDir is the materialized local build context.
	ContextDir string
	// Dockerfile is the path of the Dockerfile inside ContextDir.
	Dockerfile string
	// Tag is the image reference to export, already normalized. Empty discards
	// the build result.
	Tag string
	// Platform is an OCI platform string such as "linux/amd64".
	Platform string
	// BuildArgs are Dockerfile build arguments.
	BuildArgs map[string]string
	// Target selects a build stage.
	Target string
	// NoCache disables BuildKit cache reuse.
	NoCache bool
	// Auth carries registry credentials for base image pulls inside the build.
	Auth *RegistryAuth
}

// SolveResult summarizes a completed solve.
type SolveResult struct {
	// ImageID is the image config digest ("sha256:...") when an image was
	// exported.
	ImageID string
	// ImageName is the exported image name when one was requested.
	ImageName string
	// Digest is the exported manifest digest when an image was exported.
	Digest string
	// ExporterResponse is BuildKit's raw exporter metadata.
	ExporterResponse map[string]string
}

// NormalizeReference parses a Docker image reference and returns its canonical
// form with the default domain, library namespace, and tag applied, matching
// how containerd records pulled images.
func NormalizeReference(ref string) (string, error) {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrInvalidReference, ref, err)
	}
	return reference.TagNameOnly(named).String(), nil
}

// StoreConfig configures the containerd-backed image store.
type StoreConfig struct {
	// Namespace is the containerd namespace holding the images. Empty leaves
	// the client default in place.
	Namespace string
	// Snapshotter is the snapshotter used to unpack pulled images. Empty lets
	// containerd choose its default.
	Snapshotter string
	// Platform is the default pull platform. Empty uses the client default.
	Platform string
}

// ContainerdStore implements ImageStore on top of a containerd image service.
type ContainerdStore struct {
	client      *containerd.Client
	namespace   string
	snapshotter string
	platform    string
}

// NewContainerdStore wraps a containerd client as an ImageStore.
func NewContainerdStore(client *containerd.Client, cfg StoreConfig) *ContainerdStore {
	return &ContainerdStore{
		client:      client,
		namespace:   cfg.Namespace,
		snapshotter: cfg.Snapshotter,
		platform:    cfg.Platform,
	}
}

func (s *ContainerdStore) withNamespace(ctx context.Context) context.Context {
	if s.namespace == "" {
		return ctx
	}
	return namespaces.WithNamespace(ctx, s.namespace)
}

// Pull downloads and unpacks ref for the requested platform.
func (s *ContainerdStore) Pull(ctx context.Context, ref string, opts PullOptions) (ImageRecord, error) {
	ctx = s.withNamespace(ctx)

	normalized, err := NormalizeReference(ref)
	if err != nil {
		return ImageRecord{}, err
	}

	remoteOpts := []containerd.RemoteOpt{containerd.WithResolver(newResolver(ctx, opts.Auth, normalized)), containerd.WithPullUnpack}
	if s.snapshotter != "" {
		remoteOpts = append(remoteOpts, containerd.WithPullSnapshotter(s.snapshotter))
	}
	platform := opts.Platform
	if platform == "" {
		platform = s.platform
	}
	if platform != "" {
		remoteOpts = append(remoteOpts, containerd.WithPlatform(platform))
	}

	img, err := s.client.Pull(ctx, normalized, remoteOpts...)
	if err != nil {
		return ImageRecord{}, translatePullError(normalized, err)
	}

	records, err := s.records(ctx)
	if err != nil {
		return ImageRecord{}, err
	}
	for _, record := range records {
		if record.Name == img.Name() {
			return record, nil
		}
	}
	return s.recordFromImage(ctx, img)
}

// Get resolves a canonical reference, full digest, or digest prefix to a
// stored record, including its sibling repo tags and digests.
func (s *ContainerdStore) Get(ctx context.Context, refOrID string) (ImageRecord, error) {
	ctx = s.withNamespace(ctx)

	if normalized, err := NormalizeReference(refOrID); err == nil {
		if img, err := s.client.GetImage(ctx, normalized); err == nil {
			if records, listErr := s.records(ctx); listErr == nil {
				for _, record := range records {
					if record.Name == img.Name() {
						return record, nil
					}
				}
			}
		} else if !cerrdefs.IsNotFound(err) {
			return ImageRecord{}, translateStoreErr(err)
		}
	}

	records, err := s.records(ctx)
	if err != nil {
		return ImageRecord{}, err
	}
	for _, record := range records {
		if recordMatchesID(record, strings.TrimSpace(refOrID)) {
			return record, nil
		}
	}
	return ImageRecord{}, fmt.Errorf("%w: %s", ErrNotFound, refOrID)
}

// List returns every stored image record.
func (s *ContainerdStore) List(ctx context.Context) ([]ImageRecord, error) {
	ctx = s.withNamespace(ctx)
	return s.records(ctx)
}

// Delete removes exactly one stored reference. Deleting a record never deletes
// content that another record still references; containerd's garbage collector
// reclaims unreferenced blobs.
func (s *ContainerdStore) Delete(ctx context.Context, ref string) error {
	ctx = s.withNamespace(ctx)

	normalized, err := NormalizeReference(ref)
	if err == nil {
		if deleteErr := s.client.ImageService().Delete(ctx, normalized); deleteErr == nil {
			return nil
		} else if !cerrdefs.IsNotFound(deleteErr) {
			return translateStoreErr(deleteErr)
		}
	}

	if deleteErr := s.client.ImageService().Delete(ctx, ref); deleteErr != nil {
		if cerrdefs.IsNotFound(deleteErr) {
			return fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		return translateStoreErr(deleteErr)
	}
	return nil
}

// records lists every image record and groups sibling references that share a
// target digest.
func (s *ContainerdStore) records(ctx context.Context) ([]ImageRecord, error) {
	images, err := s.client.ListImages(ctx)
	if err != nil {
		return nil, translateStoreErr(err)
	}

	records := make([]ImageRecord, 0, len(images))
	for _, img := range images {
		record, err := s.recordFromImage(ctx, img)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	attachSiblingReferences(records)
	return records, nil
}

func (s *ContainerdStore) recordFromImage(ctx context.Context, img containerd.Image) (ImageRecord, error) {
	config, err := img.Config(ctx)
	if err != nil {
		return ImageRecord{}, fmt.Errorf("read image config descriptor: %w", err)
	}
	spec, err := img.Spec(ctx)
	if err != nil {
		return ImageRecord{}, fmt.Errorf("read image spec: %w", err)
	}
	size, err := img.Size(ctx)
	if err != nil {
		return ImageRecord{}, fmt.Errorf("read image size: %w", err)
	}

	metadata := img.Metadata()
	createdAt := metadata.CreatedAt
	if spec.Created != nil {
		createdAt = *spec.Created
	}

	return ImageRecord{
		Name:         img.Name(),
		TargetDigest: img.Target().Digest.String(),
		ConfigDigest: config.Digest.String(),
		Platform: ocispec.Platform{
			OS:           spec.OS,
			Architecture: spec.Architecture,
			Variant:      spec.Variant,
		},
		Size:      size,
		CreatedAt: createdAt,
		Labels:    metadata.Labels,
	}, nil
}

// recordMatchesID reports whether a record's config digest, target digest, or
// a digest prefix matches id. Docker clients commonly pass 12-character ID
// prefixes.
func recordMatchesID(record ImageRecord, id string) bool {
	if id == "" {
		return false
	}
	for _, candidate := range []string{record.ConfigDigest, record.TargetDigest} {
		if candidate == id {
			return true
		}
		value := strings.TrimPrefix(candidate, string(digest.Canonical)+":")
		if value != "" && strings.HasPrefix(value, id) {
			return true
		}
	}
	return false
}

// attachSiblingReferences fills RepoTags and RepoDigests for every record by
// looking at all records that share the same target digest.
func attachSiblingReferences(records []ImageRecord) {
	byTarget := map[string][]int{}
	for i := range records {
		target := records[i].TargetDigest
		if target == "" {
			continue
		}
		byTarget[target] = append(byTarget[target], i)
	}

	for target, siblings := range byTarget {
		targetDigest, err := digest.Parse(target)
		if err != nil {
			continue
		}
		for _, i := range siblings {
			for _, j := range siblings {
				name := records[j].Name
				named, err := reference.ParseNormalizedNamed(name)
				if err != nil {
					records[i].RepoTags = appendUnique(records[i].RepoTags, name)
					continue
				}
				records[i].RepoTags = appendUnique(records[i].RepoTags, reference.FamiliarString(named))
				if digested, ok := named.(reference.Digested); ok {
					records[i].RepoDigests = appendUnique(records[i].RepoDigests, reference.FamiliarString(digested))
					continue
				}
				digested, err := reference.WithDigest(reference.TrimNamed(named), targetDigest)
				if err != nil {
					continue
				}
				records[i].RepoDigests = appendUnique(records[i].RepoDigests, reference.FamiliarString(digested))
			}
		}
	}
}

func appendUnique(values []string, additions ...string) []string {
	for _, value := range additions {
		if value == "" || containsString(values, value) {
			continue
		}
		values = append(values, value)
	}
	return values
}

func containsString(values []string, value string) bool {
	return slices.Contains(values, value)
}

// translateStoreErr normalizes containerd's not-found errors to ErrNotFound so
// callers never depend on containerd error types.
func translateStoreErr(err error) error {
	if err == nil {
		return nil
	}
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}

// translatePullError maps registry pull failures to Docker semantics. A
// registry that denies access to a missing or private repository reports 401,
// which Docker surfaces as 404 so private repositories stay indistinguishable
// from missing ones.
func translatePullError(ref string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "pull access denied"),
		strings.Contains(message, "insufficient_scope"),
		strings.Contains(message, "authorization failed"):
		return fmt.Errorf("%w: pull access denied for %s, repository does not exist or may require 'docker login': %w",
			ErrNotFound, familiarName(ref), err)
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	default:
		return err
	}
}

// familiarName renders a normalized reference the way Docker names it in user
// facing messages (docker.io/library/alpine:latest becomes alpine:latest).
func familiarName(ref string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ref
	}
	return reference.FamiliarString(named)
}

// BuildkitSolver implements Solver with the BuildKit Go client.
type BuildkitSolver struct {
	client *buildkitclient.Client
}

// NewBuildkitSolver wraps a BuildKit client as a Solver.
func NewBuildkitSolver(client *buildkitclient.Client) *BuildkitSolver {
	return &BuildkitSolver{client: client}
}

const (
	dockerfileFrontend  = "dockerfile.v0"
	contextLocalName    = "context"
	dockerfileLocalName = "dockerfile"
)

// Solve materializes the local context through a BuildKit session and runs the
// Dockerfile frontend.
func (s *BuildkitSolver) Solve(ctx context.Context, opts SolveOptions, statusCh chan *buildkitclient.SolveStatus) (SolveResult, error) {
	local, err := fsutil.NewFS(opts.ContextDir)
	if err != nil {
		return SolveResult{}, fmt.Errorf("open build context %s: %w", opts.ContextDir, err)
	}

	filename := opts.Dockerfile
	if filename == "" {
		filename = "Dockerfile"
	}
	attrs := map[string]string{"filename": filename}
	if opts.Platform != "" {
		attrs["platform"] = opts.Platform
	}
	if opts.Target != "" {
		attrs["target"] = opts.Target
	}
	if opts.NoCache {
		attrs["no-cache"] = "true"
	}
	for key, value := range opts.BuildArgs {
		attrs["build-arg:"+key] = value
	}

	solveOpt := buildkitclient.SolveOpt{
		Frontend:      dockerfileFrontend,
		FrontendAttrs: attrs,
		LocalMounts: map[string]fsutil.FS{
			contextLocalName:    local,
			dockerfileLocalName: local,
		},
	}
	if opts.Tag != "" {
		solveOpt.Exports = []buildkitclient.ExportEntry{{
			Type:  buildkitclient.ExporterImage,
			Attrs: map[string]string{"name": opts.Tag, "push": "false"},
		}}
	}
	if opts.Auth != nil {
		solveOpt.Session = append(solveOpt.Session, newSessionAuth(opts.Auth, opts.Platform))
	}

	response, err := s.client.Solve(ctx, nil, solveOpt, statusCh)
	if err != nil {
		return SolveResult{}, err
	}
	if response == nil {
		return SolveResult{}, nil
	}

	exporter := response.ExporterResponse
	result := SolveResult{ExporterResponse: exporter}
	result.ImageID = exporter[exptypes.ExporterImageConfigDigestKey]
	if result.ImageID == "" {
		result.ImageID = exporter[exptypes.ExporterConfigDigestKey]
	}
	result.ImageName = exporter[exptypes.ExporterImageNameKey]
	result.Digest = exporter[exptypes.ExporterImageDigestKey]
	return result, nil
}
