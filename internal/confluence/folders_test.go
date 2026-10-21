package confluence

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// folderConfluence is an in-memory space that records pages by title.
type folderConfluence struct {
	t        *testing.T
	mu       sync.Mutex
	pages    map[string]map[string]any // by title
	nextID   int
	requests []string
	upload   string
	labels   []string
}

func (f *folderConfluence) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/wiki/api/v2/spaces":
		io.WriteString(w, `{"results":[{"id":"9001"}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/wiki/api/v2/pages":
		page, found := f.pages[r.URL.Query().Get("title")]
		if !found {
			io.WriteString(w, `{"results":[]}`)
			return
		}
		encoded, _ := json.Marshal(map[string]any{"results": []any{map[string]any{"id": page["id"], "version": map[string]int{"number": 1}}}})
		w.Write(encoded)
	case r.Method == http.MethodPost && r.URL.Path == "/wiki/api/v2/pages",
		r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/wiki/api/v2/pages/"):
		var page map[string]any
		if err := json.NewDecoder(r.Body).Decode(&page); err != nil {
			f.t.Fatalf("decode page: %v", err)
		}
		title := page["title"].(string)
		if existing, found := f.pages[title]; found {
			page["id"] = existing["id"]
			page["parentId"] = existing["parentId"]
		} else {
			f.nextID++
			page["id"] = string(rune('0' + f.nextID))
		}
		f.pages[title] = page
		encoded, _ := json.Marshal(map[string]any{"id": page["id"], "_links": map[string]string{"webui": "/pages/" + page["id"].(string), "base": "https://wiki"}})
		w.Write(encoded)
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/child/attachment"):
		if r.Header.Get("X-Atlassian-Token") != "no-check" {
			f.t.Error("attachment upload without X-Atlassian-Token: no-check")
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			f.t.Fatalf("attachment form: %v", err)
		}
		content, _ := io.ReadAll(file)
		f.upload = header.Filename + "=" + string(content)
		io.WriteString(w, `{"results":[]}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/label"):
		var labels []map[string]string
		json.NewDecoder(r.Body).Decode(&labels)
		for _, label := range labels {
			f.labels = append(f.labels, label["name"])
		}
		io.WriteString(w, `{"results":[]}`)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPublishPageUnderCreatesFoldersOnce(t *testing.T) {
	fake := &folderConfluence{t: t, pages: map[string]map[string]any{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := &Client{BaseURL: server.URL, SpaceKey: "NB", ParentPageID: "77"}

	first, err := client.PublishPageUnder(context.Background(), []string{"RFCs", "RFCs 2026"}, "RFC-2026-11 Bulk Withdrawal", "# RFC")
	if err != nil {
		t.Fatalf("PublishPageUnder() error = %v", err)
	}
	if first.ID == "" || first.URL != "https://wiki/pages/"+first.ID {
		t.Fatalf("PublishPageUnder() = %+v, want the page id and URL", first)
	}
	if got := fake.pages["RFCs"]["parentId"]; got != "77" {
		t.Fatalf("RFCs parent = %v, want the configured parent page", got)
	}
	if got := fake.pages["RFCs 2026"]["parentId"]; got != fake.pages["RFCs"]["id"] {
		t.Fatalf("RFCs 2026 parent = %v, want the RFCs page", got)
	}
	if got := fake.pages["RFC-2026-11 Bulk Withdrawal"]["parentId"]; got != fake.pages["RFCs 2026"]["id"] {
		t.Fatalf("RFC parent = %v, want the year page", got)
	}

	// A second document reuses the folders, and republishing updates in place.
	if _, err := client.PublishPageUnder(context.Background(), []string{"RFCs", "RFCs 2026"}, "RFC-2026-12 Other", "# RFC"); err != nil {
		t.Fatal(err)
	}
	again, err := client.PublishPageUnder(context.Background(), []string{"RFCs", "RFCs 2026"}, "RFC-2026-11 Bulk Withdrawal", "# RFC v2")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || len(fake.pages) != 4 {
		t.Fatalf("pages = %d, republished id = %s, want 4 pages and the same id %s", len(fake.pages), again.ID, first.ID)
	}
	if !strings.Contains(fake.pages["RFC-2026-11 Bulk Withdrawal"]["body"].(map[string]any)["value"].(string), "RFC v2") {
		t.Fatal("republishing did not update the page body")
	}
}

func TestAttachFileAndAddLabel(t *testing.T) {
	fake := &folderConfluence{t: t, pages: map[string]map[string]any{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := &Client{BaseURL: server.URL, SpaceKey: "NB"}

	path := filepath.Join(t.TempDir(), "RFC-2026-11.docx")
	if err := os.WriteFile(path, []byte("word"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.AttachFile(context.Background(), "42", path); err != nil {
		t.Fatalf("AttachFile() error = %v", err)
	}
	if fake.upload != "RFC-2026-11.docx=word" {
		t.Fatalf("uploaded %q, want the file name and content", fake.upload)
	}
	if err := client.AddLabel(context.Background(), "42", "NBS-12"); err != nil {
		t.Fatalf("AddLabel() error = %v", err)
	}
	if strings.Join(fake.labels, ",") != "nbs-12" {
		t.Fatalf("labels = %q, want the lower-cased key", fake.labels)
	}
	if err := client.AttachFile(context.Background(), "42", filepath.Join(t.TempDir(), "missing.docx")); err == nil {
		t.Fatal("AttachFile() of a missing file succeeded")
	}
}
