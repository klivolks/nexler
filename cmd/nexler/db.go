// This file implements `nexler db add <name> -type <mongo|mysql|postgres|
// mssql> [-dir <app-dir>] [-host] [-port] [-dbname] [-user] [-password]` —
// see internal/scaffold/db.go for the actual logic. Deliberately a
// separate top-level command from both `nexler init db` (provisions the
// core schema on a live database connection — a different thing) and the
// existing `nexler add <package>` (vendors npm packages — unrelated).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

// allowedDBTypesCLI mirrors internal/scaffold's own (unexported)
// allowedDBTypes — kept as a small separate copy here so runDbAdd can
// fail fast on an invalid -type before prompting for connection details,
// rather than only discovering it deep inside scaffold.AddDB after
// asking several more questions nobody's going to use.
var allowedDBTypesCLI = map[string]bool{
	"mongo": true, "mysql": true, "postgres": true, "mssql": true,
}

// runDb dispatches `nexler db ...`. Only "add" exists today — room for a
// future "db list"/"db remove", mirroring runInit's own two-level
// dispatch shape.
func runDb(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "nexler: usage: nexler db add <name> -type <mongo|mysql|postgres|mssql> [-dir <app-dir>]")
		os.Exit(1)
	}
	switch args[0] {
	case "add":
		runDbAdd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "nexler: unknown resource %q for db\n\nsupported: add\n", args[0])
		os.Exit(1)
	}
}

func runDbAdd(args []string) {
	fs := flag.NewFlagSet("db add", flag.ExitOnError)
	dbType := fs.String("type", "", "database type for this connection: mongo, mysql, postgres, or mssql")
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	host := fs.String("host", "", "database host; blank (default) leaves .env's DSN blank for you to fill in by hand")
	port := fs.String("port", "", "database port; defaults to the type's conventional port (3306/5432/1433/27017) when -host is set")
	dbName := fs.String("dbname", "", "database name")
	user := fs.String("user", "", "database username (blank = no auth)")
	password := fs.String("password", "", "database password (blank = no auth; passed/prompted in plain text, never masked)")
	fs.Parse(reorderFlagsFirst(fs, args))

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	name := ""
	if fs.NArg() >= 1 {
		name = fs.Arg(0)
	} else {
		name = promptRequired("Connection name (e.g. analytics)")
	}

	typeVal := *dbType
	if !set["type"] {
		typeVal = promptChoice("Database type", []string{"mongo", "mysql", "postgres", "mssql"}, typeVal)
	} else if !allowedDBTypesCLI[typeVal] {
		fmt.Fprintf(os.Stderr, "nexler: unsupported -type %q (supported: mongo, mysql, postgres, mssql)\n", typeVal)
		os.Exit(1)
	}

	dirVal := *dir
	if !set["dir"] {
		dirVal = prompt("App directory", dirVal)
	}

	// Fail fast on a reserved/invalid/already-taken name before asking
	// five more questions about connection details nobody's going to use.
	if err := scaffold.CheckDBConnectionName(dirVal, name); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	// Same "blank host = blank DSN, fill in by hand later" convention as
	// `create app -db-host` — see that flow's own comment for why every
	// prompt after host is gated on host being non-blank.
	hostVal := *host
	if !set["host"] {
		hostVal = prompt("Database host (blank = leave DSN blank, fill in .env yourself later)", hostVal)
	}
	portVal, dbNameVal, userVal, passwordVal := *port, *dbName, *user, *password
	if hostVal != "" {
		if !set["port"] {
			portVal = prompt("Database port", scaffold.DefaultDBPort(typeVal))
		}
		if !set["dbname"] {
			dbNameVal = prompt("Database name", dbNameVal)
		}
		if !set["user"] {
			userVal = prompt("Database username (blank = no auth)", userVal)
		}
		if !set["password"] {
			passwordVal = prompt("Database password (blank = no auth; typed in PLAIN TEXT, not masked)", passwordVal)
		}
	}

	cfg := scaffold.DBAddConfig{
		AppDir:   dirVal,
		Name:     name,
		DBType:   typeVal,
		Host:     hostVal,
		Port:     portVal,
		DBName:   dbNameVal,
		User:     userVal,
		Password: passwordVal,
	}

	if err := scaffold.AddDB(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Added connection %q (%s) to %s/.env.\n", name, typeVal, dirVal)
	if hostVal != "" {
		fmt.Println("DSN written from the details you gave.")
	} else {
		fmt.Println("DSN left blank — fill it in yourself, or re-run with -host.")
	}
	accessor := "SQL"
	if typeVal == "mongo" {
		accessor = "Mongo"
	}
	fmt.Printf("Reachable in code as db.%s(%q) — never promoted to \"core\".\n", accessor, name)
}
