// Package apply runs a plan in three phases. Prepare downloads and stages
// everything without touching installs, commit moves it into place, cleanup
// deletes what was replaced.
package apply

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/install"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/plan"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
	"github.com/sctlin/che/internal/utils"
)

type Executor struct {
	Sources map[string]*source.Source
	Lock    *lock.Lock
	State   *state.State
	Resolve plan.ResolveFunc
	// SaveLock is called once, after prepare.
	SaveLock func() error
	// SaveState is called after each committed action.
	SaveState func() error
	Log       func(format string, args ...any)
}

func (e *Executor) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

// step is a prepared action.
type step struct {
	action plan.Action
	// staged is the dir unpacked next to the install dir, empty when nothing
	// was downloaded.
	staged string
	// links maps link paths to their targets in the final install dir.
	links map[string]string
}

type run struct {
	*Executor
	// tmp holds the downloads.
	tmp string
	// managed are the links state records, for any source.
	managed map[string]bool
	// acquired are the links the plan's actions create.
	acquired map[string]bool
	// released are the install dirs the plan's actions give up.
	released map[string]bool
}

// Run prepares every action, then commits them in plan order, then cleans up.
// When a commit fails, the dirs set aside so far are kept for recovery.
func (e *Executor) Run(ctx context.Context, actions []plan.Action) error {
	tmp, err := os.MkdirTemp("", "che-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	r := newRun(e, tmp, actions)

	steps, err := r.prepareAll(ctx, actions)
	if err != nil {
		return err
	}
	if err := e.SaveLock(); err != nil {
		discard(steps)
		return err
	}

	var trash []string
	for i, s := range steps {
		e.logf("%s %s", s.action.Kind, s.action.ID)
		t, err := r.commit(s)
		trash = append(trash, t...)
		if err != nil {
			discard(steps[i:])
			if len(trash) > 0 {
				e.logf("kept for recovery:\n  %s", strings.Join(trash, "\n  "))
			}
			return fmt.Errorf("%s: %w", s.action.ID, err)
		}
	}

	// TODO: emptied parent dirs, e.g. a group dir after its last source is
	// removed, are left behind. Walking up and os.Remove-ing empty parents,
	// bounded by the top-level install_dir, would clean them up. That needs
	// config.Load to expose the top-level install_dir.
	for _, d := range trash {
		if err := os.RemoveAll(d); err != nil {
			e.logf("failed to delete %s: %v", d, err)
		}
	}
	return nil
}

func newRun(e *Executor, tmp string, actions []plan.Action) *run {
	r := &run{
		Executor: e,
		tmp:      tmp,
		managed:  map[string]bool{},
		acquired: map[string]bool{},
		released: map[string]bool{},
	}
	for _, entry := range e.State.Sources {
		for l := range entry.Links {
			r.managed[l] = true
		}
	}

	for _, a := range actions {
		for _, l := range a.Links {
			r.acquired[l] = true
		}
		if a.Prev != nil && (a.Desired == nil || a.Prev.InstallDir != a.Desired.InstallDir) {
			r.released[a.Prev.InstallDir] = true
		}
	}
	return r
}

// discard deletes the staging dirs of steps that weren't committed. Committed
// ones were renamed away, so there is nothing left to delete.
func discard(steps []*step) {
	for _, s := range steps {
		if s.staged != "" {
			os.RemoveAll(s.staged)
		}
	}
}

// prepareAll prepares every action, collecting errors so they are reported
// together.
func (r *run) prepareAll(ctx context.Context, actions []plan.Action) ([]*step, error) {
	var steps []*step
	var errs []error
	for _, a := range actions {
		s, err := r.prepare(ctx, a)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.ID, err))
			continue
		}
		steps = append(steps, s)
	}

	if len(errs) > 0 {
		discard(steps)
		return nil, errors.Join(errs...)
	}
	return steps, nil
}

func (r *run) prepare(ctx context.Context, a plan.Action) (*step, error) {
	if a.Prev != nil && a.Prev.Package != nil {
		return nil, errors.New("package installs aren't supported yet")
	}
	s := &step{action: a}
	if a.Kind == plan.Remove {
		return s, nil
	}

	if err := supported(*a.Desired); err != nil {
		return nil, err
	}
	if err := r.checkLinks(a.Links); err != nil {
		return nil, err
	}
	if err := r.checkInstallDir(a); err != nil {
		return nil, err
	}

	if a.Kind == plan.Modify {
		links, err := modifyLinks(a)
		if err != nil {
			return nil, err
		}
		s.links = links
		return s, nil
	}

	if err := r.stage(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func supported(d state.Spec) error {
	switch {
	case d.Inputs.Command != "":
		return errors.New("command installs aren't supported yet")
	case d.Inputs.Build != "":
		return errors.New("builds aren't supported yet")
	}

	switch config.Format(d.Format) {
	case config.TarGz, config.TarXz, config.Zip, config.Gz, config.Xz, config.Binary:
		return nil
	}
	return fmt.Errorf("format %s isn't supported yet", d.Format)
}

// checkLinks refuses to overwrite paths che doesn't manage. Links another
// source manages were already checked by the plan.
func (r *run) checkLinks(links []string) error {
	for _, l := range links {
		_, err := os.Lstat(l)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case !r.managed[l]:
			return fmt.Errorf("%s exists and isn't managed by che", l)
		}
	}
	return nil
}

// checkInstallDir refuses to install over an existing dir, unless the action
// itself or another one in the plan gives it up.
func (r *run) checkInstallDir(a plan.Action) error {
	dir := a.Desired.InstallDir
	if (a.Prev != nil && a.Prev.InstallDir == dir) || r.released[dir] {
		return nil
	}

	_, err := os.Lstat(dir)
	switch {
	case err == nil:
		return fmt.Errorf("%s already exists", dir)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return err
}

// modifyLinks keeps the installed targets, matched by link name, moved to the
// new install dir if it changed.
func modifyLinks(a plan.Action) (map[string]string, error) {
	byName := map[string]string{}
	for l, target := range a.Prev.Links {
		byName[filepath.Base(l)] = target
	}

	links := map[string]string{}
	for _, l := range a.Links {
		target, ok := byName[filepath.Base(l)]
		if !ok {
			return nil, fmt.Errorf("no installed target for link %s", l)
		}
		if rel, err := filepath.Rel(a.Prev.InstallDir, target); err == nil && filepath.IsLocal(rel) {
			target = filepath.Join(a.Desired.InstallDir, rel)
		}
		links[l] = target
	}
	return links, nil
}

// stage downloads the artifact, unpacks it next to the install dir and
// resolves the link targets.
func (r *run) stage(ctx context.Context, s *step) (err error) {
	a, d := s.action, s.action.Desired

	art, err := r.artifact(ctx, a)
	if err != nil {
		return err
	}

	r.logf("downloading %s", art.Name)
	file := filepath.Join(r.tmp, a.ID+"-"+art.Name)
	sum, err := r.Sources[a.ID].Fetcher.Download(ctx, art, file)
	if err != nil {
		return err
	}
	r.recordChecksum(a.ID, art.Name, sum)

	parent := filepath.Dir(d.InstallDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(parent, filepath.Base(d.InstallDir)+".new-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(staged)
		}
	}()

	if err := install.Unpack(config.Format(d.Format), file, staged, singleFileName(*d, art.Vars)); err != nil {
		return err
	}

	links := map[string]string{}
	for _, l := range a.Links {
		rel, err := install.ResolveBin(staged, d.Inputs.Bin[filepath.Base(l)], art.Vars)
		if err != nil {
			return err
		}
		if err := ensureExec(filepath.Join(staged, rel)); err != nil {
			return err
		}
		links[l] = filepath.Join(d.InstallDir, rel)
	}

	s.staged, s.links = staged, links
	return nil
}

// artifact returns the planned artifact, or resolves it when planning didn't
// need one, e.g. a reinstall for a bin change.
func (r *run) artifact(ctx context.Context, a plan.Action) (fetch.Artifact, error) {
	if a.Artifact != nil {
		return *a.Artifact, nil
	}

	v := fetch.Version{Tag: a.Desired.Tag, Commit: a.Desired.Commit}
	art, err := r.Resolve(ctx, r.Sources[a.ID], v)
	if err != nil {
		return fetch.Artifact{}, err
	}
	if art.Name != a.Desired.Asset {
		return fetch.Artifact{}, fmt.Errorf("resolved asset %s, the plan expected %s, run plan again", art.Name, a.Desired.Asset)
	}
	return art, nil
}

// recordChecksum stores sum the first time an asset is downloaded. Later
// downloads are verified against it.
func (r *run) recordChecksum(id, asset, sum string) {
	entry := r.Lock.Sources[id]
	if _, ok := entry.Checksums[asset]; ok {
		return
	}
	if entry.Checksums == nil {
		entry.Checksums = map[string]string{}
	}
	entry.Checksums[asset] = sum
	r.Lock.Sources[id] = entry
}

// singleFileName is where single-file formats are saved: the path of the only
// bin, which the plan guarantees.
func singleFileName(d state.Spec, vars map[string]string) string {
	if !config.SingleFile(config.Format(d.Format)) {
		return ""
	}
	for _, p := range d.Inputs.Bin {
		return utils.ExpandVars(p, vars)
	}
	return ""
}

// ensureExec sets the exec bits on files that have none, e.g. bins from zips
// made on Windows.
func ensureExec(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&0o111 != 0 {
		return nil
	}
	return os.Chmod(path, info.Mode().Perm()|0o111)
}

// commit moves a prepared step into place and records it in state. It returns
// the dirs set aside, which cleanup deletes.
func (r *run) commit(s *step) ([]string, error) {
	a := s.action
	if a.Kind == plan.Remove {
		return r.remove(a)
	}

	d := a.Desired
	var trash []string
	switch a.Kind {
	case plan.Modify:
		if a.Prev.InstallDir != d.InstallDir {
			if err := moveDir(a.Prev.InstallDir, d.InstallDir); err != nil {
				return nil, err
			}
		}
	default: // Install, Upgrade, Reinstall, Replace
		old, err := swap(a.Prev, s.staged, d.InstallDir)
		if old != "" {
			trash = append(trash, old)
		}
		if err != nil {
			return trash, err
		}
	}

	for _, l := range slices.Sorted(maps.Keys(s.links)) {
		if err := link(l, s.links[l]); err != nil {
			return trash, err
		}
	}
	if a.Prev != nil {
		r.release(a.Prev.Links, s.links)
	}

	entry := state.Entry{Spec: *d, Links: s.links, InstalledAt: time.Now()}
	if a.Kind == plan.Modify {
		entry.InstalledAt = a.Prev.InstalledAt
	}
	r.State.Sources[a.ID] = entry
	return trash, r.SaveState()
}

func (r *run) remove(a plan.Action) ([]string, error) {
	var trash []string
	if a.Prev.Inputs.Command != "" {
		r.logf("%s was installed by a custom command, its files must be removed manually", a.ID)
	} else {
		r.release(a.Prev.Links, nil)
		old, err := setAside(a.Prev.InstallDir)
		if err != nil {
			return nil, err
		}
		if old != "" {
			trash = append(trash, old)
		}
	}

	delete(r.State.Sources, a.ID)
	return trash, r.SaveState()
}

// swap sets the previous install aside and renames the staged dir into place.
// It returns the set-aside dir, which is restored if the rename fails.
func swap(prev *state.Entry, staged, dir string) (string, error) {
	var old string
	if prev != nil && prev.Inputs.Command == "" {
		var err error
		if old, err = setAside(prev.InstallDir); err != nil {
			return "", err
		}
	}

	if err := os.Rename(staged, dir); err != nil {
		if old == "" {
			return "", err
		}
		if rerr := os.Rename(old, prev.InstallDir); rerr != nil {
			return old, errors.Join(err, rerr)
		}
		return "", err
	}
	return old, nil
}

// setAside renames dir to dir.old-XXXXXXXX next to it. os.Rename refuses to
// replace an existing dir, so a name collision fails instead of clobbering. A
// missing dir is nothing to set aside.
func setAside(dir string) (string, error) {
	_, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	old := dir + ".old-" + rand.Text()[:8]
	if err := os.Rename(dir, old); err != nil {
		return "", err
	}
	return old, nil
}

func moveDir(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s already exists", to)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}
