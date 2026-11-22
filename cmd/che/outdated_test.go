package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
)

// fakeFetcher returns latest[constraint] from Latest.
type fakeFetcher struct {
	latest map[string]string
}

func (fakeFetcher) Origin() string { return "fake" }

func (f fakeFetcher) Latest(_ context.Context, c fetch.Constraint) (fetch.Version, error) {
	tag, ok := f.latest[c.Version]
	if !ok {
		return fetch.Version{}, errors.New("unreachable")
	}
	return fetch.Version{Tag: tag}, nil
}

func (fakeFetcher) Resolve(context.Context, fetch.Version) (fetch.Artifact, error) {
	return fetch.Artifact{}, errors.New("unused")
}

func (fakeFetcher) Download(context.Context, fetch.Artifact, string) (string, error) {
	return "", errors.New("unused")
}

func TestOutdated(t *testing.T) {
	src := func(id, constraint string, latest map[string]string) *source.Source {
		return &source.Source{ID: id, Config: config.SourceConfig{Version: constraint}, Fetcher: fakeFetcher{latest}}
	}
	installed := func(tag string) state.Entry { return state.Entry{Spec: state.Spec{Tag: tag}} }

	app := App{
		sources: map[string]*source.Source{
			"fzf":     src("fzf", "", map[string]string{"": "v0.75.0"}),
			"node":    src("node", "^22", map[string]string{"^22": "22.14.0", "": "24.1.0"}),
			"lsp.zls": src("lsp.zls", "", map[string]string{"": "0.17.0"}),
			"rg":      src("rg", "", map[string]string{"": "14.1.0"}),
			"broken":  src("broken", "", nil),
		},
		lock: &lock.Lock{Sources: map[string]lock.Entry{
			"fzf":     {Tag: "v0.67.0"},
			"node":    {Tag: "22.11.0"},
			"lsp.zls": {Tag: "0.17.0"},
			"rg":      {Tag: "14.1.0"},
		}},
		state: &state.State{Sources: map[string]state.Entry{
			"fzf":  installed("v0.67.0"),
			"node": installed("22.11.0"),
			"rg":   installed("14.1.0"),
		}},
	}

	rows, err := outdatedRows(context.Background(), app, []string{"broken", "fzf", "lsp.zls", "node", "rg"})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("expected an error for broken, got %v", err)
	}

	var out strings.Builder
	printOutdated(&out, rows)

	// rg is current, so it's left out
	want := `ID        INSTALLED   LOCKED    WANTED    LATEST
fzf       v0.67.0     v0.67.0   v0.75.0   v0.75.0
lsp.zls   -           0.17.0    0.17.0    0.17.0
node      22.11.0     22.11.0   22.14.0   24.1.0    constraint ^22
`
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestOutdatedAllCurrent(t *testing.T) {
	var out strings.Builder
	printOutdated(&out, nil)

	if got := out.String(); got != "All sources are up to date\n" {
		t.Errorf("got %q", got)
	}
}
