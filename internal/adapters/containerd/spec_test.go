package containerd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func testImageConfig() ocispec.Image {
	return ocispec.Image{
		Config: ocispec.ImageConfig{
			Env:        []string{"PATH=/usr/bin:/bin", "LANG=C"},
			Entrypoint: []string{"/bin/sh"},
			Cmd:        []string{"-c", "echo default"},
			WorkingDir: "/image/workdir",
			User:       "65534",
		},
	}
}

func TestBuildSpecTranslatesDockerConfig(t *testing.T) {
	cfg := Config{
		ID:         "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Image:      "docker.io/library/alpine:latest",
		Command:    []string{"echo", "hi"},
		Env:        map[string]string{"FOO": "bar"},
		WorkingDir: "/override",
		User:       "1000:1000",
		Hostname:   "custom-host",
		Labels:     map[string]string{"app": "demo"},
		Mounts: []Mount{
			{Type: "bind", Source: "/host/data", Destination: "/data", ReadOnly: true},
			{Type: "tmpfs", Destination: "/tmp"},
		},
		Terminal: true,
	}
	spec, err := buildSpec(context.Background(), cfg, testImageConfig(), "testns")
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.Process == nil {
		t.Fatal("spec process is nil")
	}
	wantArgs := []string{"/bin/sh", "echo", "hi"}
	if !slices.Equal(spec.Process.Args, wantArgs) {
		t.Fatalf("args = %v, want %v", spec.Process.Args, wantArgs)
	}
	if got := countEnv(spec.Process.Env, "FOO=bar"); got != 1 {
		t.Fatalf("FOO=bar count = %d, want 1 (env %v)", got, spec.Process.Env)
	}
	if got := countEnv(spec.Process.Env, "PATH=/usr/bin:/bin"); got != 1 {
		t.Fatalf("image PATH was not preserved: %v", spec.Process.Env)
	}
	if spec.Process.Cwd != "/override" {
		t.Fatalf("cwd = %q, want /override", spec.Process.Cwd)
	}
	if spec.Process.User.UID != 1000 || spec.Process.User.GID != 1000 {
		t.Fatalf("user = %d:%d, want 1000:1000", spec.Process.User.UID, spec.Process.User.GID)
	}
	if spec.Hostname != "custom-host" {
		t.Fatalf("hostname = %q, want custom-host", spec.Hostname)
	}
	if got := spec.Annotations["app"]; got != "demo" {
		t.Fatalf("annotation app = %q, want demo", got)
	}
	if !spec.Process.Terminal {
		t.Fatal("terminal was not set")
	}
	if len(spec.Mounts) < 2 {
		t.Fatalf("mounts = %v, want the two translated mounts", spec.Mounts)
	}
	dataMount := findMount(spec.Mounts, "/data")
	if dataMount == nil {
		t.Fatalf("mount /data missing from %v", spec.Mounts)
	}
	if dataMount.Type != "bind" || dataMount.Source != "/host/data" {
		t.Fatalf("bind mount = %+v", dataMount)
	}
	if !slices.Contains(dataMount.Options, "rbind") || !slices.Contains(dataMount.Options, "ro") {
		t.Fatalf("bind mount options = %v, want rbind+ro", dataMount.Options)
	}
	tmpMount := findMount(spec.Mounts, "/tmp")
	if tmpMount == nil || tmpMount.Type != "tmpfs" {
		t.Fatalf("tmpfs mount = %+v", tmpMount)
	}
	if spec.Linux == nil || !strings.Contains(spec.Linux.CgroupsPath, "testns") {
		t.Fatalf("cgroups path %+v does not carry the namespace", spec.Linux)
	}
}

func TestBuildSpecAppliesDockerArgumentPrecedence(t *testing.T) {
	image := ocispec.Image{Config: ocispec.ImageConfig{
		Entrypoint: []string{"entry"},
		Cmd:        []string{"cmd"},
	}}
	tests := []struct {
		name       string
		entrypoint []string
		command    []string
		want       []string
	}{
		{name: "image defaults", want: []string{"entry", "cmd"}},
		{name: "entrypoint override", entrypoint: []string{"custom-entry"}, want: []string{"custom-entry", "cmd"}},
		{name: "command override", command: []string{"custom-cmd"}, want: []string{"entry", "custom-cmd"}},
		{name: "both overridden", entrypoint: []string{"e2"}, command: []string{"c2"}, want: []string{"e2", "c2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := buildSpec(context.Background(), Config{
				ID:         "id-1",
				Image:      "image",
				Entrypoint: test.entrypoint,
				Command:    test.command,
			}, image, "testns")
			if err != nil {
				t.Fatalf("buildSpec: %v", err)
			}
			if !slices.Equal(spec.Process.Args, test.want) {
				t.Fatalf("args = %v, want %v", spec.Process.Args, test.want)
			}
		})
	}
}

func TestBuildSpecUsesImageDefaults(t *testing.T) {
	spec, err := buildSpec(context.Background(), Config{
		ID:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Image: "image",
	}, testImageConfig(), "testns")
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.Process.Cwd != "/image/workdir" {
		t.Fatalf("cwd = %q, want image working dir", spec.Process.Cwd)
	}
	if spec.Process.User.UID != 65534 {
		t.Fatalf("uid = %d, want image user 65534", spec.Process.User.UID)
	}
	if spec.Hostname != "0123456789ab" {
		t.Fatalf("hostname = %q, want first 12 ID characters", spec.Hostname)
	}
}

func TestBuildSpecAppliesNamespaceToContext(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "context-ns")
	spec, err := buildSpec(ctx, Config{ID: "id-1", Image: "image"}, ocispec.Image{
		Config: ocispec.ImageConfig{Cmd: []string{"true"}},
	}, "argument-ns")
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if !strings.Contains(spec.Linux.CgroupsPath, "context-ns") {
		t.Fatalf("cgroups path %q does not prefer the context namespace", spec.Linux.CgroupsPath)
	}
}

func TestBuildSpecRejectsInvalidConfig(t *testing.T) {
	image := ocispec.Image{Config: ocispec.ImageConfig{Cmd: []string{"true"}}}
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "missing id", cfg: Config{Image: "image"}},
		{name: "missing image", cfg: Config{ID: "id-1"}},
		{name: "no command", cfg: Config{ID: "id-1", Image: "image"}},
		{name: "named user", cfg: Config{ID: "id-1", Image: "image", User: "root"}},
		{name: "relative mount", cfg: Config{ID: "id-1", Image: "image", Mounts: []Mount{{Destination: "relative"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			imageConfig := image
			if test.name == "no command" {
				imageConfig = ocispec.Image{}
			}
			_, err := buildSpec(context.Background(), test.cfg, imageConfig, "testns")
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func countEnv(env []string, want string) int {
	count := 0
	for _, value := range env {
		if value == want {
			count++
		}
	}
	return count
}

func findMount(mounts []specs.Mount, destination string) *specs.Mount {
	for index := range mounts {
		if mounts[index].Destination == destination {
			return &mounts[index]
		}
	}
	return nil
}
