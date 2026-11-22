package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

const defaultIndent = 2

// AddSource returns content with a source inserted at id. Lines are only
// inserted, every other byte is kept. The source goes at the end of the
// deepest existing group of its ID, creating the missing groups. Values are
// always double-quoted.
func AddSource(content []byte, id string, conf SourceConfig) ([]byte, error) {
	segs := strings.Split(id, ".")
	for _, s := range segs {
		if s == "" || s == "defaults" {
			return nil, fmt.Errorf("invalid source ID %q", id)
		}
	}
	conf = normalize(conf)

	before, err := Parse(content)
	if err != nil {
		return nil, err
	}

	file, err := parser.ParseBytes(content, 0)
	if err != nil {
		return nil, err
	}
	if len(file.Docs) > 1 {
		return nil, errors.New("sources file has several documents")
	}
	var top []*ast.MappingValueNode
	if len(file.Docs) == 1 && file.Docs[0].Body != nil {
		if top, err = pairs(file.Docs[0].Body); err != nil {
			return nil, err
		}
	}

	var block []string
	var after int // index of the line the block goes after, -1 for the start
	lines := strings.Split(string(content), "\n")
	sources := find(top, "sources")
	if sources == nil {
		block = append([]string{"sources:"}, render(segs, conf, defaultIndent, defaultIndent)...)
		after = lastContentLine(lines)
	} else {
		block, after, err = insertInto(sources, segs, conf, lines)
		if err != nil {
			return nil, err
		}
	}

	out := splice(content, lines, after, block)

	if err := verify(out, before, id, segs, conf); err != nil {
		return nil, err
	}
	return out, nil
}

// insertInto finds the deepest existing group of segs under sources and
// renders what's missing at the end of it.
func insertInto(sources *ast.MappingValueNode, segs []string, conf SourceConfig, lines []string) ([]string, int, error) {
	parent := sources
	i := 0
	for ; i < len(segs); i++ {
		kids, err := groupPairs(parent)
		if err != nil {
			return nil, 0, err
		}
		child := find(kids, segs[i])
		if child == nil {
			break
		}

		id := strings.Join(segs[:i+1], ".")
		switch {
		case i == len(segs)-1:
			return nil, 0, fmt.Errorf("%s already exists", id)
		case isSourceNode(child.Value):
			return nil, 0, fmt.Errorf("%s is a source, it can't contain %s", id, strings.Join(segs, "."))
		}
		parent = child
	}

	kids, err := groupPairs(parent)
	if err != nil {
		return nil, 0, err
	}
	parentCol := column(parent.Key)
	childCol := parentCol + defaultIndent
	if len(kids) > 0 {
		childCol = column(kids[0].Key)
	}
	indent := max(childCol-parentCol, 1)

	return render(segs[i:], conf, childCol, indent), groupEnd(lines, line(parent.Key), childCol), nil
}

// groupEnd returns the index of the group's last line: the last line indented
// at least childCol before a less indented line that isn't blank or a comment.
// Comments above the next key stay with it.
func groupEnd(lines []string, keyLine, childCol int) int {
	last := keyLine - 1
	for j := keyLine; j < len(lines); j++ {
		l := strings.TrimRight(lines[j], "\r")
		trimmed := strings.TrimLeft(l, " ")
		if trimmed == "" {
			continue
		}
		if len(l)-len(trimmed) < childCol {
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			break
		}
		last = j
	}
	return last
}

func lastContentLine(lines []string) int {
	for j := len(lines) - 1; j >= 0; j-- {
		if strings.TrimSpace(lines[j]) != "" {
			return j
		}
	}
	return -1
}

// splice inserts block after the line at index after, keeping every other
// byte, and the file's line endings.
func splice(content []byte, lines []string, after int, block []string) []byte {
	nl := "\n"
	if bytes.Contains(content, []byte("\r\n")) {
		nl = "\r\n"
	}

	offset := 0
	for j := 0; j <= after; j++ {
		offset += len(lines[j]) + 1
	}

	var out bytes.Buffer
	if offset > len(content) { // the last line has no newline
		out.Write(content)
		out.WriteString(nl)
		offset = len(content)
	} else {
		out.Write(content[:offset])
	}
	for _, l := range block {
		out.WriteString(l + nl)
	}
	out.Write(content[offset:])
	return out.Bytes()
}

// verify checks the inserted source reads back as conf, and every other
// source resolves as before.
func verify(out []byte, before map[string]ResolvedSource, id string, segs []string, conf SourceConfig) error {
	after, err := Parse(out)
	if err != nil {
		return err
	}

	if _, ok := after[id]; !ok || len(after) != len(before)+1 {
		return fmt.Errorf("internal error: adding %s didn't add exactly that source", id)
	}
	for other, rs := range before {
		a, ok := after[other]
		if !ok || !reflect.DeepEqual(a.Config, rs.Config) || a.BinDefaulted != rs.BinDefaulted {
			return fmt.Errorf("internal error: adding %s changed %s", id, other)
		}
	}

	got, err := readSource(out, segs)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalize(got), conf) {
		return fmt.Errorf("internal error: %s reads back as %+v, want %+v", id, got, conf)
	}
	return nil
}

func readSource(content []byte, segs []string) (SourceConfig, error) {
	file, err := parser.ParseBytes(content, 0)
	if err != nil {
		return SourceConfig{}, err
	}
	node := find(mustPairs(file.Docs[0].Body), "sources")
	for _, s := range segs {
		if node == nil {
			break
		}
		node = find(mustPairs(node.Value), s)
	}
	if node == nil {
		return SourceConfig{}, fmt.Errorf("internal error: %s not found after adding it", strings.Join(segs, "."))
	}

	var c SourceConfig
	err = yaml.NodeToValue(node.Value, &c, yaml.DisallowUnknownField())
	return c, err
}

// field is a config key in the order sources are written.
type field struct {
	key string
	val string
	m   map[string]string
}

func fields(c SourceConfig) []field {
	return []field{
		{key: "github", val: c.Github},
		{key: "gitlab", val: c.Gitlab},
		{key: "git", val: c.Git},
		{key: "url", val: c.URL},
		{key: "jq", val: c.Jq},
		{key: "ref", val: c.Ref},
		{key: "version", val: c.Version},
		{key: "asset", val: c.Asset},
		{key: "format", val: string(c.Format)},
		{key: "rename", m: c.Rename},
		{key: "bin", m: c.Bin},
		{key: "build", val: c.Build},
		{key: "command", val: c.Command},
		{key: "env", m: c.Env},
		{key: "install_dir", val: c.InstallDir},
		{key: "bin_dir", val: c.BinDir},
	}
}

// render writes the groups in segs, then the source's set fields, starting at
// column col.
func render(segs []string, c SourceConfig, col, indent int) []string {
	var out []string
	pad := func(n int) string { return strings.Repeat(" ", n) }

	for _, s := range segs {
		out = append(out, pad(col)+quoteKey(s)+":")
		col += indent
	}
	for _, f := range fields(c) {
		switch {
		case len(f.m) > 0:
			out = append(out, pad(col)+f.key+":")
			for _, k := range slices.Sorted(maps.Keys(f.m)) {
				out = append(out, pad(col+indent)+quoteKey(k)+": "+strconv.Quote(f.m[k]))
			}
		case f.val != "":
			out = append(out, pad(col)+f.key+": "+strconv.Quote(f.val))
		}
	}
	return out
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// yaml11Words are read as booleans or null by YAML 1.1 parsers, other tools
// reading the file may be one.
var yaml11Words = []string{"y", "n", "yes", "no", "on", "off", "true", "false", "null"}

// quoteKey leaves keys plain unless YAML would read them as something other
// than the same string, e.g. "yes" or "1.0".
func quoteKey(k string) string {
	if plainKey.MatchString(k) && !slices.Contains(yaml11Words, strings.ToLower(k)) {
		var v any
		if err := yaml.Unmarshal([]byte(k), &v); err == nil && v == k {
			return k
		}
	}
	return strconv.Quote(k)
}

// normalize drops empty maps, which aren't written.
func normalize(c SourceConfig) SourceConfig {
	if len(c.Rename) == 0 {
		c.Rename = nil
	}
	if len(c.Env) == 0 {
		c.Env = nil
	}
	if len(c.Bin) == 0 {
		c.Bin = nil
	}
	return c
}

func pairs(n ast.Node) ([]*ast.MappingValueNode, error) {
	switch n := n.(type) {
	case *ast.MappingNode:
		if n.IsFlowStyle {
			return nil, fmt.Errorf("line %d: groups in flow style ({...}) can't be added to, add the source by hand", n.GetToken().Position.Line)
		}
		return n.Values, nil
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{n}, nil
	case *ast.CommentGroupNode:
		return nil, nil
	}
	return nil, fmt.Errorf("line %d: expected a mapping", n.GetToken().Position.Line)
}

func mustPairs(n ast.Node) []*ast.MappingValueNode {
	p, _ := pairs(n)
	return p
}

// groupPairs returns the children of a group, none for an empty one.
func groupPairs(group *ast.MappingValueNode) ([]*ast.MappingValueNode, error) {
	if _, ok := group.Value.(*ast.NullNode); ok {
		return nil, nil
	}
	return pairs(group.Value)
}

func find(ps []*ast.MappingValueNode, key string) *ast.MappingValueNode {
	for _, p := range ps {
		if p.Key.GetToken().Value == key {
			return p
		}
	}
	return nil
}

func isSourceNode(n ast.Node) bool {
	var ps []*ast.MappingValueNode
	switch n := n.(type) {
	case *ast.MappingNode:
		ps = n.Values
	case *ast.MappingValueNode:
		ps = []*ast.MappingValueNode{n}
	}
	return slices.ContainsFunc(ps, func(p *ast.MappingValueNode) bool {
		return slices.Contains(fetcherKeys, p.Key.GetToken().Value)
	})
}

// column returns the 0-based column of a node.
func column(n ast.Node) int { return n.GetToken().Position.Column - 1 }

// line returns the 1-based line of a node.
func line(n ast.Node) int { return n.GetToken().Position.Line }

// RenderSource returns the source as AddSource writes it, groups included,
// without indentation.
func RenderSource(id string, conf SourceConfig) string {
	return strings.Join(render(strings.Split(id, "."), normalize(conf), 0, defaultIndent), "\n")
}
