package config

import (
	"strings"
	"testing"
)

var rg = SourceConfig{Github: "BurntSushi/ripgrep", Bin: BinSpec{"rg": "ripgrep-*/rg"}}

func TestAddSource(t *testing.T) {
	tests := []struct {
		name string
		in   string
		id   string
		conf SourceConfig
		want string
	}{
		{
			name: "existing group",
			id:   "tools.rg",
			conf: rg,
			in: `sources:
  # language servers
  lsp:
    zls:
      github: "zigtools/zls"
  tools:
    fzf:
      github: "junegunn/fzf"

defaults:
  bin_dir: "/bin"
`,
			want: `sources:
  # language servers
  lsp:
    zls:
      github: "zigtools/zls"
  tools:
    fzf:
      github: "junegunn/fzf"
    rg:
      github: "BurntSushi/ripgrep"
      bin:
        rg: "ripgrep-*/rg"

defaults:
  bin_dir: "/bin"
`,
		},
		{
			name: "new group under existing one",
			id:   "tools.cli.rg",
			conf: rg,
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
  lsp:
    zls:
      github: "zigtools/zls"
`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
    cli:
      rg:
        github: "BurntSushi/ripgrep"
        bin:
          rg: "ripgrep-*/rg"
  lsp:
    zls:
      github: "zigtools/zls"
`,
		},
		{
			name: "new group at the end of sources",
			id:   "editors.hx",
			conf: SourceConfig{Github: "helix-editor/helix"},
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
defaults:
  bin_dir: "/bin"
`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
  editors:
    hx:
      github: "helix-editor/helix"
defaults:
  bin_dir: "/bin"
`,
		},
		{
			name: "top-level source",
			id:   "rg",
			conf: rg,
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
  rg:
    github: "BurntSushi/ripgrep"
    bin:
      rg: "ripgrep-*/rg"
`,
		},
		{
			name: "no sources key",
			id:   "rg",
			conf: rg,
			in: `defaults:
  bin_dir: "/bin"

`,
			want: `defaults:
  bin_dir: "/bin"
sources:
  rg:
    github: "BurntSushi/ripgrep"
    bin:
      rg: "ripgrep-*/rg"

`,
		},
		{
			name: "empty file",
			id:   "rg",
			conf: rg,
			in:   ``,
			want: `sources:
  rg:
    github: "BurntSushi/ripgrep"
    bin:
      rg: "ripgrep-*/rg"
`,
		},
		{
			name: "empty sources",
			id:   "tools.rg",
			conf: rg,
			in: `sources:

defaults:
  bin_dir: "/bin"
`,
			want: `sources:
  tools:
    rg:
      github: "BurntSushi/ripgrep"
      bin:
        rg: "ripgrep-*/rg"

defaults:
  bin_dir: "/bin"
`,
		},
		{
			name: "no final newline",
			id:   "tools.rg",
			conf: SourceConfig{Github: "BurntSushi/ripgrep"},
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
    rg:
      github: "BurntSushi/ripgrep"
`,
		},
		{
			name: "four space indent",
			id:   "tools.cli.rg",
			conf: SourceConfig{Github: "BurntSushi/ripgrep"},
			in: `sources:
    tools:
        fzf:
            github: "junegunn/fzf"
`,
			want: `sources:
    tools:
        fzf:
            github: "junegunn/fzf"
        cli:
            rg:
                github: "BurntSushi/ripgrep"
`,
		},
		{
			name: "comments stay in place",
			id:   "tools.rg",
			conf: SourceConfig{Github: "BurntSushi/ripgrep"},
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"   # fuzzy finder
      # pinned on purpose

  # editors below
  editors:
    hx:
      github: "helix-editor/helix"
`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"   # fuzzy finder
      # pinned on purpose
    rg:
      github: "BurntSushi/ripgrep"

  # editors below
  editors:
    hx:
      github: "helix-editor/helix"
`,
		},
		{
			name: "crlf line endings",
			id:   "tools.rg",
			conf: SourceConfig{Github: "BurntSushi/ripgrep"},
			in:   "sources:\r\n  tools:\r\n    fzf:\r\n      github: \"junegunn/fzf\"\r\n",
			want: "sources:\r\n  tools:\r\n    fzf:\r\n      github: \"junegunn/fzf\"\r\n    rg:\r\n      github: \"BurntSushi/ripgrep\"\r\n",
		},
		{
			name: "quoting",
			id:   "lsp.zls",
			conf: SourceConfig{
				Defaults: Defaults{
					Rename: map[string]string{"amd64": "x86_64"},
					Env:    map[string]string{"yes": "on", "A": "has \"quotes\" # and: colons"},
				},
				Github:  "zigtools/zls",
				Version: "0.10",
				Bin:     BinSpec{"zls": "*/zls"},
			},
			in: "sources:\n",
			want: `sources:
  lsp:
    zls:
      github: "zigtools/zls"
      version: "0.10"
      rename:
        amd64: "x86_64"
      bin:
        zls: "*/zls"
      env:
        A: "has \"quotes\" # and: colons"
        "yes": "on"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AddSource([]byte(tt.in), tt.id, tt.conf)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestAddSourceErrors(t *testing.T) {
	in := `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
  flow: {hx: {github: "helix-editor/helix"}}
`
	tests := []struct {
		name    string
		id      string
		conf    SourceConfig
		wantErr string
	}{
		{"existing source", "tools.fzf", rg, "tools.fzf already exists"},
		{"existing group", "tools", rg, "tools already exists"},
		{"source as group", "tools.fzf.x", rg, "tools.fzf is a source"},
		{"defaults segment", "tools.defaults", rg, "invalid source ID"},
		{"empty segment", "tools..rg", rg, "invalid source ID"},
		{"flow style group", "flow.rg", rg, "flow style"},
		{"duplicate link", "tools.fzf2", SourceConfig{Github: "x/fzf", Bin: BinSpec{"fzf": "fzf"}}, "linked by"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AddSource([]byte(in), tt.id, tt.conf)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error: got %v, want %q", err, tt.wantErr)
			}
		})
	}
}
