package main

import (
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	planner "github.com/sctlin/che/internal/plan"
	"github.com/sctlin/che/internal/state"
)

func TestLinkTarget(t *testing.T) {
	desired := &state.Spec{
		InstallDir: "/pkgs/godot",
		Inputs:     state.Inputs{Bin: config.BinSpec{"godot": "Godot_v${tag}_linux.${arch}", "rg": "*/rg"}},
	}
	vars := map[string]string{"tag": "4.7.2-stable", "arch": "x86_64"}

	tests := []struct {
		name   string
		action planner.Action
		link   string
		want   string
	}{
		{
			name:   "resolved release expands variables",
			action: planner.Action{Kind: planner.Install, Desired: desired, Artifact: &fetch.Artifact{Vars: vars}},
			link:   "/bin/godot",
			want:   "/pkgs/godot/Godot_v4.7.2-stable_linux.x86_64",
		},
		{
			name:   "globs stay",
			action: planner.Action{Kind: planner.Install, Desired: desired, Artifact: &fetch.Artifact{Vars: vars}},
			link:   "/bin/rg",
			want:   "/pkgs/godot/*/rg",
		},
		{
			name:   "unresolved release keeps variables",
			action: planner.Action{Kind: planner.Reinstall, Desired: desired},
			link:   "/bin/godot",
			want:   "/pkgs/godot/Godot_v${tag}_linux.${arch}",
		},
		{
			name: "modify keeps the installed target, moved",
			action: planner.Action{
				Kind:    planner.Modify,
				Desired: desired,
				Prev: &state.Entry{
					Spec:  state.Spec{InstallDir: "/old/godot"},
					Links: map[string]string{"/oldbin/godot": "/old/godot/Godot_v4.7.2-stable_linux.x86_64"},
				},
			},
			link: "/bin/godot",
			want: "/pkgs/godot/Godot_v4.7.2-stable_linux.x86_64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkTarget(tt.action, tt.link); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
