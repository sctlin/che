package config_test

import (
	"testing"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/fetch"
)

func TestParseOrigin(t *testing.T) {
	tests := []struct {
		in      string
		want    config.SourceConfig
		wantErr bool
	}{
		{in: "github:junegunn/fzf", want: config.SourceConfig{Github: "junegunn/fzf"}},
		{in: "junegunn/fzf", want: config.SourceConfig{Github: "junegunn/fzf"}},
		{in: "https://github.com/junegunn/fzf", want: config.SourceConfig{Github: "junegunn/fzf"}},
		{in: "https://github.com/junegunn/fzf.git", want: config.SourceConfig{Github: "junegunn/fzf"}},
		{in: "https://github.com/junegunn/fzf/", want: config.SourceConfig{Github: "junegunn/fzf"}},
		{in: "gitlab:group/sub/repo", want: config.SourceConfig{Gitlab: "group/sub/repo"}},
		{in: "https://gitlab.com/group/repo", want: config.SourceConfig{Gitlab: "group/repo"}},
		{in: "git:https://example.com/x.git", want: config.SourceConfig{Git: "https://example.com/x.git"}},
		{in: "https://example.com/releases.json", want: config.SourceConfig{URL: "https://example.com/releases.json"}},

		{in: "github:junegunn", wantErr: true},
		{in: "https://github.com/junegunn/fzf/releases", wantErr: true},
		{in: "https://gitlab.com/group/repo/-/releases", wantErr: true},
		{in: "git:", wantErr: true},
		{in: "fzf", wantErr: true},
		{in: "ftp://example.com/x", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := config.ParseOrigin(tt.in)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Github != tt.want.Github || got.Gitlab != tt.want.Gitlab || got.Git != tt.want.Git || got.URL != tt.want.URL {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// ParseOrigin reverses the fetchers' Origin.
func TestParseOriginRoundTrip(t *testing.T) {
	for _, o := range []string{
		"github:junegunn/fzf",
		"gitlab:group/sub/repo",
		"git:https://example.com/x.git",
		"https://example.com/releases.json",
	} {
		c, err := config.ParseOrigin(o)
		if err != nil {
			t.Fatal(err)
		}
		f, err := fetch.NewFetcher(c, fetch.Env{})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.Origin(); got != o {
			t.Errorf("%s: round trip gave %s", o, got)
		}
	}
}
