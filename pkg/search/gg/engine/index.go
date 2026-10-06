package engine

import (
	"strings"
	"sync"
)

// BigramKey encodes two lowercase ASCII bytes into a uint16.
func BigramKey(a, b byte) uint16 {
	return (uint16(a) << 8) | uint16(b)
}

// InvertedIndex stores bitsets of candidate file indices containing each bigram.
// Uses flattened []uint64 bitsets for maximum CPU cache efficiency and minimal GC overhead.
type InvertedIndex struct {
	mu         sync.RWMutex
	columns    map[uint16][]uint64
	activeDocs []uint64
	numDocs    int
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

	// Mark doc as active
	if wordIdx >= len(idx.activeDocs) {
		newActive := make([]uint64, wordIdx+1)
		copy(newActive, idx.activeDocs)
		idx.activeDocs = newActive
	}
	idx.activeDocs[wordIdx] |= bitMask

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

// RemoveDocument removes bigrams for docID corresponding to path and marks it inactive.
func (idx *InvertedIndex) RemoveDocument(docID int, path string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	wordIdx := docID / 64
	bitMask := uint64(1) << (docID % 64)

	// Mark doc as inactive
	if wordIdx < len(idx.activeDocs) {
		idx.activeDocs[wordIdx] &^= bitMask
	}

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

// FilterWords returns active docIDs that contain all bigrams across all input words.
func (idx *InvertedIndex) FilterWords(words []string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	wordsCount := len(idx.activeDocs)
	if wordsCount == 0 {
		return nil
	}

	// Initialize intersection bitset with active documents
	intersect := make([]uint64, wordsCount)
	copy(intersect, idx.activeDocs)

	hasBigramFilter := false

	for _, w := range words {
		clean := strings.ToLower(w)
		if len(clean) < 2 {
			continue
		}

		hasBigramFilter = true
		for i := 0; i < len(clean)-1; i++ {
			k := BigramKey(clean[i], clean[i+1])
			col, ok := idx.columns[k]
			if !ok {
				return nil // A required bigram does not exist in any file
			}
			for j := 0; j < wordsCount; j++ {
				if j < len(col) {
					intersect[j] &= col[j]
				} else {
					intersect[j] = 0
				}
			}
		}
	}

	_ = hasBigramFilter

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

// FilterCandidates returns active docIDs matching all tokens in query without treating spaces as bigrams.
func (idx *InvertedIndex) FilterCandidates(query string) []int {
	words := strings.Fields(query)
	return idx.FilterWords(words)
}

// Reset clears the index.
func (idx *InvertedIndex) Reset() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.columns = make(map[uint16][]uint64)
	idx.activeDocs = nil
	idx.numDocs = 0
}
