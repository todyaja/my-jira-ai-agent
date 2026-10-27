package devdb

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dev.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConnectionsReadsEveryDatabase(t *testing.T) {
	path := writeEnv(t, "# dev\nCBS_DB_URL=jdbc:mysql://10.0.0.5:15009/cbsdb?useSSL=false\nCBS_DB_USER=cbs_rw\nCBS_DB_PASS='s3cret'\n\nCT_DB_URL=jdbc:mysql://10.0.0.5:15009/ctdb\nCT_DB_USER=ct_rw\nCT_DB_PASS=x\n")
	connections, err := LoadConnections(path, "10.0.0.5:15009")
	if err != nil {
		t.Fatal(err)
	}
	if got := Names(connections); !reflect.DeepEqual(got, []string{"cbsdb", "ctdb"}) {
		t.Fatalf("names = %v", got)
	}
	cbs := connections["cbsdb"]
	if cbs.User != "cbs_rw" || cbs.Password != "s3cret" || cbs.Address != "10.0.0.5:15009" {
		t.Fatalf("cbsdb = %+v", cbs)
	}
}

func TestLoadConnectionsRefusesAnotherEnvironment(t *testing.T) {
	path := writeEnv(t, "CBS_DB_URL=jdbc:mysql://10.0.0.5:15002/cbsdb\nCBS_DB_USER=cbs_rw\nCBS_DB_PASS=x\n")
	_, err := LoadConnections(path, "10.0.0.5:15009")
	if err == nil || !strings.Contains(err.Error(), "not the development database") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "x\n") {
		t.Fatal("the error must not echo the password")
	}
	if _, err := LoadConnections(path, ""); err == nil {
		t.Fatal("an empty development address must be refused")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		sql     string
		kind    string
		refused string
	}{
		{sql: "SELECT * FROM t WHERE a = 'DROP TABLE x'", kind: Read},
		{sql: "show create table cfb_agent", kind: Read},
		{sql: "DESCRIBE t", kind: Read},
		{sql: "/* note */ SELECT 1", kind: Read},
		{sql: "SELECT * FROM t FOR UPDATE", refused: "locking"},
		{sql: "CREATE TABLE IF NOT EXISTS t (a int)", kind: Change},
		{sql: "CREATE UNIQUE INDEX i ON t (a)", kind: Change},
		{sql: "ALTER TABLE t ADD COLUMN b varchar(23)", kind: Change},
		{sql: "INSERT INTO t SELECT 'x' FROM dual WHERE NOT EXISTS (SELECT 1 FROM t)", kind: Change},
		{sql: "UPDATE t SET a = 1 WHERE b = 2", kind: Change},
		{sql: "DELETE FROM t WHERE a = 'drop'", kind: Change},
		{sql: "UPDATE t SET a = 1", refused: "without WHERE"},
		{sql: "DELETE FROM t -- WHERE a = 1", refused: "without WHERE"},
		{sql: "DROP TABLE t", refused: "DROP"},
		{sql: "TRUNCATE TABLE t", refused: "TRUNCATE"},
		{sql: "ALTER TABLE t DROP COLUMN a", refused: "dropping"},
		{sql: "ALTER TABLE t RENAME TO u", refused: "dropping"},
		{sql: "CREATE USER u", refused: "CREATE TABLE"},
		{sql: "CREATE DATABASE d", refused: "CREATE TABLE"},
		{sql: "ALTER USER u", refused: "ALTER TABLE"},
		{sql: "GRANT ALL ON *.* TO u", refused: "GRANT"},
		{sql: "SET GLOBAL x = 1", refused: "SET"},
		{sql: "SELECT 1 /*! , (SELECT 1) */", refused: "/*!"},
		{sql: "-- only a comment", refused: "not a SQL"},
	}
	for _, test := range cases {
		kind, err := Classify(test.sql)
		if test.refused != "" {
			if err == nil || !strings.Contains(err.Error(), test.refused) {
				t.Errorf("%q: err = %v, want refusal mentioning %q", test.sql, err, test.refused)
			}
			continue
		}
		if err != nil || kind != test.kind {
			t.Errorf("%q: kind = %q err = %v, want %q", test.sql, kind, err, test.kind)
		}
	}
}

func TestSplitKeepsSemicolonsInsideStringsAndComments(t *testing.T) {
	script := "-- PRE-CHECK; nothing here\nSELECT 'a;b' FROM t;\n/* x; y */\nINSERT INTO t VALUES ('it''s; ok', \"q\\\";\");\n# trailing; comment\n;\n"
	got := Split(script)
	want := []string{"-- PRE-CHECK; nothing here\nSELECT 'a;b' FROM t", "/* x; y */\nINSERT INTO t VALUES ('it''s; ok', \"q\\\";\")"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Split = %#v\nwant %#v", got, want)
	}
}
