package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DevDB is the team's development database. Agents reach it only through
// the read-only devdb tool, which reads the credentials itself and refuses
// any other address; the workflow applies approved SQL plans itself. The
// zero value means no database access.
type DevDB struct {
	// Tool is the devdb executable.
	Tool string
	// EnvFile holds the <PREFIX>DB_URL, _USER and _PASS entries; the agents
	// are never told where it is.
	EnvFile string
	// Address is the development host:port every URL must use.
	Address string
}

func (d DevDB) enabled() bool {
	return d.Tool != "" && d.EnvFile != ""
}

// loadDevDB reads DEV_DB_ENV_FILE, DEV_DB_ADDRESS and DEV_DB_TOOL, which
// defaults to devdb beside this program.
func loadDevDB(getenv func(string) string) (DevDB, error) {
	db := DevDB{EnvFile: strings.TrimSpace(getenv("DEV_DB_ENV_FILE")), Address: strings.TrimSpace(getenv("DEV_DB_ADDRESS")), Tool: strings.TrimSpace(getenv("DEV_DB_TOOL"))}
	if db.EnvFile == "" {
		return DevDB{}, nil
	}
	if db.Address == "" {
		return DevDB{}, fmt.Errorf("DEV_DB_ADDRESS must be set to the development database's host:port when DEV_DB_ENV_FILE is")
	}
	if db.Tool == "" {
		executable, err := os.Executable()
		if err != nil {
			return DevDB{}, fmt.Errorf("find devdb: %w", err)
		}
		db.Tool = filepath.Join(filepath.Dir(executable), "devdb")
		if runtime.GOOS == "windows" {
			db.Tool += ".exe"
		}
	}
	if _, err := os.Stat(db.Tool); err != nil {
		return DevDB{}, fmt.Errorf("DEV_DB_TOOL: %w; build it with go build -o bin/ ./cmd/devdb", err)
	}
	if strings.ContainsAny(db.Tool, shellMetacharacters) {
		return DevDB{}, fmt.Errorf("DEV_DB_TOOL must be a plain path")
	}
	return db, nil
}

// env passes the credentials file and address to the tool through the
// agent's environment, which its commands cannot change.
func (d DevDB) env() []string {
	if !d.enabled() {
		return nil
	}
	return []string{"DEVDB_ENV_FILE=" + d.EnvFile, "DEVDB_ADDRESS=" + d.Address}
}

func (d DevDB) command() string {
	return slashPath(d.Tool)
}

// rules allow listing the databases and the read-only query; nothing an
// agent runs can change the database.
func (d DevDB) rules() []string {
	if !d.enabled() {
		return nil
	}
	return bashRules([]string{d.command() + " databases", d.command() + " query"})
}

// readSection tells an agent how to read the development database.
func (d DevDB) readSection() string {
	if !d.enabled() {
		return "\n\nThere is no database access in this run; base database work on the table definitions in the code."
	}
	return fmt.Sprintf(`

You can read the team's development database - never another environment - with:
- %[1]s databases - lists the databases
- %[1]s query <database> "<one SELECT, SHOW, DESCRIBE or EXPLAIN>" - read-only, at most 200 rows
Use it to read real DDL (SHOW CREATE TABLE of a similar table, for the column types, collation, primary key, shardkey and partitioning to copy) and to check the data a change depends on. You cannot change the database.`, d.command())
}
