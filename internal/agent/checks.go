package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Checks are the commands that verify a branch, worked out from its worktree
// before each engineer or reviewer run, so the agent knows exactly what to run.
type Checks struct {
	// Environment says where the checks run and why.
	Environment string
	// Setup installs missing dependencies before the checks; empty when
	// nothing needs installing.
	Setup []string
	// Commands run the checks, in order.
	Commands []string
}

// all returns every command the checks need, setup first.
func (c Checks) all() []string {
	return append(append([]string(nil), c.Setup...), c.Commands...)
}

// composeFiles are the file names Docker Compose looks for by default.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// dockerProbeTimeout bounds each docker command used to choose the checks.
const dockerProbeTimeout = 15 * time.Second

// checkDetector chooses the checks. Its docker probes are fields so tests
// can replace them.
type checkDetector struct {
	// dockerRunning reports whether the Docker daemon answers.
	dockerRunning func(ctx context.Context) bool
	// composeService names the compose service the checks run in, or ""
	// when there is no service that builds the project.
	composeService func(ctx context.Context, dir string) string
}

func newCheckDetector() checkDetector {
	return checkDetector{dockerRunning: dockerRunning, composeService: composeService}
}

// detect runs the checks in the repository's Docker Compose service when it
// has one and Docker is running, and on the host otherwise.
func (d checkDetector) detect(ctx context.Context, dir string) Checks {
	local := localChecks(dir)
	if !hasComposeFile(dir) {
		local.Environment = "on this machine (the repository has no Docker Compose setup)"
		return local
	}
	if !d.dockerRunning(ctx) {
		local.Environment = "on this machine (the repository has a Docker Compose setup, but Docker is not running)"
		return local
	}
	service := d.composeService(ctx, dir)
	if service == "" {
		local.Environment = "on this machine (no Docker Compose service builds the project)"
		return local
	}
	// The image installs the dependencies, so the container needs no setup.
	docker := Checks{Environment: "in Docker, in the " + service + " Docker Compose service"}
	for _, command := range local.Commands {
		docker.Commands = append(docker.Commands, "docker compose run --rm -T "+service+" "+command)
	}
	return docker
}

// localChecks reads the checks from the project's manifest: package.json
// scripts for Node, the standard tools for Go.
func localChecks(dir string) Checks {
	if fileExists(filepath.Join(dir, "package.json")) {
		return nodeChecks(dir)
	}
	if fileExists(filepath.Join(dir, "go.mod")) {
		return Checks{Commands: []string{"go build ./...", "go vet ./...", "go test ./..."}}
	}
	return Checks{}
}

// nodeChecks runs the test, lint, type check and build scripts the project
// defines, with the package manager its lockfile belongs to.
func nodeChecks(dir string) Checks {
	run, install, binary := "npm run ", "npm install", "npx "
	switch {
	case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
		run, install, binary = "pnpm run ", "pnpm install --frozen-lockfile", "pnpm exec "
	case fileExists(filepath.Join(dir, "yarn.lock")):
		run, install, binary = "yarn run ", "yarn install --frozen-lockfile", "yarn "
	case fileExists(filepath.Join(dir, "package-lock.json")):
		install = "npm ci"
	}

	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &manifest)
	}

	checks := Checks{}
	if !fileExists(filepath.Join(dir, "node_modules")) {
		checks.Setup = []string{install}
	}
	typeChecked := false
	for _, script := range []string{"test", "lint", "typecheck", "type-check", "build"} {
		if _, ok := manifest.Scripts[script]; !ok {
			continue
		}
		checks.Commands = append(checks.Commands, run+script)
		typeChecked = typeChecked || strings.HasPrefix(script, "type")
	}
	if !typeChecked && fileExists(filepath.Join(dir, "tsconfig.json")) {
		checks.Commands = append(checks.Commands, binary+"tsc --noEmit")
	}
	return checks
}

func hasComposeFile(dir string) bool {
	for _, name := range composeFiles {
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dockerRunning(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, dockerProbeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run() == nil
}

// composeService picks the service that builds the project: the only one with
// a build section, or the first by name when several have one.
func composeService(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, dockerProbeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "compose", "config", "--format", "json")
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return buildService(output)
}

func buildService(config []byte) string {
	var parsed struct {
		Services map[string]struct {
			Build json.RawMessage `json:"build"`
		} `json:"services"`
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		return ""
	}
	names := []string{}
	for name, service := range parsed.Services {
		if len(service.Build) > 0 && string(service.Build) != "null" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[0]
}
