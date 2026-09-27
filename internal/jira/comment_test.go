package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientGetCommentsReadsPropertiesAndAuthor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("expand"); got != "properties" {
			t.Errorf("expand = %q, want properties", got)
		}
		io.WriteString(w, `{"startAt":0,"total":1,"comments":[{"id":"10","author":{"accountId":"acc-1","displayName":"Ana"},"created":"2026-09-01","body":{"type":"doc","version":1,"content":[]},"properties":[{"key":"jira-ai-agent.prd-maker","value":{"done":true}}]}]}`)
	}))
	defer server.Close()

	comments, err := (&Client{BaseURL: server.URL}).GetComments(context.Background(), "ABC-1")
	if err != nil || len(comments) != 1 {
		t.Fatalf("GetComments() = (%v, %v), want one comment", comments, err)
	}
	if comments[0].AuthorAccountID != "acc-1" || string(comments[0].Properties["jira-ai-agent.prd-maker"]) != `{"done":true}` {
		t.Fatalf("comment = %+v, want account ID and property", comments[0])
	}
}

func TestClientAddCommentPostsADFBody(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/issue/ABC-1/comment" {
			t.Errorf("request = %s %s, want POST comment path", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"20"}`)
	}))
	defer server.Close()

	id, err := (&Client{BaseURL: server.URL}).AddComment(context.Background(), "ABC-1", TextADF("Hello"))
	if err != nil || id != "20" {
		t.Fatalf("AddComment() = (%q, %v), want new comment ID", id, err)
	}
	if body != `{"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]}]}}` {
		t.Fatalf("body = %s, want ADF under body", body)
	}
}

func TestClientSetCommentPropertyPutsValue(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/rest/api/3/comment/10/properties/jira-ai-agent.prd-maker" {
			t.Errorf("request = %s %s, want PUT comment property path", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := (&Client{BaseURL: server.URL}).SetCommentProperty(context.Background(), "10", "jira-ai-agent.prd-maker", json.RawMessage(`{"done":true}`)); err != nil {
		t.Fatalf("SetCommentProperty() error = %v", err)
	}
	if body != `{"done":true}` {
		t.Fatalf("body = %s, want property value", body)
	}
}

func TestClientCommentWritesDoNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "sensitive response")
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL}

	_, addErr := client.AddComment(context.Background(), "ABC-1", TextADF("x"))
	for name, err := range map[string]error{
		"POST comment: Jira returned HTTP 403":         addErr,
		"PUT comment property: Jira returned HTTP 403": client.SetCommentProperty(context.Background(), "10", "key", json.RawMessage(`{}`)),
	} {
		if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "sensitive response") {
			t.Fatalf("error = %v, want %q without response body", err, name)
		}
	}
}

func TestReplyADFQuotesOriginalAndMentionsKnownAuthor(t *testing.T) {
	long := strings.Repeat("a", maxQuotedRunes+50)
	reply := string(ReplyADF(Comment{Author: "Ana", AuthorAccountID: "acc-1", Body: long}, "Done:", "view PRD", "https://example.com/prd"))
	for _, want := range []string{`"type":"mention"`, `"id":"acc-1"`, `"text":"@Ana"`, `"href":"https://example.com/prd"`, `"type":"blockquote"`, strings.Repeat("a", maxQuotedRunes) + "…"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply = %s, want it to contain %q", reply, want)
		}
	}
	if _, err := adfToPlainText(json.RawMessage(reply)); err != nil {
		t.Fatalf("reply is not valid ADF: %v", err)
	}

	withoutAccount := string(ReplyADF(Comment{Author: "Ana", Body: "hi"}, "Done:", "view PRD", "https://example.com/prd"))
	if strings.Contains(withoutAccount, "mention") {
		t.Fatalf("reply = %s, want no mention without an account ID", withoutAccount)
	}
}

func TestClientIssuePropertyRoundTrip(t *testing.T) {
	stored := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/ABC-1/properties/jira-ai-agent.implementation" {
			t.Errorf("path = %s, want issue property path", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			if stored == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			io.WriteString(w, `{"key":"jira-ai-agent.implementation","value":`+stored+`}`)
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			stored = string(raw)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL}

	if _, found, err := client.GetIssueProperty(context.Background(), "ABC-1", "jira-ai-agent.implementation"); err != nil || found {
		t.Fatalf("GetIssueProperty() before set = (%v, %v), want not found", found, err)
	}
	if err := client.SetIssueProperty(context.Background(), "ABC-1", "jira-ai-agent.implementation", json.RawMessage(`{"reviews":[]}`)); err != nil {
		t.Fatalf("SetIssueProperty() error = %v", err)
	}
	value, found, err := client.GetIssueProperty(context.Background(), "ABC-1", "jira-ai-agent.implementation")
	if err != nil || !found || string(value) != `{"reviews":[]}` {
		t.Fatalf("GetIssueProperty() = (%s, %v, %v), want stored value", value, found, err)
	}
}
