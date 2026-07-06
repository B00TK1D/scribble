package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
)

// FontResult holds the output of font randomization.
type FontResult struct {
	FontBytes []byte
	CharMap   *CharMap
}

// RandomizeFont shuffles the cmap to map each glyph to a random PUA
// codepoint and returns the modified font as TTF.
func RandomizeFont(baseFont []byte, seed int64, usedChars []rune) (*FontResult, error) {
	if len(baseFont) < 12 {
		return nil, fmt.Errorf("font too small (%d bytes)", len(baseFont))
	}

	numTables := int(binary.BigEndian.Uint16(baseFont[4:6]))

	// Parse table directory
	var cmapOffset, cmapLength uint32
	var maxpOffset uint32
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(baseFont[base : base+4])
		off := binary.BigEndian.Uint32(baseFont[base+8 : base+12])
		ln := binary.BigEndian.Uint32(baseFont[base+12 : base+16])
		switch tag {
		case 0x636D6170: // cmap
			cmapOffset = off
			cmapLength = ln
		case 0x6D617870: // maxp
			maxpOffset = off
		}
	}
	if cmapOffset == 0 {
		return nil, fmt.Errorf("font has no cmap table")
	}
	if maxpOffset == 0 {
		return nil, fmt.Errorf("font has no maxp table")
	}

	numGlyphs := int(binary.BigEndian.Uint16(baseFont[maxpOffset+4 : maxpOffset+6]))

	origMap := parseCmap(baseFont[cmapOffset : cmapOffset+cmapLength])
	if len(origMap) == 0 {
		return nil, fmt.Errorf("cmap contains no glyph mappings")
	}

	// Build CharMap for characters present in the font
	supportedChars := make([]rune, 0, len(usedChars))
	for _, r := range usedChars {
		if _, ok := origMap[r]; ok {
			supportedChars = append(supportedChars, r)
		}
	}
	charMap := NewCharMap(supportedChars, seed)

	// Build new cmap: merge original entries + PUA entries
	mergedCmap := make(map[rune]uint16, len(origMap)+len(charMap.Forward))
	for k, v := range origMap {
		mergedCmap[k] = v
	}
	for orig, pua := range charMap.Forward {
		mergedCmap[pua] = origMap[orig]
	}
	newCmap := buildCmap4(mergedCmap)

	// Build new post table with randomized glyph names
	newPost := buildRandomPostTable(numGlyphs, seed+1)

	// Build the new font: append cmap + post, update directory entries
	result := make([]byte, len(baseFont)+len(newCmap)+len(newPost))
	copy(result, baseFont)
	copy(result[len(baseFont):], newCmap)
	copy(result[len(baseFont)+len(newCmap):], newPost)

	newCmapOffset := uint32(len(baseFont))
	newPostOffset := uint32(len(baseFont) + len(newCmap))

	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(result[base : base+4])
		if tag == 0x636D6170 { // cmap
			binary.BigEndian.PutUint32(result[base+8:base+12], newCmapOffset)
			binary.BigEndian.PutUint32(result[base+12:base+16], uint32(len(newCmap)))
		}
		if tag == 0x706F7374 { // post
			binary.BigEndian.PutUint32(result[base+8:base+12], newPostOffset)
			binary.BigEndian.PutUint32(result[base+12:base+16], uint32(len(newPost)))
		}
	}

	zeroHeadChecksumAdjustment(result, numTables)

	return &FontResult{
		FontBytes: result,
		CharMap:   charMap,
	}, nil
}

func zeroHeadChecksumAdjustment(font []byte, numTables int) {
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(font[base : base+4])
		if tag == 0x68656164 {
			offset := binary.BigEndian.Uint32(font[base+8 : base+12])
			if offset+12 <= uint32(len(font)) {
				binary.BigEndian.PutUint32(font[offset+8:offset+12], 0)
			}
			return
		}
	}
}

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
		if platform == 3 && encoding == 1 && format == 4 {
			for k, v := range parseCmapFormat4(data[subOff:]) {
				result[k] = v
			}
		}
		if platform == 3 && encoding == 10 && format == 12 {
			for k, v := range parseCmapFormat12(data[subOff:]) {
				result[k] = v
			}
		}
		offset += 8
	}
	return result
}

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
	base += segCount*2 + 2
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
	for i := 0; i < segCount-1; i++ {
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
	segs = append(segs, segment{startCode: 0xFFFF, endCode: 0xFFFF, glyphID: 0})
	segCount := len(segs)
	f4len := 14 + segCount*8 + 2
	f4 := make([]byte, f4len)
	binary.BigEndian.PutUint16(f4[0:], 4)
	binary.BigEndian.PutUint16(f4[2:], uint16(f4len))
	binary.BigEndian.PutUint16(f4[4:], 0)
	binary.BigEndian.PutUint16(f4[6:], uint16(segCount*2))
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
	off += 2
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
		binary.BigEndian.PutUint16(f4[off:], 0)
		off += 2
	}
	hdrLen := 12
	buf := make([]byte, hdrLen+len(f4))
	binary.BigEndian.PutUint16(buf[0:], 0)
	binary.BigEndian.PutUint16(buf[2:], 1)
	binary.BigEndian.PutUint16(buf[4:], 3)
	binary.BigEndian.PutUint16(buf[6:], 1)
	binary.BigEndian.PutUint32(buf[8:], uint32(hdrLen))
	copy(buf[hdrLen:], f4)
	return buf
}

func buildEmptyCmap() []byte {
	f4len := 14 + 8 + 2
	f4 := make([]byte, f4len)
	binary.BigEndian.PutUint16(f4[0:], 4)
	binary.BigEndian.PutUint16(f4[2:], uint16(f4len))
	binary.BigEndian.PutUint16(f4[6:], 2)
	binary.BigEndian.PutUint16(f4[8:], 2)
	binary.BigEndian.PutUint16(f4[14:], 0xFFFF)
	binary.BigEndian.PutUint16(f4[18:], 0xFFFF)
	binary.BigEndian.PutUint16(f4[20:], 1)
	hdrLen := 12
	buf := make([]byte, hdrLen+f4len)
	binary.BigEndian.PutUint16(buf[2:], 1)
	binary.BigEndian.PutUint16(buf[4:], 3)
	binary.BigEndian.PutUint16(buf[6:], 1)
	binary.BigEndian.PutUint32(buf[8:], uint32(hdrLen))
	copy(buf[hdrLen:], f4)
	return buf
}

func GenerateFontKey() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 16)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// buildRandomPostTable creates a post table (format 2.0) where every glyph
// has a random single-byte name. This prevents reverse-engineering the
// character mapping from glyph names.
//
// post table format 2.0 layout:
//   - version (4 bytes): 0x00020000
//   - italicAngle (4 bytes): fixed-point
//   - underlinePosition (2 bytes)
//   - underlineThickness (2 bytes)
//   - isFixedPitch (4 bytes)
//   - minMemType42 (4 bytes)
//   - maxMemType42 (4 bytes)
//   - minMemType1 (4 bytes)
//   - maxMemType1 (4 bytes)
//   - numGlyphs (2 bytes)
//   - glyphNameIndex[numGlyphs] (2 bytes each): indices into name table
//   - names[]: Pascal strings (length byte + data)
//
// Standard Mac name indices are 0-257. Custom names use indices 258+.
func buildRandomPostTable(numGlyphs int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))

	// Generate a pool of random single-byte characters (0x01-0xFF range,
	// avoiding only 0x00 which is the null terminator)
	namePool := make([]byte, 255)
	for i := range namePool {
		namePool[i] = byte(i + 1)
	}
	// Shuffle the pool
	rng.Shuffle(len(namePool), func(i, j int) {
		namePool[i], namePool[j] = namePool[j], namePool[i]
	})

	// Build name indices: each glyph gets index 258+i, pointing to a
	// custom name that is a single random byte
	nameIndices := make([]uint16, numGlyphs)
	for i := 0; i < numGlyphs; i++ {
		nameIndices[i] = uint16(258 + i)
	}

	// Build custom name strings (Pascal strings: 1 byte length + data)
	// Each name is a single random byte from the pool
	nameStrings := make([]byte, numGlyphs*2) // 2 bytes per name (len + char)
	for i := 0; i < numGlyphs; i++ {
		nameStrings[i*2] = 1            // length = 1
		nameStrings[i*2+1] = namePool[i%len(namePool)]
	}

	// Calculate total size
	headerSize := 32 // fixed header (version through maxMemType1)
	indicesSize := numGlyphs * 2
	totalSize := headerSize + 2 + indicesSize + len(nameStrings) // +2 for numGlyphs

	buf := make([]byte, totalSize)

	// Header
	binary.BigEndian.PutUint32(buf[0:], 0x00020000)  // version 2.0
	binary.BigEndian.PutUint32(buf[4:], 0)            // italicAngle
	binary.BigEndian.PutUint16(buf[8:], 0xFFFF)       // underlinePosition (-1 as int16)
	binary.BigEndian.PutUint16(buf[10:], 1)           // underlineThickness
	binary.BigEndian.PutUint32(buf[12:], 0)           // isFixedPitch
	binary.BigEndian.PutUint32(buf[16:], 0)           // minMemType42
	binary.BigEndian.PutUint32(buf[20:], 0)           // maxMemType42
	binary.BigEndian.PutUint32(buf[24:], 0)           // minMemType1
	binary.BigEndian.PutUint32(buf[28:], 0)           // maxMemType1

	// numGlyphs
	binary.BigEndian.PutUint16(buf[32:], uint16(numGlyphs))

	// glyphNameIndex array
	off := 34
	for _, idx := range nameIndices {
		binary.BigEndian.PutUint16(buf[off:], idx)
		off += 2
	}

	// Custom name strings
	copy(buf[off:], nameStrings)

	return buf
}
