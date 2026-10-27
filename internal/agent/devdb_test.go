package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func TestLoadDevDB(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "devdb.exe")
	if err := os.WriteFile(tool, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if db, err := loadDevDB(env(nil)); err != nil || db.enabled() {
		t.Fatalf("unset = %+v, %v; want no database access", db, err)
	}
	if _, err := loadDevDB(env(map[string]string{"DEV_DB_ENV_FILE": "dev.env", "DEV_DB_TOOL": tool})); err == nil || !strings.Contains(err.Error(), "DEV_DB_ADDRESS") {
		t.Fatalf("err = %v, want the missing address reported", err)
	}
	if _, err := loadDevDB(env(map[string]string{"DEV_DB_ENV_FILE": "dev.env", "DEV_DB_ADDRESS": "h:1", "DEV_DB_TOOL": tool + ".missing"})); err == nil {
		t.Fatal("a missing tool must be reported")
	}
	db, err := loadDevDB(env(map[string]string{"DEV_DB_ENV_FILE": "dev.env", "DEV_DB_ADDRESS": "h:15009", "DEV_DB_TOOL": tool}))
	if err != nil || !db.enabled() {
		t.Fatalf("db = %+v, %v", db, err)
	}
	if got := strings.Join(db.env(), ","); got != "DEVDB_ENV_FILE=dev.env,DEVDB_ADDRESS=h:15009" {
		t.Fatalf("env = %s", got)
	}
}

func TestParsePlanReadsTheSQLMigrationPlan(t *testing.T) {
	answer := "<<<OVERVIEW>>>\n# O\n<<<REPO bnc_busi | Batch>>>\n# B\n<<<SQL MIGRATION PLAN>>>\nAdds a table.\n\n## cbsdb\n\n### DDL\n\n```sql\nCREATE TABLE IF NOT EXISTS t (a int);\n```\n"
	plan, err := parsePlan(answer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.SQL, "Adds a table.") || !strings.Contains(plan.SQL, "## cbsdb") || !strings.HasSuffix(plan.SQL, "```") {
		t.Fatalf("SQL = %q", plan.SQL)
	}
	if prompt := repoPlanPrompt(workflow.PlanInput{CurrentOverview: "<p>o</p>", CurrentSQL: "<h2>cbsdb</h2>"}, DevDB{}); !strings.Contains(prompt, "<<<SQL MIGRATION PLAN>>>") || !strings.Contains(prompt, "Current SQL migration plan (Confluence storage format; reviewers may have edited the scripts on the page - keep their edits):\n<h2>cbsdb</h2>") {
		t.Fatalf("revision prompt = %q", prompt)
	}
}

func TestDevDBRulesAndPromptAreReadOnly(t *testing.T) {
	db := DevDB{Tool: `C:\agent\bin\devdb.exe`, EnvFile: `C:\creds\dev.env`, Address: "h:15009"}
	if rules := strings.Join(db.rules(), ","); rules != "Bash(C:/agent/bin/devdb.exe databases:*),Bash(C:/agent/bin/devdb.exe query:*)" {
		t.Fatalf("rules = %s", rules)
	}
	prompt := db.readSection()
	if strings.Contains(prompt, "dev.env") || !strings.Contains(prompt, "C:/agent/bin/devdb.exe query") || strings.Contains(prompt, "apply") {
		t.Fatalf("prompt = %q; want the read-only tool and never the credentials file", prompt)
	}
	if section := (DevDB{}).readSection(); strings.Contains(section, "devdb") || !strings.Contains(section, "no database access") {
		t.Fatalf("section without a database = %q", section)
	}
	if len((DevDB{}).rules()) != 0 || len((DevDB{}).env()) != 0 {
		t.Fatal("no database must add no rules or environment")
	}
}
