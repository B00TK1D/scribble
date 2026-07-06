package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
)

// FontResult holds the output of font randomization: the modified font
// bytes and the character map used for HTML text replacement.
type FontResult struct {
	FontBytes []byte
	CharMap   *CharMap
}

// RandomizeFont takes the raw bytes of a TrueType/OpenType font, shuffles
// the cmap table so each glyph maps to a random PUA codepoint, and returns
// the modified font bytes plus the character mapping for HTML replacement.
//
// It works by direct binary manipulation: appending the new cmap at the end
// of the font and updating the table directory entry. This avoids bugs in
// font library re-serialization (especially for variable fonts).
func RandomizeFont(baseFont []byte, seed int64) (*FontResult, error) {
	if len(baseFont) < 12 {
		return nil, fmt.Errorf("font too small (%d bytes)", len(baseFont))
	}

	// Parse the offset table
	sfVersion := binary.BigEndian.Uint32(baseFont[0:4])
	numTables := int(binary.BigEndian.Uint16(baseFont[4:6]))

	// Verify this is a valid SFNT font
	if sfVersion != 0x00010000 && sfVersion != 0x4F54544F { // TrueType or CFF
		return nil, fmt.Errorf("unsupported font format: 0x%08X", sfVersion)
	}

	dirEnd := 12 + numTables*16
	if len(baseFont) < dirEnd {
		return nil, fmt.Errorf("font too small for %d table entries", numTables)
	}

	// Scan table directory to find cmap
	type tableEntry struct {
		tag    uint32
		offset uint32
		length uint32
	}

	var cmapOffset, cmapLength uint32
	cmapFound := false

	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(baseFont[base : base+4])
		// checksum at base+4..base+8
		offset := binary.BigEndian.Uint32(baseFont[base+8 : base+12])
		length := binary.BigEndian.Uint32(baseFont[base+12 : base+16])

		if tag == 0x636D6170 { // "cmap"
			cmapOffset = offset
			cmapLength = length
			cmapFound = true
		}
	}

	if !cmapFound {
		return nil, fmt.Errorf("font has no cmap table")
	}

	if cmapOffset+cmapLength > uint32(len(baseFont)) {
		return nil, fmt.Errorf("cmap table extends beyond font data")
	}

	// Parse the original cmap
	origCmapBytes := baseFont[cmapOffset : cmapOffset+cmapLength]
	origMap := parseCmap(origCmapBytes)
	if len(origMap) == 0 {
		return nil, fmt.Errorf("cmap contains no glyph mappings")
	}

	// Collect all original codepoints
	originals := make([]rune, 0, len(origMap))
	for codepoint := range origMap {
		originals = append(originals, codepoint)
	}

	// Create the randomized character map
	charMap := NewCharMap(originals, seed)

	// Build new cmap: PUA codepoint -> same glyphID as original
	puaToGlyph := make(map[rune]uint16, len(charMap.Forward))
	for orig, pua := range charMap.Forward {
		glyphID := origMap[orig]
		puaToGlyph[pua] = glyphID
	}

	// Build new cmap binary
	newCmap := buildCmap4(puaToGlyph)

	// Build the modified font by appending the new cmap and updating the
	// directory entry. This preserves all other table bytes exactly.
	result := make([]byte, len(baseFont)+len(newCmap))
	copy(result, baseFont)
	copy(result[len(baseFont):], newCmap)

	newCmapOffset := uint32(len(baseFont))

	// Update the cmap directory entry: offset and length
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(result[base : base+4])
		if tag == 0x636D6170 { // "cmap"
			binary.BigEndian.PutUint32(result[base+8:base+12], newCmapOffset)
			binary.BigEndian.PutUint32(result[base+12:base+16], uint32(len(newCmap)))
			break
		}
	}

	// Zero out the head table checksum adjustment so OTS doesn't reject
	// the font for a checksum mismatch (we changed file contents).
	zeroHeadChecksumAdjustment(result, numTables)

	return &FontResult{
		FontBytes: result,
		CharMap:   charMap,
	}, nil
}

// zeroHeadChecksumAdjustment sets the checksumAdjustment field in the
// head table to 0. This field is at offset 8 in the head table.
func zeroHeadChecksumAdjustment(font []byte, numTables int) {
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(font[base : base+4])
		if tag == 0x68656164 { // "head"
			offset := binary.BigEndian.Uint32(font[base+8 : base+12])
			if offset+12 <= uint32(len(font)) {
				binary.BigEndian.PutUint32(font[offset+8:offset+12], 0)
			}
			return
		}
	}
}

// parseCmap reads a cmap table and returns codepoint -> glyphID mappings.
// Supports format 4 (BMP) and format 12 (full Unicode).
func parseCmap(data []byte) map[rune]uint16 {
	result := make(map[rune]uint16)
	if len(data) < 4 {
		return result
	}

	numTables := int(uint16(data[2])<<8 | uint16(data[3]))

	offset := 4
	for i := 0; i < numTables && offset+8 <= len(data); i++ {
		platform := uint16(data[offset])<<8 | uint16(data[offset+1])
		encoding := uint16(data[offset+2])<<8 | uint16(data[offset+3])
		subOff := uint32(data[offset+4])<<24 | uint32(data[offset+5])<<16 |
			uint32(data[offset+6])<<8 | uint32(data[offset+7])

		if subOff+2 > uint32(len(data)) {
			offset += 8
			continue
		}

		format := uint16(data[subOff])<<8 | uint16(data[subOff+1])

		// platform 3 encoding 1 = Windows Unicode BMP (format 4)
		if platform == 3 && encoding == 1 && format == 4 {
			m := parseCmapFormat4(data[subOff:])
			for k, v := range m {
				result[k] = v
			}
		}

		// platform 3 encoding 10 = Windows Unicode Full (format 12)
		if platform == 3 && encoding == 10 && format == 12 {
			m := parseCmapFormat12(data[subOff:])
			for k, v := range m {
				result[k] = v
			}
		}

		offset += 8
	}

	return result
}

// parseCmapFormat4 parses a format 4 cmap subtable.
func parseCmapFormat4(data []byte) map[rune]uint16 {
	result := make(map[rune]uint16)
	if len(data) < 14 {
		return result
	}

	segCount := int(uint16(data[6])<<8|uint16(data[7])) / 2
	if len(data) < 16+segCount*8 {
		return result
	}

	endCodes := make([]uint16, segCount)
	startCodes := make([]uint16, segCount)
	idDeltas := make([]int16, segCount)
	idRangeOffsets := make([]uint16, segCount)

	base := 14
	for i := 0; i < segCount; i++ {
		endCodes[i] = uint16(data[base+i*2])<<8 | uint16(data[base+i*2+1])
	}
	base += segCount*2 + 2 // +2 for reserved padding
	for i := 0; i < segCount; i++ {
		startCodes[i] = uint16(data[base+i*2])<<8 | uint16(data[base+i*2+1])
	}
	base += segCount * 2
	for i := 0; i < segCount; i++ {
		idDeltas[i] = int16(uint16(data[base+i*2])<<8 | uint16(data[base+i*2+1]))
	}
	base += segCount * 2
	for i := 0; i < segCount; i++ {
		idRangeOffsets[i] = uint16(data[base+i*2])<<8 | uint16(data[base+i*2+1])
	}
	glyphIndexStart := base + segCount*2

	for i := 0; i < segCount-1; i++ { // skip last segment (sentinel)
		for c := int(startCodes[i]); c <= int(endCodes[i]); c++ {
			var glyphID uint16
			if idRangeOffsets[i] == 0 {
				glyphID = uint16((int(c) + int(idDeltas[i])) & 0xFFFF)
			} else {
				idx := glyphIndexStart + i*2 + int(idRangeOffsets[i]) + (c-int(startCodes[i]))*2
				if idx+2 <= len(data) {
					glyphID = uint16(data[idx])<<8 | uint16(data[idx+1])
					if glyphID != 0 {
						glyphID = uint16((int(glyphID) + int(idDeltas[i])) & 0xFFFF)
					}
				}
			}
			if glyphID != 0 {
				result[rune(c)] = glyphID
			}
		}
	}

	return result
}

// parseCmapFormat12 parses a format 12 cmap subtable.
func parseCmapFormat12(data []byte) map[rune]uint16 {
	result := make(map[rune]uint16)
	if len(data) < 16 {
		return result
	}
	nGroups := uint32(data[12])<<24 | uint32(data[13])<<16 | uint32(data[14])<<8 | uint32(data[15])
	if len(data) < 16+int(nGroups)*12 {
		return result
	}
	for i := uint32(0); i < nGroups; i++ {
		base := 16 + i*12
		startCharCode := rune(uint32(data[base])<<24 | uint32(data[base+1])<<16 | uint32(data[base+2])<<8 | uint32(data[base+3]))
		endCharCode := rune(uint32(data[base+4])<<24 | uint32(data[base+5])<<16 | uint32(data[base+6])<<8 | uint32(data[base+7]))
		startGlyphID := uint16(uint32(data[base+8])<<24 | uint32(data[base+9])<<16 | uint32(data[base+10])<<8 | uint32(data[base+11]))
		for c := startCharCode; c <= endCharCode; c++ {
			result[c] = startGlyphID + uint16(c-startCharCode)
		}
	}
	return result
}

// buildCmap4 constructs a full cmap table (with outer header) containing
// a single format 4 subtable from a PUA->glyphID map.
func buildCmap4(mapping map[rune]uint16) []byte {
	if len(mapping) == 0 {
		return buildEmptyCmap()
	}

	type entry struct {
		codepoint rune
		glyphID   uint16
	}
	entries := make([]entry, 0, len(mapping))
	for c, g := range mapping {
		entries = append(entries, entry{c, g})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].codepoint < entries[j].codepoint })

	// Group consecutive codepoints into segments where
	// glyphID = codepoint + delta (constant delta per segment).
	type segment struct {
		startCode uint16
		endCode   uint16
		glyphID   uint16
	}
	var segs []segment
	for _, e := range entries {
		cp := uint16(e.codepoint)
		if len(segs) > 0 {
			last := &segs[len(segs)-1]
			if cp == last.endCode+1 && e.glyphID == last.glyphID+(cp-last.startCode) {
				last.endCode = cp
				continue
			}
		}
		segs = append(segs, segment{startCode: cp, endCode: cp, glyphID: e.glyphID})
	}

	// Add sentinel segment (required by spec)
	segs = append(segs, segment{startCode: 0xFFFF, endCode: 0xFFFF, glyphID: 0})

	segCount := len(segs)
	f4len := 14 + segCount*8 + 2 // header + 4 arrays + padding

	// Build format 4 subtable
	f4 := make([]byte, f4len)
	binary.BigEndian.PutUint16(f4[0:], 4)
	binary.BigEndian.PutUint16(f4[2:], uint16(f4len))
	binary.BigEndian.PutUint16(f4[4:], 0) // language
	binary.BigEndian.PutUint16(f4[6:], uint16(segCount*2))

	// searchRange, entrySelector, rangeShift
	sr := uint16(1)
	es := uint16(0)
	for sr*2 <= uint16(segCount) {
		sr *= 2
		es++
	}
	sr *= 2
	binary.BigEndian.PutUint16(f4[8:], sr)
	binary.BigEndian.PutUint16(f4[10:], es)
	binary.BigEndian.PutUint16(f4[12:], uint16(segCount)*2-sr)

	off := 14
	for _, seg := range segs {
		binary.BigEndian.PutUint16(f4[off:], seg.endCode)
		off += 2
	}
	off += 2 // reserved padding
	for _, seg := range segs {
		binary.BigEndian.PutUint16(f4[off:], seg.startCode)
		off += 2
	}
	for _, seg := range segs {
		delta := int16(int(seg.glyphID) - int(seg.startCode))
		binary.BigEndian.PutUint16(f4[off:], uint16(delta))
		off += 2
	}
	for range segs {
		binary.BigEndian.PutUint16(f4[off:], 0) // idRangeOffset = 0
		off += 2
	}

	// Wrap in outer cmap table: version(2) + numTables(2) + 1 encoding record(8)
	hdrLen := 12
	total := hdrLen + len(f4)
	buf := make([]byte, total)
	binary.BigEndian.PutUint16(buf[0:], 0) // version
	binary.BigEndian.PutUint16(buf[2:], 1) // numTables
	binary.BigEndian.PutUint16(buf[4:], 3) // platformID (Windows)
	binary.BigEndian.PutUint16(buf[6:], 1) // encodingID (Unicode BMP)
	binary.BigEndian.PutUint32(buf[8:], uint32(hdrLen))
	copy(buf[hdrLen:], f4)
	return buf
}

// buildEmptyCmap returns a minimal valid cmap table with no mappings.
func buildEmptyCmap() []byte {
	f4len := 14 + 8 + 2 // header + 1 segment + padding
	f4 := make([]byte, f4len)
	binary.BigEndian.PutUint16(f4[0:], 4)
	binary.BigEndian.PutUint16(f4[2:], uint16(f4len))
	binary.BigEndian.PutUint16(f4[6:], 2) // segCountX2 = 2 (1 segment)
	binary.BigEndian.PutUint16(f4[8:], 2)
	binary.BigEndian.PutUint16(f4[14:], 0xFFFF) // endCode
	binary.BigEndian.PutUint16(f4[18:], 0xFFFF) // startCode (after padding)
	binary.BigEndian.PutUint16(f4[20:], 1)      // idDelta
	// idRangeOffset already 0

	hdrLen := 12
	buf := make([]byte, hdrLen+f4len)
	binary.BigEndian.PutUint16(buf[2:], 1)
	binary.BigEndian.PutUint16(buf[4:], 3)
	binary.BigEndian.PutUint16(buf[6:], 1)
	binary.BigEndian.PutUint32(buf[8:], uint32(hdrLen))
	copy(buf[hdrLen:], f4)
	return buf
}

// GenerateFontKey returns a random hex string for use as a font URL key.
func GenerateFontKey() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 16)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
