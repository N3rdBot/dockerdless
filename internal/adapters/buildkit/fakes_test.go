package buildkit

import (
	"context"
	"sort"
	"strings"
	"sync"

	buildkitclient "github.com/moby/buildkit/client"
)

// fakeImageStore is an in-memory ImageStore so adapter unit tests never need a
// containerd daemon.
type fakeImageStore struct {
	mu sync.Mutex

	records map[string]ImageRecord

	pulls   []string
	deletes []string

	pullErr   error
	getErr    error
	listErr   error
	deleteErr error
}

func newFakeImageStore(records ...ImageRecord) *fakeImageStore {
	store := &fakeImageStore{records: map[string]ImageRecord{}}
	for _, record := range records {
		store.records[record.Name] = record
	}
	return store
}

func (f *fakeImageStore) Pull(_ context.Context, ref string, _ PullOptions) (ImageRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pullErr != nil {
		return ImageRecord{}, f.pullErr
	}
	normalized, err := NormalizeReference(ref)
	if err != nil {
		return ImageRecord{}, err
	}
	f.pulls = append(f.pulls, normalized)
	if record, ok := f.records[normalized]; ok {
		return record, nil
	}
	return ImageRecord{}, ErrNotFound
}

func (f *fakeImageStore) Get(_ context.Context, ref string) (ImageRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return ImageRecord{}, f.getErr
	}
	if normalized, err := NormalizeReference(ref); err == nil {
		if record, ok := f.records[normalized]; ok {
			return record, nil
		}
	}
	for _, record := range f.records {
		if recordMatchesID(record, strings.TrimSpace(ref)) {
			return record, nil
		}
	}
	return ImageRecord{}, ErrNotFound
}

func (f *fakeImageStore) List(_ context.Context) ([]ImageRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]ImageRecord, 0, len(f.records))
	for _, record := range f.records {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeImageStore) Delete(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	normalized, err := NormalizeReference(ref)
	if err != nil {
		return err
	}
	if _, ok := f.records[normalized]; !ok {
		return ErrNotFound
	}
	f.deletes = append(f.deletes, normalized)
	delete(f.records, normalized)
	return nil
}

// fakeSolver is a Solver that replays canned statuses and a canned result.
type fakeSolver struct {
	mu       sync.Mutex
	statuses []*buildkitclient.SolveStatus
	result   SolveResult
	err      error
	opts     SolveOptions
	calls    int
}

func (f *fakeSolver) Solve(_ context.Context, opts SolveOptions, statusCh chan *buildkitclient.SolveStatus) (SolveResult, error) {
	f.mu.Lock()
	f.calls++
	f.opts = opts
	statuses := append([]*buildkitclient.SolveStatus(nil), f.statuses...)
	result, err := f.result, f.err
	f.mu.Unlock()

	for _, status := range statuses {
		statusCh <- status
	}
	// The BuildKit client closes the status channel before returning; solvers
	// must mirror that contract so consumers can range over it.
	close(statusCh)
	return result, err
}

func (f *fakeSolver) lastOptions() SolveOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opts
}
