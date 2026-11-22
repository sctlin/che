package main

import (
	"context"
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
)

func TestLockMissing(t *testing.T) {
	// fakeFetcher's Origin is "fake"; Latest answers per constraint.
	latest := map[string]string{"": "v2.1.0", "1.x": "v1.9.0"}

	tests := []struct {
		name       string
		constraint string
		locked     string // empty means no lock entry
		installed  string // empty means not installed
		want       string
		wantFilled bool
	}{
		{name: "locked and fits", constraint: "1.x", locked: "v1.2.0", want: "v1.2.0"},
		{name: "not locked, not installed", constraint: "1.x", want: "v1.9.0", wantFilled: true},
		{name: "not locked, installed", installed: "v2.0.0", want: "v2.0.0", wantFilled: true},
		{name: "constraint changed, installed fits", constraint: "1.x", locked: "v2.0.0", installed: "v1.5.0", want: "v1.5.0", wantFilled: true},
		{name: "constraint changed, installed doesn't fit", constraint: "1.x", locked: "v2.0.0", installed: "v2.0.0", want: "v1.9.0", wantFilled: true},
		{name: "pinned", constraint: "2.0.0", locked: "v2.1.0", installed: "v2.0.0", want: "v2.0.0", wantFilled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := App{
				sources: map[string]*source.Source{
					"tool": {ID: "tool", Config: config.SourceConfig{Version: tt.constraint}, Fetcher: fakeFetcher{latest}},
				},
				lock:  &lock.Lock{Sources: map[string]lock.Entry{}},
				state: &state.State{Sources: map[string]state.Entry{}},
			}
			if tt.locked != "" {
				app.lock.Sources["tool"] = lock.Entry{Origin: "fake", Tag: tt.locked}
			}
			if tt.installed != "" {
				app.state.Sources["tool"] = state.Entry{Spec: state.Spec{Origin: "fake", Tag: tt.installed}}
			}

			filled, err := lockMissing(context.Background(), []string{"tool"}, app)
			if err != nil {
				t.Fatal(err)
			}

			if got := app.lock.Sources["tool"].Tag; got != tt.want {
				t.Errorf("locked: got %s, want %s", got, tt.want)
			}
			if (len(filled) == 1) != tt.wantFilled {
				t.Errorf("filled: got %v, want filled %t", filled, tt.wantFilled)
			}
		})
	}
}
