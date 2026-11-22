package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sctlin/che/internal/host"
)

// nodeIndex is shaped like nodejs.org/dist/index.json, newest first.
const nodeIndex = `[
	{"version": "v22.2.0", "files": ["linux-x64", "darwin-arm64"]},
	{"version": "v22.1.0", "files": ["linux-x64", "darwin-arm64"]}
]`

// nodeJq turns nodeIndex into releases, with assets on the test server.
const nodeJq = `[.[] | .version as $v | {tag: $v, assets: [.files[] | {
	name: "node-\($v)-\(.).tar.xz",
	url: "SERVER/dist/\($v)/node-\($v)-\(.).tar.xz"
}]}]`

func httpServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func nodeFetcher(t *testing.T) (*HttpFetcher, *httptest.Server) {
	srv := httpServer(t, map[string]string{
		"/index.json": nodeIndex,
		"/dist/v22.1.0/node-v22.1.0-linux-x64.tar.xz": "node v22.1.0",
	})
	h := &HttpFetcher{
		Env:    Env{HTTP: srv.Client(), Host: host.Host{OS: "linux", Arch: "amd64"}},
		URL:    srv.URL + "/index.json",
		Jq:     strings.ReplaceAll(nodeJq, "SERVER", srv.URL),
		Rename: map[string]string{"amd64": "x64"},
	}
	return h, srv
}

func TestHttpLatest(t *testing.T) {
	h, _ := nodeFetcher(t)

	v, err := h.Latest(context.Background(), Constraint{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Tag != "v22.2.0" {
		t.Errorf("got %s, want the first release v22.2.0", v.Tag)
	}
}

func TestHttpResolveAndDownload(t *testing.T) {
	h, srv := nodeFetcher(t)

	a, err := h.Resolve(context.Background(), Version{Tag: "v22.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "node-v22.1.0-linux-x64.tar.xz" {
		t.Errorf("asset: got %s", a.Name)
	}
	if a.Vars["version"] != "22.1.0" {
		t.Errorf("version var: got %s", a.Vars["version"])
	}

	dst := filepath.Join(t.TempDir(), "node.tar.xz")
	if _, err := h.Download(context.Background(), a, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "node v22.1.0" {
		t.Errorf("downloaded %q from %s", b, srv.URL)
	}
}

func TestHttpShapes(t *testing.T) {
	release := `{"tag": "v1", "assets": [
		{"name": "tool-linux-amd64.tar.gz", "url": "u1", "sha256": "abc"},
		{"name": "tool-linux-amd64.tar.gz.sha256", "url": "u2"},
		{"name": "tool-darwin-arm64.tar.gz", "url": "u3"}
	]}`

	tests := []struct {
		name    string
		body    string
		jq      string
		asset   string
		tag     string
		want    string // asset name, empty means an error
		wantErr string
	}{
		{name: "list, no jq", body: "[" + release + "]", tag: "v1", want: "tool-linux-amd64.tar.gz"},
		{name: "single release", body: release, tag: "v1", want: "tool-linux-amd64.tar.gz"},
		{name: "jq stream", body: `{"rs": [` + release + `]}`, jq: `.rs[]`, tag: "v1", want: "tool-linux-amd64.tar.gz"},
		{name: "asset pattern", body: release, asset: "tool-darwin-${arch}.tar.gz", tag: "v1", wantErr: "no asset named tool-darwin-amd64.tar.gz"},
		{name: "tag gone", body: release, tag: "v0", wantErr: "isn't listed"},
		{name: "empty list", body: `[]`, tag: "v1", wantErr: "lists no releases"},
		{name: "missing tag", body: `[{"assets": []}]`, tag: "v1", wantErr: "has no tag"},
		{name: "unknown field", body: `[{"tag_name": "v1"}]`, tag: "v1", wantErr: "expected a list"},
		{name: "jq syntax", body: release, jq: `.[`, tag: "v1", wantErr: "jq:"},
		{name: "jq runtime", body: release, jq: `error("boom")`, tag: "v1", wantErr: "boom"},
		{name: "not json", body: `<html>`, jq: `.`, tag: "v1", wantErr: "isn't JSON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httpServer(t, map[string]string{"/releases": tt.body})
			h := &HttpFetcher{
				Env:   Env{HTTP: srv.Client(), Host: host.Host{OS: "linux", Arch: "amd64"}},
				URL:   srv.URL + "/releases",
				Jq:    tt.jq,
				Asset: tt.asset,
			}

			a, err := h.Resolve(context.Background(), Version{Tag: tt.tag})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error: got %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.Name != tt.want {
				t.Errorf("asset: got %s, want %s", a.Name, tt.want)
			}
			if a.SHA256 != "abc" {
				t.Errorf("sha256: got %q, want abc", a.SHA256)
			}
		})
	}
}

func TestHttpStatus(t *testing.T) {
	srv := httpServer(t, nil)
	h := &HttpFetcher{Env: Env{HTTP: srv.Client()}, URL: srv.URL + "/missing"}

	if _, err := h.Latest(context.Background(), Constraint{}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("error: got %v, want status 404", err)
	}
}

func TestHttpLatestConstraints(t *testing.T) {
	// not in version order, a range picks the highest anyway
	feed := `[{"tag": "v20.5.0", "assets": []}, {"tag": "v22.2.0", "assets": []}, {"tag": "v22.1.0", "assets": []}, {"tag": "v23.0.0-rc1", "assets": []}]`
	srv := httpServer(t, map[string]string{"/feed": feed})

	tests := []struct {
		constraint string
		want       string
	}{
		{constraint: "", want: "v20.5.0"},
		{constraint: "22.x", want: "v22.2.0"},
		{constraint: "x.x", want: ""},
		{constraint: "23.x", want: ""},
		{constraint: "22.1.0", want: "v22.1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.constraint, func(t *testing.T) {
			h := &HttpFetcher{Env: Env{HTTP: srv.Client()}, URL: srv.URL + "/feed"}

			v, err := h.Latest(context.Background(), Constraint{Version: tt.constraint})

			if tt.want == "" {
				if err == nil {
					t.Errorf("expected an error, got %s", v.Tag)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if v.Tag != tt.want {
				t.Errorf("got %s, want %s", v.Tag, tt.want)
			}
		})
	}
}
