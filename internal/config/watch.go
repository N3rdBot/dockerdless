package config

import (
	"fmt"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// OnConfigChange registers a callback invoked after each watched file event.
// The callback runs after a successful publish or after an error is reported.
func (s *Store) OnConfigChange(handler func(fsnotify.Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configChangeHook = handler
}

// WatchConfig loads the configured file and starts a stoppable directory watcher.
// The directory, rather than only the file, is watched so atomic replacements are
// observed just like Viper's WatchConfig behavior.
func (s *Store) WatchConfig() error {
	if err := s.Load(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	if s.configFile == "" {
		return ErrConfigFileNotSet
	}
	if s.watcher != nil {
		return ErrConfigWatchStarted
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create configuration watcher: %w", err)
	}
	configFile := filepath.Clean(s.configFile)
	if err := watcher.Add(filepath.Dir(configFile)); err != nil {
		closeErr := watcher.Close()
		if closeErr != nil {
			return fmt.Errorf("watch configuration directory: %w; close watcher: %v", err, closeErr)
		}
		return fmt.Errorf("watch configuration directory: %w", err)
	}

	s.watcher = watcher
	s.watchWG.Add(1)
	go s.watchLoop(watcher, configFile)
	return nil
}

func (s *Store) watchLoop(watcher *fsnotify.Watcher, configFile string) {
	defer s.watchWG.Done()
	realConfigFile, _ := filepath.EvalSymlinks(configFile)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !isConfigEvent(event, configFile, &realConfigFile) {
				continue
			}
			s.handleConfigChange(event)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			if err != nil {
				s.reportError(fmt.Errorf("configuration watcher: %w", err))
			}
		}
	}
}

func (s *Store) handleConfigChange(event fsnotify.Event) {
	if err := s.Load(); err != nil {
		s.reportError(fmt.Errorf("reload configuration after %s: %w", event.Op.String(), err))
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	handler := s.configChangeHook
	s.mu.Unlock()
	if handler != nil {
		handler(event)
	}
}

func isConfigEvent(event fsnotify.Event, configFile string, realConfigFile *string) bool {
	cleanName := filepath.Clean(event.Name)
	if cleanName == configFile && event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
		return true
	}

	currentRealPath, err := filepath.EvalSymlinks(configFile)
	if err != nil || currentRealPath == "" || *realConfigFile == "" {
		return false
	}
	if currentRealPath == *realConfigFile {
		return false
	}
	*realConfigFile = currentRealPath
	return true
}

// Close stops the watcher and waits for its event loop to exit.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		watcher := s.watcher
		s.watcher = nil
		s.mu.Unlock()

		if watcher != nil {
			s.closeErr = watcher.Close()
		}
		s.watchWG.Wait()
	})
	return s.closeErr
}

// Stop is an alias for Close for callers that use lifecycle terminology.
func (s *Store) Stop() error {
	return s.Close()
}
