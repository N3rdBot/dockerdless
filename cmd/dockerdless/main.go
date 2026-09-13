// Command dockerdless provides the Docker-compatible daemon entrypoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/N3rdBot/dockerdless/internal/adapters/buildkit"
	"github.com/N3rdBot/dockerdless/internal/adapters/cni"
	"github.com/N3rdBot/dockerdless/internal/adapters/containerd"
	"github.com/N3rdBot/dockerdless/internal/api"
	"github.com/N3rdBot/dockerdless/internal/app"
	"github.com/N3rdBot/dockerdless/internal/config"
	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/observability"
	"github.com/N3rdBot/dockerdless/internal/ports"
	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	buildkitclient "github.com/moby/buildkit/client"
	"go.uber.org/zap"
)

const (
	shutdownTimeout = 5 * time.Second
	// sharedContainerdNamespace is the namespace the local BuildKit worker
	// uses. Images built through BuildKit only become visible to the runtime
	// store when both adapters share it.
	sharedContainerdNamespace = "default"
	// logDirectoryName holds per-container log files beside the socket.
	logDirectoryName = "dockerdless-logs"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dockerdless: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("dockerdless", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: dockerdless [options]")
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Run the Docker-compatible dockerdless daemon.")
		fmt.Fprintln(flags.Output())
		flags.PrintDefaults()
	}
	help := flags.Bool("help", false, "print this help message")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse command-line flags: %w", err)
	}
	if *help {
		flags.Usage()
		return nil
	}

	configStore, err := loadConfigStore()
	if err != nil {
		return err
	}
	defer func() {
		_ = configStore.Close()
	}()
	cfg := configStore.Current()

	runtime, err := observability.Bootstrap(observability.BootstrapConfig{
		ServiceName:  cfg.OTelServiceName,
		LogLevel:     cfg.LogLevel,
		OTLPEndpoint: cfg.OTelEndpoint,
	})
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	logger := runtime.Logger()
	stopConfigReload := wireConfigReload(configStore, logger)
	defer stopConfigReload()
	if configFileConfigured() {
		if err := configStore.WatchConfig(); err != nil {
			return errors.Join(fmt.Errorf("watch configuration: %w", err), runtime.Shutdown(context.Background()))
		}
	}

	namespace := alignedNamespace(cfg.ContainerdNamespace, logger)
	service, registry, containerdClient, closeBackends, err := buildService(cfg, logger, namespace)
	if err != nil {
		return errors.Join(
			fmt.Errorf("initialize application: %w", err),
			runtime.Shutdown(context.Background()),
		)
	}
	defer closeBackends()
	if err := reconcileAtStartup(context.Background(), registry, logger, func(ctx context.Context) ([]domain.Container, []domain.TaskSnapshot, error) {
		return loadContainerdSnapshot(ctx, containerdClient, namespace)
	}); err != nil {
		logger.Warn("container state reconciliation failed; starting with an empty registry", zap.Error(err))
	}

	handler := api.NewHandler(api.Dependencies{
		Service: service,
		Logger:  logger.Logger,
	}, logger.Logger)

	server, err := api.NewServer(cfg.SocketPath, handler,
		api.WithShutdownTimeout(shutdownTimeout),
		api.WithSocketLogger(logger.Logger),
	)
	if err != nil {
		return errors.Join(
			fmt.Errorf("initialize API server: %w", err),
			runtime.Shutdown(context.Background()),
		)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("dockerdless daemon started",
		zap.String("socket", server.SocketPath()),
		zap.String("namespace", namespace),
		zap.String("log_dir", logDirFor(cfg.SocketPath)),
	)
	serveErr := server.Run(ctx)
	logger.Info("dockerdless daemon shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return errors.Join(serveErr, runtime.Shutdown(shutdownCtx))
}

// buildService constructs every adapter and the application service. The
// returned close function releases the backend clients.
func buildService(cfg config.Config, logger *observability.Logger, namespace string) (*app.Service, *domain.Registry, *containerdclient.Client, func(), error) {
	containerdClient, err := containerdclient.New(cfg.ContainerdSocket)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("connect to containerd at %s: %w", cfg.ContainerdSocket, err)
	}
	buildkitClient, err := buildkitclient.New(context.Background(), "unix://"+cfg.BuildKitSocket)
	if err != nil {
		_ = containerdClient.Close()
		return nil, nil, nil, nil, fmt.Errorf("connect to BuildKit at %s: %w", cfg.BuildKitSocket, err)
	}

	allocator := cni.NewPortAllocator()
	registry := domain.NewRegistry()
	service, err := app.New(app.Config{
		Runtime: app.NewRuntimeAdapter(containerd.New(containerdClient, containerd.WithNamespace(namespace))),
		Images: app.NewImageAdapter(buildkit.New(
			buildkit.NewContainerdStore(containerdClient, buildkit.StoreConfig{Namespace: namespace}),
			buildkit.NewBuildkitSolver(buildkitClient),
			buildkit.Options{Logger: logger.Logger},
		)),
		Networks: app.NewNetworkAdapter(cni.New(cni.Config{
			NetConfDir: cfg.CNIConfigDir,
			PluginDirs: []string{cfg.CNIPluginDir},
			Allocator:  allocator,
		})),
		Registry:         registry,
		Tasks:            &taskLocator{client: containerdClient, namespace: namespace},
		Allocator:        app.NewPortAllocator(allocator),
		StopTimeout:      cfg.DefaultStopTimeout,
		RequestTimeout:   cfg.RequestTimeout,
		LogDir:           logDirFor(cfg.SocketPath),
		Namespace:        namespace,
		ContainerdSocket: cfg.ContainerdSocket,
		BuildKitSocket:   cfg.BuildKitSocket,
		Logger:           logger.Logger,
	})
	if err != nil {
		_ = buildkitClient.Close()
		_ = containerdClient.Close()
		return nil, nil, nil, nil, err
	}
	return service, registry, containerdClient, func() {
		service.CloseLogSinks()
		_ = buildkitClient.Close()
		_ = containerdClient.Close()
	}, nil
}

// alignedNamespace keeps the configured namespace unless it is the built-in
// default, which does not match the local BuildKit worker.
func alignedNamespace(configured string, logger *observability.Logger) string {
	if configured != config.DefaultContainerdNamespace {
		return configured
	}
	logger.Info("aligning containerd namespace with the shared BuildKit worker",
		zap.String("configured", configured),
		zap.String("effective", sharedContainerdNamespace))
	return sharedContainerdNamespace
}

func logDirFor(socketPath string) string {
	return filepath.Join(filepath.Dir(socketPath), logDirectoryName)
}

// taskLocator resolves a running task PID through the containerd client so the
// CNI adapter can join the container network namespace.
type taskLocator struct {
	client    *containerdclient.Client
	namespace string
}

func (l *taskLocator) TaskPID(ctx context.Context, id domain.ContainerID) (int, error) {
	if l == nil || l.client == nil {
		return 0, fmt.Errorf("%w: containerd client is not configured", ports.ErrServerError)
	}
	ctx = namespaces.WithNamespace(ctx, l.namespace)
	container, err := l.client.LoadContainer(ctx, string(id))
	if err != nil {
		return 0, translateLookupError(err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return 0, translateLookupError(err)
	}
	return int(task.Pid()), nil
}

func translateLookupError(err error) error {
	if err == nil {
		return nil
	}
	if errdefs.IsNotFound(err) {
		return fmt.Errorf("%w: container task: %w", ports.ErrNotFound, err)
	}
	return fmt.Errorf("%w: container task lookup: %w", ports.ErrServerError, err)
}
