package apply

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
	"github.com/sctlin/che/internal/host"
	"github.com/sctlin/che/internal/lock"
	"github.com/sctlin/che/internal/plan"
	"github.com/sctlin/che/internal/source"
	"github.com/sctlin/che/internal/state"
)

// env is a config, lock and state on a temp dir, with a server for assets.
type env struct {
	t      *testing.T
	root   string
	srv    *httptest.Server
	assets map[string][]byte

	sources map[string]*source.Source
	lock    *lock.Lock
	state   *state.State

	resolves   int
	lockSaves  int
	stateSaves int
	// beforeCommit runs in SaveLock, between prepare and commit.
	beforeCommit func()
	logs         []string
}

func newEnv(t *testing.T) *env {
	e := &env{
		t:       t,
		root:    t.TempDir(),
		assets:  map[string][]byte{},
		sources: map[string]*source.Source{},
		lock:    &lock.Lock{Sources: map[string]lock.Entry{}},
		state:   &state.State{Sources: map[string]state.Entry{}},
	}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := e.assets[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func assetName(tag string) string { return "tool-" + tag + ".tar.gz" }

// release serves a tag's asset: pkg-<version>/bin/{tool,other}.
func (e *env) release(tag string) {
	version := strings.TrimPrefix(tag, "v")

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, name := range []string{"tool", "other"} {
		body := name + " " + tag
		h := &tar.Header{Name: "pkg-" + version + "/bin/" + name, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(h); err != nil {
			e.t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		e.t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		e.t.Fatal(err)
	}
	e.assets[assetName(tag)] = buf.Bytes()
}

func (e *env) installDir(id string) string { return filepath.Join(e.root, "pkgs", id) }

func (e *env) binDir() string { return filepath.Join(e.root, "bin") }

// add configures id, linking bin/<id> to the release's tool, and locks it at tag.
func (e *env) add(id, tag string, mod func(*config.SourceConfig)) {
	c := config.SourceConfig{Github: "x/" + id, Bin: config.BinSpec{id: "pkg-${version}/bin/tool"}}
	c.InstallDir = e.installDir(id)
	c.BinDir = e.binDir()
	if mod != nil {
		mod(&c)
	}

	src := &source.Source{
		ID:         id,
		Key:        id,
		InstallDir: c.InstallDir,
		BinDir:     c.BinDir,
		Config:     c,
		Fetcher:    &fetch.GithubFetcher{Env: fetch.Env{HTTP: e.srv.Client()}, Repo: c.Github},
	}
	e.sources[id] = src
	prev := e.lock.Sources[id]
	e.lock.Sources[id] = src.LockEntry(fetch.Version{Tag: tag}, prev)
}

func (e *env) drop(id string) {
	delete(e.sources, id)
	delete(e.lock.Sources, id)
}

func (e *env) resolve(_ context.Context, src *source.Source, v fetch.Version) (fetch.Artifact, error) {
	e.resolves++
	name := assetName(v.Tag)
	a := fetch.Artifact{
		Version: v,
		Name:    name,
		URL:     e.srv.URL + "/" + name,
		Vars:    map[string]string{"version": strings.TrimPrefix(v.Tag, "v")},
	}
	if sum, ok := e.lock.Sources[src.ID].Checksums[name]; ok {
		a.SHA256 = sum
	}
	return a, nil
}

// plan plans every configured and installed ID.
func (e *env) plan() []plan.Action {
	e.t.Helper()

	ids := slices.Collect(maps.Keys(e.sources))
	for id := range e.state.Sources {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)

	actions, err := plan.Build(context.Background(), ids, plan.Input{
		Sources: e.sources,
		Lock:    e.lock,
		State:   e.state,
		Host:    host.Host{OS: "linux", Arch: "amd64"},
		Resolve: e.resolve,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	for _, a := range actions {
		if len(a.Blocked) > 0 {
			e.t.Fatalf("%s blocked: %v", a.ID, a.Blocked)
		}
	}
	return actions
}

func (e *env) apply() error {
	e.t.Helper()

	ex := &Executor{
		Sources: e.sources,
		Lock:    e.lock,
		State:   e.state,
		Resolve: e.resolve,
		SaveLock: func() error {
			e.lockSaves++
			if e.beforeCommit != nil {
				e.beforeCommit()
			}
			return nil
		},
		SaveState: func() error {
			e.stateSaves++
			return nil
		},
		Log: func(format string, args ...any) {
			e.logs = append(e.logs, fmt.Sprintf(format, args...))
		},
	}
	return ex.Run(context.Background(), e.plan())
}

func (e *env) mustApply() {
	e.t.Helper()

	if err := e.apply(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) kinds() []string {
	var got []string
	for _, a := range e.plan() {
		got = append(got, a.Kind.String()+" "+a.ID)
	}
	return got
}

func (e *env) assertKinds(want ...string) {
	e.t.Helper()

	if got := e.kinds(); !slices.Equal(got, want) {
		e.t.Fatalf("plan: got %v, want %v", got, want)
	}
}

// assertLinked checks link resolves to a file with body.
func (e *env) assertLinked(link, body string) {
	e.t.Helper()

	got, err := os.ReadFile(link)
	if err != nil {
		e.t.Fatal(err)
	}
	if string(got) != body {
		e.t.Errorf("%s: got %q, want %q", link, got, body)
	}
}

func (e *env) assertMissing(path string) {
	e.t.Helper()

	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		e.t.Errorf("expected %s to be missing, got %v", path, err)
	}
}

// leftovers lists staging and set-aside dirs.
func (e *env) leftovers(suffix string) []string {
	e.t.Helper()

	matches, err := filepath.Glob(filepath.Join(e.root, "pkgs", "*"+suffix+"*"))
	if err != nil {
		e.t.Fatal(err)
	}
	return matches
}

func (e *env) assertClean() {
	e.t.Helper()

	if l := append(e.leftovers(".new-"), e.leftovers(".old-")...); len(l) > 0 {
		e.t.Errorf("leftover dirs: %v", l)
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestInstall(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.add("a", "v1", nil)
	e.assertKinds("install a")

	e.mustApply()

	link := filepath.Join(e.binDir(), "a")
	e.assertLinked(link, "tool v1")
	e.assertClean()

	entry := e.state.Sources["a"]
	if want := filepath.Join(e.installDir("a"), "pkg-1/bin/tool"); entry.Links[link] != want {
		t.Errorf("state link: got %s, want %s", entry.Links[link], want)
	}
	if entry.InstalledAt.IsZero() {
		t.Error("installed_at not set")
	}
	if got, want := e.lock.Sources["a"].Checksums[assetName("v1")], sum(e.assets[assetName("v1")]); got != want {
		t.Errorf("lock checksum: got %s, want %s", got, want)
	}
	if e.lockSaves != 1 || e.stateSaves != 1 {
		t.Errorf("saves: got lock %d, state %d, want 1 and 1", e.lockSaves, e.stateSaves)
	}
}

func TestUpgrade(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.release("v2")
	e.add("a", "v1", nil)
	e.mustApply()

	e.add("a", "v2", nil)
	e.assertKinds("upgrade a")
	e.mustApply()

	e.assertLinked(filepath.Join(e.binDir(), "a"), "tool v2")
	e.assertMissing(filepath.Join(e.installDir("a"), "pkg-1"))
	e.assertClean()
	if got := e.state.Sources["a"].Tag; got != "v2" {
		t.Errorf("state tag: got %s, want v2", got)
	}
}

func TestReinstallResolves(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.add("a", "v1", nil)
	e.mustApply()

	e.add("a", "v1", func(c *config.SourceConfig) {
		c.Bin["a-other"] = "pkg-${version}/bin/other"
	})
	e.assertKinds("reinstall a")
	before := e.resolves
	e.mustApply()

	// planning a bin change doesn't resolve, so the executor does
	if got := e.resolves - before; got != 1 {
		t.Errorf("resolves during apply: got %d, want 1", got)
	}
	e.assertLinked(filepath.Join(e.binDir(), "a"), "tool v1")
	e.assertLinked(filepath.Join(e.binDir(), "a-other"), "other v1")
	e.assertClean()
}

func TestModify(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.add("a", "v1", nil)
	e.mustApply()
	installedAt := e.state.Sources["a"].InstalledAt

	t.Run("bin dir moved", func(t *testing.T) {
		e.add("a", "v1", func(c *config.SourceConfig) { c.BinDir = filepath.Join(e.root, "bin2") })
		e.assertKinds("modify a")
		e.mustApply()

		e.assertLinked(filepath.Join(e.root, "bin2", "a"), "tool v1")
		e.assertMissing(filepath.Join(e.binDir(), "a"))
	})

	t.Run("install dir moved", func(t *testing.T) {
		moved := filepath.Join(e.root, "pkgs", "moved")
		e.add("a", "v1", func(c *config.SourceConfig) {
			c.BinDir = filepath.Join(e.root, "bin2")
			c.InstallDir = moved
		})
		e.assertKinds("modify a")
		e.mustApply()

		link := filepath.Join(e.root, "bin2", "a")
		e.assertLinked(link, "tool v1")
		e.assertMissing(e.installDir("a"))
		if got, want := e.state.Sources["a"].Links[link], filepath.Join(moved, "pkg-1/bin/tool"); got != want {
			t.Errorf("state link: got %s, want %s", got, want)
		}
	})

	if got := e.state.Sources["a"].InstalledAt; !got.Equal(installedAt) {
		t.Errorf("installed_at: got %s, want %s", got, installedAt)
	}
}

func TestReplace(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.add("a", "v1", nil)
	e.mustApply()

	e.add("a", "v1", func(c *config.SourceConfig) { c.Github = "y/a" })
	e.assertKinds("replace a")
	e.mustApply()

	e.assertLinked(filepath.Join(e.binDir(), "a"), "tool v1")
	e.assertClean()
	if got := e.state.Sources["a"].Origin; got != "github:y/a" {
		t.Errorf("state origin: got %s, want github:y/a", got)
	}
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.add("a", "v1", nil)
	e.mustApply()

	e.drop("a")
	e.assertKinds("remove a")
	e.mustApply()

	e.assertMissing(filepath.Join(e.binDir(), "a"))
	e.assertMissing(e.installDir("a"))
	e.assertClean()
	if _, ok := e.state.Sources["a"]; ok {
		t.Error("state entry not deleted")
	}
}

func TestRemoveKeepsRepointedLink(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.add("a", "v1", nil)
	e.mustApply()

	link := filepath.Join(e.binDir(), "a")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/elsewhere", link); err != nil {
		t.Fatal(err)
	}

	e.drop("a")
	e.mustApply()

	if got, err := os.Readlink(link); err != nil || got != "/elsewhere" {
		t.Errorf("link: got %s, %v, want /elsewhere", got, err)
	}
	if !slices.ContainsFunc(e.logs, func(l string) bool { return strings.HasPrefix(l, "left "+link) }) {
		t.Errorf("expected a warning about %s, got %v", link, e.logs)
	}
}

func TestLinkHandover(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.add("a", "v1", func(c *config.SourceConfig) { c.Bin = config.BinSpec{"tool": "pkg-${version}/bin/tool"} })
	e.mustApply()

	e.drop("a")
	e.add("b", "v1", func(c *config.SourceConfig) { c.Bin = config.BinSpec{"tool": "pkg-${version}/bin/other"} })
	e.assertKinds("remove a", "install b")
	e.mustApply()

	link := filepath.Join(e.binDir(), "tool")
	e.assertLinked(link, "other v1")
	if got, want := e.state.Sources["b"].Links[link], filepath.Join(e.installDir("b"), "pkg-1/bin/other"); got != want {
		t.Errorf("state link: got %s, want %s", got, want)
	}
}

func TestReleaseSkipsAcquired(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "tool")
	if err := os.Symlink("/target", link); err != nil {
		t.Fatal(err)
	}
	prev := map[string]string{link: "/target"}

	r := &run{Executor: &Executor{}, acquired: map[string]bool{link: true}}
	r.release(prev, nil)
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("acquired link was released: %v", err)
	}

	r.acquired = map[string]bool{}
	r.release(prev, nil)
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("link not released: %v", err)
	}
}

func TestPrepareFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *env)
	}{
		{"checksum mismatch", func(e *env) {
			entry := e.lock.Sources["a"]
			entry.Checksums = map[string]string{assetName("v1"): sum([]byte("other"))}
			e.lock.Sources["a"] = entry
		}},
		{"unmanaged file at link", func(e *env) {
			if err := os.MkdirAll(e.binDir(), 0o755); err != nil {
				e.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(e.binDir(), "a"), nil, 0o755); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"existing install dir", func(e *env) {
			if err := os.MkdirAll(e.installDir("a"), 0o755); err != nil {
				e.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(e.installDir("a"), "keep"), nil, 0o644); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"unsupported format", func(e *env) {
			e.add("a", "v1", func(c *config.SourceConfig) { c.Format = config.Rpm })
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.release("v1")
			e.add("a", "v1", nil)
			tt.setup(e)

			if err := e.apply(); err == nil {
				t.Fatal("expected an error")
			}

			if e.lockSaves != 0 || e.stateSaves != 0 {
				t.Errorf("saves: got lock %d, state %d, want none", e.lockSaves, e.stateSaves)
			}
			if len(e.state.Sources) != 0 {
				t.Errorf("state changed: %v", e.state.Sources)
			}
			e.assertClean()
		})
	}
}

func TestCommitFailureKeepsOldDirs(t *testing.T) {
	e := newEnv(t)
	e.release("v1")
	e.release("v2")
	e.add("a", "v1", nil)
	e.mustApply()

	e.add("a", "v2", nil)
	e.add("b", "v1", nil)
	// b's install dir appears after prepare checked it.
	e.beforeCommit = func() {
		if err := os.MkdirAll(e.installDir("b"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.installDir("b"), "x"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.assertKinds("upgrade a", "install b")

	if err := e.apply(); err == nil {
		t.Fatal("expected an error")
	}

	if got := e.state.Sources["a"].Tag; got != "v2" {
		t.Errorf("a: got tag %s, want v2", got)
	}
	if _, ok := e.state.Sources["b"]; ok {
		t.Error("b recorded in state")
	}
	if l := e.leftovers("a.old-"); len(l) != 1 {
		t.Errorf("a's old dir: got %v, want one kept", l)
	}
	if l := e.leftovers(".new-"); len(l) != 0 {
		t.Errorf("staging dirs left: %v", l)
	}
	if !slices.ContainsFunc(e.logs, func(l string) bool { return strings.HasPrefix(l, "kept for recovery") }) {
		t.Errorf("expected a recovery message, got %v", e.logs)
	}
}
