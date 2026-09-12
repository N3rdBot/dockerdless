package buildkit

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	buildkitclient "github.com/moby/buildkit/client"
	digest "github.com/opencontainers/go-digest"
	"go.uber.org/zap"
)

// decodeDockerStream splits a Docker JSON progress stream into messages and
// fails the test when any line is not valid JSON.
func decodeDockerStream(t *testing.T, raw []byte) []dockerMessage {
	t.Helper()
	var messages []dockerMessage
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var message dockerMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("progress line %q is not valid Docker JSON: %v", line, err)
		}
		messages = append(messages, message)
	}
	return messages
}

// TestBuildMalformedContextEmitsErrorDetailAndCleansTemp is the failing-first
// contract for Build: a context that is not a tar archive must surface a Docker
// errorDetail message and must not leak a temp context directory.
func TestBuildMalformedContextEmitsErrorDetailAndCleansTemp(t *testing.T) {
	tempRoot := t.TempDir()
	solver := &fakeSolver{}
	adapter := New(newFakeImageStore(), solver, Options{TempRoot: tempRoot, Logger: zap.NewNop()})

	var out bytes.Buffer
	err := adapter.Build(context.Background(), BuildRequest{
		Context: strings.NewReader("this is definitely not a tar archive"),
		Tag:     "docker.io/library/broken:latest",
	}, &out)
	if err == nil {
		t.Fatal("Build() error = nil, want malformed context error")
	}

	messages := decodeDockerStream(t, out.Bytes())
	if len(messages) == 0 {
		t.Fatal("Build() wrote no progress messages, want an errorDetail message")
	}
	last := messages[len(messages)-1]
	if last.ErrorDetail == nil || last.ErrorDetail.Message == "" {
		t.Fatalf("last message = %+v, want non-empty errorDetail", last)
	}

	assertTempRootEmpty(t, tempRoot)
	if solver.calls != 0 {
		t.Fatalf("solver called %d times for a malformed context, want 0", solver.calls)
	}
}

// TestBuildEmptyContextReportsMissingDockerfile locks the second half of the
// contract: an empty (but well-formed) tar stream has no Dockerfile, so Build
// must fail with an errorDetail and still clean up.
func TestBuildEmptyContextReportsMissingDockerfile(t *testing.T) {
	tempRoot := t.TempDir()
	adapter := New(newFakeImageStore(), &fakeSolver{}, Options{TempRoot: tempRoot, Logger: zap.NewNop()})

	var out bytes.Buffer
	err := adapter.Build(context.Background(), BuildRequest{
		Context: bytes.NewReader(nil),
		Tag:     "docker.io/library/empty:latest",
	}, &out)
	if err == nil {
		t.Fatal("Build() error = nil, want missing Dockerfile error")
	}

	messages := decodeDockerStream(t, out.Bytes())
	if len(messages) == 0 || messages[len(messages)-1].ErrorDetail == nil {
		t.Fatalf("messages = %+v, want trailing errorDetail", messages)
	}
	assertTempRootEmpty(t, tempRoot)
}

func assertTempRootEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", root, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("temp context leaked: %v", names)
	}
}

func TestBuildSuccessEmitsAuxAndCleansTemp(t *testing.T) {
	tempRoot := t.TempDir()
	solver := &fakeSolver{result: SolveResult{ImageID: "sha256:deadbeef00000000000000000000000000000000000000000000000000000000"}}
	adapter := New(newFakeImageStore(), solver, Options{TempRoot: tempRoot})

	contextTar := buildContextTar(t, map[string]string{
		"Dockerfile": "FROM scratch\nCOPY hello.txt /hello.txt\n",
		"hello.txt":  "hi\n",
	})
	var out bytes.Buffer
	if err := adapter.Build(context.Background(), BuildRequest{
		Context: bytes.NewReader(contextTar),
		Tag:     "demo/app:latest",
	}, &out); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	messages := decodeDockerStream(t, out.Bytes())
	var auxID string
	var successText strings.Builder
	for _, message := range messages {
		if message.Aux != nil {
			auxID = message.Aux.ID
		}
		successText.WriteString(message.Stream)
	}
	if auxID != "sha256:deadbeef00000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("aux ID = %q", auxID)
	}
	if !strings.Contains(successText.String(), "Successfully built deadbeef0000") {
		t.Fatalf("streams = %q, want Successfully built", successText.String())
	}
	if !strings.Contains(successText.String(), "Successfully tagged docker.io/demo/app:latest") {
		t.Fatalf("streams = %q, want Successfully tagged", successText.String())
	}

	assertTempRootEmpty(t, tempRoot)

	options := solver.lastOptions()
	if options.Dockerfile != "Dockerfile" {
		t.Fatalf("solver Dockerfile = %q", options.Dockerfile)
	}
	if options.Tag != "docker.io/demo/app:latest" {
		t.Fatalf("solver Tag = %q, want normalized tag", options.Tag)
	}
	if options.ContextDir == "" {
		t.Fatal("solver ContextDir is empty")
	}
}

func TestBuildSolveFailureEmitsErrorDetailAndCleansTemp(t *testing.T) {
	tempRoot := t.TempDir()
	solver := &fakeSolver{err: errors.New("solve exploded")}
	adapter := New(newFakeImageStore(), solver, Options{TempRoot: tempRoot})

	var out bytes.Buffer
	err := adapter.Build(context.Background(), BuildRequest{
		Context: bytes.NewReader(buildContextTar(t, map[string]string{"Dockerfile": "FROM scratch\n"})),
		Tag:     "demo/app:latest",
	}, &out)
	if err == nil {
		t.Fatal("Build() error = nil, want solve failure")
	}
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("Build() error = %T, want *BuildError", err)
	}
	if buildErr.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("StatusCode() = %d, want 500", buildErr.StatusCode())
	}

	messages := decodeDockerStream(t, out.Bytes())
	last := messages[len(messages)-1]
	if last.ErrorDetail == nil || !strings.Contains(last.ErrorDetail.Message, "solve exploded") {
		t.Fatalf("last message = %+v, want solve error detail", last)
	}
	assertTempRootEmpty(t, tempRoot)
}

func TestBuildVertexErrorDoesNotDuplicateErrorDetail(t *testing.T) {
	solver := &fakeSolver{
		statuses: []*buildkitclient.SolveStatus{{
			Vertexes: []*buildkitclient.Vertex{{
				Digest: digest.FromString("failing-vertex"),
				Name:   "[1/1] RUN false",
				Error:  "process did not complete successfully",
			}},
		}},
		err: errors.New("failed to solve: process did not complete successfully"),
	}
	adapter := New(newFakeImageStore(), solver, Options{TempRoot: t.TempDir()})

	var out bytes.Buffer
	if err := adapter.Build(context.Background(), BuildRequest{
		Context: bytes.NewReader(buildContextTar(t, map[string]string{"Dockerfile": "FROM scratch\n"})),
	}, &out); err == nil {
		t.Fatal("Build() error = nil, want solve failure")
	}

	messages := decodeDockerStream(t, out.Bytes())
	errorCount := 0
	for _, message := range messages {
		if message.ErrorDetail != nil {
			errorCount++
		}
	}
	if errorCount != 1 {
		t.Fatalf("errorDetail messages = %d, want 1: %s", errorCount, out.String())
	}
}

func TestBuildWithoutSolverIsNotImplemented(t *testing.T) {
	adapter := New(newFakeImageStore(), nil, Options{TempRoot: t.TempDir()})

	var out bytes.Buffer
	err := adapter.Build(context.Background(), BuildRequest{
		Context: bytes.NewReader(buildContextTar(t, map[string]string{"Dockerfile": "FROM scratch\n"})),
	}, &out)
	var buildErr *BuildError
	if !errors.As(err, &buildErr) || buildErr.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("Build() error = %v, want 501 BuildError", err)
	}
	messages := decodeDockerStream(t, out.Bytes())
	if len(messages) == 0 || messages[len(messages)-1].ErrorDetail == nil {
		t.Fatalf("messages = %+v, want errorDetail", messages)
	}
}

func TestBuildRejectsDockerfileOutsideContext(t *testing.T) {
	tempRoot := t.TempDir()
	adapter := New(newFakeImageStore(), &fakeSolver{}, Options{TempRoot: tempRoot})

	var out bytes.Buffer
	err := adapter.Build(context.Background(), BuildRequest{
		Context:    bytes.NewReader(buildContextTar(t, map[string]string{"Dockerfile": "FROM scratch\n"})),
		Dockerfile: "../outside/Dockerfile",
	}, &out)
	if err == nil {
		t.Fatal("Build() error = nil, want invalid Dockerfile path error")
	}
	messages := decodeDockerStream(t, out.Bytes())
	if len(messages) == 0 || messages[len(messages)-1].ErrorDetail == nil {
		t.Fatalf("messages = %+v, want errorDetail", messages)
	}
	assertTempRootEmpty(t, tempRoot)
}

func TestExtractTarContextRejectsArchiveTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	archive := tarArchive(t, func(writer *tar.Writer) {
		writeTarEntry(t, writer, "../escape.txt", "escaped")
	})
	err := extractTarContext(bytes.NewReader(archive), root)
	if !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("extractTarContext() error = %v, want ErrInvalidContext", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, "escape.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("traversal wrote outside the context root: %v", statErr)
	}
}

func TestExtractTarContextRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	archive := tarArchive(t, func(writer *tar.Writer) {
		header := &tar.Header{Name: "link", Linkname: "../../etc/passwd", Typeflag: tar.TypeSymlink}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
	})
	err := extractTarContext(bytes.NewReader(archive), root)
	if !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("extractTarContext() error = %v, want ErrInvalidContext", err)
	}
}

func TestExtractTarContextExtractsDirsFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	archive := tarArchive(t, func(writer *tar.Writer) {
		dir := &tar.Header{Name: "app/", Mode: 0o755, Typeflag: tar.TypeDir}
		if err := writer.WriteHeader(dir); err != nil {
			t.Fatalf("write dir header: %v", err)
		}
		writeTarEntry(t, writer, "app/config.txt", "value=1")
		link := &tar.Header{Name: "app/config-link", Linkname: "config.txt", Typeflag: tar.TypeSymlink}
		if err := writer.WriteHeader(link); err != nil {
			t.Fatalf("write symlink header: %v", err)
		}
	})

	if err := extractTarContext(bytes.NewReader(archive), root); err != nil {
		t.Fatalf("extractTarContext() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "app", "config.txt"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(content) != "value=1" {
		t.Fatalf("extracted content = %q", content)
	}
	target, err := os.Readlink(filepath.Join(root, "app", "config-link"))
	if err != nil {
		t.Fatalf("read extracted symlink: %v", err)
	}
	if target != "config.txt" {
		t.Fatalf("symlink target = %q", target)
	}
}

func tarArchive(t *testing.T, write func(*tar.Writer)) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	write(writer)
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return buffer.Bytes()
}

func writeTarEntry(t *testing.T, writer *tar.Writer, name, content string) {
	t.Helper()
	header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
}

func buildContextTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return tarArchive(t, func(writer *tar.Writer) {
		for _, name := range names {
			writeTarEntry(t, writer, name, files[name])
		}
	})
}
