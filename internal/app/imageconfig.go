package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/N3rdBot/dockerdless/internal/ports"
)

// containerdImageConfigs resolves OCI image configuration over a lazily opened
// containerd client. The BuildKit image adapter projects only identity, size,
// and labels, so the composition root supplies this reader to render the
// Docker inspect Config envelope without changing the finished adapter.
type containerdImageConfigs struct {
	socket    string
	namespace string

	once    sync.Once
	mu      sync.Mutex
	client  *containerdclient.Client
	openErr error
	closed  bool
}

func newContainerdImageConfigs(socket, namespace string) *containerdImageConfigs {
	return &containerdImageConfigs{socket: socket, namespace: namespace}
}

// ImageConfig resolves the stored image whose config digest matches
// configDigest and projects its OCI configuration.
func (c *containerdImageConfigs) ImageConfig(ctx context.Context, configDigest string) (ports.ImageConfig, error) {
	configDigest = strings.TrimSpace(configDigest)
	if configDigest == "" {
		return ports.ImageConfig{}, fmt.Errorf("%w: image config digest is required", ports.ErrInvalidArgument)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	client, err := c.openLocked()
	if err != nil {
		return ports.ImageConfig{}, fmt.Errorf("%w: open containerd for image config: %w", ports.ErrServerError, err)
	}
	if c.namespace != "" {
		ctx = namespaces.WithNamespace(ctx, c.namespace)
	}
	images, err := client.ListImages(ctx)
	if err != nil {
		return ports.ImageConfig{}, fmt.Errorf("%w: list images for config %s: %w", ports.ErrServerError, configDigest, err)
	}
	for _, image := range images {
		descriptor, err := image.Config(ctx)
		if err != nil {
			continue
		}
		if !configDigestMatches(descriptor.Digest.String(), configDigest) {
			continue
		}
		spec, err := image.Spec(ctx)
		if err != nil {
			return ports.ImageConfig{}, fmt.Errorf("%w: read image config %s: %w", ports.ErrServerError, configDigest, err)
		}
		return imageConfigFromSpec(spec), nil
	}
	return ports.ImageConfig{}, fmt.Errorf("%w: image config %s", ports.ErrNotFound, configDigest)
}

// Close releases the lazily opened containerd client. It is safe to call more
// than once and before any dial.
func (c *containerdImageConfigs) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.client == nil {
		return nil
	}
	err := c.client.Close()
	c.client = nil
	return err
}

func (c *containerdImageConfigs) openLocked() (*containerdclient.Client, error) {
	if c.closed {
		return nil, errors.New("containerd image config reader is closed")
	}
	c.once.Do(func() {
		c.client, c.openErr = containerdclient.New(c.socket)
	})
	if c.openErr != nil {
		return nil, c.openErr
	}
	if c.client == nil {
		return nil, errors.New("containerd client is not configured")
	}
	return c.client, nil
}

// configDigestMatches accepts a full digest or an unambiguous Docker-style
// prefix (clients commonly pass 12-character IDs).
func configDigestMatches(candidate, wanted string) bool {
	candidate = strings.TrimPrefix(candidate, "sha256:")
	wanted = strings.TrimPrefix(wanted, "sha256:")
	if candidate == "" || wanted == "" {
		return false
	}
	return candidate == wanted || (len(wanted) >= 12 && strings.HasPrefix(candidate, wanted))
}

// imageConfigFromSpec projects an OCI image config into the Docker-facing
// shape. Sets are sorted so inspect output is stable.
func imageConfigFromSpec(spec ocispec.Image) ports.ImageConfig {
	config := spec.Config
	return ports.ImageConfig{
		User:         config.User,
		ExposedPorts: sortedSetKeys(config.ExposedPorts),
		Env:          slices.Clone(config.Env),
		Entrypoint:   slices.Clone(config.Entrypoint),
		Cmd:          slices.Clone(config.Cmd),
		Volumes:      sortedSetKeys(config.Volumes),
		WorkingDir:   config.WorkingDir,
		Labels:       maps.Clone(config.Labels),
		StopSignal:   config.StopSignal,
	}
}

func sortedSetKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
