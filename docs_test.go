package goboot

import (
	"os"
	"strings"
	"testing"
)

// The README's ten-minute path runs from this heading to the next one. Only
// the path carries the compiled-sample guarantee, per docs/spec.md 13.
const pathHeading = "## Ten minutes to a running service"

const fromMarker = "<!-- from: "

// TestPathGoBlocksCarryAFromMarker pins the half of docs/spec.md 8.4 that is
// easiest to lose: a Go block added to the path without a marker is unchecked
// prose again, and every other test here still passes.
func TestPathGoBlocksCarryAFromMarker(t *testing.T) {
	lines := readmeLines(t)
	first, last := pathBounds(t, lines)

	for i := first; i <= last; i++ {
		if !strings.HasPrefix(lines[i], "```go") {
			continue
		}
		if i == 0 || !strings.HasPrefix(lines[i-1], fromMarker) {
			t.Errorf("README.md:%d: Go block on the ten-minute path with no %s marker above it", i+1, fromMarker)
		}
	}
}

// TestMarkedBlocksAreStillInTheirFile pins the other half: a marked block is
// byte for byte inside the example file its marker names, and CI compiles
// that file. A doc snippet rots; a build failure does not.
func TestMarkedBlocksAreStillInTheirFile(t *testing.T) {
	blocks := markedBlocks(t, readmeLines(t))
	if len(blocks) == 0 {
		t.Fatal("README.md: no from-markers at all, so no snippet is checked against an example")
	}

	for _, b := range blocks {
		src, err := os.ReadFile(b.file)
		if err != nil {
			t.Errorf("README.md:%d: %v", b.line, err)
			continue
		}
		if strings.Contains(string(src), b.code) {
			continue
		}
		t.Errorf("README.md:%d: drifted from %s, whose first missing line is %q", b.line, b.file, firstLineMissing(string(src), b.code))
	}
}

// snippet is one marked block and the file it claims to come from.
type snippet struct {
	file string
	line int // 1-based, the block's first line of code
	code string
}

// readmeLines splits the README, because a finding is only useful if it names
// the line to go and look at.
func readmeLines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	return strings.Split(string(b), "\n")
}

// pathBounds returns the first and last line of the path section. A missing
// heading is fatal, because an empty range would pass with nothing checked.
func pathBounds(t *testing.T, lines []string) (int, int) {
	t.Helper()
	first := -1
	for i, line := range lines {
		switch {
		case line == pathHeading:
			first = i
		case first >= 0 && strings.HasPrefix(line, "## "):
			return first, i - 1
		}
	}
	if first < 0 {
		t.Fatalf("README.md: no %q heading, so the ten-minute path is gone or renamed", pathHeading)
	}
	return first, len(lines) - 1
}

// markedBlocks collects every from-marked fenced block. A malformed marker is
// a failure, not a skip: a marker matching nothing is no check at all.
func markedBlocks(t *testing.T, lines []string) []snippet {
	t.Helper()
	var out []snippet
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], fromMarker) {
			continue
		}
		file := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(lines[i], fromMarker), "-->"))
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "```") {
			t.Errorf("README.md:%d: marker for %s is not followed by a fenced block", i+1, file)
			continue
		}
		end := i + 2
		for end < len(lines) && !strings.HasPrefix(lines[end], "```") {
			end++
		}
		if end == len(lines) {
			t.Errorf("README.md:%d: fenced block for %s is never closed", i+2, file)
			continue
		}
		out = append(out, snippet{file: file, line: i + 3, code: strings.Join(lines[i+2:end], "\n")})
		i = end
	}
	return out
}

// firstLineMissing names the line to go and look at. Blank lines are skipped,
// because every file contains one.
func firstLineMissing(src, code string) string {
	for _, line := range strings.Split(code, "\n") {
		if strings.TrimSpace(line) != "" && !strings.Contains(src, line) {
			return line
		}
	}
	return code // every line is there, so they have been reordered
}
