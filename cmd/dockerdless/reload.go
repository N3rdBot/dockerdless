package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/N3rdBot/dockerdless/internal/config"
	"github.com/N3rdBot/dockerdless/internal/observability"
	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const configFileEnvironment = "DOCKERDLESS_CONFIG_FILE"

func loadConfigStore() (*config.Store, error) {
	path := strings.TrimSpace(os.Getenv(configFileEnvironment))
	store := config.NewStoreWithConfigFile(path)
	if err := store.Load(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	return store, nil
}

func configFileConfigured() bool {
	return strings.TrimSpace(os.Getenv(configFileEnvironment)) != ""
}

func wireConfigReload(store *config.Store, logger *observability.Logger) func() {
	previous := store.Current()
	store.SetErrorHandler(func(err error) {
		logger.Error("configuration reload rejected; retaining previous snapshot", zap.Error(err))
	})
	store.OnConfigChange(func(_ fsnotify.Event) {
		current := store.Current()
		if current == previous {
			return
		}
		applyConfigChange(logger, previous, current)
		previous = current
	})
	return func() {
		if err := store.Close(); err != nil {
			logger.Warn("configuration watcher shutdown failed", zap.Error(err))
		}
	}
}

func applyConfigChange(logger *observability.Logger, previous, current config.Config) {
	if previous.LogLevel != current.LogLevel {
		level, err := zapcore.ParseLevel(current.LogLevel)
		if err != nil {
			logger.Error("configuration reload produced an invalid log level", zap.Error(err))
		} else {
			logger.SetLevel(level)
			logger.Info("configuration reloaded", zap.String("setting", "log-level"), zap.String("value", current.LogLevel))
		}
	}

	if previous.DefaultStopTimeout != current.DefaultStopTimeout {
		logger.Warn("configuration change requires restart", zap.String("setting", "default-stop-timeout"))
	}
	if previous.RequestTimeout != current.RequestTimeout {
		logger.Warn("configuration change requires restart", zap.String("setting", "request-timeout"))
	}
	for _, change := range startupOnlyChanges(previous, current) {
		logger.Warn("configuration change requires restart", zap.String("setting", change))
	}
}

func startupOnlyChanges(previous, current config.Config) []string {
	changes := make([]string, 0, 9)
	if previous.SocketPath != current.SocketPath {
		changes = append(changes, "socket-path")
	}
	if previous.OTelServiceName != current.OTelServiceName {
		changes = append(changes, "otel-service-name")
	}
	if previous.OTelEndpoint != current.OTelEndpoint {
		changes = append(changes, "otel-endpoint")
	}
	if previous.ContainerdNamespace != current.ContainerdNamespace {
		changes = append(changes, "containerd-namespace")
	}
	if previous.ContainerdSocket != current.ContainerdSocket {
		changes = append(changes, "containerd-socket")
	}
	if previous.CNIConfigDir != current.CNIConfigDir {
		changes = append(changes, "cni-config-dir")
	}
	if previous.CNIPluginDir != current.CNIPluginDir {
		changes = append(changes, "cni-plugin-dir")
	}
	if previous.BuildKitSocket != current.BuildKitSocket {
		changes = append(changes, "buildkit-socket")
	}
	if previous.EnableCRI != current.EnableCRI {
		changes = append(changes, "enable-cri")
	}
	if previous.EnableRootless != current.EnableRootless {
		changes = append(changes, "enable-rootless")
	}
	return changes
}
