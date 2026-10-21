package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGetIssueKeepsCustomFieldsAndReadsCheckedOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"key":"NBS-3","fields":{"summary":"S","description":null,"customfield_10100":[{"id":"1","value":"bnc_busi"},{"id":"2","value":" sump-vue "}],"customfield_10101":null}}`)
	}))
	defer server.Close()
	issue, err := (&Client{BaseURL: server.URL}).GetIssue(context.Background(), "NBS-3")
	if err != nil {
		t.Fatalf("GetIssue() error = %v", err)
	}
	if issue.Summary != "S" {
		t.Fatalf("Summary = %q", issue.Summary)
	}
	checked, err := issue.CheckedOptions("customfield_10100")
	if err != nil || !reflect.DeepEqual(checked, []string{"bnc_busi", "sump-vue"}) {
		t.Fatalf("CheckedOptions() = %q, %v", checked, err)
	}
	for _, empty := range []string{"customfield_10101", "customfield_missing"} {
		if checked, err := issue.CheckedOptions(empty); err != nil || len(checked) != 0 {
			t.Fatalf("CheckedOptions(%s) = %q, %v, want none", empty, checked, err)
		}
	}
	notCheckbox := Issue{Fields: map[string]json.RawMessage{"customfield_1": json.RawMessage(`"text"`)}}
	if _, err := notCheckbox.CheckedOptions("customfield_1"); err == nil {
		t.Fatal("CheckedOptions() of a text field succeeded")
	}
}

func TestFindFieldIDPrefersTheProjectsOwnField(t *testing.T) {
	fields := `[
		{"id":"customfield_1","name":"Repositories"},
		{"id":"customfield_2","name":"repositories","scope":{"type":"PROJECT","project":{"id":"99"}}},
		{"id":"customfield_3","name":"Repositories","scope":{"type":"PROJECT","project":{"id":"10033"}}},
		{"id":"customfield_4","name":"Team"}
	]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/project/NBS":
			io.WriteString(w, `{"id":"10033"}`)
		case "/rest/api/3/field":
			io.WriteString(w, fields)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL}

	if id, found, err := client.FindFieldID(context.Background(), "REPOSITORIES", "NBS"); err != nil || !found || id != "customfield_3" {
		t.Fatalf("FindFieldID() = %q, %t, %v, want the NBS field", id, found, err)
	}
	fields = `[{"id":"customfield_1","name":"Repositories"},{"id":"customfield_2","name":"Repositories","scope":{"type":"PROJECT","project":{"id":"99"}}}]`
	if id, found, err := client.FindFieldID(context.Background(), "Repositories", "NBS"); err != nil || !found || id != "customfield_1" {
		t.Fatalf("FindFieldID() = %q, %t, %v, want the site-wide field", id, found, err)
	}
	fields = `[{"id":"customfield_4","name":"Team"}]`
	if _, found, err := client.FindFieldID(context.Background(), "Repositories", "NBS"); err != nil || found {
		t.Fatalf("FindFieldID() found = %t, %v, want not found", found, err)
	}
}
