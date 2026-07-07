package main

import (
	"math/rand"
	"sort"
)

const (
	puaStart rune = 0xE000
	puaEnd   rune = 0xF8FF
)

// CharMap holds the mapping between original characters and their randomized
// PUA codepoints. Supports one-to-many: each original character maps to
// multiple PUA variants (different glyph shapes), and each occurrence in
// HTML randomly picks one variant.
type CharMap struct {
	Forward map[rune][]rune // original -> [PUA variant 0, PUA variant 1, ...]
	Reverse map[rune]rune   // PUA -> original
	Rng     *rand.Rand      // for picking random variants
}

// NewCharMap creates a randomized mapping with `variants` PUA codepoints
// per original character. Each character gets `variants` different PUA
// codepoints, each mapped to a different glyph in the font.
func NewCharMap(originals []rune, seed int64, variants int) *CharMap {
	available := make([]rune, 0, int(puaEnd-puaStart+1))
	for c := puaStart; c <= puaEnd; c++ {
		available = append(available, c)
	}

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(available), func(i, j int) {
		available[i], available[j] = available[j], available[i]
	})

	sorted := make([]rune, len(originals))
	copy(sorted, originals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	fwd := make(map[rune][]rune, len(sorted))
	rev := make(map[rune]rune, len(sorted)*variants)

	slot := 0
	for _, r := range sorted {
		var puas []rune
		for v := 0; v < variants && slot < len(available); v++ {
			pua := available[slot]
			puas = append(puas, pua)
			rev[pua] = r
			slot++
		}
		fwd[r] = puas
	}

	return &CharMap{Forward: fwd, Reverse: rev, Rng: rand.New(rand.NewSource(seed + 1))}
}
