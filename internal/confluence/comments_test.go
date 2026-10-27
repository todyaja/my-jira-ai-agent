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

type fakeComments struct {
	t        *testing.T
	applied  map[string]bool
	requests []string
	bodies   map[string]map[string]any
}

func (f *fakeComments) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if r.Body != nil && r.Method != http.MethodGet {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Fatalf("decode %s %s: %v", r.Method, r.URL.Path, err)
		}
		f.bodies[r.Method+" "+r.URL.Path] = body
	}
	query := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/wiki/api/v2/pages/42/footer-comments":
		if query.Get("cursor") == "" {
			io.WriteString(w, `{"results":[{"id":"f1","version":{"createdAt":"2026-10-23T03:00:00Z"},"body":{"storage":{"value":"<p>Nomor RFC salah &amp; judul</p><p>kedua</p>"}}}],"_links":{"next":"/wiki/api/v2/pages/42/footer-comments?cursor=next"}}`)
			return
		}
		io.WriteString(w, `{"results":[{"id":"f2","version":{"createdAt":"2026-10-23T01:00:00Z"},"body":{"storage":{"value":"<p>sudah</p>"}}}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/wiki/api/v2/pages/42/inline-comments":
		if got := strings.Join(query["resolution-status"], ","); got != "open,reopened,dangling" {
			f.t.Errorf("resolution-status = %q", got)
		}
		io.WriteString(w, `{"results":[`+
			`{"id":"i1","resolutionStatus":"open","version":{"createdAt":"2026-10-23T02:00:00Z"},"body":{"storage":{"value":"<p>tambahkan alur lama</p>"}},"properties":{"inlineOriginalSelection":"Alur baru"}},`+
			`{"id":"i2","resolutionStatus":"dangling","version":{"createdAt":"2026-10-23T04:00:00Z"},"body":{"storage":{"value":"<p>teks hilang</p>"}},"properties":{"inlineOriginalSelection":"lama"}}]}`)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/properties"):
		if query.Get("key") != AppliedProperty {
			f.t.Errorf("property key = %q", query.Get("key"))
		}
		id := strings.Split(r.URL.Path, "/")[5]
		if f.applied[id] {
			io.WriteString(w, `{"results":[{"key":"`+AppliedProperty+`"}]}`)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/wiki/api/v2/inline-comments/i1":
		io.WriteString(w, `{"id":"i1","version":{"number":3},"body":{"storage":{"value":"<p>tambahkan alur lama</p>"}}}`)
	case r.Method == http.MethodPost || r.Method == http.MethodPut:
		io.WriteString(w, `{}`)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func newFakeComments(t *testing.T) (*fakeComments, *Client, func()) {
	fake := &fakeComments{t: t, applied: map[string]bool{"f2": true}, bodies: map[string]map[string]any{}}
	server := httptest.NewServer(fake)
	return fake, newTestClient(server.URL), server.Close
}

func TestOpenCommentsSkipsAppliedAndSortsOldestFirst(t *testing.T) {
	fake, client, done := newFakeComments(t)
	defer done()

	comments, err := client.OpenComments(context.Background(), "42")
	if err != nil {
		t.Fatalf("OpenComments() error = %v", err)
	}
	ids := []string{}
	for _, comment := range comments {
		ids = append(ids, comment.ID)
	}
	if strings.Join(ids, ",") != "i1,f1,i2" {
		t.Fatalf("comments = %+v, want i1, f1, i2 (f2 is applied)", comments)
	}
	if comments[0].Selection != "Alur baru" || !comments[0].Inline || comments[0].Dangling || comments[0].PageID != "42" {
		t.Fatalf("inline comment = %+v", comments[0])
	}
	if comments[1].Text != "Nomor RFC salah & judul\nkedua" || comments[1].Inline {
		t.Fatalf("footer comment = %+v", comments[1])
	}
	if !comments[2].Dangling {
		t.Fatalf("dangling comment = %+v", comments[2])
	}
	// The open inline comment needs no property check; the others do.
	for _, request := range fake.requests {
		if request == "GET /wiki/api/v2/comments/i1/properties" {
			t.Fatal("checked the applied property of an open inline comment")
		}
	}
}

func TestCloseCommentRepliesAndResolvesOrMarksApplied(t *testing.T) {
	fake, client, done := newFakeComments(t)
	defer done()
	ctx := context.Background()

	if err := client.CloseComment(ctx, Comment{ID: "i1", PageID: "42", Inline: true}, "Done - applied in the revised RFC."); err != nil {
		t.Fatalf("CloseComment(inline) error = %v", err)
	}
	reply := fake.bodies["POST /wiki/api/v2/inline-comments"]
	if reply["parentCommentId"] != "i1" || reply["body"].(map[string]any)["value"] != "<p>Done - applied in the revised RFC.</p>" {
		t.Fatalf("inline reply = %v", reply)
	}
	resolve := fake.bodies["PUT /wiki/api/v2/inline-comments/i1"]
	if resolve["resolved"] != true || resolve["version"].(map[string]any)["number"] != float64(4) {
		t.Fatalf("resolve = %v, want resolved at version 4", resolve)
	}

	if err := client.CloseComment(ctx, Comment{ID: "f1", PageID: "42"}, "Done"); err != nil {
		t.Fatalf("CloseComment(footer) error = %v", err)
	}
	if fake.bodies["POST /wiki/api/v2/footer-comments"]["parentCommentId"] != "f1" || fake.bodies["POST /wiki/api/v2/comments/f1/properties"]["key"] != AppliedProperty {
		t.Fatalf("footer close = %v", fake.bodies)
	}

	fake.requests = nil
	if err := client.CloseComment(ctx, Comment{ID: "i2", PageID: "42", Inline: true, Dangling: true}, "Done"); err != nil {
		t.Fatalf("CloseComment(dangling) error = %v", err)
	}
	if strings.Join(fake.requests, ",") != "POST /wiki/api/v2/comments/i2/properties" {
		t.Fatalf("dangling close requests = %v, want only the applied property", fake.requests)
	}
}
