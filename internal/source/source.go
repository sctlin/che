package source

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/host"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/state"
)

type Source struct {
	// ID is the full source ID of the source, containing its full parent hierarchy
	// eg. tools.lsp.gopls
	ID string
	// Key is the base of the ID, used as the default link name
	// eg. gopls
	Key string
	// InstallDir is the directory in which the source will be installed
	InstallDir string
	// BinDir is the directory in which the sources binary will be linked to
	BinDir string
	Config config.SourceConfig
	// Fetcher is the fetch strategy used to get the remote source based on the sources config
	Fetcher fetch.Fetcher
}

func NewSources(resolved map[string]config.ResolvedSource, env fetch.Env) (map[string]*Source, error) {
	sources := map[string]*Source{}
	for id, rs := range resolved {
		source, err := NewSource(rs, env)
		if err != nil {
			return nil, fmt.Errorf("error while creating source %s: %w", id, err)
		}
		sources[id] = source
	}
	return sources, nil
}

func NewSource(rs config.ResolvedSource, env fetch.Env) (*Source, error) {
	fetcher, err := fetch.NewFetcher(rs.Config, env)
	if err != nil {
		return nil, fmt.Errorf("error while creating fetcher: %w", err)
	}
	return &Source{
		ID:         rs.ID,
		Key:        rs.Key,
		InstallDir: rs.Config.InstallDir,
		BinDir:     rs.Config.BinDir,
		Config:     rs.Config,
		Fetcher:    fetcher,
	}, nil
}
func detectFormat(c config.SourceConfig, asset string, host host.Host) (config.Format, error) {
	switch {
	case c.Git != "":
		return config.Tree, nil
	case c.Format == config.Package:
		if host.PackageFormat != "" {
			return config.Format(host.PackageFormat), nil
		}
		return "", errors.New("could not detect host native package format")
	case c.Format != "":
		return c.Format, nil
	}

	return config.FormatFromExtension(strings.ToLower(asset)), nil
}

func (s *Source) Spec(v fetch.Version, a *fetch.Artifact, prev *state.Entry, host host.Host) (state.Spec, error) {
	spec := state.Spec{
		Origin:     s.Fetcher.Origin(),
		Tag:        v.Tag,
		Commit:     v.Commit,
		InstallDir: s.InstallDir,
		Inputs:     s.Inputs(), // config fields copied verbatim
	}

	switch {
	case a != nil: // resolved: new install, new version, or asset inputs changed
		f, err := detectFormat(s.Config, a.Name, host)
		if err != nil {
			return state.Spec{}, err
		}
		spec.Asset, spec.Format = a.Name, string(f)
	case prev != nil: // nothing release-related changed: reuse what's installed
		spec.Asset, spec.Format = prev.Asset, prev.Format
	}
	return spec, nil
}

func (s *Source) Inputs() state.Inputs {
	return state.Inputs{
		AssetPattern: s.Config.Asset,
		Format:       string(s.Config.Format),
		Rename:       s.Config.Rename,
		Bin:          s.Config.Bin,
		Build:        s.Config.Build,
		Command:      s.Config.Command,
		Env:          s.Config.Env,
	}
}

// LinkPaths returns the sorted link paths the source creates. Packages and
// custom commands create none, since che doesn't own their files.
func (s *Source) LinkPaths(format string) []string {
	if s.Config.Command != "" || isPackage(config.Format(format)) {
		return nil
	}

	links := make([]string, 0, len(s.Config.Bin))
	for name := range s.Config.Bin {
		links = append(links, filepath.Join(s.BinDir, name))
	}
	slices.Sort(links)
	return links
}

func isPackage(f config.Format) bool {
	return f == config.Package || f == config.Deb || f == config.Rpm
}

func (s *Source) LockEntry(v fetch.Version, prev lock.Entry) lock.Entry {
	e := lock.Entry{
		Origin: s.Fetcher.Origin(),
		Ref:    s.Config.Ref,
		Tag:    v.Tag,
		Commit: v.Commit,
	}
	if prev.Tag == v.Tag && prev.Commit == v.Commit {
		e.Checksums = prev.Checksums
	}
	return e
}

func (s *Source) Latest(ctx context.Context, version string) (fetch.Version, error) {
	return s.Fetcher.Latest(ctx, fetch.Constraint{Version: version})
}

func (s *Source) Install(ctx context.Context, artifact fetch.Artifact) (state.Entry, error) {
	entry := state.Entry{}
	return entry, nil
}

func (s *Source) Upgrade(ctx context.Context, artifact fetch.Artifact, prev state.Entry) (state.Entry, error) {
	entry := state.Entry{}
	return entry, nil
}
