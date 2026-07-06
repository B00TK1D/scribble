package main

import (
	"math/rand"
	"sort"
)

const (
	puaStart rune = 0xE000
	puaEnd   rune = 0xF8FF
)

// CharMap holds the bidirectional mapping between original characters
// and their randomized PUA (Private Use Area) codepoints.
type CharMap struct {
	Forward map[rune]rune // original -> PUA
	Reverse map[rune]rune // PUA -> original
}

// NewCharMap creates a randomized mapping from the given set of original
// codepoints to unique PUA codepoints. Each call with the same input
// produces a different mapping (due to shuffling).
func NewCharMap(originals []rune, seed int64) *CharMap {
	// Assign each original character a unique PUA codepoint
	available := make([]rune, 0, int(puaEnd-puaStart+1))
	for c := puaStart; c <= puaEnd; c++ {
		available = append(available, c)
	}

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(available), func(i, j int) {
		available[i], available[j] = available[j], available[i]
	})

	// If we have more originals than PUA slots, truncate
	n := len(originals)
	if n > len(available) {
		n = len(available)
	}

	// Sort originals for deterministic assignment (shuffling is in PUA order)
	sorted := make([]rune, len(originals))
	copy(sorted, originals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	fwd := make(map[rune]rune, n)
	rev := make(map[rune]rune, n)
	for i := 0; i < n; i++ {
		fwd[sorted[i]] = available[i]
		rev[available[i]] = sorted[i]
	}

	return &CharMap{Forward: fwd, Reverse: rev}
}
