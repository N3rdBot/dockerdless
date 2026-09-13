package api

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// fieldPolicy classifies every field of a Docker container-create request as
// one of handled, rejected, or documented-ignored. The completeness test in
// container_fields_test.go reflects over the moby types and fails when a field
// is missing here, so a new moby field cannot be silently dropped.
type fieldPolicy uint8

const (
	// fieldHandled means the daemon applies the field's semantics.
	fieldHandled fieldPolicy = iota
	// fieldRejected means a non-default value fails the create with HTTP 501.
	fieldRejected
	// fieldIgnored means the value is accepted and deliberately not applied;
	// the compatibility doc lists every ignored field and why.
	fieldIgnored
)

// configFieldPolicy is keyed by the API JSON name of each container.Config
// field.
//
// Config.ExposedPorts is classified rejected because exposure-only ports
// cannot be represented; when every exposed port is also published through
// HostConfig.PortBindings, the requested exposure is honored by the port
// machinery and the request is accepted.
var configFieldPolicy = map[string]fieldPolicy{
	"Hostname":        fieldHandled,
	"Domainname":      fieldRejected,
	"User":            fieldHandled,
	"AttachStdin":     fieldIgnored,
	"AttachStdout":    fieldIgnored,
	"AttachStderr":    fieldIgnored,
	"ExposedPorts":    fieldRejected,
	"Tty":             fieldHandled,
	"OpenStdin":       fieldHandled,
	"StdinOnce":       fieldIgnored,
	"Env":             fieldHandled,
	"Cmd":             fieldHandled,
	"Healthcheck":     fieldRejected,
	"ArgsEscaped":     fieldIgnored,
	"Image":           fieldHandled,
	"Volumes":         fieldRejected,
	"WorkingDir":      fieldHandled,
	"Entrypoint":      fieldHandled,
	"NetworkDisabled": fieldRejected,
	"OnBuild":         fieldRejected,
	"Labels":          fieldHandled,
	"StopSignal":      fieldRejected,
	"StopTimeout":     fieldRejected,
	"Shell":           fieldIgnored,
}

// hostConfigFieldPolicy is keyed by the API JSON name of each
// container.HostConfig field, including the fields promoted from the embedded
// container.Resources struct. Every Resources field is classified rejected
// because the aggregate struct is rejected as one unit with the single
// "HostConfig.Resources is not supported" response.
var hostConfigFieldPolicy = map[string]fieldPolicy{
	"Binds":           fieldHandled,
	"ContainerIDFile": fieldRejected,
	"LogConfig":       fieldIgnored,
	"NetworkMode":     fieldHandled,
	"PortBindings":    fieldHandled,
	"RestartPolicy":   fieldRejected,
	"AutoRemove":      fieldIgnored,
	"VolumeDriver":    fieldRejected,
	"VolumesFrom":     fieldRejected,
	"ConsoleSize":     fieldIgnored,
	"Annotations":     fieldRejected,

	"CapAdd":          fieldRejected,
	"CapDrop":         fieldRejected,
	"CgroupnsMode":    fieldRejected,
	"Dns":             fieldIgnored,
	"DnsOptions":      fieldIgnored,
	"DnsSearch":       fieldIgnored,
	"ExtraHosts":      fieldIgnored,
	"GroupAdd":        fieldIgnored,
	"IpcMode":         fieldRejected,
	"Cgroup":          fieldRejected,
	"Links":           fieldRejected,
	"OomScoreAdj":     fieldRejected,
	"PidMode":         fieldRejected,
	"Privileged":      fieldRejected,
	"PublishAllPorts": fieldIgnored,
	"ReadonlyRootfs":  fieldRejected,
	"SecurityOpt":     fieldRejected,
	"StorageOpt":      fieldRejected,
	"Tmpfs":           fieldIgnored,
	"UTSMode":         fieldRejected,
	"UsernsMode":      fieldRejected,
	"ShmSize":         fieldIgnored,
	"Sysctls":         fieldRejected,
	"Runtime":         fieldRejected,
	"Umask":           fieldRejected,

	"Isolation": fieldIgnored,

	"Mounts":        fieldHandled,
	"MaskedPaths":   fieldRejected,
	"ReadonlyPaths": fieldRejected,
	"Init":          fieldIgnored,

	// Promoted from the embedded container.Resources; rejected as a unit.
	"CpuShares":            fieldRejected,
	"Memory":               fieldRejected,
	"NanoCpus":             fieldRejected,
	"CgroupParent":         fieldRejected,
	"BlkioWeight":          fieldRejected,
	"BlkioWeightDevice":    fieldRejected,
	"BlkioDeviceReadBps":   fieldRejected,
	"BlkioDeviceWriteBps":  fieldRejected,
	"BlkioDeviceReadIOps":  fieldRejected,
	"BlkioDeviceWriteIOps": fieldRejected,
	"CpuPeriod":            fieldRejected,
	"CpuQuota":             fieldRejected,
	"CpuRealtimePeriod":    fieldRejected,
	"CpuRealtimeRuntime":   fieldRejected,
	"CpusetCpus":           fieldRejected,
	"CpusetMems":           fieldRejected,
	"Devices":              fieldRejected,
	"DeviceCgroupRules":    fieldRejected,
	"DeviceRequests":       fieldRejected,
	"MemoryReservation":    fieldRejected,
	"MemorySwap":           fieldRejected,
	"MemorySwappiness":     fieldRejected,
	"OomKillDisable":       fieldRejected,
	"PidsLimit":            fieldRejected,
	"Ulimits":              fieldRejected,
	"CpuCount":             fieldRejected,
	"CpuPercent":           fieldRejected,
	"IOMaximumIOps":        fieldRejected,
	"IOMaximumBandwidth":   fieldRejected,
}

// validateContainerCreate enforces the single create-path field policy. Every
// field is classified handled, rejected, or ignored; rejected fields fail with
// a Docker 501 before the application service is called, so a create never
// falsely reports success for a request the daemon cannot honor.
func validateContainerCreate(payload *container.CreateRequest) *DockerError {
	if payload == nil {
		return nil
	}
	if apiErr := validateConfigFields(payload.Config, payload.HostConfig); apiErr != nil {
		return apiErr
	}
	if payload.HostConfig == nil {
		return nil
	}
	return validateHostConfigFields(payload.HostConfig)
}

func validateConfigFields(config *container.Config, hostConfig *container.HostConfig) *DockerError {
	if config == nil {
		return nil
	}
	switch {
	case len(config.ExposedPorts) > 0 && !exposedPortsPublished(config.ExposedPorts, hostConfig):
		return NewNotImplemented("Config.ExposedPorts is not supported")
	case config.Domainname != "":
		return NewNotImplemented("Config.Domainname is not supported")
	case config.Healthcheck != nil:
		return NewNotImplemented("Config.Healthcheck is not supported")
	case len(config.Volumes) > 0:
		return NewNotImplemented("Config.Volumes is not supported")
	case config.NetworkDisabled:
		return NewNotImplemented("Config.NetworkDisabled is not supported")
	case len(config.OnBuild) > 0:
		return NewNotImplemented("Config.OnBuild is not supported")
	case config.StopSignal != "":
		return NewNotImplemented("Config.StopSignal is not supported")
	case config.StopTimeout != nil:
		return NewNotImplemented("Config.StopTimeout is not supported")
	}
	return nil
}

// exposedPortsPublished reports whether every exposed port also carries a host
// binding, which is how testcontainers-go always sends requests derived from an
// image EXPOSE.
func exposedPortsPublished(exposed network.PortSet, hostConfig *container.HostConfig) bool {
	if hostConfig == nil {
		return false
	}
	for port := range exposed {
		if len(hostConfig.PortBindings[port]) == 0 {
			return false
		}
	}
	return true
}

// hostConfigFieldValidators apply the HostConfig field policy in a fixed
// order: the first rejected field decides the response, so each validator owns
// one contiguous run of the original switch and the slice order preserves the
// original precedence between runs.
var hostConfigFieldValidators = []func(*container.HostConfig) *DockerError{
	validateHostConfigPrivilegeFields,
	validateHostConfigPolicyFields,
	validateHostConfigNamespaceFields,
	validateHostConfigHardeningFields,
	validateHostConfigRuntimeFields,
	validateHostConfigHostPathFields,
}

func validateHostConfigFields(hostConfig *container.HostConfig) *DockerError {
	for _, validate := range hostConfigFieldValidators {
		if apiErr := validate(hostConfig); apiErr != nil {
			return apiErr
		}
	}
	if apiErr := validateStructuredMounts(hostConfig.Mounts); apiErr != nil {
		return apiErr
	}
	return validateBinds(hostConfig.Binds)
}

// validateHostConfigPrivilegeFields rejects fields that would widen the
// container's privileges beyond what the daemon implements.
func validateHostConfigPrivilegeFields(hostConfig *container.HostConfig) *DockerError {
	switch {
	case hostConfig.Privileged:
		return NewNotImplemented("HostConfig.Privileged is not supported")
	case len(hostConfig.CapAdd) > 0:
		return NewNotImplemented("HostConfig.CapAdd is not supported")
	case len(hostConfig.CapDrop) > 0:
		return NewNotImplemented("HostConfig.CapDrop is not supported")
	case len(hostConfig.Devices) > 0:
		return NewNotImplemented("HostConfig.Devices is not supported")
	case hostConfig.ReadonlyRootfs:
		return NewNotImplemented("HostConfig.ReadonlyRootfs is not supported")
	}
	return nil
}

// validateHostConfigPolicyFields rejects the resource aggregate, the restart
// policy, and security options as whole units.
func validateHostConfigPolicyFields(hostConfig *container.HostConfig) *DockerError {
	switch {
	case !reflect.DeepEqual(hostConfig.Resources, container.Resources{}):
		return NewNotImplemented("HostConfig.Resources is not supported")
	case hostConfig.RestartPolicy.Name != "" || hostConfig.RestartPolicy.MaximumRetryCount != 0:
		return NewNotImplemented("HostConfig.RestartPolicy is not supported")
	case len(hostConfig.SecurityOpt) > 0:
		return NewNotImplemented("HostConfig.SecurityOpt is not supported")
	}
	return nil
}

// validateHostConfigNamespaceFields rejects every namespace override.
func validateHostConfigNamespaceFields(hostConfig *container.HostConfig) *DockerError {
	switch {
	case hostConfig.PidMode != "":
		return NewNotImplemented("HostConfig.PidMode is not supported")
	case hostConfig.IpcMode != "":
		return NewNotImplemented("HostConfig.IpcMode is not supported")
	case hostConfig.UTSMode != "":
		return NewNotImplemented("HostConfig.UTSMode is not supported")
	case hostConfig.UsernsMode != "":
		return NewNotImplemented("HostConfig.UsernsMode is not supported")
	case hostConfig.CgroupnsMode != "":
		return NewNotImplemented("HostConfig.CgroupnsMode is not supported")
	}
	return nil
}

// validateHostConfigHardeningFields rejects kernel-level hardening knobs.
func validateHostConfigHardeningFields(hostConfig *container.HostConfig) *DockerError {
	switch {
	case len(hostConfig.Sysctls) > 0:
		return NewNotImplemented("HostConfig.Sysctls is not supported")
	case len(hostConfig.MaskedPaths) > 0:
		return NewNotImplemented("HostConfig.MaskedPaths is not supported")
	case len(hostConfig.ReadonlyPaths) > 0:
		return NewNotImplemented("HostConfig.ReadonlyPaths is not supported")
	}
	return nil
}

// validateHostConfigRuntimeFields rejects runtime selection and per-container
// host visibility overrides. The default runtime is the only accepted Runtime.
func validateHostConfigRuntimeFields(hostConfig *container.HostConfig) *DockerError {
	switch {
	case hostConfig.Runtime != "" && hostConfig.Runtime != defaultContainerRuntime:
		return NewNotImplemented("HostConfig.Runtime is not supported")
	case len(hostConfig.VolumesFrom) > 0:
		return NewNotImplemented("HostConfig.VolumesFrom is not supported")
	case hostConfig.OomScoreAdj != 0:
		return NewNotImplemented("HostConfig.OomScoreAdj is not supported")
	case hostConfig.ContainerIDFile != "":
		return NewNotImplemented("HostConfig.ContainerIDFile is not supported")
	}
	return nil
}

// validateHostConfigHostPathFields rejects host-path, daemon-reference, and
// cgroup overrides.
func validateHostConfigHostPathFields(hostConfig *container.HostConfig) *DockerError {
	switch {
	case hostConfig.VolumeDriver != "":
		return NewNotImplemented("HostConfig.VolumeDriver is not supported")
	case len(hostConfig.Annotations) > 0:
		return NewNotImplemented("HostConfig.Annotations is not supported")
	case hostConfig.Cgroup != "":
		return NewNotImplemented("HostConfig.Cgroup is not supported")
	case len(hostConfig.Links) > 0:
		return NewNotImplemented("HostConfig.Links is not supported")
	case len(hostConfig.StorageOpt) > 0:
		return NewNotImplemented("HostConfig.StorageOpt is not supported")
	case hostConfig.Umask != nil:
		return NewNotImplemented("HostConfig.Umask is not supported")
	}
	return nil
}

// validateStructuredMounts rejects HostConfig.Mounts entries whose type is not
// translatable or whose source cannot be honored, before any provisioning.
func validateStructuredMounts(mounts []mount.Mount) *DockerError {
	for _, requested := range mounts {
		mountType := requested.Type
		if mountType == "" {
			mountType = mount.TypeBind
		}
		switch mountType {
		case mount.TypeBind:
			if !filepath.IsAbs(requested.Source) {
				return NewInvalidParameter(fmt.Sprintf(
					"HostConfig.Mounts source %q must be an absolute path for a bind mount", requested.Source))
			}
		case mount.TypeTmpfs:
			if requested.Source != "" {
				return NewInvalidParameter(fmt.Sprintf(
					"HostConfig.Mounts source %q must be empty for a tmpfs mount", requested.Source))
			}
		default:
			return NewNotImplemented(fmt.Sprintf("HostConfig.Mounts type %q is not supported", mountType))
		}
	}
	return nil
}

// validateBinds rejects malformed legacy Binds entries instead of silently
// dropping them, matching the documented "400 rather than silently dropping"
// policy.
func validateBinds(binds []string) *DockerError {
	for _, bind := range binds {
		parts := strings.Split(bind, ":")
		if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return NewInvalidParameter(fmt.Sprintf("invalid bind mount specification: %q", bind))
		}
	}
	return nil
}
