package qr

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The error correction is the part a mistake hides in best: a symbol with
// wrong ECC still draws, and still scans on a good camera, until one module
// is smudged. The vector is the standard worked example, "HELLO WORLD" as a
// version 1-M symbol.
func TestReedSolomonMatchesTheWorkedExample(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	if got := reedSolomon(data, 10); !bytes.Equal(got, want) {
		t.Fatalf("reedSolomon = %v, want %v", got, want)
	}
}

func TestFormatAndVersionWordsMatchTheStandardsTables(t *testing.T) {
	for _, tc := range []struct {
		mask int
		want string
	}{
		{0, "101010000010010"},
		{1, "101000100100101"},
		{2, "101111001111100"},
		{3, "101101101001011"},
		{4, "100010111111001"},
		{5, "100000011001110"},
		{6, "100111110010111"},
		{7, "100101010100000"},
	} {
		if got := fmt.Sprintf("%015b", formatBits(tc.mask)); got != tc.want {
			t.Errorf("formatBits(M, %d) = %s, want %s", tc.mask, got, tc.want)
		}
	}
	for _, tc := range []struct {
		version int
		want    string
	}{
		{7, "000111110010010100"},
		{8, "001000010110111100"},
		{10, "001010010011010011"},
	} {
		if got := fmt.Sprintf("%018b", versionBits(tc.version)); got != tc.want {
			t.Errorf("versionBits(%d) = %s, want %s", tc.version, got, tc.want)
		}
	}
}

// readCodewords walks a finished symbol the way a scanner does -- the same
// zigzag, skipping function modules, removing the mask -- and returns the
// bytes it finds. Placement and reading written separately would agree on a
// shared mistake; this one leans on newCode only for which modules are
// function modules, which the finder, timing and alignment checks pin down.
func readCodewords(c *Code, n int) []byte {
	ref := newCode(c.Version)
	var bits bitBuffer
	upward := true
	for right := c.Size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for i := 0; i < c.Size; i++ {
			y := i
			if upward {
				y = c.Size - 1 - i
			}
			for dx := 0; dx < 2; dx++ {
				x := right - dx
				if ref.reservedMap[y][x] {
					continue
				}
				bits = append(bits, c.Dark(x, y) != maskAt(c.Mask, x, y))
			}
		}
		upward = !upward
	}
	return bits.bytes()[:n]
}

func TestEverySupportedVersionRoundTripsItsCodewords(t *testing.T) {
	for v := 1; v <= maxVersion; v++ {
		b := levelM[v]
		// The longest payload the version holds, so the version chosen is v.
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		n := (8*b.dataCodewords() - 4 - countBits) / 8
		data := bytes.Repeat([]byte("otpauth://totp/"), 20)[:n]
		c, err := Encode(data)
		if err != nil {
			t.Fatalf("version %d: %v", v, err)
		}
		if c.Version != v || c.Size != 17+4*v {
			t.Fatalf("%d bytes chose version %d (size %d), want %d", n, c.Version, c.Size, v)
		}
		want := interleave(v, dataCodewords(v, data))
		if got := readCodewords(c, len(want)); !bytes.Equal(got, want) {
			t.Errorf("version %d: the codewords read back differ from the ones placed", v)
		}
	}
}

func TestTheFunctionPatternsAreWhereAScannerLooks(t *testing.T) {
	c, err := Encode([]byte("otpauth://totp/Zoomies:ada?secret=JBSWY3DPEHPK3PXP&issuer=Zoomies"))
	if err != nil {
		t.Fatal(err)
	}
	finder := []string{"1111111", "1000001", "1011101", "1011101", "1011101", "1000001", "1111111"}
	for _, at := range [][2]int{{0, 0}, {c.Size - 7, 0}, {0, c.Size - 7}} {
		for dy, row := range finder {
			for dx, ch := range row {
				if c.Dark(at[0]+dx, at[1]+dy) != (ch == '1') {
					t.Fatalf("the finder at %v is wrong at (%d,%d)", at, dx, dy)
				}
			}
		}
	}
	for i := 8; i < c.Size-8; i++ {
		if c.Dark(i, 6) != (i%2 == 0) || c.Dark(6, i) != (i%2 == 0) {
			t.Fatalf("the timing pattern is broken at %d", i)
		}
	}
	if !c.Dark(8, c.Size-8) {
		t.Fatal("the dark module is light")
	}
	// Both copies of the format word say level M and the chosen mask.
	var first, second int
	for i := 0; i <= 5; i++ {
		first |= b2i(c.Dark(8, i)) << i
	}
	first |= b2i(c.Dark(8, 7))<<6 | b2i(c.Dark(8, 8))<<7 | b2i(c.Dark(7, 8))<<8
	for i := 9; i < 15; i++ {
		first |= b2i(c.Dark(14-i, 8)) << i
	}
	for i := 0; i < 8; i++ {
		second |= b2i(c.Dark(c.Size-1-i, 8)) << i
	}
	for i := 8; i < 15; i++ {
		second |= b2i(c.Dark(8, c.Size-15+i)) << i
	}
	if first != formatBits(c.Mask) || second != first {
		t.Fatalf("format words %015b and %015b, want %015b", first, second, formatBits(c.Mask))
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestTooMuchDataIsRefusedRatherThanTruncated(t *testing.T) {
	if _, err := Encode(bytes.Repeat([]byte("a"), 300)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("300 bytes = %v, want ErrTooLong", err)
	}
}

func TestTheSVGIsSelfContainedAndCarriesTheQuietZone(t *testing.T) {
	c, err := Encode([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	svg := c.SVG("#000", "#fff")
	for _, want := range []string{`viewBox="0 0 29 29"`, `fill="#fff"`, `fill="#000"`, `M4 4h7v1h-7z`} {
		if !strings.Contains(svg, want) {
			t.Errorf("the SVG does not contain %q", want)
		}
	}
	for _, banned := range []string{"<script", "href", "http://example"} {
		if strings.Contains(svg, banned) {
			t.Errorf("the SVG contains %q", banned)
		}
	}
}
