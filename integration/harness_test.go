//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// TestMain disables the Testcontainers reaper before any testcontainers code
// can read its configuration, and removes the shared daemon build directory
// after the whole package finished. The MVP deliberately runs without Ryuk:
// not enabling it is part of the contract these tests prove.
func TestMain(m *testing.M) {
	// TESTCONTAINERS_RYUK_DISABLED is read once by testcontainers' config
	// singleton, so it must be set before the first test body runs.
	os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")

	code := m.Run()
	cleanupSharedBuild()
	os.Exit(code)
}

// TestIntegrationPrerequisites is the harness self-test. It records the exact
// environment it detected, then builds and runs the REAL daemon binary on an
// ephemeral Unix socket, drives ping/version over that socket, and proves the
// cleanup path removes the socket again.
func TestIntegrationPrerequisites(t *testing.T) {
	prerequisites := detectPrerequisites()
	prerequisites.require(t)
	t.Logf("prerequisites: euid=%d containerd=%s buildkit=%s cni=%s namespace=%s",
		prerequisites.euid,
		prerequisites.containerdSocket,
		prerequisites.buildkitSocket,
		prerequisites.cniPluginDir,
		prerequisites.namespace,
	)

	daemon := startDaemon(t, prerequisites)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ping, err := daemon.Client().Ping(ctx, client.PingOptions{})
	if err != nil {
		t.Fatalf("ping daemon: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if ping.APIVersion != "1.44" {
		t.Fatalf("ping api version = %q, want %q", ping.APIVersion, "1.44")
	}
	t.Logf("daemon ping: api=%s os=%s experimental=%t", ping.APIVersion, ping.OSType, ping.Experimental)

	info, err := daemon.Client().Info(ctx, client.InfoOptions{})
	if err != nil {
		t.Fatalf("daemon info: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if info.Info.ServerVersion == "" {
		t.Fatalf("daemon info carried no server version: %+v", info.Info)
	}
	t.Logf("daemon info: server_version=%s os=%s arch=%s", info.Info.ServerVersion, info.Info.OperatingSystem, info.Info.Architecture)

	socketPath := daemon.SocketPath()
	if err := daemon.Stop(ctx); err != nil {
		t.Fatalf("stop daemon: %v\n--- daemon logs ---\n%s", err, daemon.Logs())
	}
	if _, statErr := os.Stat(socketPath); !os.IsNotExist(statErr) {
		t.Fatalf("socket %s still present after daemon stop (stat error %v)", socketPath, statErr)
	}
	t.Logf("daemon stopped cleanly; socket %s removed", socketPath)
}
