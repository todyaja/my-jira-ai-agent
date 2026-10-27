package devdb

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	maxRows        = 200
	statementLimit = 2 * time.Minute
)

// Runner runs scripts on the development database's databases.
type Runner struct {
	Connections map[string]Connection
}

// NewRunner loads the connections from the env file, refusing any not at
// address.
func NewRunner(envFile, address string) (*Runner, error) {
	connections, err := LoadConnections(envFile, address)
	if err != nil {
		return nil, err
	}
	return &Runner{Connections: connections}, nil
}

// Check splits the script and checks every statement without connecting:
// with readOnly only reads are allowed, otherwise anything Classify allows.
func (r *Runner) Check(database, script string, readOnly bool) ([]string, error) {
	if _, found := r.Connections[strings.ToLower(database)]; !found {
		return nil, fmt.Errorf("no database %s on the development server; the databases are %s", database, strings.Join(Names(r.Connections), ", "))
	}
	statements := Split(script)
	for index, statement := range statements {
		kind, err := Classify(statement)
		if err == nil && readOnly && kind != Read {
			err = fmt.Errorf("only SELECT, SHOW, DESCRIBE and EXPLAIN are allowed here")
		}
		if err != nil {
			return nil, fmt.Errorf("statement %d refused: %w\n%s", index+1, err, statement)
		}
	}
	return statements, nil
}

// Run checks the whole script, then runs it statement by statement on one
// connection, writing each result to out. A read-only run uses a read-only
// session, so the server refuses writes too. It stops at the first failure;
// DDL commits as it runs, so statements before it stay applied.
func (r *Runner) Run(ctx context.Context, database, script string, readOnly bool, out io.Writer) error {
	statements, err := r.Check(database, script, readOnly)
	if err != nil {
		return err
	}
	if len(statements) == 0 {
		return nil
	}
	db, err := open(r.Connections[strings.ToLower(database)])
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", database, err)
	}
	defer conn.Close()
	if readOnly {
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
			return fmt.Errorf("make the session read-only: %w", err)
		}
	}
	for index, statement := range statements {
		if len(statements) > 1 {
			fmt.Fprintf(out, "-- statement %d of %d\n", index+1, len(statements))
		}
		if err := execute(ctx, conn, statement, out); err != nil {
			if len(statements) > 1 {
				return fmt.Errorf("statement %d failed; statements before it were applied: %w", index+1, err)
			}
			return err
		}
	}
	return nil
}

func open(connection Connection) (*sql.DB, error) {
	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = connection.Address
	config.DBName = connection.Database
	config.User = connection.User
	config.Passwd = connection.Password
	config.Timeout = 10 * time.Second
	// The URL's parameters are JDBC settings, which this driver would send
	// as session variables, so none are passed on.
	config.MultiStatements = false
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return nil, fmt.Errorf("configure %s: %w", connection.Database, err)
	}
	return sql.OpenDB(connector), nil
}

func execute(ctx context.Context, conn *sql.Conn, statement string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, statementLimit)
	defer cancel()
	if kind, _ := Classify(statement); kind != Read {
		result, err := conn.ExecContext(ctx, statement)
		if err != nil {
			return err
		}
		affected, _ := result.RowsAffected()
		fmt.Fprintf(out, "OK, %d row(s) affected\n", affected)
		return nil
	}
	rows, err := conn.QueryContext(ctx, statement)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, strings.Join(columns, "\t"))
	values := make([]sql.NullString, len(columns))
	pointers := make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	count := 0
	for rows.Next() {
		if count == maxRows {
			fmt.Fprintf(out, "-- stopped at %d rows; narrow the query\n", maxRows)
			return nil
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		cells := make([]string, len(values))
		for index, value := range values {
			cells[index] = "NULL"
			if value.Valid {
				cells[index] = value.String
			}
		}
		fmt.Fprintln(out, strings.Join(cells, "\t"))
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Fprintf(out, "-- %d row(s)\n", count)
	return nil
}
