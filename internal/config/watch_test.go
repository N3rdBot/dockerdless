package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestStorePublishesValidReplacementAndRetainsInvalidReplacement(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfig(configPath, "socket-path: /tmp/dockerdless-initial.sock\nlog-level: info\n"); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	store, errorsSeen, changesSeen := startReloadTestStore(t, configPath)
	waitForSnapshot(t, store, func(cfg Config) bool {
		return cfg.SocketPath == "/tmp/dockerdless-initial.sock"
	})

	reloaded := publishValidReplacement(t, store, configPath, changesSeen)
	assertInvalidReplacementRetained(t, store, configPath, errorsSeen, reloaded)
}

func startReloadTestStore(t *testing.T, configPath string) (*Store, <-chan error, <-chan fsnotify.Event) {
	t.Helper()
	store := NewStoreWithConfigFile(configPath)
	errorsSeen := make(chan error, 4)
	changesSeen := make(chan fsnotify.Event, 4)
	store.SetErrorHandler(func(err error) {
		select {
		case errorsSeen <- err:
		default:
		}
	})
	store.OnConfigChange(func(event fsnotify.Event) {
		select {
		case changesSeen <- event:
		default:
		}
	})
	if err := store.WatchConfig(); err != nil {
		t.Fatalf("start config watch: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close config watch: %v", err)
		}
	})
	return store, errorsSeen, changesSeen
}

func publishValidReplacement(t *testing.T, store *Store, configPath string, changesSeen <-chan fsnotify.Event) Config {
	t.Helper()
	if err := writeConfigAtomically(configPath, "socket-path: /tmp/dockerdless-reloaded.sock\nlog-level: debug\n"); err != nil {
		t.Fatalf("write valid replacement: %v", err)
	}
	reloaded := waitForSnapshot(t, store, func(cfg Config) bool {
		return cfg.SocketPath == "/tmp/dockerdless-reloaded.sock" && cfg.LogLevel == "debug"
	})
	if reloaded.SocketPath != "/tmp/dockerdless-reloaded.sock" {
		t.Fatalf("valid replacement socket path = %q", reloaded.SocketPath)
	}
	select {
	case <-changesSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for config change callback")
	}
	return reloaded
}

func assertInvalidReplacementRetained(t *testing.T, store *Store, configPath string, errorsSeen <-chan error, reloaded Config) {
	t.Helper()
	if err := writeConfigAtomically(configPath, "socket-path: [\n"); err != nil {
		t.Fatalf("write invalid replacement: %v", err)
	}
	select {
	case err := <-errorsSeen:
		if err == nil {
			t.Fatal("invalid replacement reported a nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for invalid replacement error")
	}
	if got := store.Current(); got != reloaded {
		t.Fatalf("invalid replacement changed snapshot: got %#v, want %#v", got, reloaded)
	}
}

func TestStoreConcurrentReadersAndReloads(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfig(configPath, "socket-path: /tmp/dockerdless-0.sock\n"); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	store := NewStoreWithConfigFile(configPath)
	if err := store.WatchConfig(); err != nil {
		t.Fatalf("start config watch: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close config watch: %v", err)
		}
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 500 {
				cfg := store.Current()
				if err := cfg.Validate(); err != nil {
					t.Errorf("reader observed invalid snapshot: %v", err)
					return
				}
			}
		})
	}

	wg.Go(func() {
		for iteration := range 40 {
			sequence := iteration + 1
			content := fmt.Sprintf("socket-path: /tmp/dockerdless-%d.sock\nrequest-timeout: %ds\n", sequence, sequence+1)
			if err := writeConfigAtomically(configPath, content); err != nil {
				t.Errorf("write replacement %d: %v", sequence, err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	wg.Wait()
}

func TestStoreCloseStopsWatcher(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfig(configPath, "socket-path: /tmp/dockerdless-before-close.sock\n"); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	store := NewStoreWithConfigFile(configPath)
	changesSeen := make(chan struct{}, 1)
	store.OnConfigChange(func(_ fsnotify.Event) {
		select {
		case changesSeen <- struct{}{}:
		default:
		}
	})
	if err := store.WatchConfig(); err != nil {
		t.Fatalf("start config watch: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close config watch: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close config watch a second time: %v", err)
	}

	if err := writeConfigAtomically(configPath, "socket-path: /tmp/dockerdless-after-close.sock\n"); err != nil {
		t.Fatalf("write after close: %v", err)
	}
	select {
	case <-changesSeen:
		t.Fatal("config callback ran after watcher shutdown")
	case <-time.After(100 * time.Millisecond):
	}
	if err := store.WatchConfig(); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("WatchConfig() after Close() error = %v, want %v", err, ErrStoreClosed)
	}
}

func writeConfig(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}

func writeConfigAtomically(path, content string) error {
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write temporary config %q: %w", temporaryPath, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		cleanupErr := os.Remove(temporaryPath)
		if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			return fmt.Errorf("rename temporary config: %w; remove temporary config: %w", err, cleanupErr)
		}
		return fmt.Errorf("rename temporary config: %w", err)
	}
	return nil
}

func waitForSnapshot(t *testing.T, store *Store, predicate func(Config) bool) Config {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cfg := store.Current()
		if predicate(cfg) {
			return cfg
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for configuration snapshot; current = %#v", store.Current())
	return Config{}
}
