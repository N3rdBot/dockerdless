package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	"go.uber.org/zap"
)

const (
	// DefaultStopTimeout bounds SIGTERM-to-SIGKILL escalation.
	DefaultStopTimeout = 10 * time.Second
	// DefaultRequestTimeout bounds a single non-streaming daemon request.
	DefaultRequestTimeout = 30 * time.Second
	// exitStatusTimeout bounds the exit-status probe used by inspect/list.
	exitStatusTimeout = 2 * time.Second
)

// Config configures the application service.
type Config struct {
	// Runtime is the containerd lifecycle adapter.
	Runtime ports.RuntimeController
	// Images is the BuildKit image adapter.
	Images ports.ImageController
	// Networks is the CNI network adapter.
	Networks ports.NetworkController
	// Registry is the in-memory Docker identity index.
	Registry *domain.Registry
	// Tasks resolves running task PIDs for network namespace attachment.
	Tasks ports.TaskLocator
	// Allocator reserves concrete host ports before CNI port mapping.
	Allocator ports.PortAllocator
	// StopTimeout is the default stop escalation timeout.
	StopTimeout time.Duration
	// RequestTimeout bounds one non-streaming request.
	RequestTimeout time.Duration
	// LogDir is the directory holding per-container log files.
	LogDir string
	// Namespace is the active containerd namespace, reported by /info.
	Namespace string
	// Snapshotter is the active containerd snapshotter, reported by /info.
	Snapshotter string
	// ContainerdSocket and BuildKitSocket are the backend socket paths
	// reported by /info.
	ContainerdSocket string
	BuildKitSocket   string
	// ImageConfigs optionally resolves OCI image configuration for inspect
	// responses. Nil uses a containerd-backed reader when ContainerdSocket is
	// set; tests may inject a fake.
	ImageConfigs ports.ImageConfigReader
	// Logger receives service logs; nil uses a no-op logger.
	Logger *zap.Logger
	// Clock supplies the current time; nil uses time.Now.
	Clock func() time.Time
}

// Service is the dockerdless use-case layer. It composes the domain registry,
// the runtime/image/network adapters, and the stream helpers.
type Service struct {
	runtime  ports.RuntimeController
	images   ports.ImageController
	networks ports.NetworkController
	registry *domain.Registry
	tasks    ports.TaskLocator
	alloc    ports.PortAllocator

	stopTimeout      time.Duration
	requestTimeout   time.Duration
	logDir           string
	namespace        string
	snapshotter      string
	containerdSocket string
	buildkitSocket   string
	logger           *zap.Logger
	now              func() time.Time

	imageConfigs      ports.ImageConfigReader
	closeImageConfigs func() error

	allocMu sync.Mutex
	execMu  sync.Mutex
	execs   map[string]*pendingExec
	logMu   sync.Mutex
	logs    map[domain.ContainerID]*logSink
}

// New validates dependencies and constructs the service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Runtime == nil:
		return nil, errors.New("application runtime port is required")
	case cfg.Images == nil:
		return nil, errors.New("application image port is required")
	case cfg.Networks == nil:
		return nil, errors.New("application network port is required")
	case cfg.Registry == nil:
		return nil, errors.New("application registry is required")
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = DefaultStopTimeout
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if strings.TrimSpace(cfg.LogDir) == "" {
		cfg.LogDir = "dockerdless-logs"
	}
	service := &Service{
		runtime:          cfg.Runtime,
		images:           cfg.Images,
		networks:         cfg.Networks,
		registry:         cfg.Registry,
		tasks:            cfg.Tasks,
		alloc:            cfg.Allocator,
		stopTimeout:      cfg.StopTimeout,
		requestTimeout:   cfg.RequestTimeout,
		logDir:           cfg.LogDir,
		namespace:        cfg.Namespace,
		snapshotter:      cfg.Snapshotter,
		containerdSocket: cfg.ContainerdSocket,
		buildkitSocket:   cfg.BuildKitSocket,
		logger:           cfg.Logger,
		now:              cfg.Clock,
		execs:            make(map[string]*pendingExec),
		logs:             make(map[domain.ContainerID]*logSink),
	}
	switch {
	case cfg.ImageConfigs != nil:
		service.imageConfigs = cfg.ImageConfigs
	case strings.TrimSpace(cfg.ContainerdSocket) != "":
		reader := newContainerdImageConfigs(cfg.ContainerdSocket, cfg.Namespace)
		service.imageConfigs = reader
		service.closeImageConfigs = reader.Close
	}
	return service, nil
}

// Logger returns the service logger.
func (s *Service) Logger() *zap.Logger { return s.logger }

// Info returns daemon-wide counters and backend identity.
func (s *Service) Info(ctx context.Context) (ports.SystemStatus, error) {
	containers, err := s.ContainerList(ctx, true)
	if err != nil {
		return ports.SystemStatus{}, err
	}
	status := ports.SystemStatus{
		Containers:       len(containers),
		Namespace:        s.namespace,
		Snapshotter:      s.snapshotter,
		ContainerdSocket: s.containerdSocket,
		BuildKitSocket:   s.buildkitSocket,
	}
	for _, container := range containers {
		switch container.State {
		case domain.ContainerStateRunning:
			status.ContainersRunning++
		case domain.ContainerStatePaused:
			status.ContainersPaused++
		default:
			status.ContainersStopped++
		}
	}
	images, err := s.images.List(ctx)
	if err != nil {
		status.Warnings = append(status.Warnings, fmt.Sprintf("image list failed: %v", err))
	} else {
		status.Images = len(images)
	}
	return status, nil
}

func (s *Service) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.requestTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.requestTimeout)
}

func (s *Service) cloneBindings(bindings []domain.PortBinding) []domain.PortBinding {
	return append([]domain.PortBinding(nil), bindings...)
}

func cloneLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	maps.Copy(out, labels)
	return out
}

func (s *Service) reservationsFor(bindings []domain.PortBinding) []ports.PortReservation {
	reservations := make([]ports.PortReservation, 0, len(bindings))
	for _, binding := range bindings {
		reservations = append(reservations, ports.PortReservation{
			Protocol: binding.Protocol,
			HostIP:   binding.HostIP,
			HostPort: binding.HostPort,
		})
	}
	return reservations
}

func (s *Service) releaseBindings(bindings []domain.PortBinding) {
	if s.alloc == nil || len(bindings) == 0 {
		return
	}
	s.allocMu.Lock()
	s.alloc.Release(s.reservationsFor(bindings)...)
	s.allocMu.Unlock()
}

// resolveContainer finds a container by full ID, name, or unique ID prefix.
func (s *Service) resolveContainer(ctx context.Context, ref string) (domain.Container, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "/")
	if ref == "" {
		return domain.Container{}, invalidError("container id or name must not be empty")
	}
	if container, err := s.registry.Get(ctx, domain.ContainerID(ref)); err == nil {
		return container, nil
	} else if !errors.Is(err, domain.ErrContainerNotFound) {
		return domain.Container{}, translateError(err)
	}
	if container, err := s.registry.GetByName(ref); err == nil {
		return container, nil
	}
	if len(ref) >= 3 {
		var (
			found   domain.Container
			matches int
		)
		for _, container := range s.registry.List() {
			if strings.HasPrefix(string(container.ID), ref) {
				found = container
				matches++
			}
		}
		switch matches {
		case 1:
			return found, nil
		case 0:
		default:
			return domain.Container{}, invalidError("multiple containers match prefix %s", ref)
		}
	}
	return domain.Container{}, notFoundError("No such container: %s", ref)
}

func (s *Service) save(ctx context.Context, container domain.Container) {
	container.UpdatedAt = s.now()
	if err := s.registry.Save(ctx, container); err != nil {
		s.logger.Warn("failed to persist container state",
			zap.String("container_id", string(container.ID)),
			zap.Error(err))
	}
}
