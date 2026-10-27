// Command devdb is how the agents use the team's development database. It
// reads the credentials itself, from DEVDB_ENV_FILE, and refuses any
// database not at DEVDB_ADDRESS, so an agent can neither see a password nor
// reach another environment.
//
//	devdb databases                    list the databases
//	devdb query <database> "<sql>"     run one read-only statement
//
// Approved SQL migration plans are applied by the workflow itself, never by
// an agent.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tody-aja/jira-ai-agent/internal/devdb"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "devdb:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	runner, err := devdb.NewRunner(os.Getenv("DEVDB_ENV_FILE"), os.Getenv("DEVDB_ADDRESS"))
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "databases" {
		fmt.Fprintln(out, strings.Join(devdb.Names(runner.Connections), "\n"))
		return nil
	}
	if len(args) != 3 || args[0] != "query" {
		return fmt.Errorf(`usage: devdb databases | devdb query <database> "<sql>"`)
	}
	if statements := devdb.Split(args[2]); len(statements) != 1 {
		return fmt.Errorf("query runs exactly one statement")
	}
	return runner.Run(context.Background(), args[1], args[2], true, out)
}
