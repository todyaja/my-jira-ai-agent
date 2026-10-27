package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

// SQLRunner runs scripts on the development database. CheckSQL checks a
// script without connecting; RunSQL checks and runs it and returns what it
// printed, up to a failure. readOnly allows reads only.
type SQLRunner interface {
	CheckSQL(database, script string, readOnly bool) error
	RunSQL(ctx context.Context, database, script string, readOnly bool) (string, error)
}

// The parts of each database's section of a SQL migration plan, in the
// order they are written and run. The rollback is never run.
const (
	sqlPreCheck = "pre-check"
	sqlDDL      = "ddl"
	sqlDML      = "dml"
	sqlVerify   = "verify"
	sqlRollback = "rollback"
)

// sqlDatabasePlan is one database's section of a SQL migration plan.
type sqlDatabasePlan struct {
	Database string
	Parts    map[string]string
}

func (p sqlDatabasePlan) forward() string {
	return joinScripts(p.Parts[sqlDDL], p.Parts[sqlDML])
}

var (
	// storageBlock finds the headings and code blocks of a Confluence page in
	// storage format, in order.
	storageBlock = regexp.MustCompile(`(?s)<h([1-6])[^>]*>(.*?)</h[1-6]>|<ac:plain-text-body><!\[CDATA\[(.*?)\]\]></ac:plain-text-body>`)
	storageTag   = regexp.MustCompile(`<[^>]+>`)
	notLetters   = regexp.MustCompile(`[^a-z]+`)
)

// sqlPart names the part a "### " heading opens, or "".
func sqlPart(heading string) string {
	switch notLetters.ReplaceAllString(strings.ToLower(heading), "") {
	case "precheck", "precheckqueries":
		return sqlPreCheck
	case "ddl":
		return sqlDDL
	case "dml":
		return sqlDML
	case "verify", "verification":
		return sqlVerify
	case "rollback":
		return sqlRollback
	}
	return ""
}

// parseSQLPlan reads the approved SQL migration plan page: each "## "
// heading names a database, each "### " heading under it a part, and the
// code blocks under a part are its script. It reads the page as published,
// so edits the reviewers made on the page are what runs. Sections without
// code, such as a summary, are left out.
func parseSQLPlan(storage string) []sqlDatabasePlan {
	plans := []sqlDatabasePlan{}
	var current *sqlDatabasePlan
	part := ""
	for _, match := range storageBlock.FindAllStringSubmatch(storage, -1) {
		if match[1] != "" {
			heading := strings.TrimSpace(html.UnescapeString(storageTag.ReplaceAllString(match[2], "")))
			switch match[1] {
			case "1":
				current, part = nil, ""
			case "2":
				plans = append(plans, sqlDatabasePlan{Database: strings.Trim(heading, "`"), Parts: map[string]string{}})
				current, part = &plans[len(plans)-1], ""
			case "3":
				part = sqlPart(heading)
			}
			continue
		}
		if current == nil || part == "" {
			continue
		}
		code := strings.ReplaceAll(match[3], "]]]]><![CDATA[>", "]]>")
		current.Parts[part] = joinScripts(current.Parts[part], code)
	}
	withCode := []sqlDatabasePlan{}
	for _, plan := range plans {
		if len(plan.Parts) > 0 {
			withCode = append(withCode, plan)
		}
	}
	return withCode
}

func joinScripts(scripts ...string) string {
	kept := []string{}
	for _, script := range scripts {
		if script = strings.TrimSpace(script); script != "" {
			kept = append(kept, strings.TrimRight(script, ";")+";")
		}
	}
	return strings.Join(kept, "\n")
}

// sqlPlanHash identifies what a plan changes, so an unchanged approved plan
// is not applied twice.
func sqlPlanHash(plans []sqlDatabasePlan) string {
	hash := sha256.New()
	for _, plan := range plans {
		fmt.Fprintf(hash, "%s\x00%s\x00", strings.ToLower(plan.Database), plan.forward())
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// maxSQLOutput keeps one script's output from flooding the Jira comment.
const maxSQLOutput = 3000

// applySQLPlan runs the approved SQL migration plan on the development
// database before implementation starts: for each database in the plan's
// order, the pre-check, then the DDL and DML, then the verify queries. Every
// script is checked before any runs. The results are posted on the issue,
// also when a script fails, and the plan is recorded as applied so it is
// not run again unless the reviewers change it.
func applySQLPlan(ctx context.Context, key string, deps Dependencies, documents documentsState) error {
	if documents.SQL == nil {
		return nil
	}
	content, err := pageContent(ctx, deps.Bank.Publisher, documents.SQL.Title)
	if err != nil {
		return err
	}
	plans := parseSQLPlan(content)
	changes := false
	for _, plan := range plans {
		changes = changes || plan.forward() != ""
	}
	if !changes {
		return nil
	}
	hash := sqlPlanHash(plans)
	if hash == documents.SQLApplied {
		if deps.Logger != nil {
			deps.Logger.Printf("SQL plan already applied on the development database: issue=%s", key)
		}
		return nil
	}
	runner := deps.Bank.SQL
	if runner == nil {
		return fmt.Errorf("the approved SQL migration plan cannot be applied: no development database is configured (DEV_DB_ENV_FILE)")
	}

	type step struct {
		database, part, script string
		readOnly               bool
	}
	steps := []step{}
	for _, plan := range plans {
		for _, part := range []string{sqlPreCheck, sqlDDL, sqlDML, sqlVerify} {
			if script := plan.Parts[part]; script != "" {
				steps = append(steps, step{database: plan.Database, part: part, script: script, readOnly: part == sqlPreCheck || part == sqlVerify})
			}
		}
	}
	for _, step := range steps {
		if err := runner.CheckSQL(step.database, step.script, step.readOnly); err != nil {
			return fmt.Errorf("%s %s: %w; nothing was run, fix the SQL migration plan and request the TRD again", step.database, step.part, err)
		}
	}

	report := []string{"# SQL migration plan on the development database", "", fmt.Sprintf("Ran the approved [%s](%s): pre-check, DDL, DML and verify for each database. The rollback was not run.", documents.SQL.Title, documents.SQL.URL)}
	var failure error
	for _, step := range steps {
		output, err := runner.RunSQL(ctx, step.database, step.script, step.readOnly)
		if runes := []rune(output); len(runes) > maxSQLOutput {
			output = string(runes[:maxSQLOutput]) + "\n... (cut)"
		}
		report = append(report, "", fmt.Sprintf("## %s - %s", step.database, step.part), "", "```", strings.TrimSpace(output), "```")
		if err != nil {
			failure = fmt.Errorf("%s %s failed: %w", step.database, step.part, err)
			report = append(report, "", "**Failed:** "+err.Error(), "", "Fix the SQL migration plan and request the TRD again; scripts must be safe to run twice.")
			break
		}
	}
	if _, err := deps.Writer.AddComment(ctx, key, jira.MarkdownADF(strings.Join(report, "\n"))); err != nil {
		return fmt.Errorf("comment: %w", err)
	}
	if failure != nil {
		return failure
	}
	documents.SQLApplied = hash
	if err := saveDocumentsState(ctx, deps.Writer, key, documents); err != nil {
		return err
	}
	if deps.Logger != nil {
		deps.Logger.Printf("SQL plan applied on the development database: issue=%s databases=%d", key, len(plans))
	}
	return nil
}

// noSQLChanges reports whether the planner said the change needs no
// database change.
func noSQLChanges(plan string) bool {
	plan = strings.ToLower(strings.Trim(strings.TrimSpace(plan), ".*_ "))
	return plan == "" || plan == "none" || plan == "n/a" || strings.HasPrefix(plan, "none.") || strings.HasPrefix(plan, "no database change")
}
