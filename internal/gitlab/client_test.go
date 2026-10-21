package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenMergeRequest(t *testing.T) {
	var created map[string]any
	existing := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-secret" {
			t.Errorf("PRIVATE-TOKEN = %q", r.Header.Get("PRIVATE-TOKEN"))
		}
		// The project path is one escaped path segment.
		if !strings.HasPrefix(r.URL.EscapedPath(), "/api/v4/projects/byb-sunline%2Ficore%2Fbnc_busi/merge_requests") {
			t.Errorf("path = %s, want the escaped project path", r.URL.EscapedPath())
		}
		switch r.Method {
		case http.MethodGet:
			query := r.URL.Query()
			if query.Get("source_branch") != "feature/NBS-12" || query.Get("target_branch") != "master" || query.Get("state") != "opened" {
				t.Errorf("query = %v", query)
			}
			if existing == "" {
				io.WriteString(w, `[]`)
			} else {
				io.WriteString(w, `[{"web_url":"`+existing+`"}]`)
			}
		case http.MethodPost:
			json.NewDecoder(r.Body).Decode(&created)
			io.WriteString(w, `{"web_url":"http://gitlab/byb-sunline/icore/bnc_busi/-/merge_requests/87"}`)
		}
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL + "/", Token: "glpat-secret"}

	got, err := client.OpenMergeRequest(context.Background(), "byb-sunline/icore/bnc_busi", "feature/NBS-12", "master", "NBS-12: Bulk withdrawal", "body")
	if err != nil || got != "http://gitlab/byb-sunline/icore/bnc_busi/-/merge_requests/87" {
		t.Fatalf("OpenMergeRequest() = %q, %v", got, err)
	}
	if created["source_branch"] != "feature/NBS-12" || created["target_branch"] != "master" || created["title"] != "NBS-12: Bulk withdrawal" {
		t.Fatalf("created = %v", created)
	}

	created, existing = nil, "http://gitlab/mr/1"
	if got, err := client.OpenMergeRequest(context.Background(), "byb-sunline/icore/bnc_busi", "feature/NBS-12", "master", "t", "b"); err != nil || got != existing || created != nil {
		t.Fatalf("OpenMergeRequest() with an open one = %q, %v, created %v; want the existing one and no POST", got, err, created)
	}
}

func TestOpenMergeRequestHidesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "private details")
	}))
	defer server.Close()
	_, err := (&Client{BaseURL: server.URL}).OpenMergeRequest(context.Background(), "g/p", "s", "t", "", "")
	if err == nil || strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v, want the status without the body", err)
	}
}

func TestProjectPath(t *testing.T) {
	for remote, want := range map[string]string{
		"http://192.168.138.192/byb-sunline/icore/bnc_busi.git":             "byb-sunline/icore/bnc_busi",
		"http://user:pass@192.168.138.192/byb-sunline/icore/es/es-busi.git": "byb-sunline/icore/es/es-busi",
		"https://gitlab.com/group/repo/":                                    "group/repo",
		"git@192.168.138.192:byb-sunline/icore/bnc_iobus.git":               "byb-sunline/icore/bnc_iobus",
	} {
		if got, err := ProjectPath(remote); err != nil || got != want {
			t.Errorf("ProjectPath(%q) = %q, %v, want %q", remote, got, err, want)
		}
	}
	for _, remote := range []string{"", "repo", "http://host/repo.git"} {
		if _, err := ProjectPath(remote); err == nil {
			t.Errorf("ProjectPath(%q) succeeded", remote)
		}
	}
}
