package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"slices"
	"strings"

	applier "github.com/sctlin/che/internal/apply"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/lock"
	planner "github.com/sctlin/che/internal/plan"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
	"github.com/sctlin/che/internal/version"
)

type Target struct {
	ID       string
	InConfig bool
	InState  bool
}

// resolveTargets maps args to IDs: exact config ID, exact state ID, then
// unambiguous last-segment match against config IDs.
// Unknown or ambiguous args are errors.
// Checks that all targets pass the requirements before returning them
func resolveTargets(args []string, conf map[string]*source.Source, st *state.State, all []string, reqFunc RequirementFunc) ([]Target, error) {
	var targets []Target
	if len(args) == 0 {
		// no args, we use the whole source file
		args = all
	}
	var errs []error
	seen := map[string]bool{}

	for _, arg := range args {
		id, err := resolveID(arg, conf, st.Sources)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[id] {
			continue
		}
		_, inConfig := conf[id]
		_, inState := st.Sources[id]
		targets = append(targets, Target{ID: id, InConfig: inConfig, InState: inState})
		seen[id] = true
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := checkRequirement(targets, reqFunc); err != nil {
		return nil, fmt.Errorf("failed to check requirements: %w", err)
	}
	return targets, errors.Join(errs...)
}

// resolveID matches arg in order: exact config ID, exact state ID, config ID suffix.
// State entries are only matched exactly, since targeting an orphan removes it
func resolveID(arg string, conf map[string]*source.Source, st map[string]state.Entry) (string, error) {
	if _, ok := conf[arg]; ok {
		return arg, nil
	}
	if _, ok := st[arg]; ok {
		return arg, nil
	}
	var found []string
	for id := range conf {
		if strings.HasSuffix(id, "."+arg) {
			found = append(found, id)
		}
	}
	slices.Sort(found)
	switch len(found) {
	case 0:
		return "", fmt.Errorf("unknown source %s", arg)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("ambiguous source %s matches: %s", arg, strings.Join(found, ", "))
	}
}

func checkRequirement(targets []Target, reqFunc func(Target) error) error {
	if reqFunc == nil {
		return nil // plan, apply: any resolved target is valid
	}
	var errs []error
	for _, target := range targets {
		if err := reqFunc(target); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type RequirementFunc func(Target) error
type RunFunc func(App, []string, RequirementFunc, map[string]any) error
type Cmd struct {
	// Action is the action that the Cmd represents
	Action          string
	RequirementFunc RequirementFunc
	RunFunc         RunFunc
	RegisterFlags   func() func() map[string]any
	// SourcesOptional lets the command run without a sources file.
	SourcesOptional bool
}

type ErrOrphan struct {
	t Target
}

func (e ErrOrphan) Error() string {
	return fmt.Sprintf(`source %s installed but not in source file, add it back or run "che apply %s" to uninstall it`, e.t.ID, e.t.ID)
}

func inConfigRequirement(t Target) error {
	var err error
	if !t.InConfig {
		err = ErrOrphan{t}
	}
	return err
}

var cmds = map[string]Cmd{
	"update":   updateCmd,
	"outdated": outdatedCmd,
	"plan":     planCmd,
	"apply":    applyCmd,
	"add":      addCmd,
	"rm":       rmCmd,
}

func getCmd(action string) (Cmd, error) {
	if cmd, ok := cmds[action]; ok {
		return cmd, nil
	}
	return Cmd{}, fmt.Errorf("unsupported action %s", action)
}

type sourceUpdateInfo struct {
	s    *source.Source
	prev lock.Entry
	next lock.Entry
}

// prints changes as follows:
// ID              LOCKED    WANTED    LATEST
// fzf             v0.67.0   v0.68.1   v0.68.1
// node            22.11.0   22.14.0   24.1.0     constraint ^22
// lsp.lua-ls      3.13.5    3.13.5    3.13.5
// WANTED: the newest version that satisfies the constraint, what update should write
// LATEST: the newest version overall, same as wanted if no constraint
func printUpdates(changes []sourceUpdateInfo) {
	var builder strings.Builder
	builder.WriteString("ID\t\tLOCKED\tWANTED\tLATEST\n")
	for _, change := range changes {
		// TODO: no constraint yet so we are displaying next twice
		_, err := fmt.Fprintf(&builder, "%s\t\t%s\t%s\t%s\n", change.s.ID, change.prev.Display(), change.next.Display(), change.next.Display())
		if err != nil {
			// handle
		}
	}
}

func printPendingUpgrades(targets []Target, lock *lock.Lock, state *state.State) {
	var pending []string
	for _, t := range targets {
		locked := lock.Sources[t.ID]
		installed, ok := state.Sources[t.ID]
		switch {
		case !ok:
			pending = append(pending, fmt.Sprintf("  %s: not installed, %s locked", t.ID, locked.Display()))
		case installed.Tag != locked.Tag || installed.Commit != locked.Commit:
			pending = append(pending, fmt.Sprintf("  %s: %s installed, %s locked", t.ID, installed.Display(), locked.Display()))
		}
	}
	if len(pending) > 0 {
		fmt.Printf("%d sources can be upgraded, run \"che apply\"\n", len(pending))
	}
	// TODO: if verbose print pending info
}
func flagAsType[V any](key string, flags map[string]any) (V, bool) {
	var v V
	value, ok := flags[key]
	if !ok {
		return v, ok
	}
	v, ok = value.(V)
	return v, ok
}
func asSet[K comparable, V any](m map[K]V) map[K]struct{} {
	ret := make(map[K]struct{}, len(m))
	for k := range m {
		ret[k] = struct{}{}
	}
	return ret
}
func update(app App, args []string, reqFunc RequirementFunc, flags map[string]any) error {
	sources := app.sources
	state := app.state
	lockFile := app.lock
	targets, err := resolveTargets(args, sources, state, slices.Sorted(maps.Keys(sources)), reqFunc)
	if err != nil {
		return fmt.Errorf("failed to resolve targets: %w", err)
	}
	// after checking the requirements all targets should be checkeable for updates
	var changes []string
	for _, target := range targets {
		source := sources[target.ID]
		prev := lockFile.Sources[target.ID]
		version, err := source.Latest(context.Background(), source.Config.Version)
		if err != nil {
			return fmt.Errorf("error while getting latest package for source %s: %w", target.ID, err)
		}
		next := source.LockEntry(version, prev)
		if next.Tag != prev.Tag || next.Commit != prev.Commit {
			changes = append(changes, fmt.Sprintf("  %s: %s -> %s", target.ID, prev.Display(), next.Display()))
		}
		lockFile.Sources[target.ID] = next
	}
	lockFile.Prune(asSet(sources))
	if len(changes) == 0 {
		fmt.Println("Lock is up to date")
	} else {
		fmt.Printf("The following sources have newer packages:\n%s\n", strings.Join(changes, "\n"))
	}
	printPendingUpgrades(targets, lockFile, state)
	return lockFile.Save(lockPath)
}

func union(conf map[string]*source.Source, st *state.State) iter.Seq[string] {
	ret := map[string]bool{}
	for k := range conf {
		ret[k] = true
	}
	for k := range st.Sources {
		ret[k] = true
	}
	return maps.Keys(ret)
}

// lockMissing fills lock entries for configured IDs that have none, whose
// origin no longer matches the config, or whose version no longer satisfies
// the constraint. Installed sources keep their installed version if it still
// fits; others get the latest that does. Returns the filled IDs.
func lockMissing(ctx context.Context, ids []string, app App) ([]string, error) {
	var filled []string
	var errs []error

	for _, id := range ids {
		src, ok := app.sources[id]
		if !ok {
			continue // orphan: removal needs no version
		}

		// validated with the config
		constraint, err := version.Parse(src.Config.Version)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}

		prev, locked := app.lock.Sources[id]
		origin := src.Fetcher.Origin()
		if locked && prev.Origin == origin && constraint.Matches(prev.Tag) {
			continue
		}

		var v fetch.Version
		if e, ok := app.state.Sources[id]; ok && e.Origin == origin && constraint.Matches(e.Tag) {
			v = fetch.Version{Tag: e.Tag, Commit: e.Commit}
		} else {
			latest, err := src.Latest(ctx, src.Config.Version)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
				continue
			}
			v = latest
		}

		app.lock.Sources[id] = src.LockEntry(v, prev)
		filled = append(filled, id)
	}
	return filled, errors.Join(errs...)
}

// resolveArtifact resolves the host's asset, preferring the lock's checksum
// over the one the release reports.
func resolveArtifact(lk *lock.Lock) planner.ResolveFunc {
	return func(ctx context.Context, src *source.Source, v fetch.Version) (fetch.Artifact, error) {
		a, err := src.Fetcher.Resolve(ctx, v)
		if err != nil {
			return fetch.Artifact{}, err
		}
		if sum, ok := lk.Sources[src.ID].Checksums[a.Name]; ok {
			a.SHA256 = sum
		}
		return a, nil
	}
}

type planResult struct {
	// filled are the IDs whose lock entries were filled in memory.
	filled  []string
	actions []planner.Action
	// buildErr holds the sources that failed to plan, actions are still valid.
	buildErr error
}

// buildPlan resolves targets, fills missing lock entries in memory and plans
// the targets. Shared by plan and apply.
func buildPlan(c context.Context, app App, args []string, reqFunc RequirementFunc) (planResult, error) {
	all := slices.Sorted(union(app.sources, app.state))
	targets, err := resolveTargets(args, app.sources, app.state, all, reqFunc)
	if err != nil {
		return planResult{}, fmt.Errorf("failed to resolve targets: %w", err)
	}
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.ID
	}

	filled, err := lockMissing(c, ids, app)
	if err != nil {
		return planResult{}, fmt.Errorf("failed to lock sources: %w", err)
	}

	actions, buildErr := planner.Build(c, ids, planner.Input{
		Sources: app.sources,
		Lock:    app.lock,
		State:   app.state,
		Host:    app.host,
		Resolve: resolveArtifact(app.lock),
	})
	if buildErr != nil {
		buildErr = fmt.Errorf("failed to plan some sources:\n%w", buildErr)
	}
	return planResult{filled: filled, actions: actions, buildErr: buildErr}, nil
}

func printPlanResult(r planResult, lk *lock.Lock) {
	for _, id := range r.filled {
		fmt.Printf("will lock %s at %s\n", id, lk.Sources[id].Display())
	}
	if len(r.filled) > 0 {
		fmt.Println()
	}
	printPlan(r.actions)
}

func plan(app App, args []string, reqFunc RequirementFunc, flags map[string]any) error {
	r, err := buildPlan(context.Background(), app, args, reqFunc)
	if err != nil {
		return err
	}

	printPlanResult(r, app.lock)
	return r.buildErr
}

func apply(app App, args []string, reqFunc RequirementFunc, flags map[string]any) error {
	yes, _ := flagAsType[bool]("yes", flags)
	c := context.Background()

	r, err := buildPlan(c, app, args, reqFunc)
	if err != nil {
		return err
	}

	printPlanResult(r, app.lock)
	if r.buildErr != nil {
		return fmt.Errorf("refusing to apply, %w", r.buildErr)
	}
	if n := countBlocked(r.actions); n > 0 {
		return fmt.Errorf("refusing to apply, %d actions are blocked", n)
	}

	// Entries of unconfigured sources are dropped, removals don't need them.
	pruned := app.lock.Prune(asSet(app.sources))

	if len(r.actions) == 0 {
		// nothing to install, but lock changes are still worth keeping
		if len(r.filled) > 0 || pruned > 0 {
			return app.lock.Save(lockPath)
		}
		return nil
	}

	if !yes {
		ok, err := confirm("\nApply these changes? [y/N] ")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Apply cancelled")
			return nil
		}
	}

	ex := &applier.Executor{
		Sources:   app.sources,
		Lock:      app.lock,
		State:     app.state,
		Resolve:   resolveArtifact(app.lock),
		SaveLock:  func() error { return app.lock.Save(lockPath) },
		SaveState: func() error { return app.state.Save(statePath) },
		Log: func(format string, args ...any) {
			fmt.Printf(format+"\n", args...)
		},
	}
	if err := ex.Run(c, r.actions); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}

	fmt.Println("Apply complete")
	return nil
}

func countBlocked(actions []planner.Action) int {
	n := 0
	for _, a := range actions {
		if len(a.Blocked) > 0 {
			n++
		}
	}
	return n
}

// confirm asks a yes/no question on stdin, which must be a terminal. Character
// devices such as /dev/null pass the check, so EOF is also refused.
func confirm(prompt string) (bool, error) {
	errNoAnswer := errors.New("no confirmation, pass -yes to skip it")

	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false, errNoAnswer
	}

	fmt.Print(prompt)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if errors.Is(err, io.EOF) {
		fmt.Println()
		return false, errNoAnswer
	}
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

var (
	updateCmd Cmd = Cmd{
		Action:          "update",
		RequirementFunc: inConfigRequirement,
		RunFunc:         update,
	}
	outdatedCmd Cmd = Cmd{
		Action:          "outdated",
		RequirementFunc: inConfigRequirement,
		RunFunc:         outdated,
	}
	planCmd Cmd = Cmd{
		Action:  "plan",
		RunFunc: plan,
	}
	applyCmd Cmd = Cmd{
		Action:        "apply",
		RunFunc:       apply,
		RegisterFlags: yesFlag("applies the plan without asking for confirmation"),
	}
	addCmd Cmd = Cmd{
		Action:          "add",
		RunFunc:         add,
		RegisterFlags:   registerAddFlags,
		SourcesOptional: true,
	}
	rmCmd Cmd = Cmd{
		Action:          "rm",
		RequirementFunc: inConfigRequirement,
		RunFunc:         rm,
		RegisterFlags:   yesFlag("removes the sources without asking for confirmation"),
	}
)

// yesFlag registers -yes, which skips confirmation.
func yesFlag(usage string) func() func() map[string]any {
	return func() func() map[string]any {
		yes := flag.Bool("yes", false, usage)
		return func() map[string]any {
			return map[string]any{"yes": *yes}
		}
	}
}
