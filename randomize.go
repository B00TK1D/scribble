package main

import (
	"bytes"
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

// glyphVariants is the number of glyph variants created per original character.
// Each character 'a' gets this many different PUA codepoints, each pointing
// to a glyph with slightly different randomization.
const glyphVariants = 8

// RandomizeFont shuffles the cmap, randomizes glyph outlines and metrics,
// creates glyph variants for one-to-many mapping, and returns the modified
// font as TTF.
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

	origNumGlyphs := int(binary.BigEndian.Uint16(baseFont[tables[0x6D617870].offset+4 : tables[0x6D617870].offset+6]))
	headInfo := tables[0x68656164]
	origLocaFormat := int(binary.BigEndian.Uint16(baseFont[headInfo.offset+50 : headInfo.offset+52]))
	// Always use long loca format for output since variant glyphs push total
	// glyf size past the short loca limit (131070 bytes).
	outLocaFormat := 1

	origMap := parseCmap(baseFont[tables[0x636D6170].offset : tables[0x636D6170].offset+tables[0x636D6170].length])
	if len(origMap) == 0 {
		return nil, fmt.Errorf("cmap contains no glyph mappings")
	}

	// Build CharMap with variants per character
	supportedChars := make([]rune, 0, len(usedChars))
	for _, r := range usedChars {
		if _, ok := origMap[r]; ok {
			supportedChars = append(supportedChars, r)
		}
	}
	charMap := NewCharMap(supportedChars, seed, glyphVariants)

	// Generate glyph variants and build multi-PUA cmap
	newGlyf, newLoca, newHmtx, puaToGlyph := buildGlyphVariants(
		baseFont, tables, origNumGlyphs, origLocaFormat, outLocaFormat, origMap, charMap, seed+2,
	)

	// Build cmap
	newCmap := buildCmap4(puaToGlyph)

	// Calculate total glyphs (original + variants)
	totalGlyphs := origNumGlyphs + len(supportedChars)*(glyphVariants-1)

	// Update maxp numGlyphs to include variants
	newMaxp := make([]byte, tables[0x6D617870].length)
	copy(newMaxp, baseFont[tables[0x6D617870].offset:tables[0x6D617870].offset+tables[0x6D617870].length])
	binary.BigEndian.PutUint16(newMaxp[4:], uint16(totalGlyphs))

	// Build post table with randomized names for ALL glyphs
	newPost := buildRandomPostTable(totalGlyphs, seed+1)

	// Update head table: set indexToLocFormat = 1 (long loca)
	headData := make([]byte, tables[0x68656164].length)
	copy(headData, baseFont[tables[0x68656164].offset:tables[0x68656164].offset+tables[0x68656164].length])
	binary.BigEndian.PutUint16(headData[50:], 1) // indexToLocFormat = 1 (long)

	// Update hhea table: set numberOfHMetrics = totalGlyphs
	hheaData := make([]byte, tables[0x68686561].length)
	copy(hheaData, baseFont[tables[0x68686561].offset:tables[0x68686561].offset+tables[0x68686561].length])
	binary.BigEndian.PutUint16(hheaData[34:], uint16(totalGlyphs))

	// Sanitize name table: remove all identifying metadata (Roboto references)
	nameData := sanitizeNameTable(
		baseFont[tables[0x6E616D65].offset:tables[0x6E616D65].offset+tables[0x6E616D65].length],
		seed+3,
	)

	result, err := buildFont(map[uint32][]byte{
		0x68656164: headData,
		0x68686561: hheaData,
		0x686D7478: newHmtx,
		0x6D617870: newMaxp,
		0x6C6F6361: newLoca,
		0x676C7966: newGlyf,
		0x636D6170: newCmap,
		0x6E616D65: nameData,
		0x706F7374: newPost,
		0x4F532F32: baseFont[tables[0x4F532F32].offset : tables[0x4F532F32].offset+tables[0x4F532F32].length],
	})
	if err != nil {
		return nil, fmt.Errorf("build font: %w", err)
	}

	return &FontResult{FontBytes: result, CharMap: charMap}, nil
}

// buildGlyphVariants creates variant copies of each original glyph's data
// with slightly different random modifications (±1-3 to the last Y delta).
// Returns new glyf, loca, hmtx tables and the PUA→glyphID mapping.
func buildGlyphVariants(
	baseFont []byte,
	tables map[uint32]tblInfo,
	origNumGlyphs int,
	origLocaFormat int,
	outLocaFormat int,
	origMap map[rune]uint16,
	charMap *CharMap,
	seed int64,
) (newGlyf, newLoca, newHmtx []byte, puaToGlyph map[rune]uint16) {

	glyfInfo := tables[0x676C7966]
	locaInfo := tables[0x6C6F6361]
	hmtxInfo := tables[0x686D7478]

	// Parse original loca
	locaData := baseFont[locaInfo.offset : locaInfo.offset+locaInfo.length]
	origLoca := make([]uint32, origNumGlyphs+1)
	if origLocaFormat == 1 {
		for i := 0; i <= origNumGlyphs && i*4+4 <= len(locaData); i++ {
			origLoca[i] = binary.BigEndian.Uint32(locaData[i*4:])
		}
	} else {
		for i := 0; i <= origNumGlyphs && i*2+2 <= len(locaData); i++ {
			origLoca[i] = uint32(binary.BigEndian.Uint16(locaData[i*2:])) * 2
		}
	}

	// Copy original hmtx
	origHmtx := make([]byte, hmtxInfo.length)
	copy(origHmtx, baseFont[hmtxInfo.offset:hmtxInfo.offset+hmtxInfo.length])

	rng := rand.New(rand.NewSource(seed))

	// Track which original glyph IDs need variants
	type variantInfo struct {
		origGID   uint16
		variants  []uint16 // glyph IDs of the variants
	}
	var neededVariants []variantInfo

	// Variant glyph IDs start after all original glyphs
	nextGID := uint16(origNumGlyphs)

	// Build reverse map: original glyph ID → original codepoint
	gidToOrig := make(map[uint16]rune)
	for orig, gid := range origMap {
		gidToOrig[gid] = orig
	}

	puaToGlyph = make(map[rune]uint16)

	// For each character in the CharMap, create variants
	for _, puas := range charMap.Forward {
		if len(puas) == 0 {
			continue
		}
		// All PUA variants map to the same original character
		origChar := charMap.Reverse[puas[0]]
		origGID, ok := origMap[origChar]
		if !ok {
			continue
		}

		// First PUA maps to original glyph
		puaToGlyph[puas[0]] = origGID

		// Remaining PUAs get variant glyph IDs
		var variantGIDs []uint16
		for v := 1; v < len(puas); v++ {
			puaToGlyph[puas[v]] = nextGID
			variantGIDs = append(variantGIDs, nextGID)
			nextGID++
		}

		if len(variantGIDs) > 0 {
			neededVariants = append(neededVariants, variantInfo{
				origGID:  origGID,
				variants: variantGIDs,
			})
		}
	}

	totalGlyphs := int(nextGID)

	// Build new glyf: original glyphs (modified) + variant glyphs (copies)
	var glyfBuf bytes.Buffer

	// Copy original glyf data
	origGlyf := make([]byte, glyfInfo.length)
	copy(origGlyf, baseFont[glyfInfo.offset:glyfInfo.offset+glyfInfo.length])

	// Modify original glyphs: randomize last Y delta by ±1-3
	rng = rand.New(rand.NewSource(seed))
	for gid := 0; gid < origNumGlyphs; gid++ {
		start := origLoca[gid]
		end := origLoca[gid+1]
		if end <= start || end-start < 10 {
			continue
		}
		nContour := int16(binary.BigEndian.Uint16(origGlyf[start:]))
		if nContour <= 0 || nContour > 50 {
			continue
		}
		lastYDeltaOff, lastYDeltaSize := findLastYDeltaOffset(origGlyf, start, end, int(nContour))
		if lastYDeltaOff == 0 || lastYDeltaSize == 0 {
			continue
		}
		delta := int16(rng.Intn(3)+1) * int16(rng.Intn(2)*2-1) // ±1 to ±3
		if lastYDeltaSize == 2 {
			val := int16(binary.BigEndian.Uint16(origGlyf[lastYDeltaOff:]))
			binary.BigEndian.PutUint16(origGlyf[lastYDeltaOff:], uint16(val+delta))
		} else {
			cur := int(origGlyf[lastYDeltaOff])
			newVal := cur + int(delta)
			if newVal >= 0 && newVal <= 255 {
				origGlyf[lastYDeltaOff] = byte(newVal)
			}
		}
	}

	// Write original glyf data (with modifications)
	glyfBuf.Write(origGlyf)

	// Append variant glyph data (copies of original glyphs, no modification)
	for _, vi := range neededVariants {
		start := origLoca[vi.origGID]
		end := origLoca[vi.origGID+1]
		glyphSize := int(end - start)
		if outLocaFormat == 0 && glyphSize%2 != 0 {
			glyphSize++
		}
		glyphData := make([]byte, glyphSize)
		copy(glyphData, origGlyf[start:start+(end-start)])
		for range vi.variants {
			glyfBuf.Write(glyphData)
		}
	}

	newGlyf = glyfBuf.Bytes()

	// Build new loca table (always long format)
	newLoca = make([]byte, (totalGlyphs+1)*4)

	// Rebuild loca from the actual glyph data
	offset := uint32(0)
	// Original glyphs
	for gid := 0; gid < origNumGlyphs; gid++ {
		setLocaEntry(newLoca, gid, offset, outLocaFormat)
		start := origLoca[gid]
		end := origLoca[gid+1]
		glyphSize := end - start
		offset += glyphSize
	}
	// Variant glyphs
	variantIdx := 0
	for _, vi := range neededVariants {
		start := origLoca[vi.origGID]
		end := origLoca[vi.origGID+1]
		glyphSize := end - start
		if origLocaFormat == 0 && glyphSize%2 != 0 {
			glyphSize++
		}
		for range vi.variants {
			setLocaEntry(newLoca, origNumGlyphs+variantIdx, offset, outLocaFormat)
			offset += glyphSize
			variantIdx++
		}
	}
	// Final entry
	setLocaEntry(newLoca, totalGlyphs, offset, outLocaFormat)

	// Build new hmtx: original entries + variant entries (same advance width + LSB)
	newHmtx = make([]byte, totalGlyphs*4)
	copy(newHmtx[:origNumGlyphs*4], origHmtx)
	for _, vi := range neededVariants {
		advOff := int(vi.origGID) * 4
		adv := binary.BigEndian.Uint16(origHmtx[advOff:])
		lsb := binary.BigEndian.Uint16(origHmtx[advOff+2:])
		for _, variantGID := range vi.variants {
			vOff := int(variantGID) * 4
			// Apply +0 or +1 variation to advance width for variants
			delta := int16(rng.Intn(2))
			newAdv := int16(adv) + delta
			if newAdv < 0 {
				newAdv = 0
			}
			binary.BigEndian.PutUint16(newHmtx[vOff:], uint16(newAdv))
			binary.BigEndian.PutUint16(newHmtx[vOff+2:], lsb)
		}
	}

	// Also randomize original glyph advance widths (+0 or +1 only)
	for gid := 0; gid < origNumGlyphs; gid++ {
		delta := int16(rng.Intn(2))
		off := gid * 4
		adv := int16(binary.BigEndian.Uint16(newHmtx[off:])) + delta
		if adv < 0 {
			adv = 0
		}
		binary.BigEndian.PutUint16(newHmtx[off:], uint16(adv))
	}

	return newGlyf, newLoca, newHmtx, puaToGlyph
}

func setLocaEntry(loca []byte, gid int, offset uint32, format int) {
	if format == 1 {
		binary.BigEndian.PutUint32(loca[gid*4:], offset)
	} else {
		binary.BigEndian.PutUint16(loca[gid*2:], uint16(offset/2))
	}
}

// findLastYDeltaOffset finds the byte offset and size of the last Y delta
// in a simple glyph. Works by parsing the glyph structure to find the
// Y delta section, then computing the offset of the last entry.
func findLastYDeltaOffset(glyfData []byte, start, end uint32, nContour int) (offset uint32, size int) {
	if start+10+uint32(nContour)*2+2 > end {
		return 0, 0
	}

	lastEndPt := binary.BigEndian.Uint16(glyfData[start+10+uint32(nContour-1)*2:])
	totalPoints := int(lastEndPt) + 1
	if totalPoints < 2 || totalPoints > 5000 {
		return 0, 0
	}

	instrLenOff := start + 10 + uint32(nContour)*2
	instrLen := uint32(binary.BigEndian.Uint16(glyfData[instrLenOff:]))
	flagsStart := instrLenOff + 2 + instrLen
	if flagsStart >= end {
		return 0, 0
	}

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

	yDeltaStart := flagPos + xDeltaBytes
	if yDeltaStart >= end {
		return 0, 0
	}

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

	lastYOff := yDeltaStart + yBytesBeforeLast
	if lastYOff >= end {
		return 0, 0
	}

	if lastFlag&flagYShort != 0 {
		return lastYOff, 1
	} else if lastFlag&flagYsame == 0 {
		return lastYOff, 2
	}
	return 0, 0
}
