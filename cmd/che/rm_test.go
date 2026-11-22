package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
)

const rmFile = `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
    rg:
      github: "BurntSushi/ripgrep"
  lsp:
    zls:
      github: "zigtools/zls"
`

// rmApp points sourcesPath at a temp file holding rmFile and returns an App
// configured from it.
func rmApp(t *testing.T) (App, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(path, []byte(rmFile), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := sourcesPath
	t.Cleanup(func() { sourcesPath = prev })
	sourcesPath = path

	rs, err := config.Parse([]byte(rmFile))
	if err != nil {
		t.Fatal(err)
	}
	sources, err := source.NewSources(rs, fetch.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return App{
		sources: sources,
		lock:    &lock.Lock{Sources: map[string]lock.Entry{}},
		state:   &state.State{Sources: map[string]state.Entry{}},
	}, path
}

func TestRm(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		yes     bool
		want    string // empty means an error and the file unchanged
		wantErr bool
	}{
		{
			name: "one source",
			args: []string{"tools.rg"},
			yes:  true,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
  lsp:
    zls:
      github: "zigtools/zls"
`,
		},
		{
			name: "suffix match and several sources",
			args: []string{"rg", "zls"},
			yes:  true,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
`,
		},
		{name: "not configured", args: []string{"tools.bat"}, yes: true, wantErr: true},
		{name: "no args", yes: true, wantErr: true},
		{name: "not confirmed", args: []string{"tools.rg"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, path := rmApp(t)
			noInput(t)

			err := rm(app, tt.args, inConfigRequirement, map[string]any{"yes": tt.yes})

			got := read(t, path)
			if tt.wantErr {
				if err == nil {
					t.Error("expected an error")
				}
				if got != rmFile {
					t.Errorf("file changed:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

// noInput gives the test an empty file as stdin, so confirm never waits for a
// terminal.
func noInput(t *testing.T) {
	t.Helper()

	f, err := os.Create(filepath.Join(t.TempDir(), "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	t.Cleanup(func() {
		os.Stdin = prev
		f.Close()
	})
	os.Stdin = f
}
