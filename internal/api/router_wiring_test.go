package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
)

// fakeService is an in-memory Service used to exercise HTTP behavior without
// infrastructure. Individual operations are overridden through function
// fields; unset operations report ErrServerError so tests fail loudly.
type fakeService struct {
	info           func(context.Context) (ports.SystemStatus, error)
	imageInspect   func(context.Context, string) (ports.ImageDetail, error)
	imageList      func(context.Context) ([]ports.ImageDetail, error)
	imagePull      func(context.Context, ports.PullRequest, io.Writer) error
	imageBuild     func(context.Context, ports.BuildRequest, io.Writer) error
	imageRemove    func(context.Context, string, bool) (ports.ImageRemoveResult, error)
	create         func(context.Context, ports.ContainerCreateRequest) (ports.ContainerCreateResult, error)
	list           func(context.Context, bool) ([]domain.Container, error)
	inspect        func(context.Context, string) (domain.Container, error)
	start          func(context.Context, string) error
	stop           func(context.Context, string, *time.Duration) error
	remove         func(context.Context, string, bool) error
	logs           func(context.Context, string, ports.LogRequest, io.Writer, io.Writer) error
	execCreate     func(context.Context, string, ports.ExecCreateRequest) (string, error)
	execStart      func(context.Context, string, ports.ExecStartRequest) (int, error)
	execInspect    func(context.Context, string) (domain.ExecRecord, error)
	networkList    func(context.Context) ([]ports.NetworkDetail, error)
	networkInspect func(context.Context, string) (ports.NetworkDetail, error)
	networkCreate  func(context.Context, ports.NetworkCreateRequest) (ports.NetworkCreateResult, error)
	networkConnect func(context.Context, ports.NetworkConnectRequest) error
	networkRemove  func(context.Context, string) error
}

func (f *fakeService) Info(ctx context.Context) (ports.SystemStatus, error) {
	if f.info != nil {
		return f.info(ctx)
	}
	return ports.SystemStatus{}, ports.ErrServerError
}

func (f *fakeService) ImageInspect(ctx context.Context, ref string) (ports.ImageDetail, error) {
	if f.imageInspect != nil {
		return f.imageInspect(ctx, ref)
	}
	return ports.ImageDetail{}, ports.ErrServerError
}

func (f *fakeService) ImageList(ctx context.Context) ([]ports.ImageDetail, error) {
	if f.imageList != nil {
		return f.imageList(ctx)
	}
	return nil, ports.ErrServerError
}

func (f *fakeService) ImagePull(ctx context.Context, request ports.PullRequest, out io.Writer) error {
	if f.imagePull != nil {
		return f.imagePull(ctx, request, out)
	}
	return ports.ErrServerError
}

func (f *fakeService) ImageBuild(ctx context.Context, request ports.BuildRequest, out io.Writer) error {
	if f.imageBuild != nil {
		return f.imageBuild(ctx, request, out)
	}
	return ports.ErrServerError
}

func (f *fakeService) ImageRemove(ctx context.Context, ref string, force bool) (ports.ImageRemoveResult, error) {
	if f.imageRemove != nil {
		return f.imageRemove(ctx, ref, force)
	}
	return ports.ImageRemoveResult{}, ports.ErrServerError
}

func (f *fakeService) ContainerCreate(ctx context.Context, request ports.ContainerCreateRequest) (ports.ContainerCreateResult, error) {
	if f.create != nil {
		return f.create(ctx, request)
	}
	return ports.ContainerCreateResult{}, ports.ErrServerError
}

func (f *fakeService) ContainerList(ctx context.Context, all bool) ([]domain.Container, error) {
	if f.list != nil {
		return f.list(ctx, all)
	}
	return nil, ports.ErrServerError
}

func (f *fakeService) ContainerInspect(ctx context.Context, ref string) (domain.Container, error) {
	if f.inspect != nil {
		return f.inspect(ctx, ref)
	}
	return domain.Container{}, ports.ErrServerError
}

func (f *fakeService) ContainerStart(ctx context.Context, ref string) error {
	if f.start != nil {
		return f.start(ctx, ref)
	}
	return ports.ErrServerError
}

func (f *fakeService) ContainerStop(ctx context.Context, ref string, timeout *time.Duration) error {
	if f.stop != nil {
		return f.stop(ctx, ref, timeout)
	}
	return ports.ErrServerError
}

func (f *fakeService) ContainerRemove(ctx context.Context, ref string, force bool) error {
	if f.remove != nil {
		return f.remove(ctx, ref, force)
	}
	return ports.ErrServerError
}

func (f *fakeService) ContainerLogs(ctx context.Context, ref string, options ports.LogRequest, stdout, stderr io.Writer) error {
	if f.logs != nil {
		return f.logs(ctx, ref, options, stdout, stderr)
	}
	return ports.ErrServerError
}

func (f *fakeService) ExecCreate(ctx context.Context, ref string, request ports.ExecCreateRequest) (string, error) {
	if f.execCreate != nil {
		return f.execCreate(ctx, ref, request)
	}
	return "", ports.ErrServerError
}

func (f *fakeService) ExecStart(ctx context.Context, id string, request ports.ExecStartRequest) (int, error) {
	if f.execStart != nil {
		return f.execStart(ctx, id, request)
	}
	return 0, ports.ErrServerError
}

func (f *fakeService) ExecInspect(ctx context.Context, id string) (domain.ExecRecord, error) {
	if f.execInspect != nil {
		return f.execInspect(ctx, id)
	}
	return domain.ExecRecord{}, ports.ErrServerError
}

func (f *fakeService) NetworkList(ctx context.Context) ([]ports.NetworkDetail, error) {
	if f.networkList != nil {
		return f.networkList(ctx)
	}
	return nil, ports.ErrServerError
}

func (f *fakeService) NetworkInspect(ctx context.Context, ref string) (ports.NetworkDetail, error) {
	if f.networkInspect != nil {
		return f.networkInspect(ctx, ref)
	}
	return ports.NetworkDetail{}, ports.ErrServerError
}

func (f *fakeService) NetworkCreate(ctx context.Context, request ports.NetworkCreateRequest) (ports.NetworkCreateResult, error) {
	if f.networkCreate != nil {
		return f.networkCreate(ctx, request)
	}
	return ports.NetworkCreateResult{}, ports.ErrServerError
}

func (f *fakeService) NetworkConnect(ctx context.Context, request ports.NetworkConnectRequest) error {
	if f.networkConnect != nil {
		return f.networkConnect(ctx, request)
	}
	return ports.ErrServerError
}

func (f *fakeService) NetworkRemove(ctx context.Context, ref string) error {
	if f.networkRemove != nil {
		return f.networkRemove(ctx, ref)
	}
	return ports.ErrServerError
}

// TestRouter_containerCreateUnknownImageReturnsDocker404 locks the wiring
// contract: POST /containers/create must reach the application service and map
// an absent image to Docker's 404 envelope instead of 501 (unwired) or 201
// (accidental success).
func TestRouter_containerCreateUnknownImageReturnsDocker404(t *testing.T) {
	// Given
	service := &fakeService{
		create: func(_ context.Context, request ports.ContainerCreateRequest) (ports.ContainerCreateResult, error) {
			if request.Image != "missing:latest" {
				t.Fatalf("handler passed image %q, want %q", request.Image, "missing:latest")
			}
			return ports.ContainerCreateResult{}, fmt.Errorf("%w: No such image: %s", ports.ErrNotFound, request.Image)
		},
	}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	request := httptest.NewRequest(http.MethodPost, "/containers/create", strings.NewReader(`{"Image":"missing:latest"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, request)

	// Then
	if recorder.Code == http.StatusOK || recorder.Code == http.StatusNotImplemented {
		t.Fatalf("expected create to be wired to the service, got status %d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected Docker status %d, got %d body=%s", http.StatusNotFound, recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode Docker error envelope: %v", err)
	}
	if envelope.Message != "No such image: missing:latest" {
		t.Fatalf("expected Docker image message, got %q", envelope.Message)
	}
}
