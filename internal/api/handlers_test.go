package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N3rdBot/dockerdless/internal/domain"
	"github.com/N3rdBot/dockerdless/internal/ports"
	"github.com/N3rdBot/dockerdless/internal/streams"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
)

func TestHandlers_containerCreateWiresRequestAndReturns201(t *testing.T) {
	var received ports.ContainerCreateRequest
	service := &fakeService{create: func(_ context.Context, request ports.ContainerCreateRequest) (ports.ContainerCreateResult, error) {
		received = request
		return ports.ContainerCreateResult{ID: "abc123"}, nil
	}}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	body := `{
		"Image":"alpine:latest",
		"Cmd":["echo","hi"],
		"Env":["A=b"],
		"Tty":true,
		"Labels":{"app":"web"},
		"HostConfig":{"NetworkMode":"bridge","PortBindings":{"80/tcp":[{"HostPort":"18080"}]}}
	}`
	request := httptest.NewRequest(http.MethodPost, "/v1.44/containers/create?name=web", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response container.CreateResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if response.ID != "abc123" {
		t.Fatalf("expected created id abc123, got %q", response.ID)
	}
	switch {
	case received.Name != "web":
		t.Fatalf("expected query name, got %q", received.Name)
	case received.Image != "alpine:latest":
		t.Fatalf("expected image, got %q", received.Image)
	case !received.TTY:
		t.Fatal("expected tty flag")
	case received.NetworkMode != "bridge":
		t.Fatalf("expected bridge network mode, got %q", received.NetworkMode)
	case len(received.PortBindings) != 1:
		t.Fatalf("expected one port binding, got %+v", received.PortBindings)
	case received.PortBindings[0].ContainerPort != 80 || received.PortBindings[0].HostPort != 18080:
		t.Fatalf("unexpected port binding %+v", received.PortBindings[0])
	case len(received.Command) != 2 || received.Command[0] != "echo":
		t.Fatalf("unexpected command %v", received.Command)
	case received.Env["A"] != "b":
		t.Fatalf("unexpected env %v", received.Env)
	}
}

func TestHandlers_containerInspectMapsStateNetworkingAndFiltersInternalLabels(t *testing.T) {
	item := domain.Container{
		ID:             "c1",
		Name:           "web",
		State:          domain.ContainerStateRunning,
		ImageReference: "alpine:latest",
		ImageDigest:    "sha256:deadbeef",
		CreatedAt:      time.Now(),
		Labels:         map[string]string{ports.LabelTTY: "true", "app": "web"},
		PortBindings:   []domain.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 18080}},
		Networks:       []domain.NetworkAttachment{{NetworkID: "bridge-id", Name: "bridge", IPAddress: "10.88.0.2", IPPrefixLen: 24}},
	}
	service := &fakeService{inspect: func(context.Context, string) (domain.Container, error) { return item, nil }}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	request := httptest.NewRequest(http.MethodGet, "/containers/web/json", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response container.InspectResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode inspect response: %v", err)
	}
	if response.State == nil || !response.State.Running {
		t.Fatalf("expected running state, got %+v", response.State)
	}
	if response.Config == nil || !response.Config.Tty {
		t.Fatalf("expected tty config from internal label, got %+v", response.Config)
	}
	if _, leaked := response.Config.Labels[ports.LabelTTY]; leaked {
		t.Fatalf("expected internal label filtered, got %v", response.Config.Labels)
	}
	portMap := response.NetworkSettings.Ports
	if len(portMap) != 1 {
		t.Fatalf("expected published ports, got %+v", portMap)
	}
	endpoint := response.NetworkSettings.Networks["bridge"]
	if endpoint == nil || endpoint.IPAddress.String() != "10.88.0.2" {
		t.Fatalf("expected attached endpoint address, got %+v", endpoint)
	}
}

func TestHandlers_containerLogsMultiplexesNonTTYStream(t *testing.T) {
	service := &fakeService{
		inspect: func(context.Context, string) (domain.Container, error) {
			return domain.Container{ID: "c1", Name: "web"}, nil
		},
		logs: func(_ context.Context, _ string, _ ports.LogRequest, stdout, stderr io.Writer) error {
			_, _ = stdout.Write([]byte("hello\n"))
			_, _ = stderr.Write([]byte("oops\n"))
			return nil
		},
	}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	request := httptest.NewRequest(http.MethodGet, "/containers/web/logs", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != streams.MediaTypeMultiplexedStream {
		t.Fatalf("expected multiplexed content type, got %q", got)
	}
	frame, err := streams.NewReader(bytes.NewReader(recorder.Body.Bytes())).ReadFrame()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.Stream != streams.Stdout || string(frame.Data) != "hello\n" {
		t.Fatalf("unexpected first frame %+v", frame)
	}
	second, err := streams.NewReader(bytes.NewReader(recorder.Body.Bytes()[len(frame.Data)+8:])).ReadFrame()
	if err != nil {
		t.Fatalf("read second frame: %v", err)
	}
	if second.Stream != streams.Stderr || string(second.Data) != "oops\n" {
		t.Fatalf("unexpected second frame %+v", second)
	}
}

func TestHandlers_containerLogsUsesRawStreamForTTY(t *testing.T) {
	service := &fakeService{
		inspect: func(context.Context, string) (domain.Container, error) {
			return domain.Container{ID: "c1", Name: "web", Labels: map[string]string{ports.LabelTTY: "true"}}, nil
		},
		logs: func(_ context.Context, _ string, _ ports.LogRequest, stdout, _ io.Writer) error {
			_, _ = stdout.Write([]byte("tty output"))
			return nil
		},
	}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/containers/web/logs", nil))

	if got := recorder.Header().Get("Content-Type"); got != streams.MediaTypeRawStream {
		t.Fatalf("expected raw content type, got %q", got)
	}
	if got := recorder.Body.String(); got != "tty output" {
		t.Fatalf("expected raw stream, got %q", got)
	}
}

func TestHandlers_execCreateAndInspect(t *testing.T) {
	service := &fakeService{
		execCreate: func(_ context.Context, ref string, request ports.ExecCreateRequest) (string, error) {
			if ref != "web" || len(request.Command) != 2 {
				t.Fatalf("unexpected exec create ref=%q request=%+v", ref, request)
			}
			return "exec-1", nil
		},
		execInspect: func(context.Context, string) (domain.ExecRecord, error) {
			exitCode := 7
			return domain.ExecRecord{ID: "exec-1", ContainerID: "c1", ExitCode: &exitCode, Command: []string{"sh", "-c", "exit 7"}}, nil
		},
	}
	handler := NewRouterWithDependencies(Dependencies{Service: service})

	createRecorder := httptest.NewRecorder()
	handler.ServeHTTP(createRecorder, httptest.NewRequest(http.MethodPost, "/containers/web/exec", strings.NewReader(`{"Cmd":["sh","-c"],"AttachStdout":true}`)))
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created container.ExecCreateResponse
	if err := json.NewDecoder(createRecorder.Body).Decode(&created); err != nil {
		t.Fatalf("decode exec create: %v", err)
	}
	if created.ID != "exec-1" {
		t.Fatalf("expected exec-1, got %q", created.ID)
	}

	inspectRecorder := httptest.NewRecorder()
	handler.ServeHTTP(inspectRecorder, httptest.NewRequest(http.MethodGet, "/exec/exec-1/json", nil))
	if inspectRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", inspectRecorder.Code)
	}
	var inspected container.ExecInspectResponse
	if err := json.NewDecoder(inspectRecorder.Body).Decode(&inspected); err != nil {
		t.Fatalf("decode exec inspect: %v", err)
	}
	if inspected.ExitCode == nil || *inspected.ExitCode != 7 || inspected.ProcessConfig.Entrypoint != "sh" {
		t.Fatalf("unexpected exec inspect %+v", inspected)
	}
}

func TestHandlers_networkLifecycle(t *testing.T) {
	service := &fakeService{
		networkList: func(context.Context) ([]ports.NetworkDetail, error) {
			return []ports.NetworkDetail{
				{ID: "bridge-id", Name: "bridge", Driver: "bridge", Mode: "network", Subnet: "10.88.0.0/24", Gateway: "10.88.0.1"},
				{ID: "host-id", Name: "host", Driver: "host", Mode: "host"},
			}, nil
		},
		networkCreate: func(_ context.Context, request ports.NetworkCreateRequest) (ports.NetworkCreateResult, error) {
			if request.Name != "frontend" || request.EnableIPv6 {
				t.Fatalf("unexpected network create %+v", request)
			}
			return ports.NetworkCreateResult{ID: "network-frontend"}, nil
		},
		networkConnect: func(_ context.Context, request ports.NetworkConnectRequest) error {
			if request.Network != "network-frontend" || request.Container != "c1" || len(request.Aliases) != 1 {
				t.Fatalf("unexpected connect request %+v", request)
			}
			return nil
		},
		networkInspect: func(_ context.Context, ref string) (ports.NetworkDetail, error) {
			if ref != "qa-net" {
				t.Fatalf("unexpected inspect ref %q", ref)
			}
			return ports.NetworkDetail{ID: "net-id", Name: "qa-net", Driver: "bridge", Mode: "network", Subnet: "10.88.2.0/24", Gateway: "10.88.2.1"}, nil
		},
		networkRemove: func(context.Context, string) error { return nil },
	}
	handler := NewRouterWithDependencies(Dependencies{Service: service})

	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/v1.44/networks", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRecorder.Code)
	}
	var summaries []network.Summary
	if err := json.NewDecoder(listRecorder.Body).Decode(&summaries); err != nil {
		t.Fatalf("decode network list: %v", err)
	}
	if len(summaries) != 2 || summaries[0].Name != "bridge" || summaries[0].IPAM.Config[0].Subnet.String() != "10.88.0.0/24" {
		t.Fatalf("unexpected network summaries %+v", summaries)
	}

	inspectRecorder := httptest.NewRecorder()
	handler.ServeHTTP(inspectRecorder, httptest.NewRequest(http.MethodGet, "/networks/qa-net", nil))
	if inspectRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200 network inspect, got %d", inspectRecorder.Code)
	}
	var inspectedNetwork network.Inspect
	if err := json.NewDecoder(inspectRecorder.Body).Decode(&inspectedNetwork); err != nil {
		t.Fatalf("decode network inspect: %v", err)
	}
	if inspectedNetwork.Name != "qa-net" || inspectedNetwork.ID != "net-id" {
		t.Fatalf("unexpected network inspect %+v", inspectedNetwork)
	}

	createRecorder := httptest.NewRecorder()
	handler.ServeHTTP(createRecorder, httptest.NewRequest(http.MethodPost, "/networks/create", strings.NewReader(`{"Name":"frontend","Driver":"bridge"}`)))
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createRecorder.Code)
	}

	connectRecorder := httptest.NewRecorder()
	handler.ServeHTTP(connectRecorder, httptest.NewRequest(http.MethodPost, "/networks/network-frontend/connect", strings.NewReader(`{"Container":"c1","EndpointConfig":{"Aliases":["web"]}}`)))
	if connectRecorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", connectRecorder.Code, connectRecorder.Body.String())
	}

	removeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(removeRecorder, httptest.NewRequest(http.MethodDelete, "/networks/network-frontend", nil))
	if removeRecorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", removeRecorder.Code)
	}
}

func TestHandlers_imageInspectAndList(t *testing.T) {
	detail := ports.ImageDetail{
		ID:          "sha256:deadbeef",
		RepoTags:    []string{"alpine:latest"},
		RepoDigests: []string{"alpine@sha256:deadbeef"},
		Platform:    "linux/amd64",
		Size:        1024,
		Created:     time.Unix(1700000000, 0),
	}
	service := &fakeService{
		imageInspect: func(context.Context, string) (ports.ImageDetail, error) { return detail, nil },
		imageList:    func(context.Context) ([]ports.ImageDetail, error) { return []ports.ImageDetail{detail}, nil },
	}
	handler := NewRouterWithDependencies(Dependencies{Service: service})

	inspectRecorder := httptest.NewRecorder()
	handler.ServeHTTP(inspectRecorder, httptest.NewRequest(http.MethodGet, "/images/alpine:latest/json", nil))
	if inspectRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", inspectRecorder.Code)
	}
	var inspected map[string]any
	if err := json.NewDecoder(inspectRecorder.Body).Decode(&inspected); err != nil {
		t.Fatalf("decode image inspect: %v", err)
	}
	if inspected["Id"] != "sha256:deadbeef" || inspected["Architecture"] != "amd64" {
		t.Fatalf("unexpected image inspect %+v", inspected)
	}

	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/images/json", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRecorder.Code)
	}
	if !strings.Contains(listRecorder.Body.String(), `"alpine:latest"`) {
		t.Fatalf("expected repo tag in list, got %s", listRecorder.Body.String())
	}
}

func TestHandlers_pingAndVersionAndInfo(t *testing.T) {
	service := &fakeService{info: func(context.Context) (ports.SystemStatus, error) {
		return ports.SystemStatus{Containers: 2, ContainersRunning: 1, Images: 3, Namespace: "default"}, nil
	}}
	handler := NewRouterWithDependencies(Dependencies{Service: service})

	headRecorder := httptest.NewRecorder()
	handler.ServeHTTP(headRecorder, httptest.NewRequest(http.MethodHead, "/_ping", nil))
	if headRecorder.Code != http.StatusOK || headRecorder.Body.Len() != 0 {
		t.Fatalf("expected empty 200 HEAD, got %d %q", headRecorder.Code, headRecorder.Body.String())
	}
	if headRecorder.Header().Get("Docker-Experimental") != "false" {
		t.Fatalf("expected Docker-Experimental header, got %q", headRecorder.Header().Get("Docker-Experimental"))
	}

	versionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(versionRecorder, httptest.NewRequest(http.MethodGet, "/v1.44/version", nil))
	var version system.VersionResponse
	if err := json.NewDecoder(versionRecorder.Body).Decode(&version); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if version.APIVersion != AdvertisedAPIVersion || version.MinAPIVersion != MinimumAPIVersion || len(version.Components) == 0 {
		t.Fatalf("unexpected version response %+v", version)
	}

	infoRecorder := httptest.NewRecorder()
	handler.ServeHTTP(infoRecorder, httptest.NewRequest(http.MethodGet, "/info", nil))
	var info system.Info
	if err := json.NewDecoder(infoRecorder.Body).Decode(&info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.Containers != 2 || info.ContainersRunning != 1 || info.Images != 3 {
		t.Fatalf("unexpected info counters %+v", info)
	}
	if info.Containerd == nil || info.Containerd.Namespaces.Containers != "default" {
		t.Fatalf("expected containerd namespace in info, got %+v", info.Containerd)
	}
}

func TestHandlers_containerListAppliesFilters(t *testing.T) {
	service := &fakeService{list: func(context.Context, bool) ([]domain.Container, error) {
		return []domain.Container{
			{ID: "c1", Name: "web", State: domain.ContainerStateRunning, Labels: map[string]string{"app": "web"}},
			{ID: "c2", Name: "db", State: domain.ContainerStateExited, Labels: map[string]string{"app": "db"}},
		}, nil
	}}
	handler := NewRouterWithDependencies(Dependencies{Service: service})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, `/containers/json?all=1&filters={"label":["app=web"]}`, nil))

	var summaries []container.Summary
	if err := json.NewDecoder(recorder.Body).Decode(&summaries); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != "c1" {
		t.Fatalf("expected label-filtered list, got %+v", summaries)
	}

	regexRecorder := httptest.NewRecorder()
	handler.ServeHTTP(regexRecorder, httptest.NewRequest(http.MethodGet, `/containers/json?all=1&filters={"name":{"^/db$":true}}`, nil))
	var regexSummaries []container.Summary
	if err := json.NewDecoder(regexRecorder.Body).Decode(&regexSummaries); err != nil {
		t.Fatalf("decode regex-filtered list: %v", err)
	}
	if len(regexSummaries) != 1 || regexSummaries[0].ID != "c2" {
		t.Fatalf("expected anchored name filter to select db, got %+v", regexSummaries)
	}
}
