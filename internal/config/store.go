package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

var (
	// ErrStoreClosed indicates that a store no longer accepts configuration work.
	ErrStoreClosed = errors.New("configuration store is closed")
	// ErrConfigFileNotSet indicates that file watching was requested without a path.
	ErrConfigFileNotSet = errors.New("configuration file is not set")
	// ErrConfigWatchStarted indicates that a store already owns a watcher.
	ErrConfigWatchStarted = errors.New("configuration watch is already started")
)

// ErrorHandler receives configuration errors observed during asynchronous reloads.
type ErrorHandler func(error)

// Store owns Viper and publishes only validated, immutable configuration snapshots.
// Runtime workers must use Current and must not access Viper directly.
type Store struct {
	current atomic.Pointer[Config]

	mu           sync.Mutex
	v            *viper.Viper
	initErr      error
	configFile   string
	errorHandler ErrorHandler
	errorCh      chan error
	closed       bool

	watcher          *fsnotify.Watcher
	watchWG          sync.WaitGroup
	closeOnce        sync.Once
	closeErr         error
	configChangeHook func(fsnotify.Event)
}

// NewStore creates a store initialized with the validated defaults.
func NewStore() *Store {
	v, err := newViper()
	cfg := Defaults()
	store := &Store{
		v:       v,
		initErr: err,
		errorCh: make(chan error, 16),
	}
	store.current.Store(&cfg)
	return store
}

// NewStoreWithConfigFile creates a store configured to read path before watching it.
func NewStoreWithConfigFile(path string) *Store {
	store := NewStore()
	store.SetConfigFile(path)
	return store
}

// Current returns a value copy of the last valid configuration snapshot.
func (s *Store) Current() Config {
	if snapshot := s.current.Load(); snapshot != nil {
		return *snapshot
	}
	return Defaults()
}

// SetConfigFile sets the file used by Load and WatchConfig.
func (s *Store) SetConfigFile(path string) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath != "" {
		if absolutePath, err := filepath.Abs(cleanPath); err == nil {
			cleanPath = absolutePath
		} else {
			cleanPath = filepath.Clean(cleanPath)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.configFile = cleanPath
	if s.v != nil && cleanPath != "" {
		s.v.SetConfigFile(cleanPath)
	}
}

// SetErrorHandler replaces the callback used to report asynchronous reload errors.
func (s *Store) SetErrorHandler(handler ErrorHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorHandler = handler
}

// Errors returns a best-effort stream of asynchronous reload errors.
func (s *Store) Errors() <-chan error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errorCh
}

// Load reads the configured file, decodes a complete Config, validates it, and
// publishes it atomically. A failed load leaves the previous snapshot untouched.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}
	if s.initErr != nil {
		return fmt.Errorf("initialize configuration decoder: %w", s.initErr)
	}
	if s.v == nil {
		return errors.New("configuration decoder is unavailable")
	}
	if s.configFile != "" {
		s.v.SetConfigFile(s.configFile)
		if err := s.v.ReadInConfig(); err != nil {
			return fmt.Errorf("read configuration file %q: %w", s.configFile, err)
		}
	}

	cfg, err := decodeConfig(s.v)
	if err != nil {
		return err
	}
	s.current.Store(&cfg)
	return nil
}

func (s *Store) reportError(err error) {
	if err == nil {
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	handler := s.errorHandler
	select {
	case s.errorCh <- err:
	default:
	}
	s.mu.Unlock()

	if handler != nil {
		handler(err)
	}
}
