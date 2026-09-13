package buildkit

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	buildkitclient "github.com/moby/buildkit/client"
	digest "github.com/opencontainers/go-digest"
	"go.uber.org/zap"
)

// BuildRequest is one Docker POST /build request decoded into adapter input.
type BuildRequest struct {
	// Context is the tar archive of the build context (the request body).
	Context io.Reader
	// Tag is the image reference to apply, e.g. "example/app:latest". Empty
	// discards the build result.
	Tag string
	// Dockerfile is the Dockerfile path inside the context. Empty defaults to
	// "Dockerfile".
	Dockerfile string
	// Platform selects the build platform, e.g. "linux/amd64".
	Platform string
	// BuildArgs are Dockerfile build arguments.
	BuildArgs map[string]string
	// Target selects a build stage.
	Target string
	// NoCache disables BuildKit cache reuse.
	NoCache bool
	// Auth carries registry credentials for base image pulls inside the build.
	Auth *RegistryAuth
}

// BuildError reports a failed build with the HTTP status the API layer should
// return. When Build returns a BuildError, the progress stream has already
// received its final errorDetail message.
type BuildError struct {
	// Status is the HTTP status to map (4xx for bad input, 5xx for build
	// infrastructure failures).
	Status int
	// Message is the Docker-facing failure message.
	Message string
	// Err is the underlying cause.
	Err error
}

// Error returns the Docker-facing failure message.
func (e *BuildError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap returns the underlying cause.
func (e *BuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StatusCode returns the HTTP status for this failure, defaulting to 500.
func (e *BuildError) StatusCode() int {
	if e == nil || e.Status < http.StatusBadRequest {
		return http.StatusInternalServerError
	}
	return e.Status
}

// Build materializes the tar context, runs a BuildKit solve, and writes Docker
// JSON progress messages to out. The temporary context is always removed, on
// success and on failure. A failed build emits an errorDetail message as its
// final stream line.
func (a *Adapter) Build(ctx context.Context, request BuildRequest, out io.Writer) error {
	progress := NewProgressWriter(out)

	if a.solver == nil {
		message := "build is not available: BuildKit solver is not configured"
		return a.failBuild(progress, http.StatusNotImplemented, message, ErrNoSolver)
	}
	if request.Context == nil {
		return a.failBuild(progress, http.StatusBadRequest, "build context is required", ErrInvalidContext)
	}

	contextDir, err := a.materializeContext(request.Context, request.Dockerfile)
	if err != nil {
		return a.failBuild(progress, http.StatusBadRequest, err.Error(), err)
	}
	defer a.cleanupContext(contextDir)

	tag := ""
	if strings.TrimSpace(request.Tag) != "" {
		tag, err = NormalizeReference(request.Tag)
		if err != nil {
			return a.failBuild(progress, http.StatusBadRequest, err.Error(), err)
		}
	}

	solveOptions := SolveOptions{
		ContextDir: contextDir,
		Dockerfile: dockerfileName(request.Dockerfile),
		Tag:        tag,
		Platform:   request.Platform,
		BuildArgs:  request.BuildArgs,
		Target:     request.Target,
		NoCache:    request.NoCache,
		Auth:       request.Auth,
	}

	statusCh := make(chan *buildkitclient.SolveStatus, 32)
	translate := newBuildProgress(progress)
	var translateErr error
	var translator sync.WaitGroup
	translator.Go(func() {
		for status := range statusCh {
			if translateErr == nil {
				translateErr = translate.Translate(status)
			}
		}
	})

	result, solveErr := a.solver.Solve(ctx, solveOptions, statusCh)
	translator.Wait()

	if solveErr == nil && translateErr != nil {
		solveErr = translateErr
	}
	if solveErr != nil {
		message := solveErr.Error()
		if !translate.EmittedError() {
			_ = progress.ErrorDetail(errorDetailCode, message)
		}
		a.logger.Warn("image build failed", zap.String("reference", tag), zap.Error(solveErr))
		return &BuildError{Status: http.StatusInternalServerError, Message: message, Err: solveErr}
	}

	imageID := result.ImageID
	if imageID == "" {
		imageID = result.Digest
	}
	if err := progress.Aux(imageID); err != nil {
		return &BuildError{Status: http.StatusInternalServerError, Message: err.Error(), Err: err}
	}
	if imageID != "" {
		_ = progress.Stream(fmt.Sprintf("Successfully built %s\n", shorthandDigest(digest.Digest(imageID))))
	}
	if tag != "" {
		_ = progress.Stream(fmt.Sprintf("Successfully tagged %s\n", tag))
	}
	a.logger.Info("image built", zap.String("reference", tag), zap.String("image_id", imageID))
	return nil
}

// materializeContext extracts the tar context into a temporary directory and
// verifies that the requested Dockerfile exists.
func (a *Adapter) materializeContext(reader io.Reader, dockerfile string) (string, error) {
	dir, err := os.MkdirTemp(a.tempRoot, "dockerdless-build-*")
	if err != nil {
		return "", fmt.Errorf("%w: create temp context: %w", ErrInvalidContext, err)
	}

	if err := extractTarContext(reader, dir); err != nil {
		a.cleanupContext(dir)
		return "", err
	}

	name := dockerfileName(dockerfile)
	local := filepath.FromSlash(name)
	if !filepath.IsLocal(local) {
		a.cleanupContext(dir)
		return "", fmt.Errorf("%w: invalid Dockerfile path %q", ErrInvalidContext, name)
	}
	if _, err := os.Stat(filepath.Join(dir, local)); err != nil {
		a.cleanupContext(dir)
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: cannot locate specified Dockerfile: %s", ErrInvalidContext, name)
		}
		return "", fmt.Errorf("%w: stat Dockerfile: %w", ErrInvalidContext, err)
	}
	return dir, nil
}

// cleanupContext removes a materialized context, logging (but not failing on)
// cleanup errors.
func (a *Adapter) cleanupContext(dir string) {
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		a.logger.Warn("failed to remove temp build context", zap.String("path", dir), zap.Error(err))
	}
}

func (a *Adapter) failBuild(progress *ProgressWriter, status int, message string, cause error) error {
	if writeErr := progress.ErrorDetail(errorDetailCode, message); writeErr != nil {
		cause = errors.Join(cause, writeErr)
	}
	return &BuildError{Status: status, Message: message, Err: cause}
}

// extractTarContext unpacks a Docker build context tar stream into root. All
// writes go through an os.Root, so archive traversal attempts cannot escape.
func extractTarContext(reader io.Reader, root string) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("%w: open context root: %w", ErrInvalidContext, err)
	}
	defer func() { _ = dir.Close() }()

	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: read tar stream: %w", ErrInvalidContext, err)
		}

		name, err := safeTarPath(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := dir.MkdirAll(name, directoryMode(header)); err != nil {
				return fmt.Errorf("%w: create directory %q: %w", ErrInvalidContext, name, err)
			}
		case tar.TypeReg:
			if err := writeTarFile(dir, name, header, tarReader); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := createTarSymlink(dir, name, header.Linkname); err != nil {
				return err
			}
		case tar.TypeLink:
			link, err := safeTarPath(header.Linkname)
			if err != nil {
				return err
			}
			if err := ensureParent(dir, name); err != nil {
				return err
			}
			if err := dir.Link(link, name); err != nil {
				return fmt.Errorf("%w: link %q to %q: %w", ErrInvalidContext, name, link, err)
			}
		default:
			// Character/block devices and FIFOs have no meaning in a build
			// context; skip them rather than fail the whole request.
		}
	}
}

func writeTarFile(dir *os.Root, name string, header *tar.Header, reader io.Reader) error {
	if err := ensureParent(dir, name); err != nil {
		return err
	}
	mode := fs.FileMode(header.Mode & 0o777)
	if mode == 0 {
		mode = 0o644
	}
	file, err := dir.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("%w: create file %q: %w", ErrInvalidContext, name, err)
	}
	defer func() { _ = file.Close() }()

	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("%w: write file %q: %w", ErrInvalidContext, name, err)
	}
	return nil
}

func createTarSymlink(dir *os.Root, name, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("%w: symlink %q has absolute target %q", ErrInvalidContext, name, target)
	}
	resolved := filepath.Join(filepath.Dir(name), filepath.FromSlash(target))
	if !filepath.IsLocal(resolved) {
		return fmt.Errorf("%w: symlink %q escapes the context", ErrInvalidContext, name)
	}
	if err := ensureParent(dir, name); err != nil {
		return err
	}
	if err := dir.Symlink(filepath.FromSlash(target), name); err != nil {
		return fmt.Errorf("%w: create symlink %q: %w", ErrInvalidContext, name, err)
	}
	return nil
}

func ensureParent(dir *os.Root, name string) error {
	parent := filepath.Dir(name)
	if parent == "." {
		return nil
	}
	if err := dir.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("%w: create directory %q: %w", ErrInvalidContext, parent, err)
	}
	return nil
}

// safeTarPath cleans a tar entry name and rejects anything that would escape
// the context root.
func safeTarPath(name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(name, "./")))
	if !filepath.IsLocal(cleaned) {
		return "", fmt.Errorf("%w: archive entry %q escapes the build context", ErrInvalidContext, name)
	}
	return cleaned, nil
}

func directoryMode(header *tar.Header) fs.FileMode {
	mode := fs.FileMode(header.Mode & 0o777)
	if mode == 0 {
		mode = 0o755
	}
	return mode | 0o700
}

func dockerfileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Dockerfile"
	}
	return value
}
