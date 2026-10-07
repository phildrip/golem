package fractal

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"
	bbolt "go.etcd.io/bbolt"
)

// ErrStoreClosed is returned when an operation is attempted on a closed Store.
var ErrStoreClosed = errors.New("store is closed")

var (
	bucketEvents   = []byte("events")
	bucketNodes    = []byte("nodes")
	bucketMetadata = []byte("metadata")
)

// EventListener is a callback function invoked when a new event is logged.
type EventListener func(Event)

// Store manages the append-only events log and binary summary tree on disk using bbolt.
type Store struct {
	dir        string
	db         *bbolt.DB
	mu         sync.RWMutex
	closed     bool
	events     []Event
	nodes      map[NodeKey]Node
	listeners  []EventListener
	eventChan  chan Event
	dispatchWg sync.WaitGroup
}

func encodeUint64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func nodeKeyBytes(k NodeKey) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[:8], uint64(k.Level))
	binary.BigEndian.PutUint64(b[8:], uint64(k.Index))
	return b
}

func parseNodeKeyBytes(b []byte) (NodeKey, bool) {
	if len(b) != 16 {
		return NodeKey{}, false
	}
	level := int(binary.BigEndian.Uint64(b[:8]))
	index := int(binary.BigEndian.Uint64(b[8:]))
	return NodeKey{Level: level, Index: index}, true
}

// OpenStore initializes or opens an existing fractal store at directory path using bbolt.
// If legacy events.jsonl or tree.jsonl exist, they are automatically migrated to bbolt.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.Wrapf(err, "creating storage directory %s", dir)
	}

	dbPath := filepath.Join(dir, "fractal.db")
	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, errors.Wrapf(err, "opening bbolt database %s", dbPath)
	}

	// Initialize buckets
	if err := db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketEvents); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketNodes); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketMetadata); err != nil {
			return err
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, errors.Wrap(err, "initializing bbolt buckets")
	}

	s := &Store{
		dir:       dir,
		db:        db,
		events:    make([]Event, 0),
		nodes:     make(map[NodeKey]Node),
		eventChan: make(chan Event, 1024),
	}

	// 1. Check for legacy migration
	if err := s.migrateLegacyFilesIfPresent(); err != nil {
		db.Close()
		return nil, errors.Wrap(err, "migrating legacy memory files")
	}

	// 2. Load all events and nodes into in-memory cache from bbolt
	if err := s.loadFromDB(); err != nil {
		db.Close()
		return nil, errors.Wrap(err, "loading from bbolt database")
	}

	// 3. Start background event dispatcher for strictly ordered, non-blocking listener notifications
	s.dispatchWg.Add(1)
	go s.runDispatcher()

	return s, nil
}

func (s *Store) runDispatcher() {
	defer s.dispatchWg.Done()
	for ev := range s.eventChan {
		s.mu.RLock()
		if len(s.listeners) == 0 {
			s.mu.RUnlock()
			continue
		}
		listeners := make([]EventListener, len(s.listeners))
		copy(listeners, s.listeners)
		s.mu.RUnlock()

		for _, l := range listeners {
			// Catch panics in user listeners to prevent crashing agent/server
			func(fn EventListener, e Event) {
				defer func() {
					_ = recover()
				}()
				fn(e)
			}(l, ev)
		}
	}
}

// migrateLegacyFilesIfPresent migrates existing events.jsonl and tree.jsonl into bbolt.
func (s *Store) migrateLegacyFilesIfPresent() error {
	eventsPath := filepath.Join(s.dir, "events.jsonl")
	treePath := filepath.Join(s.dir, "tree.jsonl")

	legacyEvents, err := loadEventsJSONL(eventsPath)
	if err != nil {
		return err
	}
	legacyNodes, err := loadNodesJSONL(treePath)
	if err != nil {
		return err
	}

	if len(legacyEvents) == 0 && len(legacyNodes) == 0 {
		return nil
	}

	// Write legacy data to DB if DB is currently empty
	return s.db.Update(func(tx *bbolt.Tx) error {
		bEv := tx.Bucket(bucketEvents)
		bNodes := tx.Bucket(bucketNodes)

		if bEv.Stats().KeyN == 0 && len(legacyEvents) > 0 {
			for _, ev := range legacyEvents {
				data, err := json.Marshal(ev)
				if err != nil {
					return err
				}
				if err := bEv.Put(encodeUint64(uint64(ev.ID)), data); err != nil {
					return err
				}
			}
		}

		if bNodes.Stats().KeyN == 0 && len(legacyNodes) > 0 {
			for k, node := range legacyNodes {
				data, err := json.Marshal(node)
				if err != nil {
					return err
				}
				if err := bNodes.Put(nodeKeyBytes(k), data); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) loadFromDB() error {
	return s.db.View(func(tx *bbolt.Tx) error {
		bEv := tx.Bucket(bucketEvents)
		cEv := bEv.Cursor()

		// First determine max event ID to ensure 1:1 indexing without gap corruption
		maxID := -1
		for k, v := cEv.First(); k != nil; k, v = cEv.Next() {
			var ev Event
			if err := json.Unmarshal(v, &ev); err != nil {
				continue
			}
			if ev.ID > maxID {
				maxID = ev.ID
			}
		}

		if maxID >= 0 {
			s.events = make([]Event, maxID+1)
			for k, v := cEv.First(); k != nil; k, v = cEv.Next() {
				var ev Event
				if err := json.Unmarshal(v, &ev); err != nil {
					continue
				}
				if ev.ID >= 0 && ev.ID < len(s.events) {
					s.events[ev.ID] = ev
				}
			}
		}

		bNodes := tx.Bucket(bucketNodes)
		cNodes := bNodes.Cursor()
		for k, v := cNodes.First(); k != nil; k, v = cNodes.Next() {
			var node Node
			if err := json.Unmarshal(v, &node); err != nil {
				continue
			}
			s.nodes[node.Key()] = node
		}
		return nil
	})
}

func loadEventsJSONL(path string) ([]Event, error) {
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
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// Skip torn lines
			continue
		}
		events = append(events, ev)
	}
	return events, nil
}

func loadNodesJSONL(path string) (map[NodeKey]Node, error) {
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
	return nodes, nil
}

// OnEvent registers a reactive listener called whenever a new event is appended.
func (s *Store) OnEvent(fn EventListener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// AppendEvent atomically logs a new event with bbolt and dispatches to reactive listeners.
// Optional AppendEventOpts allows setting a specific historical timestamp or metadata.
func (s *Store) AppendEvent(kind EventKind, text string, opts ...AppendEventOpts) (Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Event{}, ErrStoreClosed
	}

	ts := time.Now().UTC()
	if len(opts) > 0 && !opts[0].Timestamp.IsZero() {
		ts = opts[0].Timestamp.UTC()
	}

	id := len(s.events)
	formatted := Event{Kind: kind, Text: text}.FormatLine()
	size := len([]byte(formatted))

	ev := Event{
		ID:        id,
		Kind:      kind,
		Text:      text,
		Timestamp: ts,
		Size:      size,
	}

	data, err := json.Marshal(ev)
	if err != nil {
		s.mu.Unlock()
		return Event{}, errors.Wrap(err, "marshaling event")
	}

	// Persist atomically to bbolt
	err = s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketEvents).Put(encodeUint64(uint64(id)), data)
	})
	if err != nil {
		s.mu.Unlock()
		return Event{}, errors.Wrap(err, "writing event to bbolt")
	}

	s.events = append(s.events, ev)

	// Send to sequential background dispatcher channel non-blockingly
	select {
	case s.eventChan <- ev:
	default:
		// Channel buffer full: spin off async goroutine to avoid blocking AppendEvent
		go func(e Event) {
			s.eventChan <- e
		}(ev)
	}

	s.mu.Unlock()
	return ev, nil
}

// SaveNode persists a summary node to bbolt and registers it in memory.
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

	err = s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketNodes).Put(nodeKeyBytes(node.Key()), data)
	})
	if err != nil {
		return errors.Wrap(err, "writing tree node to bbolt")
	}

	s.nodes[node.Key()] = node
	return nil
}

// GetEvent returns the event by ID if present.
func (s *Store) GetEvent(id int) (Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || id < 0 || id >= len(s.events) {
		return Event{}, false
	}
	return s.events[id], true
}

// CountEvents returns total logged events.
func (s *Store) CountEvents() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0
	}
	return len(s.events)
}

// EventsSlice returns a copy of events in range [start, end).
func (s *Store) EventsSlice(start, end int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil
	}

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

// GetAllEvents atomically acquires a read lock and returns a safe copy of all logged events.
func (s *Store) GetAllEvents() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed || len(s.events) == 0 {
		return []Event{}
	}

	res := make([]Event, len(s.events))
	copy(res, s.events)
	return res
}

// GetEventsPaginated atomically returns events for the given offset and limit.
func (s *Store) GetEventsPaginated(offset, limit int) ([]Event, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return []Event{}, 0
	}

	total := len(s.events)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []Event{}, total
	}

	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}

	res := make([]Event, end-offset)
	copy(res, s.events[offset:end])
	return res, total
}

// GetNode retrieves a node by coordinate (level, index).
func (s *Store) GetNode(level, index int) (Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return Node{}, false
	}

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

// Close flushes and closes open bbolt database handle idempotently.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true

	// Close dispatcher channel and wait for dispatcher to finish draining
	if s.eventChan != nil {
		close(s.eventChan)
	}

	var closeErr error
	if s.db != nil {
		closeErr = s.db.Close()
		s.db = nil
	}
	s.mu.Unlock()

	s.dispatchWg.Wait()

	if closeErr != nil {
		return errors.Wrap(closeErr, "closing bbolt db")
	}
	return nil
}
