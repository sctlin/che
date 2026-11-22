package config

import (
	"strings"
	"testing"
)

func TestRemoveSource(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		id          string
		want        string
		wantRemoved string
	}{
		{
			name: "middle of a group",
			id:   "tools.fzf",
			in: `sources:
  tools:
    bat:
      github: "sharkdp/bat"
    fzf:
      github: "junegunn/fzf"
      bin:
        fzf: "fzf"
    rg:
      github: "BurntSushi/ripgrep"
`,
			want: `sources:
  tools:
    bat:
      github: "sharkdp/bat"
    rg:
      github: "BurntSushi/ripgrep"
`,
			wantRemoved: `    fzf:
      github: "junegunn/fzf"
      bin:
        fzf: "fzf"`,
		},
		{
			name: "end of a group",
			id:   "tools.rg",
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
    rg:
      github: "BurntSushi/ripgrep"

  lsp:
    zls:
      github: "zigtools/zls"
`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"

  lsp:
    zls:
      github: "zigtools/zls"
`,
			wantRemoved: `    rg:
      github: "BurntSushi/ripgrep"`,
		},
		{
			name: "comments",
			id:   "tools.rg",
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
      # pinned, stays with fzf
    # ripgrep, goes with rg
    rg:
      github: "BurntSushi/ripgrep"   # inline
  # language servers, stays with lsp
  lsp:
    zls:
      github: "zigtools/zls"
`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
      # pinned, stays with fzf
  # language servers, stays with lsp
  lsp:
    zls:
      github: "zigtools/zls"
`,
			wantRemoved: `    # ripgrep, goes with rg
    rg:
      github: "BurntSushi/ripgrep"   # inline`,
		},
		{
			name: "emptied groups are removed",
			id:   "tools.cli.rg",
			in: `sources:
  tools:
    cli:
      rg:
        github: "BurntSushi/ripgrep"
  lsp:
    zls:
      github: "zigtools/zls"
`,
			want: `sources:
  lsp:
    zls:
      github: "zigtools/zls"
`,
			wantRemoved: `  tools:
    cli:
      rg:
        github: "BurntSushi/ripgrep"`,
		},
		{
			name: "group with defaults stays",
			id:   "tools.rg",
			in: `sources:
  tools:
    defaults:
      bin_dir: "/opt/bin"
    rg:
      github: "BurntSushi/ripgrep"
`,
			want: `sources:
  tools:
    defaults:
      bin_dir: "/opt/bin"
`,
			wantRemoved: `    rg:
      github: "BurntSushi/ripgrep"`,
		},
		{
			name: "sources root stays",
			id:   "rg",
			in: `sources:
  rg:
    github: "BurntSushi/ripgrep"
defaults:
  bin_dir: "/bin"
`,
			want: `sources:
defaults:
  bin_dir: "/bin"
`,
			wantRemoved: `  rg:
    github: "BurntSushi/ripgrep"`,
		},
		{
			name: "last line without newline",
			id:   "tools.rg",
			in: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
    rg:
      github: "BurntSushi/ripgrep"`,
			want: `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
`,
			wantRemoved: `    rg:
      github: "BurntSushi/ripgrep"`,
		},
		{
			name:        "crlf line endings",
			id:          "tools.rg",
			in:          "sources:\r\n  tools:\r\n    fzf:\r\n      github: \"junegunn/fzf\"\r\n    rg:\r\n      github: \"BurntSushi/ripgrep\"\r\n",
			want:        "sources:\r\n  tools:\r\n    fzf:\r\n      github: \"junegunn/fzf\"\r\n",
			wantRemoved: "    rg:\r\n      github: \"BurntSushi/ripgrep\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed, err := RemoveSource([]byte(tt.in), tt.id)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
			if removed != tt.wantRemoved {
				t.Errorf("removed:\n%q\nwant:\n%q", removed, tt.wantRemoved)
			}
		})
	}
}

func TestRemoveSourceErrors(t *testing.T) {
	in := `sources:
  tools:
    fzf:
      github: "junegunn/fzf"
  flow: {hx: {github: "helix-editor/helix"}}
`
	tests := []struct {
		name    string
		id      string
		wantErr string
	}{
		{"unknown", "tools.rg", "isn't a configured source"},
		{"group", "tools", "isn't a configured source"},
		{"flow style group", "flow.hx", "flow style"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := RemoveSource([]byte(in), tt.id)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error: got %v, want %q", err, tt.wantErr)
			}
		})
	}
}
