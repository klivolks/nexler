// This file implements `nexler db add <name> -type <mongo|mysql|postgres|
// mssql> [-dir <app-dir>] [-host] [-port] [-dbname] [-user] [-password]` —
// adding a new named database connection to an *existing* generated app,
// retrofitting driver support for a type the app wasn't originally
// scaffolded with via `-db` if needed.
//
// Deliberately not named anything with "init db" in it — that's a
// different, existing command (`nexler init db`) that provisions the
// core schema on a live database connection. This command never opens a
// live database connection itself: it only ever edits .env and, when
// retrofitting, adds new Go source files or patches existing ones.
package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	dbConnectTmpl = "templates/db/db.go.tmpl"
	dbSQLTmpl     = "templates/db/sql.go.tmpl"
	dbMongoTmpl   = "templates/db/mongo.go.tmpl"

	mysqlHelperTmpl    = "templates/mysql/mysql.go.tmpl"
	postgresHelperTmpl = "templates/postgres/postgres.go.tmpl"
	mssqlHelperTmpl    = "templates/mssql/mssql.go.tmpl"
	mongoHelperTmpl    = "templates/mongo/mongo.go.tmpl"
)

// dbConnectMarker/dbCloseMarker are the two marker comments db/db.go.tmpl
// carries (see that file) so AddDB can insert a missing dialect-family's
// case into an existing app's db/db.go without touching anything else in
// the file — the same idempotent-marker-insertion precedent route.go
// already established for handler/model/aggregator files.
const (
	dbConnectMarker = "\t\t// nexler:db-connect (do not remove this marker)"
	dbCloseMarker   = "\t// nexler:db-close (do not remove this marker)"
)

// The exact fragments inserted before each marker — small literal
// constants rather than rendered templates, since there are only ever
// two possible fragments per marker (the SQL case, or the mongo case),
// not per-call-site-generated text the way route handlers need.
const (
	dbConnectSQLCase   = "\t\tcase \"mysql\", \"postgres\", \"mssql\":\n\t\t\terr = openSQL(name, dbType, dsn)\n"
	dbConnectMongoCase = "\t\tcase \"mongo\":\n\t\t\terr = openMongo(name, dsn)\n"
	dbCloseSQLLine     = "\terrs = append(errs, closeSQL())\n"
	dbCloseMongoLine   = "\terrs = append(errs, closeMongo())\n"
)

// DBAddConfig holds the parameters for `nexler db add`.
type DBAddConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
	// Name is the new connection's name, e.g. "analytics" — reachable
	// afterward as db.SQL("analytics")/db.Mongo("analytics"). Must not be
	// "core" (reserved for the app's own conventional default connection)
	// and must not already exist.
	Name string
	// DBType is one of "mongo", "mysql", "postgres", "mssql". If the app
	// wasn't scaffolded with this type originally, AddDB retrofits driver
	// support for it first.
	DBType string
	// Host, if non-empty, causes AddDB to build a real DSN (via
	// buildDSN) and write it to .env instead of leaving the new
	// connection's _DSN blank — same "blank host = blank DSN, fill in by
	// hand" convention `create app -db-host` uses.
	Host, Port, DBName, User, Password string
}

// dbConnNameRe validates DBAddConfig.Name — rejected outright (not
// silently sanitized) if it doesn't match, since a mismatched
// sanitization would break db.SQL("<name>")/db.Mongo("<name>") call
// sites the user writes by hand afterward.
var dbConnNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// CheckDBConnectionName validates name (rejecting "core" and anything not
// matching dbConnNameRe) and reports whether it's already declared in
// appDir's .env — exported so cmd/nexler can fail fast on an
// already-taken name *before* prompting for connection details, rather
// than only discovering the collision after asking five questions (what
// AddDB alone would do, since it doesn't validate until it already has
// every field).
func CheckDBConnectionName(appDir, name string) error {
	_, _, err := checkDBConnectionAvailable(appDir, name)
	return err
}

// checkDBConnectionAvailable is CheckDBConnectionName's implementation,
// also reused by AddDB itself — returns the app's recovered env prefix
// and its current connection-name list on success, so callers that need
// those anyway (AddDB) don't have to re-derive them.
func checkDBConnectionAvailable(appDir, name string) (prefix string, names []string, err error) {
	if strings.EqualFold(name, "core") {
		return "", nil, errors.New(`"core" is reserved for the app's own conventional default connection — pick a different name`)
	}
	if !dbConnNameRe.MatchString(name) {
		return "", nil, fmt.Errorf("invalid connection name %q — must start with a letter and contain only letters, digits, underscores", name)
	}

	prefix, err = recoverEnvPrefix(appDir)
	if err != nil {
		return "", nil, err
	}

	connKey := prefix + "_DB_CONNECTIONS"
	existing, _ := readEnvValue(appDir, connKey)
	names = splitEnvCSV(existing)
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return "", nil, fmt.Errorf("connection %q already exists in %s — pick a different name", name, connKey)
		}
	}
	return prefix, names, nil
}

// AddDB adds a new named database connection to cfg.AppDir's .env,
// retrofitting driver support for cfg.DBType first if the app wasn't
// scaffolded with it originally. Never touches -core/core/ — the app's
// core connection (if any) stays whatever it already was; the connection
// this adds is never promoted to core.
func AddDB(cfg DBAddConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}
	if !allowedDBTypes[cfg.DBType] {
		return fmt.Errorf("unsupported -type %q (supported: mongo, mysql, postgres, mssql)", cfg.DBType)
	}

	prefix, names, err := checkDBConnectionAvailable(appDir, cfg.Name)
	if err != nil {
		return err
	}

	if !dialectEnabled(appDir, cfg.DBType) {
		modulePath, err := readModulePath(appDir)
		if err != nil {
			return err
		}
		if err := retrofitDialect(appDir, modulePath, prefix, cfg.DBType); err != nil {
			return fmt.Errorf("enabling %s support: %w", cfg.DBType, err)
		}
	}

	dsn := ""
	if cfg.Host != "" {
		dsn = buildDSN(cfg.DBType, cfg.Host, cfg.Port, cfg.DBName, cfg.User, cfg.Password)
	}

	upper := strings.ToUpper(cfg.Name)
	connKey := prefix + "_DB_CONNECTIONS"
	if err := setEnvLine(appDir, connKey, strings.Join(append(names, cfg.Name), ",")); err != nil {
		return fmt.Errorf("updating %s: %w", connKey, err)
	}
	if err := setEnvLine(appDir, prefix+"_DB_"+upper+"_TYPE", cfg.DBType); err != nil {
		return fmt.Errorf("writing %s_DB_%s_TYPE: %w", prefix, upper, err)
	}
	if err := setEnvLine(appDir, prefix+"_DB_"+upper+"_DSN", dsn); err != nil {
		return fmt.Errorf("writing %s_DB_%s_DSN: %w", prefix, upper, err)
	}

	return nil
}

// dialectEnabled checks file existence — mysql/mysql.go, db/mongo.go,
// etc. — matching kpass.go's own detectAuthFiles precedent (detect state
// from what's already on disk), more robust than string-matching
// db/sql.go's import block.
func dialectEnabled(appDir, dbType string) bool {
	if dbType == "mongo" {
		_, err := os.Stat(filepath.Join(appDir, "db", "mongo.go"))
		return err == nil
	}
	_, err := os.Stat(filepath.Join(appDir, dbType, dbType+".go"))
	return err == nil
}

// retrofitDialect adds whatever's missing for dbType to exist as a
// compiled-in driver in appDir: the per-dialect helper package, the
// db/sql.go import (or a fresh db/sql.go, if this is the app's first SQL
// dialect ever) for a SQL type, a fresh db/mongo.go for mongo, and —
// only if this dialect's *family* (SQL vs. mongo) is new to the app as a
// whole — db/db.go's Connect/Close cases. Code writes happen before any
// .env writes (see AddDB) so a failure here never leaves .env
// referencing a connection type that isn't actually wired up yet, and a
// retry after a failure lands back in AddDB's "already enabled" path
// instead of getting stuck on an overwrite refusal.
func retrofitDialect(appDir, modulePath, envPrefix, dbType string) error {
	helperData := struct{ ModulePath, EnvPrefix string }{modulePath, envPrefix}

	if dbType == "mongo" {
		if err := writeTemplateFile(dbMongoTmpl, filepath.Join(appDir, "db", "mongo.go"), nil); err != nil {
			return err
		}
		if err := writeTemplateFile(mongoHelperTmpl, filepath.Join(appDir, "mongo", "mongo.go"), helperData); err != nil {
			return err
		}
	} else {
		helperTmpl := map[string]string{
			"mysql": mysqlHelperTmpl, "postgres": postgresHelperTmpl, "mssql": mssqlHelperTmpl,
		}[dbType]
		if err := writeTemplateFile(helperTmpl, filepath.Join(appDir, dbType, dbType+".go"), helperData); err != nil {
			return err
		}
		if err := retrofitSQLGo(appDir, envPrefix, dbType); err != nil {
			return err
		}
	}

	return retrofitDBGo(appDir, modulePath, envPrefix, dbType)
}

// retrofitSQLGo ensures db/sql.go imports dbType's driver: patches the
// existing file's import block (reusing route.go's insertImport as-is —
// it's already generic, no routeData coupling) if db/sql.go already
// exists (meaning some other SQL dialect is already enabled), or renders
// a fresh one if this is the app's first SQL dialect. sqlDriverName's
// switch needs no change either way — it's already unconditionally
// exhaustive for all three SQL dialects regardless of which are actually
// compiled in.
func retrofitSQLGo(appDir, envPrefix, dbType string) error {
	sqlPath := filepath.Join(appDir, "db", "sql.go")
	importPath := map[string]string{
		"mysql":    "github.com/go-sql-driver/mysql",
		"postgres": "github.com/jackc/pgx/v5/stdlib",
		"mssql":    "github.com/microsoft/go-mssqldb",
	}[dbType]

	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading %s: %w", sqlPath, err)
		}
		// First SQL dialect ever for this app — no preservation concern,
		// the file is new.
		data := struct {
			EnvPrefix                       string
			HasMySQL, HasPostgres, HasMSSQL bool
		}{
			EnvPrefix:   envPrefix,
			HasMySQL:    dbType == "mysql",
			HasPostgres: dbType == "postgres",
			HasMSSQL:    dbType == "mssql",
		}
		return writeTemplateFile(dbSQLTmpl, sqlPath, data)
	}

	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, `"`+importPath+`"`) {
		return nil // already imported (e.g. a previous partial retrofit) — nothing to do
	}
	content, err = insertImport(content, "_", importPath)
	if err != nil {
		return fmt.Errorf("%s: %w (has it been hand-edited?)", sqlPath, err)
	}
	return os.WriteFile(sqlPath, []byte(content), 0o644)
}

// retrofitDBGo ensures db/db.go's Connect/Close have a case for dbType's
// family (SQL vs. mongo): renders a fresh db/db.go if the app had zero
// -db originally (no preservation concern), else patches the existing
// file via the nexler:db-connect/nexler:db-close markers — never
// touches anything else in the file, so hand edits (pool tuning, custom
// TLS config, timeouts) survive.
func retrofitDBGo(appDir, modulePath, envPrefix, dbType string) error {
	dbGoPath := filepath.Join(appDir, "db", "db.go")
	isSQL := dbType != "mongo"

	raw, err := os.ReadFile(dbGoPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reading %s: %w", dbGoPath, err)
		}
		data := struct {
			AppName, EnvPrefix, DBTypesCSV            string
			HasMySQL, HasPostgres, HasMSSQL, HasMongo bool
		}{
			AppName:     filepath.Base(modulePath),
			EnvPrefix:   envPrefix,
			DBTypesCSV:  dbType,
			HasMySQL:    dbType == "mysql",
			HasPostgres: dbType == "postgres",
			HasMSSQL:    dbType == "mssql",
			HasMongo:    dbType == "mongo",
		}
		return writeTemplateFile(dbConnectTmpl, dbGoPath, data)
	}

	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	familyPresent := (isSQL && strings.Contains(content, `case "mysql", "postgres", "mssql":`)) ||
		(!isSQL && strings.Contains(content, `case "mongo":`))
	if familyPresent {
		return nil // this dialect family already has a case, nothing to patch
	}

	connectFragment, closeFragment := dbConnectMongoCase, dbCloseMongoLine
	if isSQL {
		connectFragment, closeFragment = dbConnectSQLCase, dbCloseSQLLine
	}

	content, err = insertBeforeMarker(content, connectFragment, dbConnectMarker,
		fmt.Sprintf("could not find the marker %q in %s — it may predate dialect-retrofitting support; add it back by hand right before the `default:` case in Connect(), or add the missing case manually", strings.TrimSpace(dbConnectMarker), dbGoPath))
	if err != nil {
		return err
	}
	content, err = insertBeforeMarker(content, closeFragment, dbCloseMarker,
		fmt.Sprintf("could not find the marker %q in %s — it may predate dialect-retrofitting support; add it back by hand right before `return errors.Join(errs...)` in Close(), or add the missing line manually", strings.TrimSpace(dbCloseMarker), dbGoPath))
	if err != nil {
		return err
	}

	return os.WriteFile(dbGoPath, []byte(content), 0o644)
}

// insertBeforeMarker inserts fragment immediately before marker in
// content, leaving the marker itself in place so it can be used again
// later — same idempotent-insertion-point shape as route.go's
// insertFragment, but taking an already-rendered fragment string
// directly instead of a template path + data, since db-retrofitting only
// ever needs one of two fixed literal fragments, not per-call-site
// generated text.
func insertBeforeMarker(content, fragment, marker, missingMarkerErr string) (string, error) {
	if !strings.Contains(content, marker) {
		return "", errors.New(missingMarkerErr)
	}
	return strings.Replace(content, marker, fragment+marker, 1), nil
}

// writeTemplateFile reads tmplPath from the embedded templateFS, renders
// it through processFile against data (data may be nil for a template
// with no {{ }} references, e.g. db/mongo.go.tmpl), and writes the
// result to destFile — refusing to overwrite if destFile already exists,
// same collision guard NewRoute uses for handler/service/store/model
// files.
func writeTemplateFile(tmplPath, destFile string, data any) error {
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — remove it first to regenerate", destFile)
	}
	raw, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	content, err := processFile(tmplPath, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(destFile), err)
	}
	if err := os.WriteFile(destFile, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", destFile, err)
	}
	return nil
}

// recoverEnvPrefix scans appDir/.env for the *unconditional* "_HOST="
// line — present in every app's .env regardless of whether -db was ever
// used, unlike "_DB_CORE_TYPE=" (route.go's readCoreDBType's own anchor),
// which only exists for apps scaffolded with -db. AddDB needs to work on
// a db-less app too (adding its very first database), so it can't assume
// a _DB_CORE_TYPE line exists to recover the prefix from.
func recoverEnvPrefix(appDir string) (string, error) {
	envPath := filepath.Join(appDir, ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return "", fmt.Errorf("reading %s (run this from inside a nexler app directory, or pass -dir): %w", envPath, err)
	}
	const suffix = "_HOST="
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, suffix); idx > 0 {
			return line[:idx], nil
		}
	}
	return "", fmt.Errorf("no %q line found in %s — is this a nexler app directory?", suffix, envPath)
}

// readEnvValue reads appDir/.env and returns the value of the first line
// whose key exactly matches key (via strings.Cut on "="), and whether it
// was found at all.
func readEnvValue(appDir, key string) (string, bool) {
	envPath := filepath.Join(appDir, ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}

// setEnvLine upserts key=value into appDir/.env: replaces an existing
// line's value in place if key is already present, else appends a new
// line. kpass.go's ensureEnvVars can't do this — it only ever appends
// brand-new *blank* placeholders for a fixed, known set of key names, and
// never updates an existing line's value or handles a dynamically-built
// key name (e.g. a per-connection-name key like
// {PREFIX}_DB_ANALYTICS_DSN, only known at call time).
func setEnvLine(appDir, key, value string) error {
	envPath := filepath.Join(appDir, ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", envPath, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	newLine := key + "=" + value
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
			lines[i] = newLine
			return os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0o644)
		}
	}
	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(envPath, []byte(content+newLine+"\n"), 0o644)
}

// splitEnvCSV splits a comma-separated .env value into trimmed,
// non-empty parts — same convention as cmd/nexler's own splitCSV, kept
// as a separate unexported copy here since that one lives in package
// main and can't be imported from internal/scaffold.
func splitEnvCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
