package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sctlin/che/internal/host"
)

const body = "release asset"

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestGithubDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	g := &GithubFetcher{Env: Env{HTTP: srv.Client()}}

	tests := []struct {
		name    string
		path    string
		want    string // expected checksum in the artifact
		wantErr bool
	}{
		{name: "matching checksum", path: "/asset", want: sum(body)},
		{name: "uppercase checksum", path: "/asset", want: strings.ToUpper(sum(body))},
		{name: "unknown checksum", path: "/asset"},
		{name: "checksum mismatch", path: "/asset", want: sum("other"), wantErr: true},
		{name: "not found", path: "/missing", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "asset")
			a := Artifact{URL: srv.URL + tt.path, SHA256: tt.want}

			got, err := g.Download(context.Background(), a, dst)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if _, statErr := os.Stat(dst); !errors.Is(statErr, fs.ErrNotExist) {
					t.Errorf("expected %s to be removed, stat: %v", dst, statErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != sum(body) {
				t.Errorf("checksum: got %s, want %s", got, sum(body))
			}
			content, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != body {
				t.Errorf("content: got %q, want %q", content, body)
			}
		})
	}
}

// roundTripFunc serves requests without a server, for fetchers with fixed URLs.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGithubResolveErrorListsCandidates(t *testing.T) {
	release := `{"tag_name": "v1", "assets": [
		{"name": "tool-darwin-arm64.tar.gz"},
		{"name": "tool-darwin-arm64.tar.gz.sha256"},
		{"name": "checksums.txt"}
	]}`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(release)), Request: r}, nil
	})}
	g := &GithubFetcher{Env: Env{HTTP: client, Host: host.Host{OS: "linux", Arch: "amd64"}}, Repo: "x/tool"}

	_, err := g.Resolve(context.Background(), Version{Tag: "v1"})

	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "tool-darwin-arm64.tar.gz") {
		t.Errorf("candidate missing from error:\n%s", msg)
	}
	if strings.Contains(msg, ".sha256") || strings.Contains(msg, "checksums.txt") {
		t.Errorf("auxiliary files listed in error:\n%s", msg)
	}
}

// fakeGithub serves GitHub API paths, with query, from routes. Missing ones
// are 404s.
func fakeGithub(routes map[string]any, requests *[]string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(r.URL.RequestURI(), "/repos/x/tool")
		*requests = append(*requests, path)

		body, ok := routes[path]
		if !ok {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		}
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
	})}
}

type rel struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func TestGithubLatestConstraints(t *testing.T) {
	// page 1 is full, so the range has to read page 2 to find 4.7.2
	page1 := []rel{
		{TagName: "3.6.3-stable"},
		{TagName: "5.0-stable", Draft: true},
		{TagName: "4.8-stable", Prerelease: true},
	}
	for i := len(page1); i < 100; i++ {
		page1 = append(page1, rel{TagName: fmt.Sprintf("1.0.%d", i)})
	}
	routes := map[string]any{
		"/releases/latest":              rel{TagName: "4.7.2-stable"},
		"/releases/tags/4.7.1-stable":   rel{TagName: "4.7.1-stable"},
		"/releases/tags/v1.2.3":         rel{TagName: "v1.2.3"},
		"/releases?per_page=100&page=1": page1,
		"/releases?per_page=100&page=2": []rel{{TagName: "4.7.2-stable"}, {TagName: "4.7.1-stable"}},
	}

	tests := []struct {
		constraint string
		want       string
		wantErr    string
	}{
		{constraint: "", want: "4.7.2-stable"},
		{constraint: "4.x", want: "4.7.2-stable"},
		{constraint: "3.x.x", want: "3.6.3-stable"},
		{constraint: "4.7.1-stable", want: "4.7.1-stable"},
		{constraint: "1.2.3", want: "v1.2.3"},
		{constraint: "5.x", wantErr: "matches 5.x"},
		{constraint: "9.9.9", wantErr: "no release tagged 9.9.9"},
	}

	for _, tt := range tests {
		t.Run(tt.constraint, func(t *testing.T) {
			var requests []string
			g := &GithubFetcher{Env: Env{HTTP: fakeGithub(routes, &requests)}, Repo: "x/tool"}

			v, err := g.Latest(context.Background(), Constraint{Version: tt.constraint})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error: got %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v, requests: %v", err, requests)
			}
			if v.Tag != tt.want {
				t.Errorf("got %s, want %s, requests: %v", v.Tag, tt.want, requests)
			}
		})
	}
}
