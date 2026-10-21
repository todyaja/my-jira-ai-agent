// Package repos finds the team's repositories under one folder, so a ticket
// can name the ones it changes by folder name.
package repos

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxDepth is how many folders deep under the root repositories are looked
// for, e.g. root/es/es-busi is depth 2.
const MaxDepth = 3

// Repo is one repository the agents may read or change.
type Repo struct {
	// Name is the repository's folder name, as tickets list it.
	Name string
	// Path is the absolute path of its main checkout.
	Path string
	// BaseBranch is the branch implementation branches start from and merge
	// requests target.
	BaseBranch string
}

// Registry is the set of repositories found under Root.
type Registry struct {
	Root  string
	repos map[string]Repo
}

// Discover finds every git repository under root, up to MaxDepth folders
// deep, without looking inside a repository for more. A folder whose .git is
// a file is a worktree or submodule of another repository and is skipped.
// Every repository's base branch is baseBranch unless baseBranches names
// one for it.
func Discover(root, baseBranch string, baseBranches map[string]string) (*Registry, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve REPOS_ROOT: %w", err)
	}
	if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("REPOS_ROOT %q is not a directory", absolute)
	}
	registry := &Registry{Root: absolute, repos: map[string]Repo{}}
	if err := registry.walk(absolute, 0); err != nil {
		return nil, err
	}
	for name, repo := range registry.repos {
		repo.BaseBranch = baseBranch
		for configured, branch := range baseBranches {
			if strings.EqualFold(configured, repo.Name) {
				repo.BaseBranch = branch
			}
		}
		registry.repos[name] = repo
	}
	return registry, nil
}

func (r *Registry) walk(dir string, depth int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // an unreadable folder holds no repositories we can use
	}
	for _, entry := range entries {
		if !entry.IsDir() || skipFolder(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if info, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			if info.IsDir() {
				if err := r.add(Repo{Name: entry.Name(), Path: path}); err != nil {
					return err
				}
			}
			continue
		}
		if depth+1 < MaxDepth {
			if err := r.walk(path, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// skipFolder leaves out folders that never hold the team's repositories:
// hidden ones, dependencies and the agents' own worktrees.
func skipFolder(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "target" || strings.HasSuffix(name, "-worktrees")
}

func (r *Registry) add(repo Repo) error {
	key := strings.ToLower(repo.Name)
	if existing, found := r.repos[key]; found {
		return fmt.Errorf("two repositories are named %s (%s and %s); tickets name repositories by folder name, so rename or move one", repo.Name, existing.Path, repo.Path)
	}
	r.repos[key] = repo
	return nil
}

// Names lists every repository name, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.repos))
	for _, repo := range r.repos {
		names = append(names, repo.Name)
	}
	sort.Strings(names)
	return names
}

// All returns every repository, sorted by name.
func (r *Registry) All() []Repo {
	all := make([]Repo, 0, len(r.repos))
	for _, name := range r.Names() {
		all = append(all, r.repos[strings.ToLower(name)])
	}
	return all
}

// Lookup finds a repository by name, ignoring case.
func (r *Registry) Lookup(name string) (Repo, bool) {
	repo, found := r.repos[strings.ToLower(strings.TrimSpace(name))]
	return repo, found
}

// Select returns the named repositories in the order given, without
// duplicates. Every name must be a known repository.
func (r *Registry) Select(names []string) ([]Repo, error) {
	selected := []Repo{}
	seen := map[string]bool{}
	unknown := []string{}
	for _, name := range names {
		repo, found := r.Lookup(name)
		if !found {
			unknown = append(unknown, strings.TrimSpace(name))
			continue
		}
		if !seen[repo.Name] {
			seen[repo.Name] = true
			selected = append(selected, repo)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown repositories %s; the repositories under %s are %s", strings.Join(unknown, ", "), r.Root, strings.Join(r.Names(), ", "))
	}
	return selected, nil
}

// ParseBaseBranches reads "name=branch" pairs separated by commas, such as
// REPO_BASE_BRANCHES="bnc-sump-server=sit-merge-all,sump-vue=develop".
func ParseBaseBranches(value string) (map[string]string, error) {
	branches := map[string]string{}
	for _, pair := range strings.Split(value, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, branch, ok := strings.Cut(pair, "=")
		name, branch = strings.TrimSpace(name), strings.TrimSpace(branch)
		if !ok || name == "" || branch == "" {
			return nil, fmt.Errorf("REPO_BASE_BRANCHES entries must look like name=branch, got %q", pair)
		}
		branches[name] = branch
	}
	return branches, nil
}
