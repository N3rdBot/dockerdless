package buildkit

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/docker/go-units"
	buildkitclient "github.com/moby/buildkit/client"
	digest "github.com/opencontainers/go-digest"
)

// errorDetailCode is the machine-readable code carried by Docker errorDetail
// messages for build failures.
const errorDetailCode = 1

// dockerMessage is one line of Docker's newline-delimited JSON progress
// stream. Field names and shapes follow Docker's jsonstream.Message plus the
// legacy progress string that the Docker CLI still renders.
type dockerMessage struct {
	Stream         string          `json:"stream,omitempty"`
	Status         string          `json:"status,omitempty"`
	Progress       string          `json:"progress,omitempty"`
	ProgressDetail *dockerProgress `json:"progressDetail,omitempty"`
	ID             string          `json:"id,omitempty"`
	Aux            *dockerAux      `json:"aux,omitempty"`
	ErrorDetail    *dockerError    `json:"errorDetail,omitempty"`
	// ErrorMessage mirrors ErrorDetail.Message for older Docker clients.
	ErrorMessage string `json:"error,omitempty"`
}

// dockerProgress is the numeric companion of the progress string.
type dockerProgress struct {
	Current int64 `json:"current,omitempty"`
	Total   int64 `json:"total,omitempty"`
}

// dockerAux carries out-of-band data such as the built image ID.
type dockerAux struct {
	ID string `json:"ID"`
}

// dockerError is Docker's errorDetail object.
type dockerError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ProgressWriter serializes Docker JSON messages to an output stream. It is
// safe for concurrent use and guarantees one complete JSON document per line.
type ProgressWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
	written bool
}

// NewProgressWriter returns a writer that emits Docker JSON stream messages.
func NewProgressWriter(out io.Writer) *ProgressWriter {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return &ProgressWriter{encoder: encoder}
}

// Written reports whether at least one message was emitted.
func (p *ProgressWriter) Written() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.written
}

// Stream emits {"stream":"..."}.
func (p *ProgressWriter) Stream(text string) error {
	return p.write(dockerMessage{Stream: text})
}

// Status emits {"status":"...","progress":"..."} plus progressDetail.
func (p *ProgressWriter) Status(status, progress string, current, total int64) error {
	message := dockerMessage{Status: status, Progress: progress}
	if current > 0 || total > 0 {
		message.ProgressDetail = &dockerProgress{Current: current, Total: total}
	}
	return p.write(message)
}

// Aux emits {"aux":{"ID":"..."}}.
func (p *ProgressWriter) Aux(id string) error {
	if id == "" {
		return nil
	}
	return p.write(dockerMessage{Aux: &dockerAux{ID: id}})
}

// ErrorDetail emits {"errorDetail":{"code":...,"message":"..."}} with the
// legacy {"error":"..."} mirror.
func (p *ProgressWriter) ErrorDetail(code int, message string) error {
	return p.write(dockerMessage{
		ErrorDetail:  &dockerError{Code: code, Message: message},
		ErrorMessage: message,
	})
}

func (p *ProgressWriter) write(message dockerMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.encoder.Encode(message); err != nil {
		return fmt.Errorf("write progress message: %w", err)
	}
	p.written = true
	return nil
}

// buildStep is one numbered BuildKit vertex.
type buildStep struct {
	number int
	label  string
}

// buildProgress translates BuildKit solve status batches into Docker JSON
// messages. It numbers build steps in the order BuildKit reports them and
// de-duplicates repeated lifecycle updates.
type buildProgress struct {
	writer *ProgressWriter

	steps     map[digest.Digest]buildStep
	announced map[digest.Digest]bool
	completed map[digest.Digest]bool
	statuses  map[string]string
	sawError  bool
}

// newBuildProgress creates a translator around a progress writer.
func newBuildProgress(writer *ProgressWriter) *buildProgress {
	return &buildProgress{
		writer:    writer,
		steps:     map[digest.Digest]buildStep{},
		announced: map[digest.Digest]bool{},
		completed: map[digest.Digest]bool{},
		statuses:  map[string]string{},
	}
}

// Translate writes the Docker messages for one BuildKit status batch.
func (p *buildProgress) Translate(status *buildkitclient.SolveStatus) error {
	if status == nil {
		return nil
	}
	for _, vertex := range status.Vertexes {
		if err := p.vertex(vertex); err != nil {
			return err
		}
	}
	for _, vertexStatus := range status.Statuses {
		if err := p.vertexStatus(vertexStatus); err != nil {
			return err
		}
	}
	for _, entry := range status.Logs {
		if err := p.log(entry); err != nil {
			return err
		}
	}
	for _, warning := range status.Warnings {
		if err := p.warning(warning); err != nil {
			return err
		}
	}
	return nil
}

func (p *buildProgress) vertex(vertex *buildkitclient.Vertex) error {
	step, ok := p.steps[vertex.Digest]
	if !ok {
		name := vertex.Name
		if name == "" {
			name = shorthandDigest(vertex.Digest)
		}
		step = buildStep{
			number: len(p.steps) + 1,
			label:  fmt.Sprintf("#%d %s", len(p.steps)+1, name),
		}
		p.steps[vertex.Digest] = step
	}

	if !p.announced[vertex.Digest] && shouldAnnounce(vertex) {
		p.announced[vertex.Digest] = true
		if err := p.writer.Stream(step.label + "\n"); err != nil {
			return err
		}
	}

	if vertex.Error != "" {
		if p.completed[vertex.Digest] {
			return nil
		}
		p.completed[vertex.Digest] = true
		p.sawError = true
		return p.writer.ErrorDetail(errorDetailCode, vertex.Error)
	}

	if vertex.Completed != nil {
		if p.completed[vertex.Digest] {
			return nil
		}
		p.completed[vertex.Digest] = true
		suffix := "DONE"
		if vertex.Cached {
			suffix = "CACHED"
		}
		return p.writer.Stream(fmt.Sprintf("#%d %s\n", step.number, suffix))
	}
	return nil
}

// shouldAnnounce reports whether a vertex gets its "#N name" stream line.
// Cached steps surface only as "#N CACHED", matching buildx plain output.
func shouldAnnounce(vertex *buildkitclient.Vertex) bool {
	if vertex.Cached {
		return false
	}
	return vertex.Started != nil || vertex.Completed != nil
}

func (p *buildProgress) vertexStatus(vertexStatus *buildkitclient.VertexStatus) error {
	name := vertexStatus.Name
	if name == "" {
		name = vertexStatus.ID
	}
	if name == "" {
		name = shorthandDigest(vertexStatus.Vertex)
	}

	progress := formatProgress(vertexStatus.Current, vertexStatus.Total)
	if progress == "" && vertexStatus.Completed != nil {
		progress = "done"
	}
	if p.statuses[vertexStatus.ID] == progress {
		return nil
	}
	p.statuses[vertexStatus.ID] = progress
	return p.writer.Status(name, progress, vertexStatus.Current, vertexStatus.Total)
}

func (p *buildProgress) log(entry *buildkitclient.VertexLog) error {
	if len(entry.Data) == 0 {
		return nil
	}
	text := string(entry.Data)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return p.writer.Stream(text)
}

func (p *buildProgress) warning(warning *buildkitclient.VertexWarning) error {
	text := string(warning.Short)
	if len(warning.Detail) > 0 && len(warning.Detail[0]) > 0 {
		text += ": " + string(warning.Detail[0])
	}
	if text == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return p.writer.Stream("WARNING: " + text)
}

// EmittedError reports whether the translator already wrote an errorDetail
// message. Callers use it to avoid emitting a duplicate error for one failure.
// It is safe to call after the status channel is drained.
func (p *buildProgress) EmittedError() bool { return p.sawError }

// formatProgress renders a Docker-style "current/total (percent)" string.
func formatProgress(current, total int64) string {
	switch {
	case total <= 0 && current <= 0:
		return ""
	case total <= 0:
		return units.HumanSize(float64(current))
	default:
		percent := float64(current) / float64(total) * 100
		return fmt.Sprintf("%s/%s (%.0f%%)",
			units.HumanSize(float64(current)),
			units.HumanSize(float64(total)),
			percent)
	}
}

// shorthandDigest renders a digest as its familiar 12-character encoded prefix,
// dropping the algorithm prefix so "sha256:abc..." becomes "abc...".
func shorthandDigest(value digest.Digest) string {
	text := value.Encoded()
	if len(text) <= 12 {
		return text
	}
	return text[:12]
}
