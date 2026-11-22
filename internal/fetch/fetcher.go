package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/host"
	"github.com/sctlin/che/internal/version"
)

type Constraint struct {
	Version string
}

type Fetcher interface {
	Origin() string
	Latest(context.Context, Constraint) (Version, error)
	Resolve(context.Context, Version) (Artifact, error)
	// Download writes the artifact to dst and returns its sha256.
	Download(ctx context.Context, a Artifact, dst string) (string, error)
}

// Env holds what fetchers need from the outside world, so tests can inject fakes.
type Env struct {
	HTTP *http.Client
	Host host.Host
}

func (e Env) systemInfo(rename map[string]string) (os string, arch string) {
	os = e.Host.OS
	if renamed, ok := rename[os]; ok {
		os = renamed
	}
	arch = e.Host.Arch
	if renamed, ok := rename[arch]; ok {
		arch = renamed
	}
	return
}

type Asset struct {
	// Name is the name of the asset. Eg, "fzf-0.67.0-linux_amd64.tar.gz"
	Name string `json:"name"`
	// URL is the URL to download the asset. Eg. "https://github.com/junegunn/fzf/releases/download/v0.67.0/fzf-0.67.0-linux_amd64.tar.gz"
	URL string `json:"url"`
}

// Release represents a GitHub release or a generic release from an API
type Release struct {
	// Tag is the tag name of the release
	Tag string `json:"tag"`
	// Assets are the assets attached to the release
	Assets []Asset `json:"assets"`
	// Prerelease is true if the release is a prerelease
	Prerelease bool `json:"IsPrerelease"`
}

// Version is what the lock records
type Version struct {
	// Tag is the tag for release sources and git tags
	Tag string
	// Commit is the commit hash for git sources
	Commit string
}

// Artifact represents the remote artifact that was fetched throught the fetcher.Fetch method
type Artifact struct {
	Version Version
	// Name is the asset file name, emtpy for git
	Name string
	// URL is the download URL or clone URL for git
	URL string
	// SHA256 is the expected hash from the lock, empty if unknown
	SHA256 string
	// Vars contains the os, arch, tag, version fro expanding bin paths
	Vars map[string]string
}

func NewFetcher(c config.SourceConfig, env Env) (Fetcher, error) {
	var fetchers []Fetcher
	if c.Github != "" {
		fetchers = append(fetchers, &GithubFetcher{
			Env:    env,
			Repo:   c.Github,
			Asset:  c.Asset,
			Format: c.Format,
			Rename: c.Rename,
		})
	}
	if c.Gitlab != "" {
		fetchers = append(fetchers, &GitlabFetcher{
			Env:    env,
			Repo:   c.Gitlab,
			Asset:  c.Asset,
			Rename: c.Rename,
		})
	}
	if c.Git != "" {
		fetchers = append(fetchers, &GitFetcher{
			Env:  env,
			Repo: c.Git,
		})
	}
	if c.URL != "" {
		fetchers = append(fetchers, &HttpFetcher{
			Env:    env,
			URL:    c.URL,
			Jq:     c.Jq,
			Asset:  c.Asset,
			Format: c.Format,
			Rename: c.Rename,
		})
	}
	if len(fetchers) != 1 {
		return nil, fmt.Errorf("fetcher expected exactly one of github/gitlab/git/url")
	}
	return fetchers[0], nil
}

type BaseFetcher struct {
	Env Env
}
type GithubFetcher struct {
	BaseFetcher
	Repo   string
	Asset  string
	Format config.Format
	Rename map[string]string
}

func (g *GithubFetcher) matcher() assetMatcher {
	return assetMatcher{Env: g.Env, Asset: g.Asset, Format: g.Format, Rename: g.Rename}
}

func (g *GithubFetcher) Origin() string {
	return "github:" + g.Repo
}

// githubRelease is the part of GitHub's release object che reads.
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"` // "sha256:<hex>", empty for older assets
	} `json:"assets"`
}

// maxReleasePages bounds listing releases for a range, 100 per page.
const maxReleasePages = 10

// Latest returns GitHub's latest release without a constraint, the tagged
// release for an exact one, and the highest matching release for a range.
func (g *GithubFetcher) Latest(ctx context.Context, c Constraint) (Version, error) {
	vc, err := version.Parse(c.Version)
	if err != nil {
		return Version{}, err
	}

	switch {
	case vc.Exact():
		// the tag is tried as written, then with or without a leading "v"
		alt := "v" + c.Version
		if strings.HasPrefix(c.Version, "v") {
			alt = strings.TrimPrefix(c.Version, "v")
		}
		for _, tag := range []string{c.Version, alt} {
			var r githubRelease
			found, err := g.get(ctx, "/releases/tags/"+tag, &r)
			if err != nil {
				return Version{}, err
			}
			if found {
				return Version{Tag: r.TagName}, nil
			}
		}
		return Version{}, fmt.Errorf("%s has no release tagged %s", g.Repo, c.Version)

	case vc.Range():
		tags, err := g.releaseTags(ctx)
		if err != nil {
			return Version{}, err
		}
		tag, err := vc.Select(tags)
		if err != nil {
			return Version{}, fmt.Errorf("%s: %w", g.Repo, err)
		}
		return Version{Tag: tag}, nil
	}

	var r githubRelease
	found, err := g.get(ctx, "/releases/latest", &r)
	if err != nil {
		return Version{}, err
	}
	if !found {
		return Version{}, fmt.Errorf("%s has no releases", g.Repo)
	}
	return Version{Tag: r.TagName}, nil
}

// releaseTags lists the tags of releases, skipping drafts and prereleases.
func (g *GithubFetcher) releaseTags(ctx context.Context) ([]string, error) {
	var tags []string
	for page := 1; page <= maxReleasePages; page++ {
		var rs []githubRelease
		found, err := g.get(ctx, fmt.Sprintf("/releases?per_page=100&page=%d", page), &rs)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("github repository %s not found", g.Repo)
		}

		for _, r := range rs {
			if !r.Draft && !r.Prerelease {
				tags = append(tags, r.TagName)
			}
		}
		if len(rs) < 100 {
			break
		}
	}
	return tags, nil
}

func (g *GithubFetcher) Resolve(ctx context.Context, v Version) (Artifact, error) {
	var r githubRelease
	found, err := g.get(ctx, "/releases/tags/"+v.Tag, &r)
	if err != nil {
		return Artifact{}, err
	}
	if !found {
		return Artifact{}, fmt.Errorf("%s has no release tagged %s", g.Repo, v.Tag)
	}
	if len(r.Assets) == 0 {
		return Artifact{}, fmt.Errorf("release with tag %s has no assets", v.Tag)
	}

	cands := make([]candidate, len(r.Assets))
	for i, a := range r.Assets {
		cands[i] = candidate{Name: a.Name, URL: a.BrowserDownloadURL, SHA256: strings.TrimPrefix(a.Digest, "sha256:")}
	}
	return g.matcher().pick(v, cands)
}

// get decodes the GitHub API response for a path of the repository into out.
// found is false on 404.
func (g *GithubFetcher) get(ctx context.Context, path string, out any) (found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+g.Repo+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Add("Accept", "application/vnd.github+json")
	authorize(req)

	resp, err := g.Env.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, json.NewDecoder(resp.Body).Decode(out)
	case http.StatusNotFound:
		return false, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return false, fmt.Errorf("github %s%s: got status %d: %s", g.Repo, path, resp.StatusCode, body)
}

// authorize adds the token from CHE_GITHUB_TOKEN or GITHUB_TOKEN, if set.
func authorize(req *http.Request) {
	token, ok := os.LookupEnv("CHE_GITHUB_TOKEN")
	if !ok {
		token, ok = os.LookupEnv("GITHUB_TOKEN")
	}
	if ok {
		req.Header.Add("Authorization", "Bearer "+token)
	}
}

// Download fetches a.URL, which is the asset's browser_download_url.
// TODO: private repos need the API asset URL with
// "Accept: application/octet-stream", browser_download_url ignores the token.
func (g *GithubFetcher) Download(ctx context.Context, a Artifact, dst string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if err != nil {
		return "", err
	}
	return download(g.Env.HTTP, req, dst, a.SHA256)
}

// download writes the response body to dst and returns its sha256. When want
// is set and doesn't match, dst is removed and an error returned.
func download(client *http.Client, req *http.Request, dst, want string) (sum string, err error) {
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: got status %d", req.URL, resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
	}()

	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", fmt.Errorf("downloading %s: %w", req.URL, err)
	}

	sum = hex.EncodeToString(h.Sum(nil))
	if want != "" && !strings.EqualFold(sum, want) {
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", req.URL, sum, want)
	}
	return sum, nil
}

type GitlabFetcher struct {
	BaseFetcher
	Repo   string
	Asset  string
	Rename map[string]string
}

func (g *GitlabFetcher) Origin() string {
	return "gitlab:" + g.Repo
}

func (g *GitlabFetcher) Latest(ctx context.Context, c Constraint) (Version, error) {
	return Version{}, errors.New("not implemented")
}

func (g *GitlabFetcher) Resolve(context.Context, Version) (Artifact, error) {
	return Artifact{}, errors.New("not implemented")
}

func (*GitlabFetcher) Download(context.Context, Artifact, string) (string, error) {
	return "", errors.New("not implemented")
}

type GitFetcher struct {
	BaseFetcher
	Repo string
}

func (g *GitFetcher) Origin() string {
	return "git:" + g.Repo
}
func (g *GitFetcher) Latest(ctx context.Context, c Constraint) (Version, error) {
	return Version{}, errors.New("not implemented")
}
func (g *GitFetcher) Resolve(context.Context, Version) (Artifact, error) {
	return Artifact{}, errors.New("not implemented")
}

func (*GitFetcher) Download(context.Context, Artifact, string) (string, error) {
	return "", errors.New("not implemented")
}
