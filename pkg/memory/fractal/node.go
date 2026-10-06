package fractal

import (
	"fmt"
	"math/bits"

	"github.com/pkg/errors"
)

// DefaultNodeTargetBytes is the target upper bound in UTF-8 bytes for a single node summary.
const DefaultNodeTargetBytes = 512

// Node represents a binary summary tree node covering a range of messages.
type Node struct {
	Level int    `json:"level"`
	Index int    `json:"index"`
	Text  string `json:"text"`
	Size  int    `json:"size"` // UTF-8 byte length of Text
}

// Range returns the start (inclusive) and end (exclusive) event IDs covered by this node.
func (n Node) Range() (start int, end int) {
	count := 1 << n.Level
	start = n.Index * count
	end = start + count
	return start, end
}

// Address returns the OptChat id+n representation (e.g. 2184+8).
func (n Node) Address() string {
	start, _ := n.Range()
	return fmt.Sprintf("%d+%d", start, 1<<n.Level)
}

// NodeKey maps a (level, index) coordinate.
type NodeKey struct {
	Level int
	Index int
}

// Key returns the NodeKey for the node.
func (n Node) Key() NodeKey {
	return NodeKey{Level: n.Level, Index: n.Index}
}

// ParseAddress parses an id+n address (e.g., 2184, 8) into level and index.
// n must be a power of 2, and id must be a multiple of n.
func ParseAddress(id, n int) (level int, index int, err error) {
	if n <= 0 || (n&(n-1)) != 0 {
		return 0, 0, errors.Errorf("n must be a positive power of 2, got %d", n)
	}
	if id < 0 || id%n != 0 {
		return 0, 0, errors.Errorf("id %d must be a non-negative multiple of n %d", id, n)
	}
	level = bits.TrailingZeros(uint(n))
	index = id / n
	return level, index, nil
}
