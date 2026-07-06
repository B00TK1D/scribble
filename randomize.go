package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
)

// TrueType glyph flag constants
const (
	flagOnCurve       byte = 0x01
	flagXShort        byte = 0x02
	flagYShort        byte = 0x04
	flagRepeat        byte = 0x08
	flagXsame         byte = 0x10
	flagYsame         byte = 0x20
	flagOverlapSimple byte = 0x40
)

// RandomizeFont shuffles the cmap, randomizes glyph outlines and metrics,
// and returns the modified font as TTF.
func RandomizeFont(baseFont []byte, seed int64, usedChars []rune) (*FontResult, error) {
	if len(baseFont) < 12 {
		return nil, fmt.Errorf("font too small (%d bytes)", len(baseFont))
	}

	numTables := int(binary.BigEndian.Uint16(baseFont[4:6]))
	tables := make(map[uint32]tblInfo, numTables)
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(baseFont[base : base+4])
		off := binary.BigEndian.Uint32(baseFont[base+8 : base+12])
		ln := binary.BigEndian.Uint32(baseFont[base+12 : base+16])
		tables[tag] = tblInfo{off, ln}
	}

	required := []uint32{0x636D6170, 0x6D617870, 0x686D7478, 0x6C6F6361, 0x676C7966, 0x68656164, 0x68686561}
	for _, tag := range required {
		if _, ok := tables[tag]; !ok {
			return nil, fmt.Errorf("missing required table: 0x%08X", tag)
		}
	}

	numGlyphs := int(binary.BigEndian.Uint16(baseFont[tables[0x6D617870].offset+4 : tables[0x6D617870].offset+6]))

	origMap := parseCmap(baseFont[tables[0x636D6170].offset : tables[0x636D6170].offset+tables[0x636D6170].length])
	if len(origMap) == 0 {
		return nil, fmt.Errorf("cmap contains no glyph mappings")
	}

	supportedChars := make([]rune, 0, len(usedChars))
	for _, r := range usedChars {
		if _, ok := origMap[r]; ok {
			supportedChars = append(supportedChars, r)
		}
	}
	charMap := NewCharMap(supportedChars, seed)

	puaOnlyCmap := make(map[rune]uint16, len(charMap.Forward))
	for orig, pua := range charMap.Forward {
		puaOnlyCmap[pua] = origMap[orig]
	}
	newCmap := buildCmap4(puaOnlyCmap)
	newPost := buildRandomPostTable(numGlyphs, seed+1)

	// Randomize hmtx
	newHmtx := randomizeHmtx(baseFont, tables, numGlyphs, seed+2)

	// Randomize glyf — modify last Y delta of each simple glyph in-place
	newGlyf, newLoca := randomizeGlyfInPlace(baseFont, tables, numGlyphs, seed+2)

	result, err := buildFont(map[uint32][]byte{
		0x68656164: baseFont[tables[0x68656164].offset : tables[0x68656164].offset+tables[0x68656164].length],
		0x68686561: baseFont[tables[0x68686561].offset : tables[0x68686561].offset+tables[0x68686561].length],
		0x686D7478: newHmtx,
		0x6D617870: baseFont[tables[0x6D617870].offset : tables[0x6D617870].offset+tables[0x6D617870].length],
		0x6C6F6361: newLoca,
		0x676C7966: newGlyf,
		0x636D6170: newCmap,
		0x6E616D65: baseFont[tables[0x6E616D65].offset : tables[0x6E616D65].offset+tables[0x6E616D65].length],
		0x706F7374: newPost,
		0x4F532F32: baseFont[tables[0x4F532F32].offset : tables[0x4F532F32].offset+tables[0x4F532F32].length],
	})
	if err != nil {
		return nil, fmt.Errorf("build font: %w", err)
	}

	return &FontResult{FontBytes: result, CharMap: charMap}, nil
}

// randomizeHmtx adds ±1 random variation to each glyph's advance width.
func randomizeHmtx(baseFont []byte, tables map[uint32]tblInfo, numGlyphs int, seed int64) []byte {
	hmtxInfo := tables[0x686D7478]
	newHmtx := make([]byte, hmtxInfo.length)
	copy(newHmtx, baseFont[hmtxInfo.offset:hmtxInfo.offset+hmtxInfo.length])

	rng := rand.New(rand.NewSource(seed))
	for gid := 0; gid < numGlyphs; gid++ {
		delta := int16(rng.Intn(3)) - 1
		off := gid * 4
		adv := int16(binary.BigEndian.Uint16(newHmtx[off:])) + delta
		if adv < 0 {
			adv = 0
		}
		binary.BigEndian.PutUint16(newHmtx[off:], uint16(adv))
	}
	return newHmtx
}

// randomizeGlyfInPlace modifies the last Y delta of each simple glyph
// directly in the raw glyf binary. This avoids re-encoding the glyph —
// we find the last Y delta's byte offset and modify it by ±1.
func randomizeGlyfInPlace(baseFont []byte, tables map[uint32]tblInfo, numGlyphs int, seed int64) (newGlyf, newLoca []byte) {
	glyfInfo := tables[0x676C7966]
	locaInfo := tables[0x6C6F6361]
	headInfo := tables[0x68656164]

	locaFormat := int(binary.BigEndian.Uint16(baseFont[headInfo.offset+50 : headInfo.offset+52]))

	// Parse loca
	locaData := baseFont[locaInfo.offset : locaInfo.offset+locaInfo.length]
	glyphStarts := make([]uint32, numGlyphs+1)
	if locaFormat == 1 {
		for i := 0; i <= numGlyphs && i*4+4 <= len(locaData); i++ {
			glyphStarts[i] = binary.BigEndian.Uint32(locaData[i*4:])
		}
	} else {
		for i := 0; i <= numGlyphs && i*2+2 <= len(locaData); i++ {
			glyphStarts[i] = uint32(binary.BigEndian.Uint16(locaData[i*2:])) * 2
		}
	}

	// Copy glyf table — we modify in-place
	newGlyf = make([]byte, glyfInfo.length)
	copy(newGlyf, baseFont[glyfInfo.offset:glyfInfo.offset+glyfInfo.length])

	rng := rand.New(rand.NewSource(seed))

	for gid := 0; gid < numGlyphs; gid++ {
		start := glyphStarts[gid]
		end := glyphStarts[gid+1]
		size := end - start
		if size < 10 {
			continue
		}

		nContour := int16(binary.BigEndian.Uint16(newGlyf[start:]))
		if nContour <= 0 || nContour > 50 {
			continue
		}

		// Find the byte offset of the last Y delta in this glyph
		lastYDeltaOff, lastYDeltaSize := findLastYDeltaOffset(newGlyf, start, end, int(nContour))
		if lastYDeltaOff == 0 || lastYDeltaSize == 0 {
			continue
		}

		// Modify the last Y delta by ±1
		delta := int(rng.Intn(2))*2 - 1 // -1 or +1

		if lastYDeltaSize == 2 {
			// 2-byte signed delta
			val := int16(binary.BigEndian.Uint16(newGlyf[lastYDeltaOff:]))
			binary.BigEndian.PutUint16(newGlyf[lastYDeltaOff:], uint16(val+int16(delta)))
		} else {
			// 1-byte unsigned delta — clamp to prevent wrapping
			cur := int(newGlyf[lastYDeltaOff])
			if cur > 0 && cur < 255 {
				newGlyf[lastYDeltaOff] = byte(cur + delta)
			}
		}
	}

	// Copy loca unchanged (glyph byte sizes don't change)
	newLoca = make([]byte, locaInfo.length)
	copy(newLoca, baseFont[locaInfo.offset:locaInfo.offset+locaInfo.length])

	return newGlyf, newLoca
}

// findLastYDeltaOffset finds the byte offset and size of the last Y delta
// in a simple glyph. Works by parsing the glyph structure to find the
// Y delta section, then computing the offset of the last entry.
func findLastYDeltaOffset(glyfData []byte, start, end uint32, nContour int) (offset uint32, size int) {
	// endPtsOfContours: nContour uint16 values starting at byte 10
	if start+10+uint32(nContour)*2+2 > end {
		return 0, 0
	}

	lastEndPt := binary.BigEndian.Uint16(glyfData[start+10+uint32(nContour-1)*2:])
	totalPoints := int(lastEndPt) + 1
	if totalPoints < 2 || totalPoints > 5000 {
		return 0, 0
	}

	// instructionLength at byte 10 + nContour*2
	instrLenOff := start + 10 + uint32(nContour)*2
	instrLen := uint32(binary.BigEndian.Uint16(glyfData[instrLenOff:]))
	flagsStart := instrLenOff + 2 + instrLen
	if flagsStart >= end {
		return 0, 0
	}

	// Walk flags to count X delta bytes and find last flag
	flagPos := flagsStart
	pointsSeen := 0
	xDeltaBytes := uint32(0)
	lastFlag := byte(0)
	valid := true

	for pointsSeen < totalPoints {
		if flagPos >= end {
			valid = false
			break
		}
		f := glyfData[flagPos]
		flagPos++
		lastFlag = f
		pointsSeen++

		if f&flagXShort != 0 {
			xDeltaBytes++
		} else if f&flagXsame == 0 {
			xDeltaBytes += 2
		}

		if f&flagRepeat != 0 {
			if flagPos >= end {
				valid = false
				break
			}
			repeat := int(glyfData[flagPos])
			flagPos++
			if f&flagXShort != 0 {
				xDeltaBytes += uint32(repeat)
			} else if f&flagXsame == 0 {
				xDeltaBytes += uint32(repeat) * 2
			}
			pointsSeen += repeat
		}
	}

	if !valid || pointsSeen != totalPoints {
		return 0, 0
	}

	// Y deltas start after flags + X deltas
	yDeltaStart := flagPos + xDeltaBytes
	if yDeltaStart >= end {
		return 0, 0
	}

	// Walk flags again to sum Y delta bytes before the last point
	flagPos2 := flagsStart
	yBytesBeforeLast := uint32(0)
	pts2 := 0
	for pts2 < totalPoints-1 {
		if flagPos2 >= end {
			return 0, 0
		}
		f := glyfData[flagPos2]
		flagPos2++

		if f&flagYShort != 0 {
			yBytesBeforeLast++
		} else if f&flagYsame == 0 {
			yBytesBeforeLast += 2
		}

		pts2++
		if f&flagRepeat != 0 {
			if flagPos2 >= end {
				return 0, 0
			}
			repeat := int(glyfData[flagPos2])
			flagPos2++
			if f&flagYShort != 0 {
				yBytesBeforeLast += uint32(repeat)
			} else if f&flagYsame == 0 {
				yBytesBeforeLast += uint32(repeat) * 2
			}
			pts2 += repeat
		}
	}

	// Last Y delta offset
	lastYOff := yDeltaStart + yBytesBeforeLast
	if lastYOff >= end {
		return 0, 0
	}

	// Determine size of last Y delta from its flag
	if lastFlag&flagYShort != 0 {
		return lastYOff, 1
	} else if lastFlag&flagYsame == 0 {
		return lastYOff, 2
	}
	return 0, 0 // delta is 0 bytes (ySame && !yShort)
}
