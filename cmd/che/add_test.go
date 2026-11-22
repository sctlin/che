package main

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/lock"
	planner "github.com/sctlin/che/internal/plan"
	"github.com/sctlin/che/internal/state"
)

func TestMapFlag(t *testing.T) {
	tests := []struct {
		name    string
		path    bool
		sets    []string
		want    map[string]string
		wantErr bool
	}{
		{name: "key value", sets: []string{"amd64=x86_64", "arm64=aarch64"}, want: map[string]string{"amd64": "x86_64", "arm64": "aarch64"}},
		{name: "value with =", sets: []string{"A=b=c"}, want: map[string]string{"A": "b=c"}},
		{name: "path", path: true, sets: []string{"bin/zls"}, want: map[string]string{"zls": "bin/zls"}},
		{name: "path not allowed", sets: []string{"bin/zls"}, wantErr: true},
		{name: "duplicate", sets: []string{"a=1", "a=2"}, wantErr: true},
		{name: "empty value", sets: []string{"a="}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &mapFlag{path: tt.path}
			var err error
			for _, s := range tt.sets {
				if err = f.Set(s); err != nil {
					break
				}
			}

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected an error, got %v", f.m)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(f.m, tt.want) {
				t.Errorf("got %v, want %v", f.m, tt.want)
			}
		})
	}
}

const fzfFile = `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
`

// addEnv points sourcesPath at a temp file holding content, or none if empty,
// and stubs the upstream check with check.
func addEnv(t *testing.T, content string, check func() ([]planner.Action, error)) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "che", "sources.yaml")
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	prevPath, prevCheck := sourcesPath, checkAdded
	t.Cleanup(func() { sourcesPath, checkAdded = prevPath, prevCheck })
	sourcesPath = path
	checkAdded = func(context.Context, App, []byte, string) ([]planner.Action, error) { return check() }
	return path
}

func runAdd(args []string, noCheck bool) error {
	app := App{
		lock:  &lock.Lock{Sources: map[string]lock.Entry{}},
		state: &state.State{Sources: map[string]state.Entry{}},
	}
	flags := map[string]any{"conf": config.SourceConfig{}, "no-check": noCheck}
	return add(app, args, nil, flags)
}

func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAdd(t *testing.T) {
	install := func() ([]planner.Action, error) {
		return []planner.Action{{Kind: planner.Install, ID: "tools.rg", Desired: &state.Spec{Tag: "v1"}}}, nil
	}

	t.Run("appends to the file", func(t *testing.T) {
		path := addEnv(t, fzfFile, install)

		if err := runAdd([]string{"tools.rg", "github:BurntSushi/ripgrep"}, false); err != nil {
			t.Fatal(err)
		}

		want := fzfFile + "    rg:\n      github: \"BurntSushi/ripgrep\"\n"
		if got := read(t, path); got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("creates a missing file", func(t *testing.T) {
		path := addEnv(t, "", install)

		if err := runAdd([]string{"rg", "BurntSushi/ripgrep"}, false); err != nil {
			t.Fatal(err)
		}

		want := "sources:\n  rg:\n    github: \"BurntSushi/ripgrep\"\n"
		if got := read(t, path); got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("failed check writes nothing", func(t *testing.T) {
		path := addEnv(t, fzfFile, func() ([]planner.Action, error) {
			return nil, errors.New("several assets match")
		})

		if err := runAdd([]string{"tools.rg", "github:BurntSushi/ripgrep"}, false); err == nil {
			t.Fatal("expected an error")
		}
		if got := read(t, path); got != fzfFile {
			t.Errorf("file changed:\n%s", got)
		}
	})

	t.Run("no check skips it", func(t *testing.T) {
		path := addEnv(t, fzfFile, func() ([]planner.Action, error) {
			t.Error("check ran")
			return nil, nil
		})

		if err := runAdd([]string{"tools.rg", "github:BurntSushi/ripgrep"}, true); err != nil {
			t.Fatal(err)
		}
		if got := read(t, path); got == fzfFile {
			t.Error("file not written")
		}
	})

	t.Run("existing id writes nothing", func(t *testing.T) {
		path := addEnv(t, fzfFile, install)

		if err := runAdd([]string{"tools.fzf", "github:junegunn/fzf"}, false); err == nil {
			t.Fatal("expected an error")
		}
		if got := read(t, path); got != fzfFile {
			t.Errorf("file changed:\n%s", got)
		}
	})

	t.Run("wrong argument count", func(t *testing.T) {
		addEnv(t, fzfFile, install)

		if err := runAdd([]string{"tools.rg"}, false); err == nil {
			t.Fatal("expected an error")
		}
	})
}
