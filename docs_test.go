package goboot

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
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
		if strings.HasPrefix(b.file, "prototypes/") {
			t.Errorf("README.md:%d: marker names %s, but prototypes/ is a separate module CI does not build", b.line, b.file)
			continue
		}
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

// TestVerbatimBlocksAreMarked pins the rule docs/spec.md 13 states for the
// reference sections: a block that CAN be lifted whole out of a compiled file
// must say which file. Marking the ones that qualify by hand would be a habit,
// and 13 already spells out why a habit is not a check — so this makes it a
// rule. A reference block added later that happens to be verbatim fails until
// it carries a marker.
func TestVerbatimBlocksAreMarked(t *testing.T) {
	lines := readmeLines(t)
	srcs := compiledSources(t)

	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```go") {
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "```") {
			end++
		}
		if i > 0 && strings.HasPrefix(lines[i-1], fromMarker) {
			i = end // already marked; TestMarkedBlocksAreStillInTheirFile has it
			continue
		}
		code := strings.Join(lines[i+1:end], "\n")
		if strings.TrimSpace(code) != "" {
			for _, src := range srcs {
				if src.containsCode(code) {
					t.Errorf("README.md:%d: block is byte for byte inside %s, so it must carry a %s%s --> marker", i+2, src.path, fromMarker, src.path)
					break
				}
			}
		}
		i = end
	}
}

// source is one file CI compiles: its bytes, and which of those bytes the
// compiler actually reads.
type source struct {
	path string
	text string
	code []bool // code[i] is true where text[i] is neither comment nor space
}

// containsCode reports whether block appears in the file somewhere that is not
// wholly inside a comment. A block found only in a doc comment is two pieces of
// prose agreeing with each other: renaming the symbol they both name breaks
// neither, so it has nothing a build failure would catch and earns no marker.
// A block that merely *carries* a comment still counts, because the code around
// it is compiled.
func (s source) containsCode(block string) bool {
	for off := 0; off+len(block) <= len(s.text); {
		i := strings.Index(s.text[off:], block)
		if i < 0 {
			return false
		}
		start := off + i
		for j := start; j < start+len(block); j++ {
			if s.code[j] {
				return true
			}
		}
		off = start + 1
	}
	return false
}

// compiledSources returns every Go file in this module, in lexical order so a
// block sitting in more than one file always names the same one. prototypes/ is
// skipped: it is a separate module that CI does not build, so a marker naming a
// file in there would promise a guarantee nothing keeps.
func compiledSources(t *testing.T) []source {
	t.Helper()
	fset := token.NewFileSet()
	var out []source
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "prototypes" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, source{path: filepath.ToSlash(path), text: string(b), code: codeMask(t, fset, path, b)})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no Go files found, so every block would pass with nothing checked")
	}
	return out
}

// codeMask marks the bytes the compiler reads: everything that is not a
// comment and not whitespace.
func codeMask(t *testing.T, fset *token.FileSet, path string, src []byte) []bool {
	t.Helper()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	mask := make([]bool, len(src))
	for i, b := range src {
		mask[i] = b != ' ' && b != '\t' && b != '\n' && b != '\r'
	}
	base := fset.File(f.Pos()).Base()
	for _, group := range f.Comments {
		for _, c := range group.List {
			for i := int(c.Pos()) - base; i < int(c.End())-base; i++ {
				mask[i] = false
			}
		}
	}
	return mask
}
