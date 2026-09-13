package containerd

import (
	"context"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/N3rdBot/dockerdless/internal/domain"
)

// Config is the Docker-shaped container configuration the adapter translates
// into an OCI runtime spec and containerd metadata.
//
// Docker HostConfig port bindings are intentionally absent: publishing ports
// requires CNI port mapping and dynamic host-port allocation, which the
// networking workstream owns (Task 8). The adapter reserves nothing for them so
// the runtime spec stays network-agnostic.
type Config struct {
	// ID is the Docker container identity. An empty ID makes the adapter
	// generate a 64-character lowercase hex identifier.
	ID string
	// Name is the Docker container name; names must be unique per namespace.
	Name string
	// Image is the image reference the container is created from.
	Image domain.ImageID
	// Command overrides the image CMD (Docker "Cmd").
	Command []string
	// Entrypoint overrides the image ENTRYPOINT (Docker "Entrypoint").
	Entrypoint []string
	// Env adds or replaces image environment variables.
	Env map[string]string
	// WorkingDir overrides the image working directory (Docker "WorkingDir").
	WorkingDir string
	// User overrides the image user. Only numeric "uid" and "uid:gid" values
	// are supported at spec-generation time; named users require a mounted
	// rootfs and are rejected with ErrInvalidArgument.
	User string
	// Hostname sets the container hostname. When empty the first 12
	// characters of the container ID are used, matching Docker.
	Hostname string
	// Labels become OCI annotations so they stay inspect-visible.
	Labels map[string]string
	// Mounts are bind, tmpfs, or volume mounts translated to OCI mounts.
	Mounts []Mount
	// Terminal allocates a TTY for the container process.
	Terminal bool
	// OpenStdin keeps the container stdin open independently of TTY state.
	OpenStdin bool
	// Snapshotter optionally overrides the containerd snapshotter.
	Snapshotter string
	// SnapshotID optionally overrides the snapshot key; it defaults to ID.
	SnapshotID string
}

// Mount describes one Docker mount translated into an OCI mount.
type Mount struct {
	// Type is the mount type: "bind" (default), "tmpfs", or "volume".
	Type string
	// Source is the host path or volume name.
	Source string
	// Destination is the absolute container path; it is required.
	Destination string
	// ReadOnly mounts the source read-only.
	ReadOnly bool
	// Options are additional mount options appended to the defaults.
	Options []string
}

// buildSpec translates the Docker-shaped Config plus the resolved image config
// into an OCI runtime spec. It applies image defaults (args, env, working
// directory, user) first and then the operator overrides, matching Docker's
// container create semantics. The namespace names the containerd namespace the
// container will live in; the OCI default spec derives its cgroups path from it.
func buildSpec(ctx context.Context, cfg Config, image ocispec.Image, namespace string) (*oci.Spec, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("%w: container id is required", ErrInvalidArgument)
	}
	if namespace == "" {
		namespace = namespaces.Default
	}
	if _, ok := namespaces.Namespace(ctx); !ok {
		ctx = namespaces.WithNamespace(ctx, namespace)
	}
	if strings.TrimSpace(string(cfg.Image)) == "" {
		return nil, fmt.Errorf("%w: image reference is required", ErrInvalidArgument)
	}
	args, userOpt, mounts, err := resolveSpecInputs(cfg, image)
	if err != nil {
		return nil, err
	}
	opts := buildSpecOpts(cfg, image, args, userOpt, mounts)
	meta := &containers.Container{ID: cfg.ID, Image: string(cfg.Image)}
	return oci.GenerateSpecWithPlatform(ctx, nil, runtime.GOOS+"/"+runtime.GOARCH, meta, opts...)
}

// resolveSpecInputs applies Docker's argument, user, and mount precedence.
func resolveSpecInputs(cfg Config, image ocispec.Image) ([]string, oci.SpecOpts, []specs.Mount, error) {
	args := resolveArgs(cfg.Entrypoint, cfg.Command, image.Config.Entrypoint, image.Config.Cmd)
	if len(args) == 0 {
		return nil, nil, nil, fmt.Errorf("%w: no command specified", ErrInvalidArgument)
	}
	user, err := resolveUser(cfg.User, image.Config.User)
	if err != nil {
		return nil, nil, nil, err
	}
	userOpt, err := userSpecOpt(user)
	if err != nil {
		return nil, nil, nil, err
	}
	mounts, err := translateMounts(cfg.Mounts)
	if err != nil {
		return nil, nil, nil, err
	}
	return args, userOpt, mounts, nil
}

// buildSpecOpts assembles the OCI spec options from the resolved inputs.
func buildSpecOpts(cfg Config, image ocispec.Image, args []string, userOpt oci.SpecOpts, mounts []specs.Mount) []oci.SpecOpts {
	opts := []oci.SpecOpts{
		oci.WithEnv(append([]string(nil), image.Config.Env...)),
		oci.WithEnv(envSlice(cfg.Env)),
		oci.WithProcessArgs(args...),
	}
	if userOpt != nil {
		opts = append(opts, userOpt)
	}
	if cwd := firstNonEmpty(cfg.WorkingDir, image.Config.WorkingDir); cwd != "" {
		opts = append(opts, oci.WithProcessCwd(cwd))
	}
	if hostname := containerHostname(cfg); hostname != "" {
		opts = append(opts, oci.WithHostname(hostname))
	}
	if len(cfg.Labels) > 0 {
		opts = append(opts, oci.WithAnnotations(cloneStringMap(cfg.Labels)))
	}
	if len(mounts) > 0 {
		opts = append(opts, oci.WithMounts(mounts))
	}
	if cfg.Terminal {
		opts = append(opts, oci.WithTTY)
	}
	return opts
}

// containerHostname returns the configured hostname or Docker's ID-derived
// default.
func containerHostname(cfg Config) string {
	if cfg.Hostname != "" {
		return cfg.Hostname
	}
	return shortHostname(cfg.ID)
}

// resolveArgs implements Docker's argument precedence: an explicit entrypoint
// replaces the image ENTRYPOINT, an explicit command replaces the image CMD,
// and the final argument vector is entrypoint followed by command.
func resolveArgs(entrypoint, command, imageEntrypoint, imageCmd []string) []string {
	resolvedEntrypoint := entrypoint
	if len(resolvedEntrypoint) == 0 {
		resolvedEntrypoint = imageEntrypoint
	}
	resolvedCommand := command
	if len(resolvedCommand) == 0 {
		resolvedCommand = imageCmd
	}
	args := make([]string, 0, len(resolvedEntrypoint)+len(resolvedCommand))
	args = append(args, resolvedEntrypoint...)
	args = append(args, resolvedCommand...)
	return args
}

// resolveUser picks the operator override or the image user.
func resolveUser(override, imageUser string) (string, error) {
	user := override
	if user == "" {
		user = imageUser
	}
	if user == "" {
		return "", nil
	}
	parts := strings.Split(user, ":")
	if len(parts) > 2 {
		return "", fmt.Errorf("%w: user %q: only numeric uid[:gid] is supported", ErrInvalidArgument, user)
	}
	for _, part := range parts {
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return "", fmt.Errorf("%w: user %q: only numeric uid[:gid] is supported", ErrInvalidArgument, user)
		}
	}
	return user, nil
}

// userSpecOpt builds a spec option that sets the numeric user directly. It
// avoids containerd's oci.WithUser, which resolves users against a mounted
// rootfs that does not exist yet at spec-generation time.
func userSpecOpt(user string) (oci.SpecOpts, error) {
	if user == "" {
		return nil, nil
	}
	parts := strings.Split(user, ":")
	parsedUID, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("%w: user %q: invalid uid", ErrInvalidArgument, user)
	}
	uid := uint32(parsedUID)
	var gid uint32
	if len(parts) == 2 {
		parsedGID, parseErr := strconv.ParseUint(parts[1], 10, 32)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: user %q: invalid gid", ErrInvalidArgument, user)
		}
		gid = uint32(parsedGID)
	}
	return func(_ context.Context, _ oci.Client, _ *containers.Container, spec *oci.Spec) error {
		if spec.Process == nil {
			spec.Process = &specs.Process{}
		}
		spec.Process.User.UID = uid
		spec.Process.User.GID = gid
		return nil
	}, nil
}

// envSlice renders environment overrides deterministically.
func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

// translateMounts converts Docker-style mounts to OCI mounts.
func translateMounts(mounts []Mount) ([]specs.Mount, error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	out := make([]specs.Mount, 0, len(mounts))
	for _, mount := range mounts {
		translated, err := translateMount(mount)
		if err != nil {
			return nil, err
		}
		out = append(out, translated)
	}
	return out, nil
}

// translateMount converts one Docker-style mount to an OCI mount.
func translateMount(mount Mount) (specs.Mount, error) {
	if !strings.HasPrefix(mount.Destination, "/") {
		return specs.Mount{}, fmt.Errorf("%w: mount destination %q must be absolute", ErrInvalidArgument, mount.Destination)
	}
	mountType := mount.Type
	if mountType == "" {
		mountType = "bind"
	}
	source := mount.Source
	if mountType == "tmpfs" && source == "" {
		source = "tmpfs"
	}
	return specs.Mount{
		Destination: mount.Destination,
		Type:        mountType,
		Source:      source,
		Options:     mountOptions(mount, mountType),
	}, nil
}

// mountOptions applies Docker's default bind propagation and access mode.
func mountOptions(mount Mount, mountType string) []string {
	options := append([]string(nil), mount.Options...)
	if mountType == "bind" && !slices.Contains(options, "bind") {
		options = appendIfMissing(options, "rbind")
	}
	if mount.ReadOnly {
		return appendIfMissing(options, "ro")
	}
	if mountType == "bind" {
		options = appendIfMissing(options, "rw")
	}
	return options
}

// appendIfMissing appends option unless it is already present.
func appendIfMissing(options []string, option string) []string {
	if slices.Contains(options, option) {
		return options
	}
	return append(options, option)
}

// shortHostname mirrors Docker's default hostname (first 12 ID characters).
func shortHostname(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// cloneStringMap copies a string map so callers cannot mutate adapter state.
func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
