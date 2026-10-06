package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher provides real-time filesystem observation using fsnotify,
// automatically updating the FilePicker and handling directory trees.
type Watcher struct {
	picker      *FilePicker
	rootDir     string
	watcher     *fsnotify.Watcher
	mu          sync.Mutex
	watchedDirs map[string]bool
	stopCh      chan struct{}
	wg          sync.WaitGroup

	rescanTimer *time.Timer
	rescanMu    sync.Mutex
}

// NewWatcher initializes and starts a recursive directory watcher on rootDir.
func NewWatcher(picker *FilePicker, rootDir string) (*Watcher, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		picker:      picker,
		rootDir:     absRoot,
		watcher:     fsWatcher,
		watchedDirs: make(map[string]bool),
		stopCh:      make(chan struct{}),
	}

	// Add root directory and all existing non-ignored subdirectories
	w.addDirRecursive(absRoot)

	w.wg.Add(1)
	go w.eventLoop()

	return w, nil
}

// isIgnoredDir checks if a directory path matches default ignore patterns or gitignore.
func (w *Watcher) isIgnoredDir(absDir string) bool {
	rel, err := filepath.Rel(w.rootDir, absDir)
	if err != nil || rel == "." || rel == "" {
		return false
	}

	if w.picker != nil && w.picker.scanner != nil {
		return w.picker.scanner.IsIgnored(rel, true)
	}

	parts := toParts(rel)
	for _, part := range parts {
		if part == ".git" || part == "node_modules" || part == ".hg" || part == ".svn" {
			return true
		}
	}

	return false
}

// isIgnoredFile checks if a file path is ignored by gitignore or default rules.
func (w *Watcher) isIgnoredFile(rel string) bool {
	if w.picker != nil && w.picker.scanner != nil {
		return w.picker.scanner.IsIgnored(rel, false)
	}
	return false
}

// addDirRecursive traverses absDir and registers all non-ignored directories with fsnotify.
func (w *Watcher) addDirRecursive(absDir string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	_ = filepath.WalkDir(absDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}

		if w.isIgnoredDir(path) {
			return filepath.SkipDir
		}

		if !w.watchedDirs[path] {
			if err := w.watcher.Add(path); err == nil {
				w.watchedDirs[path] = true
			}
		}

		return nil
	})
}

// scheduleRescan debounces picker.Rescan when configuration files (e.g., .gitignore) change.
func (w *Watcher) scheduleRescan() {
	w.rescanMu.Lock()
	defer w.rescanMu.Unlock()

	if w.rescanTimer != nil {
		w.rescanTimer.Stop()
	}
	w.rescanTimer = time.AfterFunc(100*time.Millisecond, func() {
		_ = w.picker.Rescan(context.Background())
	})
}

func (w *Watcher) eventLoop() {
	defer w.wg.Done()

	for {
		select {
		case <-w.stopCh:
			return

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			_ = err

		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			rel, err := filepath.Rel(w.rootDir, ev.Name)
			if err != nil || rel == "." {
				continue
			}
			relSlash := filepath.ToSlash(rel)

			// Handle .gitignore modifications
			if filepath.Base(ev.Name) == ".gitignore" {
				w.scheduleRescan()
				continue
			}

			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.mu.Lock()
				wasDir := w.watchedDirs[ev.Name]
				if wasDir {
					delete(w.watchedDirs, ev.Name)
					_ = w.watcher.Remove(ev.Name)
				}
				w.mu.Unlock()

				if wasDir {
					// Remove any files under this directory from picker
					prefix := relSlash + "/"
					for _, f := range w.picker.Files() {
						if strings.HasPrefix(f, prefix) {
							w.picker.RemoveFile(f)
						}
					}
				} else {
					w.picker.RemoveFile(relSlash)
				}
				continue
			}

			if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 {
				fi, err := os.Stat(ev.Name)
				if err != nil {
					continue
				}

				if fi.IsDir() {
					if !w.isIgnoredDir(ev.Name) {
						w.addDirRecursive(ev.Name)
						// Scan newly added folder and add files
						_ = filepath.WalkDir(ev.Name, func(p string, d os.DirEntry, err error) error {
							if err == nil && !d.IsDir() {
								fileRel, rErr := filepath.Rel(w.rootDir, p)
								if rErr == nil && !w.isIgnoredFile(fileRel) {
									w.picker.AddFile(filepath.ToSlash(fileRel))
								}
							}
							return nil
						})
					}
				} else {
					if !w.isIgnoredDir(filepath.Dir(ev.Name)) && !w.isIgnoredFile(rel) {
						w.picker.AddFile(relSlash)
					}
				}
			}
		}
	}
}

// Close terminates the filesystem watcher and worker goroutines.
func (w *Watcher) Close() error {
	w.mu.Lock()
	select {
	case <-w.stopCh:
		w.mu.Unlock()
		return nil
	default:
		close(w.stopCh)
	}
	w.mu.Unlock()

	w.rescanMu.Lock()
	if w.rescanTimer != nil {
		w.rescanTimer.Stop()
	}
	w.rescanMu.Unlock()

	err := w.watcher.Close()
	w.wg.Wait()
	return err
}
