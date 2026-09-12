package domain

import (
	"context"
	"errors"
	"maps"
	"sort"
	"sync"
)

var (
	// ErrContainerNotFound means no container is indexed by the requested identity.
	ErrContainerNotFound = errors.New("container not found")
	// ErrContainerNameConflict means a name is already owned by another container.
	ErrContainerNameConflict = errors.New("container name already exists")
	// ErrEmptyContainerName means a rename was requested without a Docker name.
	ErrEmptyContainerName = errors.New("container name must not be empty")
	// ErrNilContext means an interface call received a nil context.
	ErrNilContext = errors.New("context must not be nil")
)

// Registry is a reconstructable in-memory container-state index.
//
// The registry is intentionally volatile: callers must rebuild it from
// container metadata and runtime tasks after a process restart.
type Registry struct {
	mu     sync.RWMutex
	byID   map[ContainerID]Container
	byName map[string]ContainerID
}

// NewRegistry constructs an empty in-memory registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:   make(map[ContainerID]Container),
		byName: make(map[string]ContainerID),
	}
}

// Get retrieves a defensive copy of a container by Docker ID.
func (r *Registry) Get(ctx context.Context, id ContainerID) (Container, error) {
	if err := contextError(ctx); err != nil {
		return Container{}, err
	}

	r.mu.RLock()
	container, ok := r.byID[id]
	r.mu.RUnlock()
	if !ok {
		return Container{}, ErrContainerNotFound
	}
	return cloneContainer(container), nil
}

// GetByName retrieves a defensive copy of a container by its Docker name.
func (r *Registry) GetByName(name string) (Container, error) {
	r.mu.RLock()
	id, ok := r.byName[name]
	container := r.byID[id]
	r.mu.RUnlock()
	if !ok {
		return Container{}, ErrContainerNotFound
	}
	return cloneContainer(container), nil
}

// List returns all containers in deterministic Docker-ID order.
func (r *Registry) List() []Container {
	r.mu.RLock()
	containers := make([]Container, 0, len(r.byID))
	for _, container := range r.byID {
		containers = append(containers, cloneContainer(container))
	}
	r.mu.RUnlock()

	sort.Slice(containers, func(i, j int) bool {
		return containers[i].ID < containers[j].ID
	})
	return containers
}

// Rename changes a container's Docker name while retaining its ID and state.
func (r *Registry) Rename(id ContainerID, name string) error {
	if name == "" {
		return ErrEmptyContainerName
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	container, ok := r.byID[id]
	if !ok {
		return ErrContainerNotFound
	}
	if existingID, exists := r.byName[name]; exists && existingID != id {
		return ErrContainerNameConflict
	}
	if container.Name != "" {
		delete(r.byName, container.Name)
	}
	container.Name = name
	r.byID[id] = cloneContainer(container)
	r.byName[name] = id
	return nil
}

// Save inserts or replaces a container and refreshes both identity indexes.
func (r *Registry) Save(ctx context.Context, container Container) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if container.ID == "" {
		return ErrEmptyIdentity
	}
	if container.ImageReference == "" {
		container.ImageReference = container.Spec.Image
	}
	if container.Spec.Image == "" {
		container.Spec.Image = container.ImageReference
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existingID, ok := r.byName[container.Name]; container.Name != "" && ok && existingID != container.ID {
		return ErrContainerNameConflict
	}
	if previous, ok := r.byID[container.ID]; ok && previous.Name != "" && previous.Name != container.Name {
		delete(r.byName, previous.Name)
	}
	stored := cloneContainer(container)
	r.byID[container.ID] = stored
	if container.Name != "" {
		r.byName[container.Name] = container.ID
	}
	return nil
}

// Remove deletes a container from both the ID and name indexes.
func (r *Registry) Remove(ctx context.Context, id ContainerID) error {
	if err := contextError(ctx); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	container, ok := r.byID[id]
	if !ok {
		return ErrContainerNotFound
	}
	delete(r.byID, id)
	if container.Name != "" {
		delete(r.byName, container.Name)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cloneContainer(container Container) Container {
	container.Spec.Command = append([]string(nil), container.Spec.Command...)
	container.Spec.Env = maps.Clone(container.Spec.Env)
	container.Labels = maps.Clone(container.Labels)
	container.Execs = append([]ExecRecord(nil), container.Execs...)
	for index := range container.Execs {
		container.Execs[index].Command = append([]string(nil), container.Execs[index].Command...)
	}
	container.PortBindings = append([]PortBinding(nil), container.PortBindings...)
	container.Networks = append([]NetworkAttachment(nil), container.Networks...)
	for index := range container.Networks {
		container.Networks[index].Aliases = append([]string(nil), container.Networks[index].Aliases...)
	}
	if container.Health != nil {
		health := *container.Health
		health.Log = append([]HealthCheckResult(nil), health.Log...)
		container.Health = &health
	}
	return container
}
