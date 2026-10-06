package engine

import (
	"strings"
	"sync"
)

// BigramKey encodes two lowercase ASCII bytes (32..126) into a uint16.
func BigramKey(a, b byte) uint16 {
	return (uint16(a) << 8) | uint16(b)
}

// InvertedIndex stores bitsets of candidate file indices containing each bigram.
// Uses flattened []uint64 bitsets for maximum CPU cache efficiency and minimal GC overhead.
type InvertedIndex struct {
	mu      sync.RWMutex
	columns map[uint16][]uint64
	numDocs int
}

// NewInvertedIndex initializes an empty bigram inverted index.
func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{
		columns: make(map[uint16][]uint64),
	}
}

// AddDocument indexes bigrams found in path for docID.
func (idx *InvertedIndex) AddDocument(docID int, path string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if docID >= idx.numDocs {
		idx.numDocs = docID + 1
	}

	wordIdx := docID / 64
	bitMask := uint64(1) << (docID % 64)

	cleanPath := strings.ToLower(path)
	if len(cleanPath) < 2 {
		return
	}

	seen := make(map[uint16]bool, len(cleanPath))
	for i := 0; i < len(cleanPath)-1; i++ {
		key := BigramKey(cleanPath[i], cleanPath[i+1])
		if seen[key] {
			continue
		}
		seen[key] = true

		col := idx.columns[key]
		if wordIdx >= len(col) {
			newCol := make([]uint64, wordIdx+1)
			copy(newCol, col)
			col = newCol
		}
		col[wordIdx] |= bitMask
		idx.columns[key] = col
	}
}

// RemoveDocument removes bigrams for docID corresponding to path.
func (idx *InvertedIndex) RemoveDocument(docID int, path string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	wordIdx := docID / 64
	bitMask := uint64(1) << (docID % 64)

	cleanPath := strings.ToLower(path)
	if len(cleanPath) < 2 {
		return
	}

	for i := 0; i < len(cleanPath)-1; i++ {
		key := BigramKey(cleanPath[i], cleanPath[i+1])
		col, ok := idx.columns[key]
		if ok && wordIdx < len(col) {
			col[wordIdx] &^= bitMask
		}
	}
}

// FilterCandidates returns docIDs that contain all consecutive bigrams in query.
// If query has < 2 characters, returns all candidate docIDs up to numDocs.
func (idx *InvertedIndex) FilterCandidates(query string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	clean := strings.ToLower(query)
	if len(clean) < 2 {
		all := make([]int, idx.numDocs)
		for i := 0; i < idx.numDocs; i++ {
			all[i] = i
		}
		return all
	}

	// Gather bigram keys from query
	var keys []uint16
	for i := 0; i < len(clean)-1; i++ {
		keys = append(keys, BigramKey(clean[i], clean[i+1]))
	}

	wordsCount := (idx.numDocs + 63) / 64
	if wordsCount == 0 {
		return nil
	}

	// Initialize intersection bitset with all 1s
	intersect := make([]uint64, wordsCount)
	for i := range intersect {
		intersect[i] = ^uint64(0)
	}

	for _, k := range keys {
		col, ok := idx.columns[k]
		if !ok {
			return nil // A required bigram does not exist in any file
		}
		for i := 0; i < wordsCount; i++ {
			if i < len(col) {
				intersect[i] &= col[i]
			} else {
				intersect[i] = 0
			}
		}
	}

	// Extract matching docIDs
	var results []int
	for wordIdx, mask := range intersect {
		if mask == 0 {
			continue
		}
		for bit := 0; bit < 64; bit++ {
			if (mask & (uint64(1) << bit)) != 0 {
				docID := wordIdx*64 + bit
				if docID < idx.numDocs {
					results = append(results, docID)
				}
			}
		}
	}

	return results
}

// Reset clears the index.
func (idx *InvertedIndex) Reset() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.columns = make(map[uint16][]uint64)
	idx.numDocs = 0
}
