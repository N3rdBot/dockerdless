package buildkit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	buildkitclient "github.com/moby/buildkit/client"
	digest "github.com/opencontainers/go-digest"
)

func TestProgressWriterEmitsValidDockerJSON(t *testing.T) {
	var out bytes.Buffer
	writer := NewProgressWriter(&out)

	if err := writer.Stream("plain text\n"); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if err := writer.Status("Downloading", "1kiB/2kiB (50%)", 1024, 2048); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if err := writer.Aux("sha256:abc"); err != nil {
		t.Fatalf("Aux() error = %v", err)
	}
	if err := writer.ErrorDetail(1, "boom"); err != nil {
		t.Fatalf("ErrorDetail() error = %v", err)
	}
	if !writer.Written() {
		t.Fatal("Written() = false, want true")
	}

	messages := decodeDockerStream(t, out.Bytes())
	if len(messages) != 4 {
		t.Fatalf("decoded %d messages, want 4: %s", len(messages), out.String())
	}
	if messages[0].Stream != "plain text\n" {
		t.Fatalf("stream = %q", messages[0].Stream)
	}
	if messages[1].Status != "Downloading" || messages[1].Progress != "1kiB/2kiB (50%)" {
		t.Fatalf("status message = %+v", messages[1])
	}
	if messages[1].ProgressDetail == nil || messages[1].ProgressDetail.Current != 1024 || messages[1].ProgressDetail.Total != 2048 {
		t.Fatalf("progress detail = %+v", messages[1].ProgressDetail)
	}
	if messages[2].Aux == nil || messages[2].Aux.ID != "sha256:abc" {
		t.Fatalf("aux = %+v", messages[2].Aux)
	}
	if messages[3].ErrorDetail == nil || messages[3].ErrorDetail.Message != "boom" {
		t.Fatalf("errorDetail = %+v", messages[3].ErrorDetail)
	}
	if messages[3].ErrorMessage != "boom" {
		t.Fatalf("legacy error mirror = %q, want boom", messages[3].ErrorMessage)
	}

	var raw map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("line %q invalid JSON: %v", line, err)
		}
	}
}

func TestProgressWriterAuxSkipsEmptyID(t *testing.T) {
	var out bytes.Buffer
	writer := NewProgressWriter(&out)
	if err := writer.Aux(""); err != nil {
		t.Fatalf("Aux(\"\") error = %v", err)
	}
	if writer.Written() {
		t.Fatalf("Aux(\"\") wrote %q, want nothing", out.String())
	}
}

func TestTranslateSolveStatusEmitsDockerMessages(t *testing.T) {
	var out bytes.Buffer
	writer := NewProgressWriter(&out)
	translator := newBuildProgress(writer)

	started := time.Now()
	completed := started.Add(time.Second)
	vertexDigest := digest.FromString("vertex-load")

	status := &buildkitclient.SolveStatus{
		Vertexes: []*buildkitclient.Vertex{
			{Digest: vertexDigest, Name: "[internal] load build definition", Started: &started},
			{Digest: vertexDigest, Name: "[internal] load build definition", Started: &started, Completed: &completed, Cached: true},
			{Digest: digest.FromString("vertex-copy"), Name: "[1/1] COPY file /file", Started: &started},
		},
		Statuses: []*buildkitclient.VertexStatus{
			{ID: "transfer", Vertex: vertexDigest, Name: "transferring context", Current: 512, Total: 1024},
		},
		Logs: []*buildkitclient.VertexLog{
			{Vertex: vertexDigest, Stream: 1, Data: []byte("hello from build")},
		},
		Warnings: []*buildkitclient.VertexWarning{
			{Short: []byte("no cache mount"), Detail: [][]byte{[]byte("consider a cache mount")}},
		},
	}
	if err := translator.Translate(status); err != nil {
		t.Fatalf("Translate() error = %v", err)
	}
	if translator.EmittedError() {
		t.Fatal("EmittedError() = true, want false")
	}

	messages := decodeDockerStream(t, out.Bytes())
	streams := make([]string, 0, len(messages))
	var statusMessage *dockerMessage
	for i := range messages {
		if messages[i].Stream != "" {
			streams = append(streams, messages[i].Stream)
		}
		if messages[i].Status != "" {
			statusMessage = &messages[i]
		}
	}
	joined := strings.Join(streams, "")
	for _, want := range []string{
		"#1 [internal] load build definition\n",
		"#1 CACHED\n",
		"#2 [1/1] COPY file /file\n",
		"hello from build\n",
		"WARNING: no cache mount: consider a cache mount\n",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("streams %q missing %q", joined, want)
		}
	}
	if statusMessage == nil {
		t.Fatalf("no status message in %s", out.String())
	}
	if statusMessage.Status != "transferring context" {
		t.Fatalf("status = %q", statusMessage.Status)
	}
	if !strings.Contains(statusMessage.Progress, "512B/1.024kB") {
		t.Fatalf("progress = %q", statusMessage.Progress)
	}
}

func TestTranslateSolveErrorEmitsErrorDetailOnce(t *testing.T) {
	var out bytes.Buffer
	writer := NewProgressWriter(&out)
	translator := newBuildProgress(writer)

	vertex := &buildkitclient.Vertex{
		Digest: digest.FromString("vertex-fail"),
		Name:   "[1/1] RUN false",
		Error:  "process \"/bin/sh -c false\" did not complete successfully: exit code: 1",
	}
	if err := translator.Translate(&buildkitclient.SolveStatus{Vertexes: []*buildkitclient.Vertex{vertex}}); err != nil {
		t.Fatalf("Translate() error = %v", err)
	}
	if err := translator.Translate(&buildkitclient.SolveStatus{Vertexes: []*buildkitclient.Vertex{vertex}}); err != nil {
		t.Fatalf("Translate() second error = %v", err)
	}
	if !translator.EmittedError() {
		t.Fatal("EmittedError() = false, want true")
	}

	messages := decodeDockerStream(t, out.Bytes())
	errorCount := 0
	for _, message := range messages {
		if message.ErrorDetail != nil {
			errorCount++
			if !strings.Contains(message.ErrorDetail.Message, "exit code: 1") {
				t.Fatalf("errorDetail = %+v", message.ErrorDetail)
			}
		}
	}
	if errorCount != 1 {
		t.Fatalf("errorDetail messages = %d, want 1 (%s)", errorCount, out.String())
	}
}

func TestTranslateSolveStatusDedupesRepeatedStatusProgress(t *testing.T) {
	var out bytes.Buffer
	writer := NewProgressWriter(&out)
	translator := newBuildProgress(writer)

	vertexStatus := &buildkitclient.VertexStatus{ID: "s1", Name: "downloading", Current: 10, Total: 100}
	for range 3 {
		if err := translator.Translate(&buildkitclient.SolveStatus{Statuses: []*buildkitclient.VertexStatus{vertexStatus}}); err != nil {
			t.Fatalf("Translate() error = %v", err)
		}
	}
	if got := strings.Count(out.String(), "\n"); got != 1 {
		t.Fatalf("status lines = %d, want 1: %s", got, out.String())
	}
}

func TestFormatProgress(t *testing.T) {
	tests := []struct {
		current int64
		total   int64
		want    string
	}{
		{current: 0, total: 0, want: ""},
		{current: 512, total: 0, want: "512B"},
		{current: 512, total: 1024, want: "512B/1.024kB (50%)"},
	}
	for _, test := range tests {
		if got := formatProgress(test.current, test.total); got != test.want {
			t.Fatalf("formatProgress(%d, %d) = %q, want %q", test.current, test.total, got, test.want)
		}
	}
}
