package plan

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/host"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
)

type fakeFetcher struct{ origin string }

func (f fakeFetcher) Origin() string { return f.origin }
func (fakeFetcher) Latest(context.Context, fetch.Constraint) (fetch.Version, error) {
	return fetch.Version{}, errors.New("unused")
}
func (fakeFetcher) Resolve(context.Context, fetch.Version) (fetch.Artifact, error) {
	return fetch.Artifact{}, errors.New("unused")
}
func (fakeFetcher) Download(context.Context, fetch.Artifact, string) (string, error) {
	return "", errors.New("unused")
}

const binDir = "/bin"

func newSource(id, repo string, mod func(*config.SourceConfig)) *source.Source {
	c := config.SourceConfig{Github: repo, Bin: config.BinSpec{id: id}}
	c.InstallDir = "/pkgs/" + id
	c.BinDir = binDir
	if mod != nil {
		mod(&c)
	}
	return &source.Source{
		ID:         id,
		Key:        id,
		InstallDir: c.InstallDir,
		BinDir:     c.BinDir,
		Config:     c,
		Fetcher:    fakeFetcher{origin: "github:" + repo},
	}
}

// installed returns the state entry an install of src at tag would have recorded.
func installed(src *source.Source, tag string) state.Entry {
	links := map[string]string{}
	for _, l := range src.LinkPaths("tar.gz") {
		links[l] = src.InstallDir + "/target"
	}
	return state.Entry{
		Spec: state.Spec{
			Origin:     src.Fetcher.Origin(),
			Format:     "tar.gz",
			Tag:        tag,
			Asset:      src.ID + "-" + tag + ".tar.gz",
			InstallDir: src.InstallDir,
			Inputs:     src.Inputs(),
		},
		Links: links,
	}
}

func locked(src *source.Source, tag string) lock.Entry {
	return lock.Entry{Origin: src.Fetcher.Origin(), Tag: tag}
}

// resolver returns assets named like installed() does, and counts calls.
func resolver(calls *int) ResolveFunc {
	return func(_ context.Context, src *source.Source, v fetch.Version) (fetch.Artifact, error) {
		*calls++
		return fetch.Artifact{Version: v, Name: src.ID + "-" + v.Tag + ".tar.gz"}, nil
	}
}

func TestBuild(t *testing.T) {
	fzf := newSource("fzf", "junegunn/fzf", nil)

	tests := []struct {
		name         string
		src          *source.Source // nil: not configured
		lockTag      string
		entry        *state.Entry // nil: not installed
		wantKind     Kind
		wantNone     bool
		wantFields   []string
		wantResolves int
	}{
		{
			name:         "configured, not installed",
			src:          fzf,
			lockTag:      "v2",
			wantKind:     Install,
			wantResolves: 1,
		},
		{
			name:     "installed, not configured",
			entry:    ptr(installed(fzf, "v1")),
			wantKind: Remove,
		},
		{
			name:     "unchanged, no network",
			src:      fzf,
			lockTag:  "v1",
			entry:    ptr(installed(fzf, "v1")),
			wantNone: true,
		},
		{
			name:         "new locked version",
			src:          fzf,
			lockTag:      "v2",
			entry:        ptr(installed(fzf, "v1")),
			wantKind:     Upgrade,
			wantFields:   []string{"version", "asset"},
			wantResolves: 1,
		},
		{
			name:       "origin changed",
			src:        newSource("fzf", "someone/fzf", nil),
			lockTag:    "v1",
			entry:      ptr(installed(fzf, "v1")),
			wantKind:   Replace,
			wantFields: []string{"origin"},
		},
		{
			name:       "build changed",
			src:        newSource("fzf", "junegunn/fzf", func(c *config.SourceConfig) { c.Build = "make" }),
			lockTag:    "v1",
			entry:      ptr(installed(fzf, "v1")),
			wantKind:   Reinstall,
			wantFields: []string{"build"},
		},
		{
			name:       "install dir moved",
			src:        newSource("fzf", "junegunn/fzf", func(c *config.SourceConfig) { c.InstallDir = "/other/fzf" }),
			lockTag:    "v1",
			entry:      ptr(installed(fzf, "v1")),
			wantKind:   Modify,
			wantFields: []string{"install_dir"},
		},
		{
			name: "extra binary",
			src: newSource("fzf", "junegunn/fzf", func(c *config.SourceConfig) {
				c.Bin = config.BinSpec{"fzf": "fzf", "fzf-tmux": "bin/fzf-tmux"}
			}),
			lockTag:    "v1",
			entry:      ptr(installed(fzf, "v1")),
			wantKind:   Reinstall,
			wantFields: []string{"bin.fzf-tmux", "link"},
		},
		{
			name: "rename changed, same asset resolved",
			src: newSource("fzf", "junegunn/fzf", func(c *config.SourceConfig) {
				c.Rename = map[string]string{"amd64": "x86_64"}
			}),
			lockTag:      "v1",
			entry:        ptr(installed(fzf, "v1")),
			wantKind:     Modify,
			wantFields:   []string{"rename.amd64"},
			wantResolves: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Input{
				Sources: map[string]*source.Source{},
				Lock:    &lock.Lock{Sources: map[string]lock.Entry{}},
				State:   &state.State{Sources: map[string]state.Entry{}},
				Host:    host.Host{OS: "linux", Arch: "amd64"},
			}
			if tt.src != nil {
				in.Sources["fzf"] = tt.src
				in.Lock.Sources["fzf"] = locked(tt.src, tt.lockTag)
			}
			if tt.entry != nil {
				in.State.Sources["fzf"] = *tt.entry
			}
			calls := 0
			in.Resolve = resolver(&calls)

			actions, err := Build(context.Background(), []string{"fzf"}, in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if calls != tt.wantResolves {
				t.Errorf("resolve calls: got %d, want %d", calls, tt.wantResolves)
			}

			if tt.wantNone {
				if len(actions) != 0 {
					t.Fatalf("expected no actions, got %+v", actions)
				}
				return
			}
			if len(actions) != 1 {
				t.Fatalf("expected 1 action, got %d: %+v", len(actions), actions)
			}

			a := actions[0]
			if (a.Artifact != nil) != (tt.wantResolves > 0) {
				t.Errorf("artifact: got %+v, want set only when resolved", a.Artifact)
			}
			if a.Kind != tt.wantKind {
				t.Errorf("kind: got %s, want %s (changes: %+v)", a.Kind, tt.wantKind, a.Changes)
			}
			if tt.wantFields != nil {
				var fields []string
				for _, c := range a.Changes {
					fields = append(fields, c.Field)
				}
				if !slices.Equal(fields, tt.wantFields) {
					t.Errorf("changed fields: got %v, want %v", fields, tt.wantFields)
				}
			}
		})
	}
}

func TestBuildNotLocked(t *testing.T) {
	in := Input{
		Sources: map[string]*source.Source{"fzf": newSource("fzf", "junegunn/fzf", nil)},
		Lock:    &lock.Lock{Sources: map[string]lock.Entry{}},
		State:   &state.State{Sources: map[string]state.Entry{}},
	}
	if _, err := Build(context.Background(), []string{"fzf"}, in); err == nil {
		t.Fatal("expected an error for an unlocked source")
	}
}

func TestBuildSingleFile(t *testing.T) {
	tests := []struct {
		name    string
		bin     config.BinSpec
		wantErr bool
	}{
		{name: "one bin", bin: config.BinSpec{"tool": "tool"}},
		{name: "two bins", bin: config.BinSpec{"a": "a", "b": "b"}, wantErr: true},
		{name: "glob", bin: config.BinSpec{"tool": "*/tool"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := newSource("tool", "x/tool", func(c *config.SourceConfig) {
				c.Format = config.Binary
				c.Bin = tt.bin
			})
			in := Input{
				Sources: map[string]*source.Source{"tool": src},
				Lock:    &lock.Lock{Sources: map[string]lock.Entry{"tool": locked(src, "v1")}},
				State:   &state.State{Sources: map[string]state.Entry{}},
				Resolve: resolver(new(int)),
			}

			_, err := Build(context.Background(), []string{"tool"}, in)

			if (err != nil) != tt.wantErr {
				t.Errorf("error: got %v, want error %t", err, tt.wantErr)
			}
		})
	}
}

func TestBuildOrder(t *testing.T) {
	a := newSource("a", "x/a", nil)
	b := newSource("b", "x/b", nil)
	in := Input{
		Sources: map[string]*source.Source{"a": a, "b": b},
		Lock: &lock.Lock{Sources: map[string]lock.Entry{
			"a": locked(a, "v1"),
			"b": locked(b, "v2"),
		}},
		State: &state.State{Sources: map[string]state.Entry{
			"b":   installed(b, "v1"),
			"old": installed(newSource("old", "x/old", nil), "v1"),
		}},
	}
	calls := 0
	in.Resolve = resolver(&calls)

	actions, err := Build(context.Background(), []string{"a", "b", "old"}, in)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, a := range actions {
		got = append(got, a.Kind.String()+" "+a.ID)
	}
	want := []string{"remove old", "upgrade b", "install a"}
	if !slices.Equal(got, want) {
		t.Errorf("order: got %v, want %v", got, want)
	}
}

func TestCheckLinkOwners(t *testing.T) {
	// tools.rg is installed with link /bin/rg; lsp.rg now wants it.
	owned := state.Entry{Links: map[string]string{"/bin/rg": "/pkgs/tools/rg/rg"}}
	st := &state.State{Sources: map[string]state.Entry{"tools.rg": owned}}

	install := Action{Kind: Install, ID: "lsp.rg", Links: []string{"/bin/rg"}}

	t.Run("owner not in plan blocks", func(t *testing.T) {
		actions := []Action{install}
		checkLinkOwners(actions, st)
		if len(actions[0].Blocked) != 1 {
			t.Errorf("expected the install to be blocked, got %v", actions[0].Blocked)
		}
	})

	t.Run("owner removed in plan frees the link", func(t *testing.T) {
		actions := []Action{install, {Kind: Remove, ID: "tools.rg", Prev: &owned}}
		checkLinkOwners(actions, st)
		if len(actions[0].Blocked) != 0 {
			t.Errorf("expected no block, got %v", actions[0].Blocked)
		}
	})
}

func ptr[T any](v T) *T { return &v }
