// Package qr encodes a short string as a QR code and renders it as SVG.
//
// It exists for one picture: the otpauth:// address an authenticator app
// scans when somebody turns two-step verification on. That is a few hundred
// bytes at most, so this is the smallest encoder that serves it -- byte mode
// only, error correction level M only, versions 1 to 10 -- rather than a
// dependency that would also do kanji, micro codes and PNG. Rendering on the
// server keeps the secret out of any third-party script, and SVG keeps it
// sharp at any size with no image library.
//
// The numbers are ISO/IEC 18004's. Where a table is written out below it is
// copied from the standard rather than derived, because a derived table that
// is subtly wrong still produces a picture, just not one a phone can read.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTooLong is returned for data that does not fit in the largest version
// this encoder supports.
var ErrTooLong = errors.New("qr: data is too long for a version 10 code at error correction level M")

// ecBlocks is one version's error correction layout at level M: the error
// correction codewords per block, then the blocks in each of the two groups
// and the data codewords each of their blocks holds.
type ecBlocks struct {
	ecPerBlock       int
	g1Blocks, g1Data int
	g2Blocks, g2Data int
}

// levelM is ISO/IEC 18004 table 9, level M, versions 1 to 10.
var levelM = [...]ecBlocks{
	1:  {10, 1, 16, 0, 0},
	2:  {16, 1, 28, 0, 0},
	3:  {26, 1, 44, 0, 0},
	4:  {18, 2, 32, 0, 0},
	5:  {24, 2, 43, 0, 0},
	6:  {16, 4, 27, 0, 0},
	7:  {18, 4, 31, 0, 0},
	8:  {22, 2, 38, 2, 39},
	9:  {22, 3, 36, 2, 37},
	10: {26, 4, 43, 1, 44},
}

// alignment is the row and column centres of the alignment patterns, from
// annex E.
var alignment = [...][]int{
	1: nil, 2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30},
	6: {6, 34}, 7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50},
}

const maxVersion = 10

func (b ecBlocks) dataCodewords() int { return b.g1Blocks*b.g1Data + b.g2Blocks*b.g2Data }

// Code is an encoded symbol: a square of modules, true for dark.
type Code struct {
	Version int
	Mask    int
	Size    int
	modules [][]bool
	// reservedMap marks function modules, which data and masks skip.
	reservedMap [][]bool
}

// Dark reports whether the module at row y, column x is dark.
func (c *Code) Dark(x, y int) bool { return c.modules[y][x] }

// Encode builds the smallest symbol that holds data, with the mask that
// scores lowest on the standard's penalty rules.
func Encode(data []byte) (*Code, error) {
	version := 0
	for v := 1; v <= maxVersion; v++ {
		// Mode indicator (4 bits), a character count (8 bits below version
		// 10, 16 from it), then the bytes.
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		if 4+countBits+8*len(data) <= 8*levelM[v].dataCodewords() {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, ErrTooLong
	}
	codewords := interleave(version, dataCodewords(version, data))

	best := (*Code)(nil)
	bestScore := 0
	for mask := 0; mask < 8; mask++ {
		c := withMask(version, codewords, mask)
		if score := c.penalty(); best == nil || score < bestScore {
			best, bestScore = c, score
		}
	}
	return best, nil
}

// withMask lays the codewords out under one mask.
func withMask(version int, codewords []byte, mask int) *Code {
	c := newCode(version)
	c.Mask = mask
	c.placeData(codewords, mask)
	c.placeFormat(mask)
	return c
}

// dataCodewords renders data as a byte-mode segment padded out to the
// version's capacity.
func dataCodewords(version int, data []byte) []byte {
	var bits bitBuffer
	bits.put(0b0100, 4)
	if version >= 10 {
		bits.put(len(data), 16)
	} else {
		bits.put(len(data), 8)
	}
	for _, b := range data {
		bits.put(int(b), 8)
	}
	capacity := 8 * levelM[version].dataCodewords()
	// Up to four zero bits of terminator, then zeros to the byte boundary.
	for i := 0; i < 4 && len(bits) < capacity; i++ {
		bits = append(bits, false)
	}
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	out := bits.bytes()
	for pad := 0; len(out) < capacity/8; pad++ {
		if pad%2 == 0 {
			out = append(out, 0xEC)
		} else {
			out = append(out, 0x11)
		}
	}
	return out
}

// interleave splits the data into the version's blocks, appends each
// block's error correction, and interleaves them as the symbol stores them.
func interleave(version int, data []byte) []byte {
	b := levelM[version]
	var blocks [][]byte
	pos := 0
	for i := 0; i < b.g1Blocks; i++ {
		blocks = append(blocks, data[pos:pos+b.g1Data])
		pos += b.g1Data
	}
	for i := 0; i < b.g2Blocks; i++ {
		blocks = append(blocks, data[pos:pos+b.g2Data])
		pos += b.g2Data
	}
	ecc := make([][]byte, len(blocks))
	for i, blk := range blocks {
		ecc[i] = reedSolomon(blk, b.ecPerBlock)
	}
	var out []byte
	longest := b.g1Data
	if b.g2Data > longest {
		longest = b.g2Data
	}
	for i := 0; i < longest; i++ {
		for _, blk := range blocks {
			if i < len(blk) {
				out = append(out, blk[i])
			}
		}
	}
	for i := 0; i < b.ecPerBlock; i++ {
		for _, e := range ecc {
			out = append(out, e[i])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Reed-Solomon over GF(256) with the QR polynomial x^8+x^4+x^3+x^2+1.
// ---------------------------------------------------------------------------

var gfExp, gfLog = func() (exp [512]byte, log [256]byte) {
	x := 1
	for i := 0; i < 255; i++ {
		exp[i] = byte(x)
		log[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		exp[i] = exp[i-255]
	}
	return exp, log
}()

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// generator returns the degree-n generator polynomial, highest term first.
func generator(n int) []byte {
	g := []byte{1}
	for i := 0; i < n; i++ {
		next := make([]byte, len(g)+1)
		for j, c := range g {
			next[j] ^= c
			next[j+1] ^= gfMul(c, gfExp[i])
		}
		g = next
	}
	return g
}

// reedSolomon returns the n error correction codewords for data.
func reedSolomon(data []byte, n int) []byte {
	g := generator(n)
	rem := make([]byte, n)
	for _, d := range data {
		factor := d ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for j := 0; j < n; j++ {
			rem[j] ^= gfMul(g[j+1], factor)
		}
	}
	return rem
}

// ---------------------------------------------------------------------------
// Module placement
// ---------------------------------------------------------------------------

func newCode(version int) *Code {
	size := 17 + 4*version
	c := &Code{Version: version, Size: size, modules: make([][]bool, size)}
	reserved := make([][]bool, size)
	for i := range c.modules {
		c.modules[i] = make([]bool, size)
		reserved[i] = make([]bool, size)
	}
	c.reservedMap = reserved
	c.placeFunctionPatterns()
	return c
}

// set places a function module, which data placement and masking skip.
func (c *Code) set(x, y int, dark bool) {
	c.modules[y][x] = dark
	c.reservedMap[y][x] = true
}

func (c *Code) placeFunctionPatterns() {
	n := c.Size
	// Finder patterns, with their separators, in three corners.
	for _, at := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		for dy := -1; dy <= 7; dy++ {
			for dx := -1; dx <= 7; dx++ {
				x, y := at[0]+dx, at[1]+dy
				if x < 0 || y < 0 || x >= n || y >= n {
					continue
				}
				ring := max(abs(dx-3), abs(dy-3))
				c.set(x, y, ring != 2 && ring != 4)
			}
		}
	}
	// Timing patterns.
	for i := 8; i < n-8; i++ {
		c.set(i, 6, i%2 == 0)
		c.set(6, i, i%2 == 0)
	}
	// Alignment patterns, everywhere they do not collide with a finder.
	// Three of the grid's corners are finder patterns; every other centre,
	// including the ones on the timing lines, gets a pattern.
	centres := alignment[c.Version]
	last := len(centres) - 1
	for i, cy := range centres {
		for j, cx := range centres {
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					c.set(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	// Reserve the format areas (written once the mask is chosen) and the
	// dark module.
	for i := 0; i < 9; i++ {
		c.reservedMap[8][i] = true
		c.reservedMap[i][8] = true
	}
	for i := 0; i < 8; i++ {
		c.reservedMap[8][n-1-i] = true
		c.reservedMap[n-1-i][8] = true
	}
	c.set(8, n-8, true)
	// Version information, from version 7.
	if c.Version >= 7 {
		bits := versionBits(c.Version)
		for i := 0; i < 18; i++ {
			dark := bits>>uint(i)&1 == 1
			a, b := i/3, n-11+i%3
			c.set(a, b, dark)
			c.set(b, a, dark)
		}
	}
}

// placeData writes the codewords in the standard's two-column zigzag, from
// the bottom-right corner, applying the mask as it goes.
func (c *Code) placeData(codewords []byte, mask int) {
	n := c.Size
	bit := 0
	total := len(codewords) * 8
	upward := true
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			// The vertical timing pattern takes a whole column.
			right = 5
		}
		for i := 0; i < n; i++ {
			y := i
			if upward {
				y = n - 1 - i
			}
			for dx := 0; dx < 2; dx++ {
				x := right - dx
				if c.reservedMap[y][x] {
					continue
				}
				dark := false
				if bit < total {
					dark = codewords[bit/8]>>uint(7-bit%8)&1 == 1
					bit++
				}
				if maskAt(mask, x, y) {
					dark = !dark
				}
				c.modules[y][x] = dark
			}
		}
		upward = !upward
	}
}

// maskAt is the eight data masks of table 10.
func maskAt(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return (x*y)%2+(x*y)%3 == 0
	case 6:
		return ((x*y)%2+(x*y)%3)%2 == 0
	default:
		return ((x+y)%2+(x*y)%3)%2 == 0
	}
}

// formatBits is the 15-bit format word for level M and a mask: a BCH(15,5)
// code, XORed with the standard's fixed pattern so it is never all light.
func formatBits(mask int) int {
	const levelMBits = 0b00
	data := levelMBits<<3 | mask
	rem := data << 10
	for i := 14; i >= 10; i-- {
		if rem>>uint(i)&1 == 1 {
			rem ^= 0x537 << uint(i-10)
		}
	}
	return (data<<10 | rem) ^ 0x5412
}

// versionBits is the 18-bit version word: a BCH(18,6) code.
func versionBits(version int) int {
	rem := version << 12
	for i := 17; i >= 12; i-- {
		if rem>>uint(i)&1 == 1 {
			rem ^= 0x1F25 << uint(i-12)
		}
	}
	return version<<12 | rem
}

func (c *Code) placeFormat(mask int) {
	n := c.Size
	bits := formatBits(mask)
	bitAt := func(i int) bool { return bits>>uint(i)&1 == 1 }
	// Around the top-left finder: bits 0-5 down column 8, then 6-7 skipping
	// the timing row, 8 on the corner, 9-14 along row 8.
	for i := 0; i <= 5; i++ {
		c.modules[i][8] = bitAt(i)
	}
	c.modules[7][8] = bitAt(6)
	c.modules[8][8] = bitAt(7)
	c.modules[8][7] = bitAt(8)
	for i := 9; i < 15; i++ {
		c.modules[8][14-i] = bitAt(i)
	}
	// The second copy, split between the other two finders.
	for i := 0; i < 8; i++ {
		c.modules[8][n-1-i] = bitAt(i)
	}
	for i := 8; i < 15; i++ {
		c.modules[n-15+i][8] = bitAt(i)
	}
	c.modules[n-8][8] = true
}

// penalty scores a finished symbol by the four rules of section 7.8.3; the
// lowest-scoring mask is the one a scanner finds easiest.
func (c *Code) penalty() int {
	n := c.Size
	score := 0
	// Rule 1: runs of five or more in a row or column.
	for y := 0; y < n; y++ {
		for _, line := range [2]func(i int) bool{
			func(i int) bool { return c.modules[y][i] },
			func(i int) bool { return c.modules[i][y] },
		} {
			run := 1
			for i := 1; i < n; i++ {
				if line(i) == line(i-1) {
					run++
					continue
				}
				if run >= 5 {
					score += run - 2
				}
				run = 1
			}
			if run >= 5 {
				score += run - 2
			}
		}
	}
	// Rule 2: two-by-two blocks of one colour.
	for y := 0; y < n-1; y++ {
		for x := 0; x < n-1; x++ {
			d := c.modules[y][x]
			if c.modules[y][x+1] == d && c.modules[y+1][x] == d && c.modules[y+1][x+1] == d {
				score += 3
			}
		}
	}
	// Rule 3: anything that looks like a finder pattern.
	pattern := []bool{true, false, true, true, true, false, true, false, false, false, false}
	for y := 0; y < n; y++ {
		for x := 0; x+len(pattern) <= n; x++ {
			fwdRow, revRow, fwdCol, revCol := true, true, true, true
			for k, p := range pattern {
				if c.modules[y][x+k] != p {
					fwdRow = false
				}
				if c.modules[y][x+len(pattern)-1-k] != p {
					revRow = false
				}
				if c.modules[x+k][y] != p {
					fwdCol = false
				}
				if c.modules[x+len(pattern)-1-k][y] != p {
					revCol = false
				}
			}
			for _, hit := range []bool{fwdRow, revRow, fwdCol, revCol} {
				if hit {
					score += 40
				}
			}
		}
	}
	// Rule 4: how far the dark proportion is from half.
	dark := 0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if c.modules[y][x] {
				dark++
			}
		}
	}
	percent := dark * 100 / (n * n)
	score += abs(percent-50) / 5 * 10
	return score
}

// SVG renders the symbol with the four-module quiet zone the standard asks
// for, as one path. The colours are the caller's: the page draws it dark on
// light in both themes, because a scanner wants contrast, not a theme.
func (c *Code) SVG(dark, light string) string {
	const quiet = 4
	size := c.Size + 2*quiet
	var path strings.Builder
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if !c.modules[y][x] {
				continue
			}
			// Runs along a row become one rectangle, which keeps a version
			// 10 symbol to a few kilobytes.
			start := x
			for x+1 < c.Size && c.modules[y][x+1] {
				x++
			}
			fmt.Fprintf(&path, "M%d %dh%dv1h-%dz", start+quiet, y+quiet, x-start+1, x-start+1)
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`+
		`<rect width="%d" height="%d" fill="%s"/><path d="%s" fill="%s"/></svg>`,
		size, size, size, size, light, path.String(), dark)
}

// bitBuffer is a big-endian bit stream.
type bitBuffer []bool

func (b *bitBuffer) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		*b = append(*b, v>>uint(i)&1 == 1)
	}
}

func (b bitBuffer) bytes() []byte {
	out := make([]byte, len(b)/8)
	for i, bit := range b {
		if bit {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
