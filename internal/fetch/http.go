package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/itchyny/gojq"
	"github.com/sctlin/che/internal/config"
	"github.com/sctlin/che/internal/version"
)

// HttpFetcher reads releases from a URL. The response, after the optional jq
// filter, must be a list of releases newest first, in httpRelease's shape. A
// single release is a list of one.
type HttpFetcher struct {
	BaseFetcher
	URL    string
	Jq     string
	Asset  string
	Format config.Format
	Rename map[string]string
}

type httpRelease struct {
	Tag    string      `json:"tag"`
	Assets []httpAsset `json:"assets"`
}

type httpAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// SHA256 is optional, downloads are verified against it when set.
	SHA256 string `json:"sha256"`
}

func (h *HttpFetcher) Origin() string {
	return h.URL
}

func (h *HttpFetcher) matcher() assetMatcher {
	return assetMatcher{Env: h.Env, Asset: h.Asset, Format: h.Format, Rename: h.Rename}
}

// Latest returns the first release without a constraint, ordering them is the
// jq filter's job, the tagged release for an exact one, and the highest
// matching release for a range.
func (h *HttpFetcher) Latest(ctx context.Context, c Constraint) (Version, error) {
	vc, err := version.Parse(c.Version)
	if err != nil {
		return Version{}, err
	}
	rs, err := h.releases(ctx)
	if err != nil {
		return Version{}, err
	}

	tags := make([]string, len(rs))
	for i, r := range rs {
		tags[i] = r.Tag
	}
	tag, err := vc.Select(tags)
	if err != nil {
		return Version{}, fmt.Errorf("%s: %w", h.URL, err)
	}
	return Version{Tag: tag}, nil
}

func (h *HttpFetcher) Resolve(ctx context.Context, v Version) (Artifact, error) {
	rs, err := h.releases(ctx)
	if err != nil {
		return Artifact{}, err
	}

	for _, r := range rs {
		if r.Tag != v.Tag {
			continue
		}
		cands := make([]candidate, len(r.Assets))
		for i, a := range r.Assets {
			cands[i] = candidate(a)
		}
		return h.matcher().pick(v, cands)
	}
	return Artifact{}, fmt.Errorf("release %s isn't listed at %s anymore, run che update", v.Tag, h.URL)
}

func (h *HttpFetcher) Download(ctx context.Context, a Artifact, dst string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if err != nil {
		return "", err
	}
	return download(h.Env.HTTP, req, dst, a.SHA256)
}

// releases fetches the URL, applies the jq filter and checks the result's shape.
func (h *HttpFetcher) releases(ctx context.Context) ([]httpRelease, error) {
	body, err := h.get(ctx)
	if err != nil {
		return nil, err
	}

	if h.Jq != "" {
		if body, err = runJq(ctx, h.Jq, body); err != nil {
			return nil, fmt.Errorf("jq: %w", err)
		}
	}

	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '{' {
		body = wrapList(trimmed)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var rs []httpRelease
	if err := dec.Decode(&rs); err != nil {
		return nil, fmt.Errorf("%s: expected a list of {tag, assets: [{name, url, sha256}]}: %w", h.URL, err)
	}

	if len(rs) == 0 {
		return nil, fmt.Errorf("%s lists no releases", h.URL)
	}
	for i, r := range rs {
		if r.Tag == "" {
			return nil, fmt.Errorf("%s: release %d has no tag", h.URL, i)
		}
	}
	return rs, nil
}

func (h *HttpFetcher) get(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", h.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.Env.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: got status %d", h.URL, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// runJq applies the filter to a JSON document. Several outputs, e.g. from
// `.[] | {...}`, are collected into a list.
func runJq(ctx context.Context, filter string, body []byte) ([]byte, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return nil, err
	}

	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("response isn't JSON: %w", err)
	}

	var outs []any
	iter := q.RunWithContext(ctx, doc)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			var halt *gojq.HaltError
			if errors.As(err, &halt) && halt.Value() == nil {
				break
			}
			return nil, err
		}
		outs = append(outs, v)
	}

	if len(outs) == 1 {
		return json.Marshal(outs[0])
	}
	return json.Marshal(outs)
}

// wrapList turns a single release object into a list of one.
func wrapList(obj []byte) []byte {
	return append(append([]byte("["), obj...), ']')
}
