package fractal

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// ErrStoreClosed is returned when an operation is attempted on a closed Store.
var ErrStoreClosed = errors.New("store is closed")

// Store manages the append-only events log and binary summary tree on disk.
type Store struct {
	dir        string
	mu         sync.RWMutex
	closed     bool
	eventsFile *os.File
	treeFile   *os.File
	events     []Event
	nodes      map[NodeKey]Node
}

// OpenStore initializes or opens an existing fractal store at directory path.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.Wrapf(err, "creating storage directory %s", dir)
	}

	eventsPath := filepath.Join(dir, "events.jsonl")
	treePath := filepath.Join(dir, "tree.jsonl")

	events, err := loadEvents(eventsPath)
	if err != nil {
		return nil, errors.Wrap(err, "loading events log")
	}

	nodes, err := loadNodes(treePath)
	if err != nil {
		return nil, errors.Wrap(err, "loading tree nodes")
	}

	eventsFile, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, errors.Wrapf(err, "opening events file %s", eventsPath)
	}

	treeFile, err := os.OpenFile(treePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		eventsFile.Close()
		return nil, errors.Wrapf(err, "opening tree file %s", treePath)
	}

	return &Store{
		dir:        dir,
		eventsFile: eventsFile,
		treeFile:   treeFile,
		events:     events,
		nodes:      nodes,
	}, nil
}

func loadEvents(path string) ([]Event, error) {
	var events []Event
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return events, nil
	}
	if err != nil {
		return nil, errors.Wrapf(err, "opening %s", path)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// Skip torn/invalid line at end of file (e.g. from crash)
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrapf(err, "scanning %s", path)
	}
	return events, nil
}

func loadNodes(path string) (map[NodeKey]Node, error) {
	nodes := make(map[NodeKey]Node)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nodes, nil
	}
	if err != nil {
		return nil, errors.Wrapf(err, "opening %s", path)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var node Node
		if err := json.Unmarshal(line, &node); err != nil {
			continue
		}
		nodes[node.Key()] = node
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrapf(err, "scanning %s", path)
	}
	return nodes, nil
}

// AppendEvent atomically logs a new event with fsync and updates the store.
func (s *Store) AppendEvent(kind EventKind, text string) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return Event{}, ErrStoreClosed
	}

	id := len(s.events)
	formatted := Event{Kind: kind, Text: text}.FormatLine()
	size := len([]byte(formatted))

	ev := Event{
		ID:        id,
		Kind:      kind,
		Text:      text,
		Timestamp: time.Now().UTC(),
		Size:      size,
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return Event{}, errors.Wrap(err, "marshaling event")
	}

	data = append(data, '\n')
	if _, err := s.eventsFile.Write(data); err != nil {
		return Event{}, errors.Wrap(err, "writing event to disk")
	}
	if err := s.eventsFile.Sync(); err != nil {
		return Event{}, errors.Wrap(err, "syncing event to disk")
	}

	s.events = append(s.events, ev)
	return ev, nil
}

// SaveNode persists a summary node to disk and registers it in memory.
func (s *Store) SaveNode(node Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}

	node.Size = len([]byte(node.Text))
	data, err := json.Marshal(node)
	if err != nil {
		return errors.Wrap(err, "marshaling node")
	}

	data = append(data, '\n')
	if _, err := s.treeFile.Write(data); err != nil {
		return errors.Wrap(err, "writing tree node to disk")
	}
	if err := s.treeFile.Sync(); err != nil {
		return errors.Wrap(err, "syncing tree node to disk")
	}

	s.nodes[node.Key()] = node
	return nil
}

// GetEvent returns the event by ID if present.
func (s *Store) GetEvent(id int) (Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if id < 0 || id >= len(s.events) {
		return Event{}, false
	}
	return s.events[id], true
}

// CountEvents returns total logged events.
func (s *Store) CountEvents() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.events)
}

// EventsSlice returns a copy of events in range [start, end).
func (s *Store) EventsSlice(start, end int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.events)
	if start < 0 {
		start = 0
	}
	if end > total {
		end = total
	}
	if start >= end {
		return nil
	}

	res := make([]Event, end-start)
	copy(res, s.events[start:end])
	return res
}

// GetNode retrieves a node by coordinate (level, index).
func (s *Store) GetNode(level, index int) (Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[NodeKey{Level: level, Index: index}]
	return node, ok
}

// GetNodeByAddress retrieves a node by its id+n address.
func (s *Store) GetNodeByAddress(id, n int) (Node, bool) {
	level, index, err := ParseAddress(id, n)
	if err != nil {
		return Node{}, false
	}
	return s.GetNode(level, index)
}

// Close flushes and closes open file handles idempotently.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	var errEvents, errTree error
	if s.eventsFile != nil {
		errEvents = s.eventsFile.Close()
		s.eventsFile = nil
	}
	if s.treeFile != nil {
		errTree = s.treeFile.Close()
		s.treeFile = nil
	}

	if errEvents != nil {
		return errors.Wrap(errEvents, "closing events file")
	}
	if errTree != nil {
		return errors.Wrap(errTree, "closing tree file")
	}
	return nil
}
