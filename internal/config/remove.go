package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// RemoveSource returns content without the source at id, and the removed
// lines. Lines are only deleted, every other byte is kept. Comment lines right
// above the source, at its indent, go with it. Groups left empty are removed
// too, the sources root stays.
func RemoveSource(content []byte, id string) ([]byte, string, error) {
	before, err := Parse(content)
	if err != nil {
		return nil, "", err
	}
	if _, ok := before[id]; !ok {
		return nil, "", fmt.Errorf("%s isn't a configured source", id)
	}

	file, err := parser.ParseBytes(content, 0)
	if err != nil {
		return nil, "", err
	}
	top, err := pairs(file.Docs[0].Body)
	if err != nil {
		return nil, "", err
	}

	// chain holds sources, the groups, then the source.
	chain := []*ast.MappingValueNode{find(top, "sources")}
	for _, s := range strings.Split(id, ".") {
		kids, err := groupPairs(chain[len(chain)-1])
		if err != nil {
			return nil, "", err
		}
		chain = append(chain, find(kids, s))
	}

	// Walk up while the group would be left empty, a group with defaults
	// still has that child.
	del := len(chain) - 1
	for del > 1 {
		kids, err := groupPairs(chain[del-1])
		if err != nil {
			return nil, "", err
		}
		if len(kids) != 1 {
			break
		}
		del--
	}

	lines := strings.Split(string(content), "\n")
	start, end := blockLines(lines, chain[del])
	out, removed := cut(content, lines, start, end)

	if err := verifyRemoved(out, before, id); err != nil {
		return nil, "", err
	}
	return out, removed, nil
}

// blockLines returns the indexes of the first and last line of a key's block:
// its head comments, the key, and the lines indented deeper.
func blockLines(lines []string, n *ast.MappingValueNode) (int, int) {
	keyCol := column(n.Key)
	keyLine := line(n.Key)

	start := keyLine - 1
	for start > 0 {
		l := strings.TrimRight(lines[start-1], "\r")
		trimmed := strings.TrimLeft(l, " ")
		if !strings.HasPrefix(trimmed, "#") || len(l)-len(trimmed) != keyCol {
			break
		}
		start--
	}
	return start, groupEnd(lines, keyLine, keyCol+1)
}

// cut deletes the lines from start to end, and returns them.
func cut(content []byte, lines []string, start, end int) ([]byte, string) {
	from := 0
	for j := range start {
		from += len(lines[j]) + 1
	}
	to := from
	for j := start; j <= end; j++ {
		to += len(lines[j]) + 1
	}
	to = min(to, len(content)) // the last line has no newline

	removed := strings.TrimRight(string(content[from:to]), "\r\n")
	out := slices.Concat(content[:from], content[to:])
	return out, removed
}

// verifyRemoved checks only id is gone and every other source resolves as
// before.
func verifyRemoved(out []byte, before map[string]ResolvedSource, id string) error {
	after, err := Parse(out)
	if err != nil {
		return err
	}

	want := slices.DeleteFunc(slices.Sorted(maps.Keys(before)), func(k string) bool { return k == id })
	if got := slices.Sorted(maps.Keys(after)); !slices.Equal(got, want) {
		return fmt.Errorf("internal error: removing %s left %v, want %v", id, got, want)
	}
	for other, a := range after {
		rs := before[other]
		if !reflect.DeepEqual(a.Config, rs.Config) || a.BinDefaulted != rs.BinDefaulted {
			return fmt.Errorf("internal error: removing %s changed %s", id, other)
		}
	}
	return nil
}
