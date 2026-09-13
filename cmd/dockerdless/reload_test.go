package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/N3rdBot/dockerdless/internal/config"
	"github.com/N3rdBot/dockerdless/internal/observability"
)

func TestConfigReloadAppliesLogLevelThroughDaemonWiring(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadTestConfig(t, configPath, "info")

	store := config.NewStoreWithConfigFile(configPath)
	var output bytes.Buffer
	logger, err := observability.NewLogger("info", observability.WithLogOutput(&output))
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	defer func() { _ = logger.Sync() }()
	stopReload := wireConfigReload(store, logger)
	defer stopReload()
	if err := store.WatchConfig(); err != nil {
		t.Fatalf("watch config: %v", err)
	}

	logger.Debug("before reload")
	if strings.Contains(output.String(), "before reload") {
		t.Fatal("debug log was emitted before log-level reload")
	}

	writeReloadTestConfig(t, configPath, "debug")
	waitForReload(t, func() bool { return logger.Level().Enabled(zap.DebugLevel) })
	logger.Debug("after reload")

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
		t.Fatalf("decode log output: %v; output=%q", err, output.String())
	}
	if record["msg"] != "after reload" {
		t.Fatalf("last log message = %v, want after reload", record["msg"])
	}
}

func writeReloadTestConfig(t *testing.T, path, level string) {
	t.Helper()
	content := []byte("socket-path: /tmp/dockerdless-reload.sock\nlog-level: " + level + "\n")
	if err := os.WriteFile(path+".tmp", content, 0o600); err != nil {
		t.Fatalf("write temporary config: %v", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatalf("replace config: %v", err)
	}
}

func waitForReload(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for config reload")
}
