package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientGetIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/issue/ABC-1" {
			t.Errorf("request = %s %s, want GET issue path", r.Method, r.URL.Path)
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:secret-token"))
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Errorf("Authorization = %q, want %q", got, wantAuth)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"key":"ABC-1","fields":{"summary":"A summary","description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Description text"}]}]}}}`)
	}))
	defer server.Close()

	issue, err := (&Client{BaseURL: server.URL, Email: "user@example.com", Token: "secret-token"}).GetIssue(context.Background(), "ABC-1")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}
	if issue.Key != "ABC-1" || issue.Summary != "A summary" || issue.Description != "Description text" {
		t.Fatalf("issue = %+v, want decoded issue", issue)
	}
	if len(issue.DescriptionADF) == 0 {
		t.Fatal("DescriptionADF is empty")
	}
}

func TestClientUpdateDescriptionSendsADFWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "user@example.com" || password != "secret-token" {
			t.Errorf("BasicAuth() = (%q, %q, %v), want configured credentials", user, password, ok)
		}
		if r.Method != http.MethodPut || r.URL.Path != "/rest/api/3/issue/ABC-1" {
			t.Errorf("request = %s %s, want PUT issue path", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "user@example.com") || strings.Contains(string(body), "secret-token") {
			t.Fatalf("request body exposes credentials: %s", body)
		}
		var payload struct {
			Description json.RawMessage `json:"description"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if !strings.Contains(string(payload.Description), `"type":"table"`) || !strings.Contains(string(payload.Description), `"text":"PRD"`) {
			t.Fatalf("description payload = %s, want preserved ADF document", payload.Description)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := (&Client{BaseURL: server.URL, Email: "user@example.com", Token: "secret-token"}).UpdateDescription(context.Background(), "ABC-1", json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"table","content":[]},{"type":"paragraph","content":[{"type":"text","text":"PRD"}]}]}`))
	if err != nil {
		t.Fatalf("UpdateDescription() error = %v", err)
	}
}

func TestClientTransitionToFindsExactNameAndPostsID(t *testing.T) {
	var postBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "user@example.com" || password != "secret-token" {
			t.Errorf("BasicAuth() = (%q, %q, %v), want configured credentials", user, password, ok)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/ABC-1/transitions":
			io.WriteString(w, `{"transitions":[{"id":"11","name":"PRD REVIEW"},{"id":"12","name":"PRD REVIEWER"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/ABC-1/transitions":
			body, _ := io.ReadAll(r.Body)
			postBody = string(body)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	if err := (&Client{BaseURL: server.URL, Email: "user@example.com", Token: "secret-token"}).TransitionTo(context.Background(), "ABC-1", "PRD REVIEW"); err != nil {
		t.Fatalf("TransitionTo() error = %v", err)
	}
	if postBody != `{"transition":{"id":"11"}}` {
		t.Fatalf("POST body = %q, want exact transition ID", postBody)
	}
}

func TestClientErrorsDoNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "sensitive response body")
	}))
	defer server.Close()

	_, err := (&Client{BaseURL: server.URL, Email: "user@example.com", Token: "secret-token"}).GetIssue(context.Background(), "ABC-1")
	if err == nil || !strings.Contains(err.Error(), "GET issue: Jira returned HTTP 401") {
		t.Fatalf("GetIssue() error = %v, want status and operation", err)
	}
	if strings.Contains(err.Error(), "sensitive response body") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error exposes sensitive data: %v", err)
	}
}

func TestClientUpdateDescriptionNon2xxDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "sensitive PUT response")
	}))
	defer server.Close()

	err := (&Client{BaseURL: server.URL}).UpdateDescription(context.Background(), "ABC-1", json.RawMessage(`{"type":"doc","version":1,"content":[]}`))
	if err == nil || !strings.Contains(err.Error(), "PUT description: Jira returned HTTP 502") {
		t.Fatalf("UpdateDescription() error = %v, want status and operation", err)
	}
	if strings.Contains(err.Error(), "sensitive PUT response") {
		t.Fatalf("error exposes response body: %v", err)
	}
}

func TestClientTransitionPostNon2xxDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"transitions":[{"id":"11","name":"PRD REVIEW"}]}`)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "sensitive POST response")
	}))
	defer server.Close()

	err := (&Client{BaseURL: server.URL}).TransitionTo(context.Background(), "ABC-1", "PRD REVIEW")
	if err == nil || !strings.Contains(err.Error(), "POST transition: Jira returned HTTP 502") {
		t.Fatalf("TransitionTo() error = %v, want status and operation", err)
	}
	if strings.Contains(err.Error(), "sensitive POST response") {
		t.Fatalf("error exposes response body: %v", err)
	}
}

func TestClientTransitionToMissingName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"transitions":[{"id":"11","name":"Done"}]}`)
	}))
	defer server.Close()

	err := (&Client{BaseURL: server.URL}).TransitionTo(context.Background(), "ABC-1", "PRD REVIEW")
	if err == nil || !strings.Contains(err.Error(), `transition "PRD REVIEW" not found`) {
		t.Fatalf("TransitionTo() error = %v, want missing transition error", err)
	}
}
