package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/ulikunitz/xz"
)

type entry struct {
	name string
	body string
	mode fs.FileMode // 0 means 0644
	dir  bool
	link string // symlink target
	hard string // hardlink target
}

// layout is a typical release archive: a versioned top-level dir.
var layout = []entry{
	{name: "pkg-1.0/", dir: true},
	{name: "pkg-1.0/bin/tool", body: "tool", mode: 0o755},
	{name: "pkg-1.0/README", body: "readme"},
	{name: "pkg-1.0/tool-link", link: "bin/tool"},
}

func perm(e entry) fs.FileMode {
	if e.mode == 0 {
		return 0o644
	}
	return e.mode
}

func tarball(t *testing.T, entries []entry) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: int64(perm(e)), Size: int64(len(e.body))}
		switch {
		case e.dir:
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case e.hard != "":
			h.Typeflag, h.Linkname = tar.TypeLink, e.hard
		default:
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}

		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body[:h.Size])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipball(t *testing.T, entries []entry) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name}
		body := e.body
		switch {
		case e.dir:
			h.SetMode(fs.ModeDir | 0o755)
		case e.link != "":
			h.SetMode(fs.ModeSymlink | 0o777)
			body = e.link
		default:
			h.SetMode(perm(e))
		}

		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	compress(t, w, b)
	return buf.Bytes()
}

func xzipped(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	compress(t, w, b)
	return buf.Bytes()
}

func compress(t *testing.T, w io.WriteCloser, b []byte) {
	t.Helper()

	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// stage writes the asset to a temp dir and returns it with an empty staging
// dir next to it.
func stage(t *testing.T, asset []byte) (file, dir string) {
	t.Helper()

	parent := t.TempDir()
	file = filepath.Join(parent, "asset")
	dir = filepath.Join(parent, "stage")
	if err := os.WriteFile(file, asset, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return file, dir
}

func assertFile(t *testing.T, path, body string, mode fs.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("%s mode: got %s, want %s", path, info.Mode().Perm(), mode)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("%s: got %q, want %q", path, got, body)
	}
}

func TestUnpackArchives(t *testing.T) {
	tests := []struct {
		format config.Format
		asset  func(*testing.T) []byte
	}{
		{config.TarGz, func(t *testing.T) []byte { return gzipped(t, tarball(t, layout)) }},
		{config.TarXz, func(t *testing.T) []byte { return xzipped(t, tarball(t, layout)) }},
		{config.Zip, func(t *testing.T) []byte { return zipball(t, layout) }},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			file, dir := stage(t, tt.asset(t))

			if err := Unpack(tt.format, file, dir, ""); err != nil {
				t.Fatal(err)
			}

			assertFile(t, filepath.Join(dir, "pkg-1.0/bin/tool"), "tool", 0o755)
			assertFile(t, filepath.Join(dir, "pkg-1.0/README"), "readme", 0o644)
			target, err := os.Readlink(filepath.Join(dir, "pkg-1.0/tool-link"))
			if err != nil {
				t.Fatal(err)
			}
			if target != "bin/tool" {
				t.Errorf("symlink target: got %s, want bin/tool", target)
			}
		})
	}
}

func TestUnpackHardlink(t *testing.T) {
	entries := append(layout, entry{name: "pkg-1.0/tool-hard", hard: "pkg-1.0/bin/tool"})
	file, dir := stage(t, gzipped(t, tarball(t, entries)))

	if err := Unpack(config.TarGz, file, dir, ""); err != nil {
		t.Fatal(err)
	}

	assertFile(t, filepath.Join(dir, "pkg-1.0/tool-hard"), "tool", 0o755)
}

func TestUnpackSingleFile(t *testing.T) {
	const body = "binary"

	tests := []struct {
		format config.Format
		asset  func(*testing.T) []byte
	}{
		{config.Gz, func(t *testing.T) []byte { return gzipped(t, []byte(body)) }},
		{config.Xz, func(t *testing.T) []byte { return xzipped(t, []byte(body)) }},
		{config.Binary, func(*testing.T) []byte { return []byte(body) }},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			file, dir := stage(t, tt.asset(t))

			if err := Unpack(tt.format, file, dir, "bin/tool"); err != nil {
				t.Fatal(err)
			}

			assertFile(t, filepath.Join(dir, "bin/tool"), body, 0o755)
		})
	}
}

func TestUnpackHostile(t *testing.T) {
	tests := []struct {
		name    string
		format  config.Format
		entries []entry
	}{
		{"parent path", config.TarGz, []entry{{name: "../evil", body: "x"}}},
		{"absolute path", config.TarGz, []entry{{name: "/evil", body: "x"}}},
		{"write through symlink", config.TarGz, []entry{
			{name: "esc", link: ".."},
			{name: "esc/evil", body: "x"},
		}},
		{"hardlink out", config.TarGz, []entry{{name: "evil", hard: "../asset"}}},
		{"zip parent path", config.Zip, []entry{{name: "../evil", body: "x"}}},
		{"zip write through symlink", config.Zip, []entry{
			{name: "esc", link: ".."},
			{name: "esc/evil", body: "x"},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset := zipball(t, tt.entries)
			if tt.format == config.TarGz {
				asset = gzipped(t, tarball(t, tt.entries))
			}
			file, dir := stage(t, asset)

			if err := Unpack(tt.format, file, dir, ""); err == nil {
				t.Error("expected an error")
			}

			if _, err := os.Lstat(filepath.Join(dir, "../evil")); !errors.Is(err, fs.ErrNotExist) {
				t.Error("evil was written outside the staging dir")
			}
		})
	}
}

func TestResolveBin(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	for _, f := range []string{"a/tool", "b/tool", "pkg-1.0/fzf", outside} {
		p := f
		if !filepath.IsAbs(f) {
			p = filepath.Join(dir, f)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"link": "pkg-1.0/fzf", "esc": outside} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	vars := map[string]string{"version": "1.0"}
	tests := []struct {
		pattern string
		want    string // empty means an error
	}{
		{"*/fzf", "pkg-1.0/fzf"},
		{"pkg-${version}/fzf", "pkg-1.0/fzf"},
		{"link", "link"},
		{"*/tool", ""},
		{"missing", ""},
		{"a", ""},
		{"esc", ""},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got, err := ResolveBin(dir, tt.pattern, vars)

			if tt.want == "" {
				if err == nil {
					t.Errorf("expected an error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}
