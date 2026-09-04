package migrate

import (
	"fmt"
	"strings"
)

// maxDiffLines bounds what the differ will compare. A workflow is a hand-
// written file of a few hundred lines; anything past this is generated, and
// nobody is going to read its diff in a review screen either way.
const maxDiffLines = 4000

// contextLines is how much unchanged text surrounds each hunk. Three is what
// git prints, which is what a reviewer's eye is trained on.
const contextLines = 3

// Diff renders a unified diff between two versions of one file.
//
// The wizard shows this before it opens anything: a migration that rewrites
// workflows in somebody's repository has to be reviewable *before* it becomes a
// pull request, not after. It returns "" when the two are identical.
func Diff(path, before, after string) string {
	if before == after {
		return ""
	}
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ this file is too large to diff here; review it in the pull request @@\n", path, path)
	}

	ops := lcsOps(a, b)
	hunks := group(ops)
	if len(hunks) == 0 {
		return ""
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	for _, h := range hunks {
		aStart, aCount, bStart, bCount := 0, 0, 0, 0
		for _, op := range h {
			if op.kind != '+' {
				if aCount == 0 {
					aStart = op.aLine
				}
				aCount++
			}
			if op.kind != '-' {
				if bCount == 0 {
					bStart = op.bLine
				}
				bCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for _, op := range h {
			out.WriteByte(op.kind)
			out.WriteString(op.text)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// op is one line of a diff: kept (' '), removed ('-') or added ('+').
type op struct {
	kind  byte
	text  string
	aLine int // 1-based line number in the before text, 0 for an addition
	bLine int // 1-based line number in the after text, 0 for a removal
}

// lcsOps walks the longest common subsequence of a and b, which is the
// smallest edit script a reviewer would call correct.
func lcsOps(a, b []string) []op {
	n, m := len(a), len(b)
	// table[i][j] is the length of the LCS of a[i:] and b[j:].
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}

	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{kind: ' ', text: a[i], aLine: i + 1, bLine: j + 1})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, op{kind: '-', text: a[i], aLine: i + 1})
			i++
		default:
			ops = append(ops, op{kind: '+', text: b[j], bLine: j + 1})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{kind: '-', text: a[i], aLine: i + 1})
	}
	for ; j < m; j++ {
		ops = append(ops, op{kind: '+', text: b[j], bLine: j + 1})
	}
	return ops
}

// group collects the changed lines into hunks with context around them, and
// drops the long runs of unchanged text between hunks.
func group(ops []op) [][]op {
	changed := make([]bool, len(ops))
	any := false
	for i, o := range ops {
		if o.kind != ' ' {
			changed[i], any = true, true
		}
	}
	if !any {
		return nil
	}

	keep := make([]bool, len(ops))
	for i, c := range changed {
		if !c {
			continue
		}
		for j := max(0, i-contextLines); j <= min(len(ops)-1, i+contextLines); j++ {
			keep[j] = true
		}
	}

	var (
		hunks [][]op
		cur   []op
	)
	for i, k := range keep {
		if k {
			cur = append(cur, ops[i])
			continue
		}
		if len(cur) > 0 {
			hunks = append(hunks, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		hunks = append(hunks, cur)
	}
	return hunks
}
