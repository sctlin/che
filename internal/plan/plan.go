package plan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/host"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
)

// Kind values are in execution order: removes free links and dirs before
// anything else claims them.
type Kind int

const (
	Remove Kind = iota
	Replace
	Modify
	Reinstall
	Upgrade
	Install
)

func (k Kind) String() string {
	return [...]string{"remove", "replace", "modify", "reinstall", "upgrade", "install"}[k]
}

type Change struct {
	Field string
	From  string
	To    string
}

type Action struct {
	Kind    Kind
	ID      string
	Changes []Change
	// Desired is what state records after the action, nil for Remove.
	Desired *state.Spec
	// Links are the desired link paths, nil for Remove, packages and commands.
	Links []string
	// Prev is the installed entry, nil for Install.
	Prev *state.Entry
	// Artifact is the resolved release asset, nil when planning didn't need
	// one. Apply resolves it itself if the action still needs files.
	Artifact *fetch.Artifact
	// Blocked holds reasons apply must refuse this action.
	Blocked []string
}

type ResolveFunc func(ctx context.Context, src *source.Source, v fetch.Version) (fetch.Artifact, error)

type Input struct {
	Sources map[string]*source.Source
	Lock    *lock.Lock
	State   *state.State
	Host    host.Host
	Resolve ResolveFunc
}

// Build plans the given IDs. Every configured ID must have a lock entry.
// Errors are collected per ID, so the actions that could be planned are
// returned together with the error.
func Build(ctx context.Context, ids []string, in Input) ([]Action, error) {
	var actions []Action
	var errs []error

	for _, id := range ids {
		a, err := planOne(ctx, id, in)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		if a != nil {
			actions = append(actions, *a)
		}
	}

	checkLinkOwners(actions, in.State)

	slices.SortFunc(actions, func(a, b Action) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), strings.Compare(a.ID, b.ID))
	})
	return actions, errors.Join(errs...)
}

func planOne(ctx context.Context, id string, in Input) (*Action, error) {
	src, inConfig := in.Sources[id]
	prev, inState := in.State.Sources[id]

	if !inConfig {
		if !inState {
			return nil, errors.New("not configured or installed")
		}
		return &Action{Kind: Remove, ID: id, Prev: &prev}, nil
	}

	locked, ok := in.Lock.Sources[id]
	if !ok {
		return nil, errors.New("not locked")
	}
	v := fetch.Version{Tag: locked.Tag, Commit: locked.Commit}

	var prevPtr *state.Entry
	if inState {
		prevPtr = &prev
	}

	var art *fetch.Artifact
	if needsResolve(src, v, prevPtr) {
		a, err := in.Resolve(ctx, src, v)
		if err != nil {
			return nil, err
		}
		art = &a
	}

	desired, err := src.Spec(v, art, prevPtr, in.Host)
	if err != nil {
		return nil, err
	}
	if err := checkSingleFile(desired); err != nil {
		return nil, err
	}
	links := src.LinkPaths(desired.Format)

	if !inState {
		return &Action{Kind: Install, ID: id, Desired: &desired, Links: links, Artifact: art}, nil
	}

	cs := diff(desired, links, prev)
	if len(cs) == 0 {
		return nil, nil
	}
	return &Action{Kind: classify(cs), ID: id, Changes: cs, Desired: &desired, Links: links, Prev: &prev, Artifact: art}, nil
}

// checkSingleFile requires single-file formats to have exactly one bin
// without globs, since the file is saved at that path.
func checkSingleFile(d state.Spec) error {
	if d.Inputs.Command != "" || !config.SingleFile(config.Format(d.Format)) {
		return nil
	}

	if len(d.Inputs.Bin) != 1 {
		return fmt.Errorf("format %s installs a single file, bin must name exactly one path", d.Format)
	}
	for _, p := range d.Inputs.Bin {
		if strings.ContainsAny(p, "*?[") {
			return fmt.Errorf("format %s installs a single file, bin %s can't be a glob", d.Format, p)
		}
	}
	return nil
}

// needsResolve reports whether the asset could differ from the installed one.
func needsResolve(src *source.Source, v fetch.Version, prev *state.Entry) bool {
	if prev == nil || prev.Tag != v.Tag || prev.Commit != v.Commit {
		return true
	}
	in := src.Inputs()
	return in.AssetPattern != prev.Inputs.AssetPattern ||
		in.Format != prev.Inputs.Format ||
		!maps.Equal(in.Rename, prev.Inputs.Rename)
}

func diff(d state.Spec, links []string, e state.Entry) []Change {
	var cs []Change
	add := func(field, from, to string) {
		if from != to {
			cs = append(cs, Change{Field: field, From: from, To: to})
		}
	}
	addMap := func(field string, from, to map[string]string) {
		keys := slices.Sorted(maps.Keys(from))
		for k := range to {
			if _, ok := from[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			add(field+"."+k, from[k], to[k])
		}
	}

	add("origin", e.Origin, d.Origin)
	add("format", e.Format, d.Format)
	add("version", e.Display(), d.Display())
	add("asset", e.Asset, d.Asset)
	add("install_dir", e.InstallDir, d.InstallDir)
	add("asset_pattern", e.Inputs.AssetPattern, d.Inputs.AssetPattern)
	add("format_override", e.Inputs.Format, d.Inputs.Format)
	add("build", e.Inputs.Build, d.Inputs.Build)
	add("command", e.Inputs.Command, d.Inputs.Command)
	addMap("rename", e.Inputs.Rename, d.Inputs.Rename)
	addMap("bin", e.Inputs.Bin, d.Inputs.Bin)
	addMap("env", e.Inputs.Env, d.Inputs.Env)

	for _, l := range links {
		if _, ok := e.Links[l]; !ok {
			add("link", "", l)
		}
	}
	for _, l := range slices.Sorted(maps.Keys(e.Links)) {
		if !slices.Contains(links, l) {
			add("link", l, "")
		}
	}
	return cs
}

// strength maps a field to the action it requires. The asset pattern, format
// override and rename only matter through the asset and format they resolve
// to, which are compared on their own, so on their own they only need state
// rewritten.
var strength = map[string]Kind{
	"origin":          Replace,
	"format":          Replace,
	"version":         Upgrade,
	"asset":           Reinstall,
	"bin":             Reinstall,
	"build":           Reinstall,
	"command":         Reinstall,
	"env":             Reinstall,
	"install_dir":     Modify,
	"link":            Modify,
	"asset_pattern":   Modify,
	"format_override": Modify,
	"rename":          Modify,
}

// rank orders kinds by how much work they do, unlike Kind's execution order.
var rank = map[Kind]int{Modify: 0, Reinstall: 1, Upgrade: 2, Replace: 3}

func classify(cs []Change) Kind {
	k := Modify
	for _, c := range cs {
		field, _, _ := strings.Cut(c.Field, ".") // "env.CGO_ENABLED" -> "env"
		if fk, ok := strength[field]; ok && rank[fk] > rank[k] {
			k = fk
		}
	}
	return k
}

// checkLinkOwners blocks actions that create a link another source still
// owns. A link is free when this plan's action for its owner releases it.
func checkLinkOwners(actions []Action, st *state.State) {
	owner := map[string]string{}
	for id, e := range st.Sources {
		for l := range e.Links {
			owner[l] = id
		}
	}

	released := map[string]bool{}
	for _, a := range actions {
		if a.Prev == nil {
			continue
		}
		for l := range a.Prev.Links {
			if !slices.Contains(a.Links, l) { // Remove has no Links, so it releases all
				released[l] = true
			}
		}
	}

	for i, a := range actions {
		for _, l := range a.Links {
			if o, ok := owner[l]; ok && o != a.ID && !released[l] {
				actions[i].Blocked = append(actions[i].Blocked,
					fmt.Sprintf("link %s is owned by %s, include it in the plan", l, o))
			}
		}
	}
}
