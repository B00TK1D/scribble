#!/usr/bin/env python3
"""Randomize glyph coordinates in a TTF font.

Reads a TTF from stdin, adds ±1 to the last X coordinate of each simple
glyph and ±1 to each advance width, writes modified font to stdout.

This is the ONLY reliable way to modify TrueType glyph data without
corrupting the font — manual binary parsing of glyf flags/deltas has
too many edge cases (repeat flags, variable-length encodings, etc.).
"""

import sys
import random

def main():
    seed = int(sys.argv[1]) if len(sys.argv) > 1 else 0
    rng = random.Random(seed)

    data = sys.stdin.buffer.read()

    from fontTools.ttLib import TTFont
    from io import BytesIO

    font = TTFont(BytesIO(data))

    # Randomize advance widths
    hmtx = font['hmtx']
    for glyph_name in font.getGlyphOrder():
        adv, lsb = hmtx[glyph_name]
        delta = rng.choice([-1, 0, 1])
        hmtx[glyph_name] = (max(0, adv + delta), lsb)

    # Randomize last X coordinate of each simple glyph
    glyf = font['glyf']
    for glyph_name in font.getGlyphOrder():
        g = glyf[glyph_name]
        if not hasattr(g, 'numberOfContours') or g.numberOfContours is None:
            continue
        if g.numberOfContours <= 0:
            continue
        if not hasattr(g, 'coordinates') or len(g.coordinates) < 2:
            continue

        coords = g.coordinates
        x, y = coords[-1]
        delta = rng.choice([-1, 1])
        coords[-1] = (x + delta, y)

    out = BytesIO()
    font.save(out)
    sys.stdout.buffer.write(out.getvalue())

if __name__ == '__main__':
    main()
