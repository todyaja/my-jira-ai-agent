package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fakeDetector(running bool, service string) checkDetector {
	return checkDetector{
		dockerRunning:  func(context.Context) bool { return running },
		composeService: func(context.Context, string) string { return service },
	}
}

const nodeManifest = `{"scripts": {"dev": "next dev", "build": "next build", "lint": "eslint", "test": "vitest run"}}`

func TestDetectChecksChoosesWhereChecksRun(t *testing.T) {
	withCompose := writeFiles(t, map[string]string{"package.json": nodeManifest, "package-lock.json": "{}", "tsconfig.json": "{}", "docker-compose.yml": "services: {}"})
	local := []string{"npm run test", "npm run lint", "npm run build", "npx tsc --noEmit"}

	tests := []struct {
		name            string
		dir             string
		detector        checkDetector
		wantEnvironment string
		wantSetup       []string
		wantCommands    []string
	}{
		{
			name:            "docker running",
			dir:             withCompose,
			detector:        fakeDetector(true, "todo-app"),
			wantEnvironment: "in Docker, in the todo-app Docker Compose service",
			wantCommands: []string{
				"docker compose run --rm -T todo-app npm run test",
				"docker compose run --rm -T todo-app npm run lint",
				"docker compose run --rm -T todo-app npm run build",
				"docker compose run --rm -T todo-app npx tsc --noEmit",
			},
		},
		{
			name:            "docker not running",
			dir:             withCompose,
			detector:        fakeDetector(false, "todo-app"),
			wantEnvironment: "Docker is not running",
			wantSetup:       []string{"npm ci"},
			wantCommands:    local,
		},
		{
			name:            "no service builds the project",
			dir:             withCompose,
			detector:        fakeDetector(true, ""),
			wantEnvironment: "no Docker Compose service builds the project",
			wantSetup:       []string{"npm ci"},
			wantCommands:    local,
		},
		{
			name:            "no compose file",
			dir:             writeFiles(t, map[string]string{"package.json": nodeManifest, "package-lock.json": "{}", "tsconfig.json": "{}", "node_modules/.keep": ""}),
			detector:        fakeDetector(true, "todo-app"),
			wantEnvironment: "no Docker Compose setup",
			wantCommands:    local,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.detector.detect(context.Background(), test.dir)
			if !strings.Contains(got.Environment, test.wantEnvironment) {
				t.Fatalf("Environment = %q, want it to contain %q", got.Environment, test.wantEnvironment)
			}
			if !slices.Equal(got.Setup, test.wantSetup) || !slices.Equal(got.Commands, test.wantCommands) {
				t.Fatalf("checks = (setup %q, commands %q), want (%q, %q)", got.Setup, got.Commands, test.wantSetup, test.wantCommands)
			}
		})
	}
}

func TestLocalChecksFollowTheProject(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantSetup    []string
		wantCommands []string
	}{
		{
			name:         "pnpm with a typecheck script",
			files:        map[string]string{"package.json": `{"scripts": {"test": "vitest", "typecheck": "tsc"}}`, "pnpm-lock.yaml": "", "tsconfig.json": "{}"},
			wantSetup:    []string{"pnpm install --frozen-lockfile"},
			wantCommands: []string{"pnpm run test", "pnpm run typecheck"},
		},
		{
			name:         "yarn without TypeScript",
			files:        map[string]string{"package.json": `{"scripts": {"lint": "eslint"}}`, "yarn.lock": ""},
			wantSetup:    []string{"yarn install --frozen-lockfile"},
			wantCommands: []string{"yarn run lint"},
		},
		{
			name:      "npm without a lockfile or scripts",
			files:     map[string]string{"package.json": `{}`},
			wantSetup: []string{"npm install"},
		},
		{
			name:         "go module",
			files:        map[string]string{"go.mod": "module example"},
			wantCommands: []string{"go build ./...", "go vet ./...", "go test ./..."},
		},
		{
			name:         "maven project",
			files:        map[string]string{"pom.xml": "<project/>"},
			wantCommands: []string{"mvn -q -B test"},
		},
		{
			name:  "unknown project",
			files: map[string]string{"README.md": "hello"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := localChecks(writeFiles(t, test.files))
			if !slices.Equal(got.Setup, test.wantSetup) || !slices.Equal(got.Commands, test.wantCommands) {
				t.Fatalf("localChecks() = (setup %q, commands %q), want (%q, %q)", got.Setup, got.Commands, test.wantSetup, test.wantCommands)
			}
		})
	}
}

func TestBuildServicePicksTheServiceThatBuilds(t *testing.T) {
	tests := map[string]string{
		`{"services": {"db": {"image": "postgres"}, "web": {"build": {"context": "."}}}}`: "web",
		`{"services": {"worker": {"build": {}}, "api": {"build": {}}}}`:                    "api",
		`{"services": {"db": {"image": "postgres"}}}`:                                      "",
		`not json`: "",
	}
	for config, want := range tests {
		if got := buildService([]byte(config)); got != want {
			t.Fatalf("buildService(%s) = %q, want %q", config, got, want)
		}
	}
}
