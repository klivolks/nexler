// Package scaffold generates the standard nexler project layout:
//
//	handlers/    HTTP handlers — request parsing, response writing
//	services/    business logic, orchestration, calls into store/apiclient/broker
//	store/       persistence — DB-backed models via the Store interface
//	models/      request/response and domain structs (struct-tag validated)
//	middleware/  auth, logging, recovery, etc.
//
// This mirrors kGate's proven handlers/services/store/models split, kept
// consistent so any app built on nexler looks familiar to the others.
//
// Every template file is embedded into the nexler binary at build time
// (see templates.go). Scaffolding a new app is pure local file I/O: no
// network access, and no shelling out to git or any other external
// process. This is what makes nexler an independently installable
// single-binary tool.
package scaffold

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// NewAppConfig holds the parameters for scaffolding a new app.
type NewAppConfig struct {
	// AppName is both the generated folder name and, when ModulePath is
	// empty, the Go module name.
	AppName string
	// OutputDir is the parent directory the app folder is created inside.
	OutputDir string
	// ModulePath is the Go module path written to go.mod and used in
	// generated import paths.
	ModulePath string
	// UI forces the homepage onto the disk-based
	// templates/html/home/{home,layout}.html + response.HTML mechanism
	// (editable without a rebuild — same convention as a route scaffolded
	// with -response html), instead of the built-in embedded homepage.
	// When set, the embedded templates/html/home.html and
	// static/css/home.css aren't generated at all, since GET / no longer
	// serves them.
	UI bool
	// AuthKind picks what middleware.RequireAuth actually enforces:
	// "none" (default) keeps today's unverified stub; "jwt" generates a
	// stdlib-only HMAC-SHA256 JWT package (auth/jwt.go) and checks a
	// bearer token; "session" generates an in-memory session/cookie
	// package (auth/session.go) and checks a session cookie; "both"
	// generates both and accepts either — e.g. bearer tokens for API
	// clients, session cookies for browser/UI clients, on the same
	// protected routes.
	AuthKind string
	// RememberMe, when AuthKind includes "session", adds a rememberMe
	// bool parameter to auth.StartSession that swaps the session/cookie
	// lifetime from a short default to a long one. Ignored otherwise.
	RememberMe bool
	// JWTSecret, if non-empty, is used verbatim as the app's JWT signing
	// secret instead of generating a fresh random one — set this when
	// multiple apps in the same ecosystem need to validate each other's
	// bearer tokens, so they all sign/verify with the same secret. Empty
	// (default) preserves today's behavior: generateJWTSecret produces a
	// fresh, unique-per-app secret. Ignored when AuthKind doesn't need JWT.
	JWTSecret string
	// DBTypes selects which database driver(s) to generate db/ support
	// for — any subset of "mongo", "mysql", "postgres", "mssql". Empty
	// (default) generates no db/ package at all. This only decides which
	// drivers are compiled in (and added to go.mod — the first nexler
	// feature that needs real third-party dependencies, since neither
	// MongoDB nor any SQL server has a stdlib-only driver); the actual
	// named connections are declared purely at runtime via
	// {{.EnvPrefix}}_DB_CONNECTIONS in .env, not here. See db.go.tmpl.
	DBTypes []string
	// CoreDB names which of DBTypes is the app's conventional default
	// connection — every route's store reaches for it (db.SQL("core")
	// or db.Mongo("core")) unless it deliberately picks a different
	// named connection. Must be a member of DBTypes when set; empty is
	// only valid when DBTypes is also empty. When DBTypes has exactly
	// one entry, that entry is core regardless of CoreDB. When DBTypes
	// has more than one and CoreDB is empty, it defaults to "mongo" if
	// present, else DBTypes' first entry.
	CoreDB string
	// CoreDBHost, if non-empty, causes NewApp to build a real DSN for the
	// "core" connection (via buildDSN) and write it to .env instead of
	// leaving {{.EnvPrefix}}_DB_CORE_DSN= blank. CoreDBPort/CoreDBName/
	// CoreDBUser/CoreDBPassword are only consulted when CoreDBHost is
	// set — leaving CoreDBHost empty (the default) reproduces the
	// original blank-DSN behavior exactly, for anyone who'd rather fill
	// .env in by hand.
	CoreDBHost, CoreDBPort, CoreDBName, CoreDBUser, CoreDBPassword string
	// MergeServiceAuth, when AuthKind is "jwt" or "both" AND DBTypes is
	// non-empty, folds X-Api-Secret service-key auth (core.VerifyServiceKey)
	// into middleware.RequireAuth itself as a fallback after JWT/session,
	// instead of generating a separate middleware.RequireServiceAuth — so a
	// service/automation caller can hit the exact same -protected routes a
	// human user would. Ignored otherwise (same eligibility as
	// RequireServiceAuth itself). Default false preserves today's
	// separate-middleware behavior — deliberately not the new default, since
	// keeping user-only and service-only routes structurally distinct is
	// still the right choice for a pure API app.
	MergeServiceAuth bool
}

// TargetPath returns the full path of the app directory to be created.
func (c NewAppConfig) TargetPath() string {
	return filepath.Join(c.OutputDir, c.AppName)
}

// templateData is the set of values available to `{{ .Field }}`
// placeholders inside template files.
type templateData struct {
	AppName        string
	ModulePath     string
	UI             bool
	EnvPrefix      string
	AuthKind       string
	RememberMe     bool
	JWTSecret      string
	HasDB          bool
	HasMongo       bool
	HasMySQL       bool
	HasPostgres    bool
	HasMSSQL       bool
	DBTypesCSV     string
	CoreDB         string
	HasCoreDB      bool
	CoreDBAccessor string
	// MergeServiceAuth folds X-Api-Secret service-key auth into
	// middleware.RequireAuth itself (as a fallback after JWT/session)
	// instead of generating a separate middleware.RequireServiceAuth. See
	// NewAppConfig.MergeServiceAuth.
	MergeServiceAuth bool
	// CoreConfigGetQuery is the dialect-correct, parameterized SELECT
	// core/config.go.tmpl's GetSetting uses — empty when CoreDBAccessor is
	// "Mongo" (unused there). See configGetQuerySQL.
	CoreConfigGetQuery string
	// CoreUserGetQuery is the same idea as CoreConfigGetQuery, for
	// core/users.go.tmpl's GetUser. See userGetQuerySQL.
	CoreUserGetQuery string
	// CoreServiceVerifyQuery is the same idea, for core/services.go.tmpl's
	// VerifyServiceKey. See serviceVerifyQuerySQL.
	CoreServiceVerifyQuery string
	// CoreServiceGetQuery is the same idea, for core/services.go.tmpl's
	// GetService. See serviceGetQuerySQL.
	CoreServiceGetQuery string
	// CoreDBDSN is the real connection string dotenv.tmpl writes to
	// {{.EnvPrefix}}_DB_CORE_DSN — empty (the default) writes a blank
	// value, same as before this field existed. See NewAppConfig.CoreDBHost.
	CoreDBDSN string
}

// NewApp scaffolds a new app directory at cfg.TargetPath() by walking the
// embedded template tree (see templateFS in templates.go), rendering each
// file through text/template, and writing the result to disk.
//
// It refuses to run if the target directory already exists and is
// non-empty, to avoid silently overwriting an existing project.
func NewApp(cfg NewAppConfig) error {
	target := cfg.TargetPath()

	authKind := cfg.AuthKind
	if authKind == "" {
		authKind = "none"
	}
	if err := validateAuthKind(authKind); err != nil {
		return err
	}
	needsJWT := authKind == "jwt" || authKind == "both"
	needsSession := authKind == "session" || authKind == "both"

	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return fmt.Errorf("target directory %q already exists and is not empty", target)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("creating app directory: %w", err)
	}

	jwtSecret := ""
	if needsJWT {
		if cfg.JWTSecret != "" {
			jwtSecret = cfg.JWTSecret
		} else {
			var err error
			jwtSecret, err = generateJWTSecret()
			if err != nil {
				return fmt.Errorf("generating JWT secret: %w", err)
			}
		}
	}

	dbTypes, err := normalizeDBTypes(cfg.DBTypes)
	if err != nil {
		return err
	}
	hasMongo := dbTypes["mongo"]
	hasMySQL := dbTypes["mysql"]
	hasPostgres := dbTypes["postgres"]
	hasMSSQL := dbTypes["mssql"]
	hasDB := hasMongo || hasMySQL || hasPostgres || hasMSSQL

	coreDB, err := resolveCoreDB(dbTypes, cfg.CoreDB)
	if err != nil {
		return err
	}
	coreDBAccessor := "SQL"
	if coreDB == "mongo" {
		coreDBAccessor = "Mongo"
	}
	coreConfigGetQuery := ""
	coreUserGetQuery := ""
	coreServiceVerifyQuery := ""
	coreServiceGetQuery := ""
	if hasDB && coreDB != "" && coreDB != "mongo" {
		coreConfigGetQuery = configGetQuerySQL(coreDB)
		coreUserGetQuery = userGetQuerySQL(coreDB)
		coreServiceVerifyQuery = serviceVerifyQuerySQL(coreDB)
		coreServiceGetQuery = serviceGetQuerySQL(coreDB)
	}
	coreDBDSN := ""
	if hasDB && coreDB != "" && cfg.CoreDBHost != "" {
		coreDBDSN = buildDSN(coreDB, cfg.CoreDBHost, cfg.CoreDBPort, cfg.CoreDBName, cfg.CoreDBUser, cfg.CoreDBPassword)
	}

	data := templateData{
		AppName:                cfg.AppName,
		ModulePath:             cfg.ModulePath,
		UI:                     cfg.UI,
		EnvPrefix:              envPrefix(cfg.AppName),
		AuthKind:               authKind,
		RememberMe:             cfg.RememberMe && needsSession,
		JWTSecret:              jwtSecret,
		HasDB:                  hasDB,
		HasMongo:               hasMongo,
		HasMySQL:               hasMySQL,
		HasPostgres:            hasPostgres,
		HasMSSQL:               hasMSSQL,
		DBTypesCSV:             dbTypesCSV(dbTypes),
		CoreDB:                 coreDB,
		HasCoreDB:              hasDB,
		MergeServiceAuth:       cfg.MergeServiceAuth && hasDB && needsJWT,
		CoreDBAccessor:         coreDBAccessor,
		CoreConfigGetQuery:     coreConfigGetQuery,
		CoreUserGetQuery:       coreUserGetQuery,
		CoreServiceVerifyQuery: coreServiceVerifyQuery,
		CoreServiceGetQuery:    coreServiceGetQuery,
		CoreDBDSN:              coreDBDSN,
	}

	err = fs.WalkDir(templateFS, templatesRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(templatesRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		// Under -ui, the homepage is served from
		// templates/html/home/{home,layout}.html (written below) via
		// response.HTML, not from these embedded files — skip them so a
		// -ui app doesn't ship a dead, never-served homepage/stylesheet.
		if cfg.UI {
			switch relSlash {
			case "templates/html/home.html.tmpl", "templates/static/css/home.css":
				return nil
			}
		}

		// The auth/ package only exists for -auth jwt|session|both; skip
		// the whole directory when it's "none", and skip whichever half
		// (jwt.go.tmpl/session.go.tmpl) isn't needed otherwise, so a
		// -auth jwt app never ships an unused session store (or vice
		// versa).
		if relSlash == "auth" && d.IsDir() {
			if authKind == "none" {
				return fs.SkipDir
			}
		}
		switch relSlash {
		case "auth/jwt.go.tmpl":
			if !needsJWT {
				return nil
			}
		case "auth/session.go.tmpl":
			if !needsSession {
				return nil
			}
		}

		// The db/ package only exists for -db mongo|mysql|postgres|mssql
		// (any subset); skip the whole directory when none were selected,
		// and skip whichever half (sql.go.tmpl/mongo.go.tmpl) isn't
		// needed otherwise — same technique as auth/ above.
		if relSlash == "db" && d.IsDir() {
			if !hasDB {
				return fs.SkipDir
			}
		}
		switch relSlash {
		case "db/sql.go.tmpl":
			if !hasMySQL && !hasPostgres && !hasMSSQL {
				return nil
			}
		case "db/mongo.go.tmpl":
			if !hasMongo {
				return nil
			}
		}

		// The mongo/ package (simplified Get/GetOne/Set/Insert/Delete/
		// Aggregate helpers, built on db.Mongo) only exists when mongo
		// is selected via -db.
		if relSlash == "mongo" && d.IsDir() {
			if !hasMongo {
				return fs.SkipDir
			}
		}

		// store/common (Base{ID}, meant to be embedded anonymously in every
		// Mongo domain struct — see store/common/common.go.tmpl) is
		// app-wide infrastructure, not tied to any particular route, so it's
		// scaffolded here rather than by `nexler create <route>`. Mongo-only
		// (bson.ObjectID has no SQL-dialect equivalent this shared type
		// could sensibly hold), same gate as mongo/ itself.
		if relSlash == "store" && d.IsDir() {
			if !hasMongo {
				return fs.SkipDir
			}
		}

		// The core/ package (app-wide system data like Config — independent
		// of whatever business data your own store/ packages manage; see
		// core/config.go.tmpl) only exists when a core database connection
		// is available at all, i.e. -db was set (any type). Provisioning
		// its schema is a separate, explicit step (`nexler init db`), not
		// done here.
		if relSlash == "core" && d.IsDir() {
			if !hasDB {
				return fs.SkipDir
			}
		}

		// core/users.go.tmpl and core/services.go.tmpl (API-key/
		// service-to-service auth — see core/services.go.tmpl's own doc
		// comment) only exist when there's both a core database connection
		// AND a JWT-capable -auth choice: API-key auth is documented as
		// JWT's service-to-service companion, and core_users' UserId is
		// explicitly the same value as the JWT "sub" claim, so neither
		// makes sense for -auth none|session alone.
		switch relSlash {
		case "core/users.go.tmpl", "core/services.go.tmpl":
			if !hasDB || !needsJWT {
				return nil
			}
		}

		// middleware/service_auth.go.tmpl has that same eligibility, PLUS
		// it's skipped whenever cfg.MergeServiceAuth is set — merged mode
		// folds its X-Api-Secret check into middleware/auth.go.tmpl's own
		// RequireAuth instead, so the separate file (and its now-unused
		// RequireServiceAuth) would just be dead code alongside it.
		if relSlash == "middleware/service_auth.go.tmpl" {
			if !hasDB || !needsJWT || cfg.MergeServiceAuth {
				return nil
			}
		}

		// handlers/admin/{users,services} — nexler's own generated admin
		// API for core_users/core_services (see handlers/admin/users/
		// users.go.tmpl's package doc comment) — same eligibility as
		// service auth above, since these routes call core.CreateUser/
		// ListServices/etc. directly. Wired into routes/protected/
		// protected.go post-walk, below, not here (needs the aggregator
		// file to already exist on disk).
		if relSlash == "handlers/admin" && d.IsDir() {
			if !hasDB || !needsJWT {
				return fs.SkipDir
			}
		}

		// The mysql/, postgres/, and mssql/ packages (simplified struct-based
		// Get/GetOne/Insert/Update/Delete/Query/Call helpers, built on
		// db.SQL) each only exist when their specific type is selected
		// via -db — independent packages, so each is gated on its own flag.
		if d.IsDir() {
			switch relSlash {
			case "mysql":
				if !hasMySQL {
					return fs.SkipDir
				}
			case "postgres":
				if !hasPostgres {
					return fs.SkipDir
				}
			case "mssql":
				if !hasMSSQL {
					return fs.SkipDir
				}
			}
		}

		destRel := destPath(rel)
		dest := filepath.Join(target, destRel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}

		raw, err := templateFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded template %s: %w", path, err)
		}

		content, err := processFile(path, raw, data)
		if err != nil {
			return fmt.Errorf("processing template %s: %w", path, err)
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}

		return os.WriteFile(dest, content, 0o644)
	})
	if err != nil {
		return err
	}

	if cfg.UI {
		if err := writeAppUIHomepage(target); err != nil {
			return fmt.Errorf("writing UI homepage templates: %w", err)
		}
	}

	if hasDB && needsJWT {
		if err := wireAggregator(target, "protected", cfg.ModulePath+"/handlers/admin/users", "adminusers"); err != nil {
			return fmt.Errorf("wiring handlers/admin/users into routes/protected/protected.go: %w", err)
		}
		if err := wireAggregator(target, "protected", cfg.ModulePath+"/handlers/admin/services", "adminservices"); err != nil {
			return fmt.Errorf("wiring handlers/admin/services into routes/protected/protected.go: %w", err)
		}
	}

	return nil
}

// writeAppUIHomepage writes templates/html/home/home.html inside a freshly
// scaffolded app directory, plus templates/html/shared/layout.html (via
// writeSharedHTMLLayout) if it doesn't already exist — the disk-based
// homepage response.HTML(w, r, "home", "home", ...) reads (see
// handlers/home/home.go.tmpl's -ui branch). Reuses the same placeholder
// content and writeIfMissing helper that route.go's writeHTMLTemplates
// uses for -response html routes, for one consistent convention. Unlike a
// route (which can opt into its own -layout module copy), the -ui homepage
// always uses the shared layout.
func writeAppUIHomepage(target string) error {
	dir := filepath.Join(target, "templates", "html", "home")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := writeIfMissing(filepath.Join(dir, "home.html"), defaultHTMLContent); err != nil {
		return err
	}
	return writeSharedHTMLLayout(target)
}

// validateAuthKind checks -auth against the supported auth modes.
func validateAuthKind(kind string) error {
	switch kind {
	case "none", "jwt", "session", "both":
		return nil
	default:
		return fmt.Errorf("unsupported -auth %q (supported: none, jwt, session, both)", kind)
	}
}

// allowedDBTypes are the database drivers `-db` can select, any subset,
// comma-separated (e.g. "mongo,postgres").
var allowedDBTypes = map[string]bool{
	"mongo": true, "mysql": true, "postgres": true, "mssql": true,
}

// normalizeDBTypes lowercases and validates each of raw's entries
// against allowedDBTypes, returning the selected set as a lookup map
// (empty/nil raw is valid — no db/ package generated at all).
func normalizeDBTypes(raw []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, t := range raw {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if !allowedDBTypes[t] {
			return nil, fmt.Errorf("unsupported -db %q (supported: mongo, mysql, postgres, mssql)", t)
		}
		out[t] = true
	}
	return out, nil
}

// dbTypesCSV renders selected's members back out as a stable,
// comma-separated string (mongo, mysql, postgres, mssql order) for use
// in generated doc comments/error messages.
func dbTypesCSV(selected map[string]bool) string {
	var parts []string
	for _, t := range []string{"mongo", "mysql", "postgres", "mssql"} {
		if selected[t] {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, ",")
}

// resolveCoreDB validates and defaults -core against the -db selection:
// core must be empty when selected is empty, and a member of selected
// when set. When -db selects exactly one type, that type is core
// regardless of what core was passed. When -db selects more than one
// and core is empty (e.g. -core was never passed, so the "which is
// core?" prompt never ran — non-interactive/scripted use), it defaults
// to "mongo" if present, else selected's first entry in
// mongo/mysql/postgres/mssql order.
func resolveCoreDB(selected map[string]bool, core string) (string, error) {
	core = strings.ToLower(strings.TrimSpace(core))
	if len(selected) == 0 {
		if core != "" {
			return "", fmt.Errorf("-core %q requires -db to select at least one database type", core)
		}
		return "", nil
	}
	if len(selected) == 1 {
		for t := range selected {
			return t, nil
		}
	}
	if core != "" {
		if !selected[core] {
			return "", fmt.Errorf("-core %q must be one of the -db types selected (%s)", core, dbTypesCSV(selected))
		}
		return core, nil
	}
	if selected["mongo"] {
		return "mongo", nil
	}
	for _, t := range []string{"mysql", "postgres", "mssql"} {
		if selected[t] {
			return t, nil
		}
	}
	return "", fmt.Errorf("unreachable: selected is non-empty but no known type matched")
}

// configGetQuerySQL returns the dialect-correct, parameterized SELECT the
// generated core package's GetSetting uses to read one core_config row —
// the only place core/config.go.tmpl needs dialect-specific SQL text,
// since a straight single-table SELECT has no single portable spelling
// across mysql/postgres/mssql (placeholder syntax, and "key"'s
// reserved-word quoting, both differ). SetSetting instead calls the
// core_config_upsert stored procedure (see `nexler init db`'s
// provisioning), which needs no per-dialect call-site text at all — Call
// already abstracts the placeholder syntax.
func configGetQuerySQL(dbType string) string {
	switch dbType {
	case "mysql":
		return "SELECT value FROM core_config WHERE `key` = ?"
	case "mssql":
		return "SELECT value FROM core_config WHERE [key] = @p1"
	default: // postgres
		return "SELECT value FROM core_config WHERE key = $1"
	}
}

// userGetQuerySQL is configGetQuerySQL's counterpart for
// core/users.go.tmpl's GetUser.
func userGetQuerySQL(dbType string) string {
	const cols = "user_id, username, user_role, user_type, status, created_at, updated_at"
	switch dbType {
	case "mysql":
		return "SELECT " + cols + " FROM core_users WHERE user_id = ?"
	case "mssql":
		return "SELECT " + cols + " FROM core_users WHERE user_id = @p1"
	default: // postgres
		return "SELECT " + cols + " FROM core_users WHERE user_id = $1"
	}
}

// serviceVerifyQuerySQL is configGetQuerySQL's counterpart for
// core/services.go.tmpl's VerifyServiceKey — looked up by key_hash, never
// the plaintext key, which is never stored.
func serviceVerifyQuerySQL(dbType string) string {
	switch dbType {
	case "mysql":
		return "SELECT name, status FROM core_services WHERE key_hash = ?"
	case "mssql":
		return "SELECT name, status FROM core_services WHERE key_hash = @p1"
	default: // postgres
		return "SELECT name, status FROM core_services WHERE key_hash = $1"
	}
}

// serviceGetQuerySQL is serviceVerifyQuerySQL's counterpart for
// core/services.go.tmpl's GetService — looked up by name (core_services'
// primary key), never key_hash.
func serviceGetQuerySQL(dbType string) string {
	const cols = "name, status, created_at, updated_at"
	switch dbType {
	case "mysql":
		return "SELECT " + cols + " FROM core_services WHERE name = ?"
	case "mssql":
		return "SELECT " + cols + " FROM core_services WHERE name = @p1"
	default: // postgres
		return "SELECT " + cols + " FROM core_services WHERE name = $1"
	}
}

// buildDSN constructs a real, dialect-correct connection string from
// separate host/port/dbname/user/password fields — used both when
// `create app -db` is given real connection details (instead of leaving
// the DSN blank for the user to fill in by hand) and by `nexler db add`
// when adding a new named connection.
func buildDSN(dbType, host, port, dbname, user, password string) string {
	switch dbType {
	case "postgres":
		suffix := ""
		if password == "" {
			// Explicit for local/no-TLS test databases: pgx's own default
			// (sslmode=prefer) would still connect via a graceful fallback
			// to plaintext when the server doesn't speak TLS, but this
			// skips that extra negotiation round trip and removes any
			// ambiguity about what actually happened.
			suffix = "?sslmode=disable"
		}
		return "postgres://" + userinfo(user, password) + host + ":" + port + "/" + dbname + suffix
	case "mssql":
		// go-mssqldb has no true "no auth" concept — blank user/password
		// here falls back to Windows/Integrated auth (the driver's own
		// documented behavior when no SQL credentials are present in the
		// URL, not something this function tries to work around).
		return "sqlserver://" + userinfo(user, password) + host + ":" + port + "?database=" + dbname
	case "mongo":
		return "mongodb://" + userinfo(user, password) + host + ":" + port + "/" + dbname
	default: // mysql
		return userinfo(user, password) + "tcp(" + host + ":" + port + ")/" + dbname
	}
}

// userinfo renders the "user:password@" (or "user@", or "") prefix
// shared by all four DSN formats above. Both go-sql-driver/mysql and
// mongo-driver document this whole segment as optional syntax — omitted
// entirely when both user and password are blank, rather than emitting a
// dangling "@" or empty "user:@".
func userinfo(user, password string) string {
	switch {
	case user == "" && password == "":
		return ""
	case password == "":
		return user + "@"
	default:
		return user + ":" + password + "@"
	}
}

// DefaultDBPort returns dbType's conventional default port, used by
// cmd/nexler to pre-fill the port prompt during `create app -db` and
// `nexler db add`.
func DefaultDBPort(dbType string) string {
	switch dbType {
	case "postgres":
		return "5432"
	case "mssql":
		return "1433"
	case "mongo":
		return "27017"
	default: // mysql
		return "3306"
	}
}

// generateJWTSecret returns a fresh random secret (32 bytes,
// base64url-encoded) for signing JWTs, written into the generated app's
// .env as {{ .EnvPrefix }}_JWT_SECRET so it works out of the box —
// exactly one call per `nexler create app -auth jwt|both`, never reused
// across apps.
func generateJWTSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// envPrefix derives an app's environment-variable namespace from its
// name, e.g. "my-app" -> "MY_APP", "3cool" -> "_3COOL" — used for
// {{ .EnvPrefix }}_HOST / {{ .EnvPrefix }}_PORT (and future env-based
// settings) so multiple apps' variables don't collide in a shared
// environment.
func envPrefix(appName string) string {
	var b strings.Builder
	for _, r := range appName {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if s == "" {
		return "APP"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "_" + s
	}
	return s
}

// destPath maps an embedded template's relative path to its output path.
//
// "gomod.tmpl" is special-cased to "go.mod": a literal go.mod file cannot
// live inside the templates/ source tree, because the Go toolchain treats
// any go.mod as a module boundary — that would silently break `go:embed`
// and the build of the nexler CLI itself. "dotenv.tmpl" is special-cased
// to ".env" for the same underlying reason as "vscode/launch.json.tmpl"
// below: go:embed silently excludes any file *or directory* whose name
// starts with "." (or "_"), so a literal ".env.tmpl" — or a source tree
// rooted at ".vscode/" — would never be embedded at all. The template
// therefore lives at the dot-free source path "vscode/launch.json.tmpl"
// and is mapped here to its real destination, ".vscode/launch.json" (VS
// Code's fixed, non-configurable location for a workspace's debug
// configurations). Every other file just has its ".tmpl" suffix stripped.
func destPath(rel string) string {
	relSlash := filepath.ToSlash(rel)
	switch relSlash {
	case "vscode/launch.json.tmpl":
		return filepath.Join(".vscode", "launch.json")
	}
	switch filepath.Base(rel) {
	case "gomod.tmpl":
		return filepath.Join(filepath.Dir(rel), "go.mod")
	case "dotenv.tmpl":
		return filepath.Join(filepath.Dir(rel), ".env")
	}
	return strings.TrimSuffix(rel, ".tmpl")
}

// render executes a template file's contents against data. Shared by both
// the app scaffold (templateData) and the route scaffold (routeData) in
// route.go.
//
// Output is normalized to LF line endings regardless of how the source
// .tmpl file itself is saved (e.g. CRLF from a Windows editor) — this
// matters because route.go's aggregator wiring does exact-byte anchor
// matching on generated files, which silently breaks if a stray \r
// sneaks in.
func render(name string, raw []byte, data any) ([]byte, error) {
	tmpl, err := template.New(name).Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(buf.Bytes(), []byte("\r\n"), []byte("\n")), nil
}

// processFile only runs text/template parsing on files whose name ends
// in ".tmpl" — everything else is copied through verbatim (still
// LF-normalized). This matters: static Go source can innocently contain
// a literal "{{" from ordinary nested composite literals (e.g.
// []map[string][]string{{"a": nil}}), which text/template's lexer would
// otherwise misparse as the start of an action, even though nothing in
// the file was meant to be a placeholder.
func processFile(name string, raw []byte, data any) ([]byte, error) {
	if !strings.HasSuffix(name, ".tmpl") {
		return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), nil
	}
	return render(name, raw, data)
}
