package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// ParseOrigin returns the config of an origin, written like the fetchers'
// Origin(): github:owner/repo, gitlab:group/repo, git:<url>, or an http(s) URL
// for the url fetcher. GitHub and GitLab URLs, and owner/repo for GitHub, are
// accepted too.
func ParseOrigin(s string) (SourceConfig, error) {
	var c SourceConfig
	var err error

	switch {
	case strings.HasPrefix(s, "github:"):
		c.Github, err = repoPath(strings.TrimPrefix(s, "github:"), true)
	case strings.HasPrefix(s, "gitlab:"):
		c.Gitlab, err = repoPath(strings.TrimPrefix(s, "gitlab:"), false)
	case strings.HasPrefix(s, "git:"):
		c.Git = strings.TrimPrefix(s, "git:")
		if c.Git == "" {
			err = fmt.Errorf("origin %q has no repository", s)
		}
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		var u *url.URL
		if u, err = url.Parse(s); err != nil {
			break
		}
		switch u.Host {
		case "github.com":
			c.Github, err = repoPath(u.Path, true)
		case "gitlab.com":
			c.Gitlab, err = repoPath(u.Path, false)
		default:
			c.URL = s
		}
	case !strings.Contains(s, ":"):
		c.Github, err = repoPath(s, true)
	default:
		err = fmt.Errorf("unknown origin %q, use github:, gitlab:, git: or an http(s) URL", s)
	}

	if err != nil {
		return SourceConfig{}, err
	}
	return c, nil
}

// repoPath trims slashes and .git from a repository path. GitHub paths are
// exactly owner/repo, GitLab ones may have subgroups.
func repoPath(p string, github bool) (string, error) {
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	segs := strings.Split(p, "/")

	valid := len(segs) >= 2 && !slices.Contains(segs, "") && !slices.Contains(segs, "-")
	if github {
		valid = valid && len(segs) == 2
	}
	if !valid {
		if github {
			return "", fmt.Errorf("expected owner/repo, got %q", p)
		}
		return "", fmt.Errorf("expected group/repo, got %q", p)
	}
	return p, nil
}
