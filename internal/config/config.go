package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
	"github.com/sctlin/che/internal/utils"
	"github.com/sctlin/che/internal/version"
)

func baseDefaults() Defaults {
	home, _ := os.UserHomeDir()
	return Defaults{
		InstallDir: filepath.Join(home, ".che", "pkgs"),
		BinDir:     cmp.Or(os.Getenv("XDG_BIN_HOME"), filepath.Join(home, ".local", "bin")),
		Rename:     map[string]string{},
		Env:        map[string]string{},
	}
}

type BinSpec map[string]string

func (b *BinSpec) UnmarshalYAML(unmarshal func(any) error) error {
	var path string
	if err := unmarshal(&path); err == nil {
		name := filepath.Base(path)
		if strings.ContainsAny(name, "*?[") {
			return fmt.Errorf("bin %q: link name can't be a glob, use a map", path)
		}
		*b = BinSpec{name: path}
		return nil
	}

	var m map[string]string
	if err := unmarshal(&m); err != nil {
		return fmt.Errorf("bin must be a string or a map of name: path")
	}
	*b = m
	return nil
}

type Defaults struct {
	InstallDir string `yaml:"install_dir"`
	BinDir     string `yaml:"bin_dir"`
	// Rename maps GOOS and GOARCH values to the names assets use, merged
	// with the inherited ones. eg. { amd64: x86_64, arm64: aarch64 }
	Rename map[string]string `yaml:"rename"`
	Env    map[string]string `yaml:"env"` // for build/command
}

func (d Defaults) IsZero() bool {
	return d.InstallDir == "" && d.BinDir == "" && len(d.Rename) == 0 && len(d.Env) == 0
}

func (d Defaults) Merge(o Defaults) Defaults {
	d.Rename = maps.Clone(d.Rename)
	d.Env = maps.Clone(d.Env)
	if o.InstallDir != "" {
		d.InstallDir = o.InstallDir
	}
	if o.BinDir != "" {
		d.BinDir = o.BinDir
	}
	for k, v := range o.Rename {
		if d.Rename == nil {
			d.Rename = map[string]string{}
		}
		d.Rename[k] = v
	}
	for k, v := range o.Env {
		if d.Env == nil {
			d.Env = map[string]string{}
		}
		d.Env[k] = v
	}
	return d
}

type SourceConfig struct {
	Defaults `yaml:",inline"`
	// Github is the github repository from which to fetch the release. eg. sctlin/che
	Github string `yaml:"github"`
	// Gitlab is the the gitlab repository from which to fetch the release. eg. sctlin/che
	Gitlab string `yaml:"gitlab"`
	// URL is the URL from which to fetch the release. eg. https://example.com/releases
	URL string `yaml:"url"`
	// Jq is the jq command to be used together with URL for formatting the HTTP response
	Jq string `yaml:"jq"`
	// Git is the repository from which to fetch the release. eg. https://github.com/sctlin/che.git
	Git string `yaml:"git"`
	Ref string `yaml:"ref"`
	// Asset is the filename of the asset that will be downloaded. It accepts arch, os, tag, version variables that will be replaced when fetching the file. The Install kind will be inferred from the extension of the file, unless explicitly set with Install.
	// eg. che-${tag}-${version}-${arch}-${os}.tar.gz"
	Asset string `yaml:"asset"`
	// Version is the version of the source that will be installed
	Version string `yaml:"version"`
	// Format is the file format of the downloaded asset. If both Format and Asset are not specified then Format will default to archive (tar.gz)
	Format Format `yaml:"format"`
	// Build contains the command with which to build, used together with a Git fetcher
	// eg. go build -o mytool ./cmd/mytool
	Build string `yaml:"build"`
	// Command contains a custom command that should be used run to install the source
	Command string `yaml:"command"`
	// Bin contains the path to the binary found in the installed package or in the case of multiple binaries, contains a mapping that maps the name of the binary that will be used in the host to the path to the binary found in the installed package. The path may contain globs
	// eg. bin/che
	// eg. {foo: bin/foo, bar: */bar}
	Bin BinSpec `yaml:"bin"` // string or map
}

func Load(path string) (map[string]ResolvedSource, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error while reading source file: %w", err)
	}
	return Parse(content)
}

// fileNode is the sources file: top-level defaults, and the sources root
// group, whose name isn't part of the IDs.
type fileNode struct {
	Defaults Defaults         `yaml:"defaults"`
	Sources  *SourceGroupNode `yaml:"sources"`
}

// Parse resolves and validates the content of a sources file.
func Parse(content []byte) (map[string]ResolvedSource, error) {
	var f fileNode
	if err := yaml.UnmarshalWithOptions(content, &f, yaml.DisallowUnknownField()); err != nil {
		return nil, fmt.Errorf("error while unmarshalling source file: %w", err)
	}

	resolvedSources := map[string]ResolvedSource{}
	if f.Sources != nil {
		if !f.Sources.Defaults.IsZero() {
			return nil, errors.New("error while unmarshalling source file: defaults go at the top level, not under sources")
		}
		resolve(f.Sources, baseDefaults().Merge(f.Defaults), []string{}, resolvedSources)
	}

	if err := validateSources(resolvedSources); err != nil {
		return nil, fmt.Errorf("error while validating source file: %w", err)
	}
	return resolvedSources, nil
}

type PosError struct {
	Pos *token.Position
	ID  string
	Msg string
}

func (e PosError) Error() string {
	return fmt.Sprintf("line %d has source %s with error: %s", e.Pos.Line, e.ID, e.Msg)
}

type Format string

func (k *Format) UnmarshalText(b []byte) error {
	if f, ok := supportedFormats[string(b)]; ok {
		*k = f
		return nil
	}

	return fmt.Errorf("invalid install %q, use one of %s", b, strings.Join(slices.Collect(maps.Keys(supportedFormats)), ","))
}

var supportedFormats = map[string]Format{
	"tar.gz":  TarGz,
	"tar.xz":  TarXz,
	"package": Package,
	"zip":     Zip,
	"deb":     Deb,
	"rpm":     Rpm,
	"gz":      Gz,
	"xz":      Xz,
}

func ExtensionFromFormat(f Format) string {
	if f == "Package" {
		// we should return the systems native package format, for now rpm
		return "rpm"
	}
	for k, v := range supportedFormats {
		if v == f {
			return k
		}
	}
	return ""
}

// suffixFormats is ordered so compound suffixes match before their single
// counterparts: ".tar.gz" must win over ".gz".
var suffixFormats = []struct {
	suffix string
	format Format
}{
	{".tar.gz", TarGz}, {".tgz", TarGz},
	{".tar.xz", TarXz}, {".txz", TarXz},
	{".zip", Zip},
	{".deb", Deb}, {".rpm", Rpm},
	{".gz", Gz}, {".xz", Xz},
}

// SingleFile reports whether f installs one file rather than a tree.
func SingleFile(f Format) bool {
	return f == Gz || f == Xz || f == Binary
}

func FormatFromExtension(file string) Format {
	for _, sf := range suffixFormats {
		if strings.HasSuffix(file, sf.suffix) {
			return sf.format
		}
	}
	return Binary
}

var (
	// TarGz represents a tag.gz file format
	TarGz Format = "tar.gz"
	// TarXz represents a tag.xz file format
	TarXz Format = "tar.xz"
	// Zip represents a zip file format
	Zip Format = "zip"
	// Package represents a native system package, such as deb or rpm depending on the system
	Package Format = "package"
	// Deb represents a deb file format
	Deb Format = "deb"
	// Rpm represents a rpm file format
	Rpm Format = "rpm"
	// Gz represents a gz file format
	Gz Format = "gz"
	// Xz represents a xz file format
	Xz Format = "xz"
	// Archive represents a compressed archive file, such as tar.gz, tar.xz or zip
	Archive Format = "archive"
	// Compressed represents a compressed file, such as gz, xz
	Compressed Format = "compressed"
	// Binary represents a binary file
	Binary Format = "binary"
	// Tree represents directory containing a git worktree
	Tree Format = "tree"
)

func validateSource(source ResolvedSource) error {
	c := source.Config
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, PosError{Pos: source.Pos, ID: source.ID, Msg: fmt.Sprintf(format, args...)})
	}

	fetchers := []string{}
	if c.Github != "" {
		fetchers = append(fetchers, "github")
	}
	if c.Gitlab != "" {
		fetchers = append(fetchers, "gitlab")
	}
	if c.Git != "" {
		fetchers = append(fetchers, "git")
	}
	if c.URL != "" {
		fetchers = append(fetchers, "url")
	}
	if len(fetchers) != 1 {
		fail("exactly one of github, gitlab, url, git required, got %s", strings.Join(fetchers, ", "))
	}

	// http fetcher checks
	if c.Jq != "" && c.URL == "" {
		fail("jq requires url")
	}
	if c.URL != "" && !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://") {
		fail("url must be http(s)")
	}

	// git fetcher checks
	isGit := c.Git != ""
	if c.Ref != "" && !isGit {
		fail("ref requires git")
	}
	// rename isn't checked, it may be inherited from defaults
	if isGit && (c.Asset != "" || c.Version != "" || c.Format != "") {
		fail("asset, version and format don't apply to git")
	}

	if _, err := version.Parse(c.Version); err != nil {
		fail("%v", err)
	}

	if c.Build != "" && c.Command != "" {
		fail("build and command are mutually exclusive")
	}
	if c.Format == Package && len(c.Bin) > 0 && !source.BinDefaulted {
		fail("bin doesn't apply to install: package")
	}

	// templates should not contain unknown variables nor unterminated variables blocks eg. ${
	for _, s := range append([]string{c.Asset}, slices.Collect(maps.Values(c.Bin))...) {
		if v := utils.UnknownVars(s, "tag", "version", "os", "arch"); len(v) > 0 {
			fail("unknown template variables %v in %s", v, s)
		}
		if strings.Count(s, "${") != len(utils.TemplateVars(s)) {
			fail("unterminated '${' in %s", s)
		}
	}

	// bin paths, install dir and bin dir should all be expanded after resolve
	for name, p := range c.Bin {
		// the link is created at bin_dir/name
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			fail("bin %q: link name can't be a path, set bin_dir for the directory", name)
		}
		if filepath.IsAbs(p) || slices.Contains(strings.Split(p, "/"), "..") {
			fail("bin %s: path must be relative to the install dir: %q", name, p)
		}
	}
	if !filepath.IsAbs(c.InstallDir) {
		fail("install_dir must be absolute after expansion, got %s", c.InstallDir)
	}
	if !filepath.IsAbs(c.BinDir) {
		fail("bin_dir must be absolute after expansion, got %s", c.BinDir)
	}
	return errors.Join(errs...)
}

func validateSources(sources map[string]ResolvedSource) error {
	var errs []error
	var sourceErrs []error
	links := map[string][]string{}
	for id, s := range sources {
		for name := range s.Config.Bin {
			l := filepath.Join(s.Config.BinDir, name)
			links[l] = append(links[l], id)
		}
		sourceErrs = append(sourceErrs, validateSource(s))
	}
	for l, ids := range links {
		if len(ids) > 1 {
			errs = append(errs, fmt.Errorf("%s: linked by %s", l, strings.Join(ids, ", ")))
		}
	}
	errs = append(sourceErrs, errs...)
	return errors.Join(errs...)
}

type ResolvedSource struct {
	ID           string
	Key          string
	Config       SourceConfig
	BinDefaulted bool
	Pos          *token.Position
}

func resolve(sg *SourceGroupNode, inherited Defaults, prefix []string, out map[string]ResolvedSource) {
	defaults := inherited.Merge(sg.Defaults)

	for key, s := range sg.Sources {
		conf := s.Config
		conf.Defaults = defaults.Merge(conf.Defaults)
		if s.Config.InstallDir == "" {
			conf.InstallDir = filepath.Join(defaults.InstallDir, key)
		} else {
			conf.InstallDir = s.Config.InstallDir
		}
		binDefaulted := conf.Bin == nil
		if binDefaulted {
			conf.Bin = BinSpec{key: key}
		}
		id := strings.Join(append(prefix, key), ".")
		out[id] = ResolvedSource{ID: id, Key: key, Config: conf, BinDefaulted: binDefaulted, Pos: s.Pos}
	}

	for key, child := range sg.Groups {
		next := defaults
		next.InstallDir = filepath.Join(next.InstallDir, key)
		resolve(child, next, append(slices.Clone(prefix), key), out)
	}
}

type SourceGroupNode struct {
	Defaults Defaults
	Groups   map[string]*SourceGroupNode
	Sources  map[string]*SourceNode
}
type SourceNode struct {
	Config SourceConfig
	Pos    *token.Position
}

var fetcherKeys = []string{"github", "gitlab", "gitea", "url", "git"}

func (sg *SourceGroupNode) UnmarshalYAML(node ast.Node) error {
	m, ok := node.(*ast.MappingNode)
	if !ok {
		return fmt.Errorf("%s: expected a mapping", node.GetToken().Position)
	}
	isSource := func(n ast.Node) bool {
		m, ok := n.(*ast.MappingNode)
		if !ok {
			return false
		}
		return slices.ContainsFunc(m.Values, func(e *ast.MappingValueNode) bool {
			return slices.Contains(fetcherKeys, e.Key.GetToken().Value)
		})
	}

	sg.Groups = map[string]*SourceGroupNode{}
	sg.Sources = map[string]*SourceNode{}

	var errs []error
	for _, e := range m.Values {
		key := e.Key.GetToken().Value
		var err error
		switch {
		case key == "defaults":
			err = yaml.NodeToValue(e.Value, &sg.Defaults, yaml.DisallowUnknownField())
		case strings.Contains(key, "."):
			err = fmt.Errorf("%s: key %q can't contain '.'", e.Key.GetToken().Position, key)
		case isSource(e.Value):
			sn := &SourceNode{Pos: e.Value.GetToken().Position}
			err = yaml.NodeToValue(e.Value, &sn.Config, yaml.DisallowUnknownField())
			sg.Sources[key] = sn
		default:
			child := &SourceGroupNode{}
			err = yaml.NodeToValue(e.Value, child, yaml.DisallowUnknownField())
			sg.Groups[key] = child
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}
