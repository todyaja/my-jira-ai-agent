// Package devdb lets the agents read and change the team's development
// database without ever seeing its credentials. Connections come from an env
// file of <PREFIX>DB_URL, <PREFIX>DB_USER and <PREFIX>DB_PASS entries, and
// every URL must point at the one address configured as development, so a
// file or entry for another environment is refused before connecting.
package devdb

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Connection is one database of the env file.
type Connection struct {
	// Database is the schema name from the URL, e.g. cbsdb.
	Database string
	Address  string
	User     string
	Password string
}

// LoadConnections reads the env file and returns its databases by name. Any
// URL whose host:port is not address is an error, so the tool cannot reach
// another environment.
func LoadConnections(path, address string) (map[string]Connection, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("no development database address is configured")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open credentials file: %w", err)
	}
	defer file.Close()
	values, err := parseEnv(file)
	if err != nil {
		return nil, err
	}
	connections := map[string]Connection{}
	for key, value := range values {
		prefix, isURL := strings.CutSuffix(key, "DB_URL")
		if !isURL {
			continue
		}
		connection, err := parseJDBC(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if !strings.EqualFold(connection.Address, address) {
			return nil, fmt.Errorf("%s points at %s, not the development database %s; refusing to use this file", key, connection.Address, address)
		}
		connection.User, connection.Password = values[prefix+"DB_USER"], values[prefix+"DB_PASS"]
		if connection.User == "" {
			return nil, fmt.Errorf("%sDB_USER is not set", prefix)
		}
		if _, duplicate := connections[strings.ToLower(connection.Database)]; duplicate {
			return nil, fmt.Errorf("database %s is listed twice", connection.Database)
		}
		connections[strings.ToLower(connection.Database)] = connection
	}
	if len(connections) == 0 {
		return nil, fmt.Errorf("the credentials file lists no <PREFIX>DB_URL")
	}
	return connections, nil
}

// Names lists the databases, sorted.
func Names(connections map[string]Connection) []string {
	names := make([]string, 0, len(connections))
	for _, connection := range connections {
		names = append(names, connection.Database)
	}
	sort.Strings(names)
	return names
}

func parseEnv(file *os.File) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}
	return values, nil
}

// parseJDBC reads jdbc:mysql://host:port/database?params.
func parseJDBC(raw string) (Connection, error) {
	parsed, err := url.Parse(strings.TrimPrefix(strings.TrimSpace(raw), "jdbc:"))
	if err != nil || parsed.Scheme != "mysql" || parsed.Host == "" {
		return Connection{}, fmt.Errorf("not a jdbc:mysql://host:port/database URL")
	}
	database := strings.Trim(parsed.Path, "/")
	if database == "" || strings.Contains(database, "/") {
		return Connection{}, fmt.Errorf("the URL names no database")
	}
	address := parsed.Host
	if parsed.Port() == "" {
		address += ":3306"
	}
	return Connection{Database: database, Address: address}, nil
}

// Statement kinds.
const (
	Read   = "read"
	Change = "change"
)

var (
	leadingKeyword = regexp.MustCompile(`^[A-Za-z]+`)
	// readKeywords start statements that change nothing.
	readKeywords = map[string]bool{"SELECT": true, "SHOW": true, "DESC": true, "DESCRIBE": true, "EXPLAIN": true, "WITH": true}
	// changeKeywords start the statements a migration script may run.
	changeKeywords = map[string]bool{"CREATE": true, "ALTER": true, "INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true}
	// createObjects are what CREATE may make: tables and indexes, never
	// users, schemas or code.
	createObject      = regexp.MustCompile(`(?i)^CREATE\s+(?:TABLE|INDEX|UNIQUE\s+INDEX)\b`)
	alterTable        = regexp.MustCompile(`(?i)^ALTER\s+TABLE\b`)
	whereClause       = regexp.MustCompile(`(?i)\bWHERE\b`)
	dropOrRename      = regexp.MustCompile(`(?i)\b(?:DROP|RENAME|TRUNCATE)\b`)
	executableComment = regexp.MustCompile(`/\*!`)
	lockingClause     = regexp.MustCompile(`(?i)\b(?:FOR\s+UPDATE|LOCK\s+IN\s+SHARE\s+MODE|INTO\s+OUTFILE|INTO\s+DUMPFILE|INTO\s+@)`)
)

// Classify says whether a statement only reads or is a change a migration
// may make, and refuses everything else: dropping, truncating or renaming
// anything, UPDATE or DELETE without WHERE, and statements other than table
// and index DDL and row changes. The check reads the statement with its
// string literals and comments blanked out, so text inside them does not
// count.
func Classify(statement string) (string, error) {
	code := strings.TrimSpace(blankLiterals(statement))
	keyword := strings.ToUpper(leadingKeyword.FindString(code))
	switch {
	case executableComment.MatchString(statement):
		// MySQL runs the text of /*! ... */, which the check cannot see.
		return "", fmt.Errorf("/*! ... */ comments are not allowed")
	case keyword == "":
		return "", fmt.Errorf("not a SQL statement")
	case readKeywords[keyword]:
		if lockingClause.MatchString(code) {
			return "", fmt.Errorf("locking reads and SELECT ... INTO are not allowed")
		}
		return Read, nil
	case !changeKeywords[keyword]:
		return "", fmt.Errorf("%s statements are not allowed; a migration may only create or alter tables and indexes and insert, update or delete rows", keyword)
	case dropOrRename.MatchString(code):
		return "", fmt.Errorf("dropping, truncating or renaming is not allowed on the development database; put it in the rollback section instead")
	case keyword == "CREATE" && !createObject.MatchString(code):
		return "", fmt.Errorf("only CREATE TABLE and CREATE INDEX are allowed")
	case keyword == "ALTER" && !alterTable.MatchString(code):
		return "", fmt.Errorf("only ALTER TABLE is allowed")
	case (keyword == "UPDATE" || keyword == "DELETE") && !whereClause.MatchString(code):
		return "", fmt.Errorf("%s without WHERE is not allowed", keyword)
	}
	return Change, nil
}

// blankLiterals replaces the contents of quoted strings, quoted identifiers
// and comments with spaces, keeping the statement's shape.
func blankLiterals(sql string) string {
	var out strings.Builder
	scan(sql, func(r rune, code bool) {
		if code {
			out.WriteRune(r)
		} else {
			out.WriteRune(' ')
		}
	}, nil)
	return out.String()
}

// Split cuts a script into statements at semicolons outside strings and
// comments, dropping empty ones and comment-only ones.
func Split(script string) []string {
	statements := []string{}
	var current strings.Builder
	hasCode := false
	flush := func() {
		if hasCode {
			statements = append(statements, strings.TrimSpace(current.String()))
		}
		current.Reset()
		hasCode = false
	}
	scan(script, func(r rune, code bool) {
		current.WriteRune(r)
		if code && !isSpace(r) {
			hasCode = true
		}
	}, func() {
		flush()
	})
	flush()
	return statements
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// scan walks sql, calling emit for every rune with whether it is code (not
// inside a string, quoted identifier or comment), and end, instead of emit,
// for every semicolon in code.
func scan(sql string, emit func(rune, bool), end func()) {
	runes := []rune(sql)
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		next := rune(0)
		if index+1 < len(runes) {
			next = runes[index+1]
		}
		switch {
		case r == '\'' || r == '"' || r == '`':
			emit(r, true)
			for index++; index < len(runes); index++ {
				if runes[index] == '\\' && r != '`' && index+1 < len(runes) {
					emit(runes[index], false)
					index++
					emit(runes[index], false)
					continue
				}
				if runes[index] == r {
					// A doubled quote is an escaped quote.
					if index+1 < len(runes) && runes[index+1] == r {
						emit(runes[index], false)
						index++
						emit(runes[index], false)
						continue
					}
					emit(r, true)
					break
				}
				emit(runes[index], false)
			}
		case r == '#' || (r == '-' && next == '-' && (index+2 >= len(runes) || isSpace(runes[index+2]))):
			for ; index < len(runes) && runes[index] != '\n'; index++ {
				emit(runes[index], false)
			}
			if index < len(runes) {
				emit('\n', true)
			}
		case r == '/' && next == '*':
			emit(r, false)
			emit(next, false)
			for index += 2; index < len(runes); index++ {
				if runes[index] == '*' && index+1 < len(runes) && runes[index+1] == '/' {
					emit('*', false)
					emit('/', false)
					index++
					break
				}
				emit(runes[index], false)
			}
		case r == ';' && end != nil:
			end()
		default:
			emit(r, true)
		}
	}
}
