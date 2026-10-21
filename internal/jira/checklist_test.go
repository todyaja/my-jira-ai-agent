package jira

import (
	"encoding/json"
	"reflect"
	"testing"
)

const checklistDescription = `{"type":"doc","version":1,"content":[
	{"type":"paragraph","content":[{"type":"text","text":"Repositories:"}]},
	{"type":"taskList","attrs":{"localId":"l1"},"content":[
		{"type":"taskItem","attrs":{"localId":"a","state":"DONE"},"content":[{"type":"text","text":" bnc_busi "}]},
		{"type":"taskItem","attrs":{"localId":"b","state":"TODO"},"content":[{"type":"text","text":"bnc_iobus"}]},
		{"type":"taskList","attrs":{"localId":"l2"},"content":[
			{"type":"taskItem","attrs":{"localId":"c","state":"DONE"},"content":[{"type":"text","text":"sump-"},{"type":"text","text":"vue","marks":[{"type":"strong"}]}]}
		]},
		{"type":"taskItem","attrs":{"localId":"d","state":"DONE"},"content":[]}
	]}
]}`

func TestChecklist(t *testing.T) {
	items, err := Checklist(json.RawMessage(checklistDescription))
	if err != nil {
		t.Fatalf("Checklist() error = %v", err)
	}
	want := []ChecklistItem{{"bnc_busi", true}, {"bnc_iobus", false}, {"sump-vue", true}, {"", true}}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("Checklist() = %+v, want %+v", items, want)
	}
	checked, err := CheckedItems(json.RawMessage(checklistDescription))
	if err != nil || !reflect.DeepEqual(checked, []string{"bnc_busi", "sump-vue"}) {
		t.Fatalf("CheckedItems() = %q, %v", checked, err)
	}
}

func TestChecklistWithoutTasks(t *testing.T) {
	for _, raw := range []string{"", "null", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"no list"}]}]}`} {
		checked, err := CheckedItems(json.RawMessage(raw))
		if err != nil || len(checked) != 0 {
			t.Fatalf("CheckedItems(%q) = %q, %v, want none", raw, checked, err)
		}
	}
	if _, err := Checklist(json.RawMessage(`{"type":`)); err == nil {
		t.Fatal("Checklist() accepted invalid JSON")
	}
}
