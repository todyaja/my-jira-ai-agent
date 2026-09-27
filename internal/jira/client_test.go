package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
			Fields struct {
				Description json.RawMessage `json:"description"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		description := string(payload.Fields.Description)
		if !strings.Contains(description, `"type":"table"`) || !strings.Contains(description, `"text":"PRD"`) {
			t.Fatalf("request body = %s, want ADF document under fields.description", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := (&Client{BaseURL: server.URL, Email: "user@example.com", Token: "secret-token"}).UpdateDescription(context.Background(), "ABC-1", json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"table","content":[]},{"type":"paragraph","content":[{"type":"text","text":"PRD"}]}]}`))
	if err != nil {
		t.Fatalf("UpdateDescription() error = %v", err)
	}
}

func TestClientTransitionToMatchesNameIgnoringCaseAndPostsID(t *testing.T) {
	var postBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "user@example.com" || password != "secret-token" {
			t.Errorf("BasicAuth() = (%q, %q, %v), want configured credentials", user, password, ok)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/ABC-1/transitions":
			io.WriteString(w, `{"transitions":[{"id":"12","name":"PRD REVIEWER"},{"id":"11","name":"PRD Review"}]}`)
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

func TestClientSearchIssuesFollowsPaginationAndReturnsStatus(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
			t.Errorf("request = %s %s, want POST search path", r.Method, r.URL.Path)
		}
		if user, password, ok := r.BasicAuth(); !ok || user != "user@example.com" || password != "secret-token" {
			t.Errorf("BasicAuth() = (%q, %q, %v), want configured credentials", user, password, ok)
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			io.WriteString(w, `{"issues":[{"id":"1","key":"ABC-1","fields":{"status":{"name":"PRD Requested"}}},{"id":"2","key":"ABC-2","fields":{"status":{"name":"Planning"}}}],"nextPageToken":"page-2"}`)
			return
		}
		io.WriteString(w, `{"issues":[{"id":"3","key":"ABC-3","fields":{"status":{"name":"Done"}}}],"isLast":true}`)
	}))
	defer server.Close()

	issues, err := (&Client{BaseURL: server.URL, Email: "user@example.com", Token: "secret-token"}).SearchIssues(context.Background(), `status = "PRD Requested"`)
	if err != nil {
		t.Fatalf("SearchIssues() error = %v", err)
	}
	want := []IssueStatus{{"ABC-1", "PRD Requested"}, {"ABC-2", "Planning"}, {"ABC-3", "Done"}}
	if fmt.Sprint(issues) != fmt.Sprint(want) {
		t.Fatalf("issues = %v, want %v", issues, want)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], `"jql":"status = \"PRD Requested\""`) || !strings.Contains(bodies[0], `"fields":["status"]`) || strings.Contains(bodies[0], "nextPageToken") {
		t.Fatalf("first body = %s, want JQL without page token", bodies[0])
	}
	if !strings.Contains(bodies[1], `"nextPageToken":"page-2"`) {
		t.Fatalf("second body = %s, want page token", bodies[1])
	}
}

func TestClientSearchIssuesNon2xxDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "sensitive search response")
	}))
	defer server.Close()

	_, err := (&Client{BaseURL: server.URL}).SearchIssues(context.Background(), "status = Done")
	if err == nil || !strings.Contains(err.Error(), "POST search: Jira returned HTTP 400") {
		t.Fatalf("SearchIssues() error = %v, want status and operation", err)
	}
	if strings.Contains(err.Error(), "sensitive search response") {
		t.Fatalf("error exposes response body: %v", err)
	}
}

func TestClientGetCommentsFollowsPaginationOldestFirst(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/issue/ABC-1/comment" {
			t.Errorf("request = %s %s, want GET comment path", r.Method, r.URL.Path)
		}
		if user, password, ok := r.BasicAuth(); !ok || user != "user@example.com" || password != "secret-token" {
			t.Errorf("BasicAuth() = (%q, %q, %v), want configured credentials", user, password, ok)
		}
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("orderBy") != "created" {
			t.Errorf("orderBy = %q, want created", r.URL.Query().Get("orderBy"))
		}
		if r.URL.Query().Get("startAt") == "0" {
			io.WriteString(w, `{"startAt":0,"total":2,"comments":[{"id":"10","author":{"displayName":"Ana"},"created":"2026-09-01T10:00:00.000+0000","body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"!prd-maker first"}]}]}}]}`)
			return
		}
		io.WriteString(w, `{"startAt":1,"total":2,"comments":[{"id":"11","author":{"displayName":"Budi"},"created":"2026-09-02T10:00:00.000+0000","body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}}]}`)
	}))
	defer server.Close()

	comments, err := (&Client{BaseURL: server.URL, Email: "user@example.com", Token: "secret-token"}).GetComments(context.Background(), "ABC-1")
	if err != nil {
		t.Fatalf("GetComments() error = %v", err)
	}
	want := []Comment{
		{ID: "10", Author: "Ana", Created: "2026-09-01T10:00:00.000+0000", Body: "!prd-maker first"},
		{ID: "11", Author: "Budi", Created: "2026-09-02T10:00:00.000+0000", Body: "second"},
	}
	if fmt.Sprint(comments) != fmt.Sprint(want) {
		t.Fatalf("comments = %v, want %v", comments, want)
	}
	if len(queries) != 2 || !strings.Contains(queries[1], "startAt=1") {
		t.Fatalf("queries = %v, want second page from startAt=1", queries)
	}
}

func TestClientGetCommentsNon2xxDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "sensitive comments response")
	}))
	defer server.Close()

	_, err := (&Client{BaseURL: server.URL}).GetComments(context.Background(), "ABC-1")
	if err == nil || !strings.Contains(err.Error(), "GET comments: Jira returned HTTP 403") {
		t.Fatalf("GetComments() error = %v, want status and operation", err)
	}
	if strings.Contains(err.Error(), "sensitive comments response") {
		t.Fatalf("error exposes response body: %v", err)
	}
}

func TestClientGetIssueStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/issue/ABC-1" || r.URL.Query().Get("fields") != "status" {
			t.Errorf("request = %s %s, want GET issue with status field", r.Method, r.URL)
		}
		io.WriteString(w, `{"key":"ABC-1","fields":{"status":{"name":"PRD Review"}}}`)
	}))
	defer server.Close()

	status, err := (&Client{BaseURL: server.URL}).GetIssueStatus(context.Background(), "ABC-1")
	if err != nil || status != "PRD Review" {
		t.Fatalf("GetIssueStatus() = (%q, %v), want PRD Review", status, err)
	}
}
