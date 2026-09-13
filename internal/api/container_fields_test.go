package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// TestContainerCreateFieldPolicyIsExhaustive reflects over every field of
// container.Config and container.HostConfig, walking the embedded Resources
// struct and resolving the JSON wire name each field is sent under, and fails
// when a field has no explicit handled/rejected/ignored classification. A
// future moby bump that adds a create field therefore breaks this test instead
// of silently dropping the field.
func TestContainerCreateFieldPolicyIsExhaustive(t *testing.T) {
	t.Run("Config", func(t *testing.T) {
		assertFieldPolicyComplete(t, reflect.TypeOf(container.Config{}), configFieldPolicy)
	})
	t.Run("HostConfig", func(t *testing.T) {
		assertFieldPolicyComplete(t, reflect.TypeOf(container.HostConfig{}), hostConfigFieldPolicy)
	})
}

// assertFieldPolicyComplete proves the policy map covers every wire field of
// structType (including fields promoted from an embedded struct) and that it
// carries no stale entry for a field the type no longer has.
func assertFieldPolicyComplete(t *testing.T, structType reflect.Type, policy map[string]fieldPolicy) {
	t.Helper()

	fields := wireFieldNames(structType)
	if len(fields) == 0 {
		t.Fatalf("reflection discovered no fields for %s", structType.Name())
	}
	covered := make(map[string]struct{}, len(fields))
	for _, name := range fields {
		covered[name] = struct{}{}
		if _, ok := policy[name]; !ok {
			t.Errorf("%s.%s is unclassified; add it to the field policy map", structType.Name(), name)
		}
	}
	for name := range policy {
		if _, ok := covered[name]; !ok {
			t.Errorf("field policy declares %s.%s but the moby type no longer has it", structType.Name(), name)
		}
	}

	valid := map[fieldPolicy]struct{}{fieldHandled: {}, fieldRejected: {}, fieldIgnored: {}}
	for name, classification := range policy {
		if _, ok := valid[classification]; !ok {
			t.Errorf("%s.%s has invalid classification %d", structType.Name(), name, classification)
		}
	}
}

// wireFieldNames returns the JSON names every exported field is encoded under,
// flattening an embedded struct exactly as encoding/json does. Non-anonymous
// struct fields keep their nested name and are not walked.
func wireFieldNames(structType reflect.Type) []string {
	var names []string
	for i := range structType.NumField() {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		name, skip := jsonFieldName(field)
		if skip {
			continue
		}
		if field.Anonymous {
			embedded := field.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct && name == field.Name {
				names = append(names, wireFieldNames(embedded)...)
				continue
			}
		}
		names = append(names, name)
	}
	return names
}

// jsonFieldName resolves the wire name for one struct field the way
// encoding/json does: an explicit tag wins, a bare tag falls back to the Go
// field name, and `json:"-"` is skipped.
func jsonFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	return name, false
}

// TestValidateContainerCreate_rejectsUnclassifiedSemanticFields pins the new
// 501 rejections for the fields the daemon used to drop silently.
func TestValidateContainerCreate_rejectsUnclassifiedSemanticFields(t *testing.T) {
	tests := []struct {
		name    string
		config  *container.Config
		host    *container.HostConfig
		message string
	}{
		{name: "Config.Domainname", config: &container.Config{Domainname: "example.com"}, message: "Config.Domainname is not supported"},
		{name: "Config.ExposedPorts", config: &container.Config{ExposedPorts: network.PortSet{mustTestPort(t, "8080/tcp"): {}}}, message: "Config.ExposedPorts is not supported"},
		{name: "Config.Healthcheck", config: &container.Config{Healthcheck: &container.HealthConfig{Test: []string{"CMD", "true"}}}, message: "Config.Healthcheck is not supported"},
		{name: "Config.Volumes", config: &container.Config{Volumes: map[string]struct{}{"/data": {}}}, message: "Config.Volumes is not supported"},
		{name: "Config.NetworkDisabled", config: &container.Config{NetworkDisabled: true}, message: "Config.NetworkDisabled is not supported"},
		{name: "Config.OnBuild", config: &container.Config{OnBuild: []string{"RUN true"}}, message: "Config.OnBuild is not supported"},
		{name: "Config.StopSignal", config: &container.Config{StopSignal: "SIGKILL"}, message: "Config.StopSignal is not supported"},
		{name: "Config.StopTimeout", config: &container.Config{StopTimeout: testIntPointer(5)}, message: "Config.StopTimeout is not supported"},
		{name: "HostConfig.ContainerIDFile", host: &container.HostConfig{ContainerIDFile: "/tmp/cid"}, message: "HostConfig.ContainerIDFile is not supported"},
		{name: "HostConfig.VolumeDriver", host: &container.HostConfig{VolumeDriver: "local"}, message: "HostConfig.VolumeDriver is not supported"},
		{name: "HostConfig.Annotations", host: &container.HostConfig{Annotations: map[string]string{"k": "v"}}, message: "HostConfig.Annotations is not supported"},
		{name: "HostConfig.Cgroup", host: &container.HostConfig{Cgroup: "container:abc"}, message: "HostConfig.Cgroup is not supported"},
		{name: "HostConfig.Links", host: &container.HostConfig{Links: []string{"db:database"}}, message: "HostConfig.Links is not supported"},
		{name: "HostConfig.StorageOpt", host: &container.HostConfig{StorageOpt: map[string]string{"size": "1G"}}, message: "HostConfig.StorageOpt is not supported"},
		{name: "HostConfig.Umask", host: &container.HostConfig{Umask: testUint32Pointer(0o022)}, message: "HostConfig.Umask is not supported"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			payload := &container.CreateRequest{
				Config:     orDefaultConfig(testCase.config),
				HostConfig: testCase.host,
			}

			got := validateContainerCreate(payload)

			if got == nil || got.Status != http.StatusNotImplemented || got.Message != testCase.message {
				t.Fatalf("unexpected validation error: %#v, want 501 %q", got, testCase.message)
			}
		})
	}
}

// TestValidateContainerCreate_acceptsPublishedExposedPorts proves the
// conditional Config.ExposedPorts policy: exposure that is honored through
// HostConfig.PortBindings is accepted (testcontainers-go always pairs them),
// while exposure with no binding is rejected above.
func TestValidateContainerCreate_acceptsPublishedExposedPorts(t *testing.T) {
	port := mustTestPort(t, "8080/tcp")
	payload := &container.CreateRequest{
		Config: &container.Config{Image: "alpine", ExposedPorts: network.PortSet{port: {}}},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{port: []network.PortBinding{{HostPort: "0"}}},
		},
	}

	if got := validateContainerCreate(payload); got != nil {
		t.Fatalf("unexpected validation error: %#v", got)
	}
}

// TestValidateContainerCreate_mountSourceValidation pins the 400 rejections for
// structured mounts with a missing/invalid source and for malformed Binds.
func TestValidateContainerCreate_mountSourceValidation(t *testing.T) {
	tests := []struct {
		name string
		host *container.HostConfig
	}{
		{name: "bind empty source", host: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "", Target: "/data"}}}},
		{name: "bind relative source", host: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "relative/path", Target: "/data"}}}},
		{name: "bind default type relative source", host: &container.HostConfig{Mounts: []mount.Mount{{Source: "relative/path", Target: "/data"}}}},
		{name: "tmpfs with source", host: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeTmpfs, Source: "/tmp", Target: "/data"}}}},
		{name: "binds empty source", host: &container.HostConfig{Binds: []string{":/data"}}},
		{name: "binds empty destination", host: &container.HostConfig{Binds: []string{"/tmp:"}}},
		{name: "binds missing colon", host: &container.HostConfig{Binds: []string{"/tmp/data"}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			payload := &container.CreateRequest{Config: &container.Config{Image: "alpine"}, HostConfig: testCase.host}

			got := validateContainerCreate(payload)

			if got == nil || got.Status != http.StatusBadRequest {
				t.Fatalf("unexpected validation error: %#v, want 400", got)
			}
		})
	}
}

// TestValidateContainerCreate_acceptsValidMounts proves the bind/tmpfs
// translation still passes validation after source checks were added.
func TestValidateContainerCreate_acceptsValidMounts(t *testing.T) {
	payload := &container.CreateRequest{
		Config: &container.Config{Image: "alpine"},
		HostConfig: &container.HostConfig{
			Binds: []string{"/tmp:/data:ro"},
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: "/tmp", Target: "/mnt"},
				{Type: mount.TypeTmpfs, Source: "", Target: "/scratch"},
			},
		},
	}

	if got := validateContainerCreate(payload); got != nil {
		t.Fatalf("unexpected validation error: %#v", got)
	}
}

// TestRequestedMounts_keepsValidEntries proves validated Binds and structured
// mounts still translate into the port DTO.
func TestRequestedMounts_keepsValidEntries(t *testing.T) {
	hostConfig := &container.HostConfig{
		Binds:  []string{"/tmp:/data:ro"},
		Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/var", Target: "/mnt"}},
	}

	mounts := requestedMounts(hostConfig)

	if len(mounts) != 2 {
		t.Fatalf("expected two translated mounts, got %#v", mounts)
	}
	if mounts[0].Source != "/tmp" || mounts[0].Destination != "/data" || !mounts[0].ReadOnly {
		t.Fatalf("unexpected bind translation: %#v", mounts[0])
	}
	if mounts[1].Source != "/var" || mounts[1].Destination != "/mnt" {
		t.Fatalf("unexpected structured translation: %#v", mounts[1])
	}
}

func mustTestPort(t *testing.T, value string) network.Port {
	t.Helper()
	port, err := network.ParsePort(value)
	if err != nil {
		t.Fatalf("parse test port %q: %v", value, err)
	}
	return port
}

func testIntPointer(value int) *int {
	return &value
}

func testUint32Pointer(value uint32) *uint32 {
	return &value
}

func orDefaultConfig(cfg *container.Config) *container.Config {
	if cfg != nil {
		return cfg
	}
	return &container.Config{Image: "alpine"}
}
