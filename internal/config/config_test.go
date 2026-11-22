package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadBinNames(t *testing.T) {
	tests := []struct {
		name    string
		bin     string
		wantErr string // empty means valid
	}{
		{name: "link name", bin: `{myzls: zls}`},
		{name: "string path", bin: `bin/zls`},
		{name: "absolute link", bin: `{"/usr/bin/zls": zls}`, wantErr: "link name can't be a path"},
		{name: "nested link", bin: `{"sub/zls": zls}`, wantErr: "link name can't be a path"},
		{name: "parent link", bin: `{"..": zls}`, wantErr: "link name can't be a path"},
		{name: "absolute target", bin: `{zls: /zls}`, wantErr: "path must be relative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sources.yaml")
			content := "defaults:\n  install_dir: /pkgs\n  bin_dir: /bin\nsources:\n  zls:\n    github: zigtools/zls\n    bin: " + tt.bin + "\n"
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := Load(path)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("error: got %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseRoot(t *testing.T) {
	content := `defaults:
  install_dir: /pkgs
  bin_dir: /bin
sources:
  fzf:
    github: junegunn/fzf
  lsp:
    zls:
      github: zigtools/zls
`
	got, err := Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"fzf":     "/pkgs/fzf",
		"lsp.zls": "/pkgs/lsp/zls",
	}
	if len(got) != len(want) {
		t.Fatalf("got IDs %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for id, dir := range want {
		rs, ok := got[id]
		if !ok {
			t.Errorf("missing %s", id)
			continue
		}
		if rs.Config.InstallDir != dir {
			t.Errorf("%s install dir: got %s, want %s", id, rs.Config.InstallDir, dir)
		}
		if rs.Config.BinDir != "/bin" {
			t.Errorf("%s bin dir: got %s, want /bin", id, rs.Config.BinDir)
		}
	}
}

func TestParseRootErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "source outside sources",
			content: "fzf:\n  github: junegunn/fzf\n",
			wantErr: "unknown field",
		},
		{
			name:    "defaults under sources",
			content: "sources:\n  defaults:\n    bin_dir: /bin\n",
			wantErr: "defaults go at the top level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error: got %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseNoSources(t *testing.T) {
	got, err := Parse([]byte("defaults:\n  bin_dir: /bin\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no sources", got)
	}
}

func TestParseRenameInherited(t *testing.T) {
	content := `defaults:
  install_dir: /pkgs
  bin_dir: /bin
  rename: {amd64: x86_64}
sources:
  lsp:
    defaults:
      rename: {linux: Linux}
    zls:
      github: zigtools/zls
      rename: {arm64: aarch64}
`
	got, err := Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"amd64": "x86_64", "linux": "Linux", "arm64": "aarch64"}
	if r := got["lsp.zls"].Config.Rename; !maps.Equal(r, want) {
		t.Errorf("rename: got %v, want %v", r, want)
	}
}

func TestParseVersion(t *testing.T) {
	for _, tt := range []struct {
		version string
		wantErr bool
	}{
		{version: "1.x"},
		{version: "4.7.2-stable"},
		{version: "1.x.3", wantErr: true},
	} {
		content := "defaults:\n  install_dir: /pkgs\n  bin_dir: /bin\nsources:\n  zls:\n    github: zigtools/zls\n    version: \"" + tt.version + "\"\n"
		_, err := Parse([]byte(content))
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: got %v, want error %t", tt.version, err, tt.wantErr)
		}
	}
}
