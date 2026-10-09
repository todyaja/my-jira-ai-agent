package confluence

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeConfluence struct {
	t            *testing.T
	existingPage string
	failStatus   int
	requests     []string
	pageBody     map[string]any
}

func (f *fakeConfluence) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if user, password, ok := r.BasicAuth(); !ok || user != "user@example.com" || password != "secret-token" {
		f.t.Errorf("BasicAuth() = (%q, %q, %v), want configured credentials", user, password, ok)
	}
	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		io.WriteString(w, "private response body")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/wiki/api/v2/spaces":
		if got := r.URL.Query().Get("keys"); got != "ENG" {
			f.t.Errorf("space keys = %q, want ENG", got)
		}
		io.WriteString(w, `{"results":[{"id":"9001","key":"ENG"}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/wiki/api/v2/pages":
		query := r.URL.Query()
		if query.Get("space-id") != "9001" || query.Get("title") != "DEMO-1: Export PRD" || query.Get("status") != "current" {
			f.t.Errorf("page query = %v, want space, title and status", query)
		}
		if f.existingPage == "" {
			io.WriteString(w, `{"results":[]}`)
			return
		}
		if query.Get("body-format") == "storage" {
			io.WriteString(w, `{"results":[{"id":"`+f.existingPage+`","version":{"number":4},"body":{"storage":{"representation":"storage","value":"<h2>Problem</h2>"}}}]}`)
			return
		}
		io.WriteString(w, `{"results":[{"id":"`+f.existingPage+`","version":{"number":4}}]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/wiki/api/v2/pages",
		r.Method == http.MethodPut && r.URL.Path == "/wiki/api/v2/pages/"+f.existingPage:
		if err := json.NewDecoder(r.Body).Decode(&f.pageBody); err != nil {
			f.t.Fatalf("decode page body: %v", err)
		}
		io.WriteString(w, `{"id":"123","_links":{"webui":"/spaces/ENG/pages/123/DEMO-1","base":"https://example.atlassian.net/wiki"}}`)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestClient(serverURL string) *Client {
	return &Client{BaseURL: serverURL, Email: "user@example.com", Token: "secret-token", SpaceKey: "ENG", ParentPageID: "77"}
}

func TestClientPublishPageCreatesPage(t *testing.T) {
	fake := &fakeConfluence{t: t}
	server := httptest.NewServer(fake)
	defer server.Close()

	got, err := newTestClient(server.URL).PublishPage(context.Background(), "DEMO-1: Export PRD", "## Problem\nText")
	if err != nil {
		t.Fatalf("PublishPage() error = %v", err)
	}
	if got != "https://example.atlassian.net/wiki/spaces/ENG/pages/123/DEMO-1" {
		t.Fatalf("PublishPage() = %q, want page web URL", got)
	}
	if strings.Join(fake.requests, ", ") != "GET /wiki/api/v2/spaces, GET /wiki/api/v2/pages, POST /wiki/api/v2/pages" {
		t.Fatalf("requests = %v", fake.requests)
	}
	body := fake.pageBody["body"].(map[string]any)
	if fake.pageBody["spaceId"] != "9001" || fake.pageBody["parentId"] != "77" || fake.pageBody["title"] != "DEMO-1: Export PRD" || fake.pageBody["status"] != "current" {
		t.Fatalf("page = %v, want space, parent, title and status", fake.pageBody)
	}
	if body["representation"] != "storage" || body["value"] != "<h2>Problem</h2><p>Text</p>" {
		t.Fatalf("page body = %v, want storage format", body)
	}
}

func TestClientPublishPageUpdatesExistingPage(t *testing.T) {
	fake := &fakeConfluence{t: t, existingPage: "555"}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(server.URL)

	for range 2 {
		if _, err := client.PublishPage(context.Background(), "DEMO-1: Export PRD", "Text"); err != nil {
			t.Fatalf("PublishPage() error = %v", err)
		}
	}
	want := "GET /wiki/api/v2/spaces, GET /wiki/api/v2/pages, PUT /wiki/api/v2/pages/555, GET /wiki/api/v2/pages, PUT /wiki/api/v2/pages/555"
	if strings.Join(fake.requests, ", ") != want {
		t.Fatalf("requests = %v, want space looked up once and page updated", fake.requests)
	}
	version := fake.pageBody["version"].(map[string]any)
	if fake.pageBody["id"] != "555" || version["number"] != float64(5) {
		t.Fatalf("page = %v, want id and next version", fake.pageBody)
	}
	if _, hasParent := fake.pageBody["parentId"]; hasParent {
		t.Fatalf("page = %v, want existing page left under its parent", fake.pageBody)
	}
}

func TestClientPublishPageFallsBackToSiteURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wiki/api/v2/spaces":
			io.WriteString(w, `{"results":[{"id":"1"}]}`)
		case "/wiki/api/v2/pages":
			if r.Method == http.MethodGet {
				io.WriteString(w, `{"results":[]}`)
				return
			}
			io.WriteString(w, `{"id":"2","_links":{"webui":"/spaces/ENG/pages/2"}}`)
		}
	}))
	defer server.Close()

	got, err := newTestClient(server.URL+"/").PublishPage(context.Background(), "DEMO-1: Export PRD", "Text")
	if err != nil || got != server.URL+"/wiki/spaces/ENG/pages/2" {
		t.Fatalf("PublishPage() = (%q, %v), want URL built from BaseURL", got, err)
	}
}

func TestClientPublishPageMissingSpace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	}))
	defer server.Close()

	_, err := newTestClient(server.URL).PublishPage(context.Background(), "DEMO-1: Export PRD", "Text")
	if err == nil || !strings.Contains(err.Error(), `space "ENG" not found`) {
		t.Fatalf("PublishPage() error = %v, want missing space", err)
	}
}

func TestClientErrorsDoNotExposeResponseBody(t *testing.T) {
	fake := &fakeConfluence{t: t, failStatus: http.StatusUnauthorized}
	server := httptest.NewServer(fake)
	defer server.Close()

	_, err := newTestClient(server.URL).PublishPage(context.Background(), "DEMO-1: Export PRD", "Text")
	if err == nil || err.Error() != "GET space: Confluence returned HTTP 401" {
		t.Fatalf("PublishPage() error = %v, want status-only error", err)
	}
}

func TestClientPageContentReturnsStorageBody(t *testing.T) {
	fake := &fakeConfluence{t: t, existingPage: "555"}
	server := httptest.NewServer(fake)
	defer server.Close()

	content, found, err := newTestClient(server.URL).PageContent(context.Background(), "DEMO-1: Export PRD")
	if err != nil || !found || content != "<h2>Problem</h2>" {
		t.Fatalf("PageContent() = (%q, %v, %v), want existing storage body", content, found, err)
	}
}

func TestClientPageContentMissingPage(t *testing.T) {
	fake := &fakeConfluence{t: t}
	server := httptest.NewServer(fake)
	defer server.Close()

	content, found, err := newTestClient(server.URL).PageContent(context.Background(), "DEMO-1: Export PRD")
	if err != nil || found || content != "" {
		t.Fatalf("PageContent() = (%q, %v, %v), want not found", content, found, err)
	}
}
