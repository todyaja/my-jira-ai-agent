package repos

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// makeTree creates folders under a temp root; a path ending in "/.git/" is a
// repository and one ending in "/.git" is a worktree's .git file.
func makeTree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, path := range paths {
		full := filepath.Join(root, filepath.FromSlash(path))
		if strings.HasSuffix(path, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("gitdir: elsewhere"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiscoverFindsNestedRepositoriesOnly(t *testing.T) {
	root := makeTree(t,
		"bnc-sump-server/.git/",
		"bnc_busi_parent/bnc_busi/.git/",
		"bnc_busi_parent/bnc_busi-sit/.git", // worktree
		"bnc_busi_parent/wt/t01/.git",       // worktree
		"es/es-busi/.git/",
		"firstmate/.git/",
		"firstmate/projects/es-busi/.git/", // inside another repository
		"sump_vue_parent/sump-vue/.git/",
		"sump_vue_parent/sump-vue/node_modules/dep/.git/",
		"bnc_busi_parent/bnc_busi-worktrees/feature-NBS-1/.git/",
		"a/b/c/too-deep/.git/",
	)
	registry, err := Discover(root, "master", map[string]string{"BNC-SUMP-SERVER": "sit-merge-all"})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	want := []string{"bnc-sump-server", "bnc_busi", "es-busi", "firstmate", "sump-vue"}
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %q, want %q", got, want)
	}
	server, _ := registry.Lookup("bnc-sump-server")
	busi, _ := registry.Lookup("BNC_BUSI")
	if server.BaseBranch != "sit-merge-all" || busi.BaseBranch != "master" {
		t.Fatalf("base branches = (%q, %q), want the override and the default", server.BaseBranch, busi.BaseBranch)
	}
	if busi.Path != filepath.Join(root, "bnc_busi_parent", "bnc_busi") {
		t.Fatalf("bnc_busi path = %q", busi.Path)
	}
}

func TestDiscoverRejectsDuplicateNamesAndMissingRoot(t *testing.T) {
	root := makeTree(t, "team-a/api/.git/", "team-b/api/.git/")
	if _, err := Discover(root, "main", nil); err == nil || !strings.Contains(err.Error(), "two repositories are named api") {
		t.Fatalf("Discover() error = %v, want a duplicate name error", err)
	}
	if _, err := Discover(filepath.Join(root, "missing"), "main", nil); err == nil {
		t.Fatal("Discover() of a missing root succeeded")
	}
}

func TestSelect(t *testing.T) {
	registry, err := Discover(makeTree(t, "bnc_busi/.git/", "sump-vue/.git/", "bnc_iobus/.git/"), "master", nil)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := registry.Select([]string{" Sump-Vue ", "bnc_busi", "sump-vue"})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(selected) != 2 || selected[0].Name != "sump-vue" || selected[1].Name != "bnc_busi" {
		t.Fatalf("Select() = %+v, want sump-vue then bnc_busi once each", selected)
	}
	if _, err := registry.Select([]string{"bnc_busi", "qris-api"}); err == nil || !strings.Contains(err.Error(), "unknown repositories qris-api") || !strings.Contains(err.Error(), "bnc_busi, bnc_iobus, sump-vue") {
		t.Fatalf("Select() error = %v, want the unknown name and the known ones", err)
	}
}

func TestParseBaseBranches(t *testing.T) {
	got, err := ParseBaseBranches(" bnc-sump-server = sit-merge-all , ,sump-vue=develop")
	if err != nil || !reflect.DeepEqual(got, map[string]string{"bnc-sump-server": "sit-merge-all", "sump-vue": "develop"}) {
		t.Fatalf("ParseBaseBranches() = %v, %v", got, err)
	}
	if _, err := ParseBaseBranches("bnc_busi"); err == nil {
		t.Fatal("ParseBaseBranches() accepted an entry without a branch")
	}
}
