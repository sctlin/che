package apply

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// link points path at target. The symlink is created under a temp name and
// renamed over path, so path always points at either the old or new target.
func link(path, target string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp := filepath.Join(dir, "."+filepath.Base(path)+".che-"+rand.Text()[:8])
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// release deletes the links in prev that keep doesn't have. Links another
// action in the plan acquires are skipped, that action overwrites them, so
// they never go missing. Links repointed outside che are left alone.
//
// TODO: handovers aren't transactional. When two sources swap links and the
// second commit fails, links and state are left inconsistent. An undo log for
// the commit phase would fix it.
func (r *run) release(prev, keep map[string]string) {
	for _, l := range slices.Sorted(maps.Keys(prev)) {
		if _, ok := keep[l]; ok || r.acquired[l] {
			continue
		}

		cur, err := os.Readlink(l)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			r.logf("left %s: %v", l, err)
		case cur != prev[l]:
			r.logf("left %s, it points to %s instead of %s", l, cur, prev[l])
		default:
			if err := os.Remove(l); err != nil {
				r.logf("left %s: %v", l, err)
			}
		}
	}
}
