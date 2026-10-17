package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenPullRequestCreatesOne(t *testing.T) {
	var created pullRequestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/app/pulls":
			if r.URL.Query().Get("head") != "octo:feature/ABC-1" || r.URL.Query().Get("state") != "open" {
				t.Errorf("query = %v, want open pulls from octo:feature/ABC-1", r.URL.Query())
			}
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/app/pulls":
			json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"html_url":"https://github.com/octo/app/pull/9"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := &Client{APIURL: server.URL, Owner: "octo", Repo: "app", Token: "secret", Base: "master"}

	got, err := client.OpenPullRequest(context.Background(), "feature/ABC-1", "ABC-1: Title", "Body")
	if err != nil || got != "https://github.com/octo/app/pull/9" {
		t.Fatalf("OpenPullRequest() = (%q, %v), want the new pull request", got, err)
	}
	if created != (pullRequestRequest{Title: "ABC-1: Title", Head: "feature/ABC-1", Base: "master", Body: "Body"}) {
		t.Fatalf("created %+v, want title, head, base and body", created)
	}
}

func TestOpenPullRequestReusesOpenOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s, want only the lookup", r.Method)
		}
		w.Write([]byte(`[{"html_url":"https://github.com/octo/app/pull/3"}]`))
	}))
	defer server.Close()
	client := &Client{APIURL: server.URL, Owner: "octo", Repo: "app", Token: "secret", Base: "master"}

	got, err := client.OpenPullRequest(context.Background(), "feature/ABC-1", "t", "b")
	if err != nil || got != "https://github.com/octo/app/pull/3" {
		t.Fatalf("OpenPullRequest() = (%q, %v), want the open pull request", got, err)
	}
}

func TestOpenPullRequestReportsHTTPErrorWithoutBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"private detail"}`))
	}))
	defer server.Close()
	client := &Client{APIURL: server.URL, Owner: "octo", Repo: "app", Token: "bad"}

	_, err := client.OpenPullRequest(context.Background(), "feature/ABC-1", "t", "b")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "private detail") {
		t.Fatalf("OpenPullRequest() error = %v, want HTTP 401 without the body", err)
	}
}

func TestParseRepository(t *testing.T) {
	tests := []struct{ owner, repo, wantOwner, wantRepo string }{
		{"octo", "app", "octo", "app"},
		{"", "octo/app", "octo", "app"},
		{"octo", "https://github.com/octo/app", "octo", "app"},
		{"", "https://github.com/octo/app.git/", "octo", "app"},
	}
	for _, tt := range tests {
		owner, repo, err := ParseRepository(tt.owner, tt.repo)
		if err != nil || owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("ParseRepository(%q, %q) = (%q, %q, %v), want (%q, %q)", tt.owner, tt.repo, owner, repo, err, tt.wantOwner, tt.wantRepo)
		}
	}
	for _, repo := range []string{"", "a/b/c"} {
		if _, _, err := ParseRepository("", repo); err == nil {
			t.Errorf("ParseRepository(%q) succeeded, want an error", repo)
		}
	}
}
