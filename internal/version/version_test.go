package version

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	valid := []string{"", "1.x", "1.x.x", "v1.x", "1.2.x", "01.2.x", "1.2.3", "4.7.2-stable", "latest"}
	for _, s := range valid {
		if _, err := Parse(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}

	invalid := []string{"x", "x.x", "1.x.3", "a.x", "1.a.x", "1.2.3.x", "+1.x", "1.x.y.x"}
	for _, s := range invalid {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q: expected an error", s)
		}
	}
}

func TestTidy(t *testing.T) {
	tests := []struct {
		tag  string
		want string // empty means not semver
	}{
		{"v1.2.3", "v1.2.3"},
		{"1.2.3", "v1.2.3"},
		{"jq-1.7.1", "v1.7.1"},
		{"release-2.0", "v2.0.0"},
		{"0.14", "v0.14.0"},
		{"4.8-rc1", "v4.8.0-rc1"},
		{"4.7.2-stable", "v4.7.2"},
		{"4.7-stable", "v4.7.0"},
		{"1.0.0-Final", "v1.0.0"},
		{"2.0.0-release", "v2.0.0"},
		{"1.2.0-rc1", "v1.2.0-rc1"},
		{"1.2.3-1", "v1.2.3-1"},
		{"4.7.2_stable", ""},
		{"nightly", ""},
		{"1.2.3.4", ""},
	}

	for _, tt := range tests {
		got, ok := Tidy(tt.tag)
		if tt.want == "" {
			if ok {
				t.Errorf("%s: got %s, want not semver", tt.tag, got)
			}
			continue
		}
		if !ok || got != tt.want {
			t.Errorf("%s: got %s, %t, want %s", tt.tag, got, ok, tt.want)
		}
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		constraint string
		tag        string
		want       bool
	}{
		{"", "anything", true},
		{"1.2.3", "v1.2.3", true},
		{"v1.2.3", "1.2.3", true},
		{"4.7.2-stable", "4.7.2-stable", true},
		{"1.2.3", "1.2.4", false},
		{"1.x", "v1.9.0", true},
		{"1.x.x", "jq-1.7.1", true},
		{"1.x", "v2.0.0", false},
		{"1.2.x", "v1.2.9", true},
		{"1.2.x", "v1.3.0", false},
		{"4.x", "4.7.2-stable", true},
		{"4.x", "4.8-rc1", false},
		{"1.x", "v1.2.0-rc1", false},
		{"1.x", "nightly", false},
	}

	for _, tt := range tests {
		c, err := Parse(tt.constraint)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Matches(tt.tag); got != tt.want {
			t.Errorf("%q matches %q: got %t, want %t", tt.constraint, tt.tag, got, tt.want)
		}
	}
}

func TestSelect(t *testing.T) {
	// newest first by date, like GitHub lists releases
	tags := []string{"3.6.3-stable", "4.8-rc1", "4.7.2-stable", "4.7.1-stable", "4.6-stable", "nightly"}

	tests := []struct {
		constraint string
		want       string
		wantErr    string
	}{
		{constraint: "", want: "3.6.3-stable"},
		{constraint: "4.x", want: "4.7.2-stable"},
		{constraint: "4.6.x", want: "4.6-stable"},
		{constraint: "4.7.1-stable", want: "4.7.1-stable"},
		{constraint: "5.x", wantErr: "none of 6 releases matches 5.x, 1 have tags that aren't semver"},
		{constraint: "4.7.3-stable", wantErr: "no release tagged"},
	}

	for _, tt := range tests {
		t.Run(tt.constraint, func(t *testing.T) {
			c, err := Parse(tt.constraint)
			if err != nil {
				t.Fatal(err)
			}

			got, err := c.Select(tags)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error: got %v, want %q", err, tt.wantErr)
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
