// Command nexler is the scaffolding CLI for the nexler framework.
//
// It does not run an application itself — it generates the
// handlers/services/store/models project layout that apps built on
// nexler are expected to follow, similar in spirit to `django-admin
// startproject` or `rails new`.
//
// It is fully self-contained: every template file it writes is embedded
// directly in this binary (see internal/scaffold), so scaffolding a new
// app requires no network access and never shells out to git. Installing
// nexler is just placing this single static binary on PATH.
//
// Usage:
//
//	nexler create app <name> [-dir <output-dir>] [-module <module-path>] [-ui]
//	nexler version
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/klivolks/nexler/internal/scaffold"
)

// cliVersion is the version of the nexler CLI itself, not of any
// generated app. A plain `go build` keeps this fallback value; a real
// release build (see .goreleaser.yml, installer/*/BUILDING.md) overrides
// it via `-ldflags "-X main.cliVersion=<version>"`, sourced from the git
// tag that triggered the release — this must stay a package-level var
// with a literal string default, since -X can only patch that shape.
var cliVersion = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "create":
		runCreate(os.Args[2:])
	case "init":
		runInit(os.Args[2:])
	case "add":
		runAdd(os.Args[2:])
	case "db":
		runDb(os.Args[2:])
	case "update":
		runUpdate(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("nexler CLI v%s\n", cliVersion)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "nexler: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// runCreate dispatches `nexler create ...`. These shapes are supported:
//
//	nexler create app <name> ...          scaffold a whole new app
//	nexler create /some/path -module ..   scaffold a route inside the
//	                                       current app (path starts with "/")
//	nexler create store <name> ...        scaffold a standalone store
//	nexler create service <name> ...      scaffold a standalone service
//
// The leading "/" is what distinguishes a route from a resource keyword
// like "app"/"store"/"service"; this leaves room for e.g. `nexler create
// model` later without a breaking CLI change.
func runCreate(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "nexler: missing argument\n\nusage:\n  nexler create app <name> [-dir <output-dir>] [-module <module-path>]\n  nexler create <route> -module <name> [-submodule <name>] [-dir <app-dir>]\n  nexler create store|service <name> [-file <name>] [-dir <app-dir>]")
		os.Exit(1)
	}

	if strings.HasPrefix(args[0], "/") {
		runCreateRoute(args[0], args[1:])
		return
	}

	switch args[0] {
	case "app":
		runCreateApp(args[1:])
	case "store", "service":
		runCreateLayer(args[0], args[1:])
	default:
		fmt.Fprintf(os.Stderr, "nexler: unknown resource %q for create\n\nsupported: app, store, service, or a route path starting with /\n", args[0])
		os.Exit(1)
	}
}

func runCreateApp(args []string) {
	fs := flag.NewFlagSet("create app", flag.ExitOnError)
	outDir := fs.String("dir", ".", "output directory for the generated app (a subfolder named after the app is created inside it)")
	modulePath := fs.String("module", "", "Go module path for the generated app (defaults to the app name)")
	ui := fs.Bool("ui", false, "serve the homepage from editable templates/html/home/{home,layout}.html on disk (via response.HTML), instead of the built-in embedded homepage")
	auth := fs.String("auth", "none", "auth method middleware.RequireAuth enforces: none (default, unverified stub), jwt (bearer token), session (cookie), or both")
	rememberMe := fs.Bool("remember-me", false, "when -auth includes session, add a rememberMe option to auth.StartSession for a longer-lived login")
	jwtSecret := fs.String("jwt-secret", "", "existing JWT signing secret to reuse instead of generating a new one — set this when multiple apps in the same ecosystem need to validate each other's tokens; blank (default) generates a fresh, unique secret")
	db := fs.String("db", "", "comma-separated database drivers to generate db/ support for: mongo, mysql, postgres, mssql (any subset; blank means none)")
	core := fs.String("core", "", "which -db type is this app's conventional default (\"core\") connection; required only when -db selects more than one type")
	dbHost := fs.String("db-host", "", "core database host; blank (default) leaves .env's DSN blank for you to fill in by hand")
	dbPort := fs.String("db-port", "", "core database port; defaults to the type's conventional port (3306/5432/1433/27017) when -db-host is set")
	dbName := fs.String("db-name", "", "core database name")
	dbUser := fs.String("db-user", "", "core database username (blank = no auth)")
	dbPassword := fs.String("db-password", "", "core database password (blank = no auth; passed/prompted in plain text, never masked)")
	mergeServiceAuth := fs.Bool("merge-service-auth", false, "fold X-Api-Secret service-key auth into RequireAuth itself (JWT/session, then service key) instead of a separate RequireServiceAuth — lets a service/automation caller hit the same protected routes a user would; only meaningful with -auth jwt|both and -db")
	multitenant := fs.Bool("multitenant", false, "thread a tenant Org through auth.ContextWithOrg/Org, StartSession/SessionFromRequest, and core.User.OrgId — requires -auth jwt, session, or both")
	fs.Parse(reorderFlagsFirst(fs, args))

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	appName := ""
	if fs.NArg() >= 1 {
		appName = fs.Arg(0)
	} else {
		appName = promptRequired("App name")
	}

	dir := *outDir
	if !set["dir"] {
		dir = prompt("Output directory", dir)
	}

	mod := *modulePath
	if !set["module"] {
		mod = prompt("Go module path", appName)
	}
	if mod == "" {
		mod = appName
	}

	uiVal := *ui
	if !set["ui"] {
		uiVal = promptUIOrAPI("Homepage type", uiVal)
	}

	authVal := *auth
	if !set["auth"] {
		authVal = promptChoice("Auth method", []string{"none", "jwt", "session", "both"}, authVal)
	}

	rememberMeVal := *rememberMe
	if !set["remember-me"] && (authVal == "session" || authVal == "both") {
		rememberMeVal = promptBool(`Support "remember me" (longer-lived login)`, rememberMeVal)
	}

	jwtSecretVal := *jwtSecret
	if !set["jwt-secret"] && (authVal == "jwt" || authVal == "both") {
		jwtSecretVal = prompt("Existing JWT secret to reuse (blank = generate a new one; typed in PLAIN TEXT, not masked)", jwtSecretVal)
	}

	dbVal := *db
	if !set["db"] && promptBool("Use a database?", false) {
		dbVal = prompt("Databases (comma-separated: mongo,mysql,postgres,mssql)", "mongo")
	}
	dbTypes := splitCSV(dbVal)

	coreVal := *core
	if !set["core"] && len(dbTypes) > 1 {
		coreDefault := dbTypes[0]
		for _, t := range dbTypes {
			if strings.EqualFold(t, "mongo") {
				coreDefault = t
				break
			}
		}
		coreVal = promptChoice("Core database", dbTypes, coreDefault)
	}

	// Real core connection details are entirely optional — hitting Enter
	// through dbHostVal (or never passing -db-host at all) reproduces the
	// original blank-DSN .env output exactly, for anyone who'd rather
	// fill it in by hand. Every prompt after host is gated on host
	// actually being non-blank, both to avoid asking pointless questions
	// in that common case and because a bare port/name/user/password
	// with no host wouldn't produce a usable DSN anyway.
	dbHostVal := *dbHost
	if len(dbTypes) > 0 && !set["db-host"] {
		dbHostVal = prompt("Core database host (blank = leave DSN blank, fill in .env yourself later)", dbHostVal)
	}
	dbPortVal, dbNameVal, dbUserVal, dbPasswordVal := *dbPort, *dbName, *dbUser, *dbPassword
	if dbHostVal != "" {
		coreType := coreVal
		if coreType == "" && len(dbTypes) > 0 {
			coreType = dbTypes[0] // single -db type: core defaults to it, -core prompt never ran
		}
		if !set["db-port"] {
			dbPortVal = prompt("Core database port", scaffold.DefaultDBPort(coreType))
		}
		if !set["db-name"] {
			dbNameVal = prompt("Core database name", dbNameVal)
		}
		if !set["db-user"] {
			dbUserVal = prompt("Core database username (blank = no auth)", dbUserVal)
		}
		if !set["db-password"] {
			dbPasswordVal = prompt("Core database password (blank = no auth; typed in PLAIN TEXT, not masked)", dbPasswordVal)
		}
	}

	mergeServiceAuthVal := *mergeServiceAuth
	if !set["merge-service-auth"] && len(dbTypes) > 0 && (authVal == "jwt" || authVal == "both") {
		mergeServiceAuthVal = promptBool("Fold service-key (X-Api-Secret) auth into RequireAuth instead of a separate RequireServiceAuth?", mergeServiceAuthVal)
	}

	multitenantVal := *multitenant
	if !set["multitenant"] && (authVal == "jwt" || authVal == "session" || authVal == "both") {
		multitenantVal = promptBool("Multi-tenant Org propagation (auth.ContextWithOrg, session/JWT Org, core.User.OrgId)?", multitenantVal)
	}

	cfg := scaffold.NewAppConfig{
		AppName:          appName,
		OutputDir:        dir,
		ModulePath:       mod,
		UI:               uiVal,
		AuthKind:         authVal,
		RememberMe:       rememberMeVal,
		JWTSecret:        jwtSecretVal,
		DBTypes:          dbTypes,
		CoreDB:           coreVal,
		CoreDBHost:       dbHostVal,
		CoreDBPort:       dbPortVal,
		CoreDBName:       dbNameVal,
		CoreDBUser:       dbUserVal,
		CoreDBPassword:   dbPasswordVal,
		MergeServiceAuth: mergeServiceAuthVal,
		Multitenant:      multitenantVal,
	}

	if err := scaffold.NewApp(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created %s in %s\n", appName, cfg.TargetPath())
	if uiVal {
		fmt.Println("Custom UI homepage: edit templates/html/home/home.html and templates/html/shared/layout.html")
	}
	jwtSecretNote := "secret pre-filled in .env"
	if jwtSecretVal != "" {
		jwtSecretNote = "using the secret you provided (shared across apps)"
	}
	switch authVal {
	case "jwt":
		fmt.Printf("Auth: JWT bearer tokens — see auth/jwt.go (IssueJWT/VerifyJWT); %s.\n", jwtSecretNote)
	case "session":
		fmt.Println("Auth: sessions — see auth/session.go (StartSession/SessionFromRequest/EndSession); in-memory only.")
	case "both":
		fmt.Printf("Auth: JWT bearer tokens + sessions — see auth/jwt.go and auth/session.go; %s.\n", jwtSecretNote)
	}
	if mergeServiceAuthVal {
		fmt.Println("Service auth: folded into RequireAuth (X-Api-Secret, after JWT/session) — no separate RequireServiceAuth generated.")
	}
	if multitenantVal {
		fmt.Println("Multi-tenant: Org threaded through auth.ContextWithOrg/Org, session/JWT, and core.User.OrgId (see auth/context.go, auth/session.go, core/users.go).")
	}
	if len(dbTypes) > 0 {
		displayTypes := make([]string, len(dbTypes))
		for i, t := range dbTypes {
			displayTypes[i] = strings.ToLower(t)
		}
		displayCore := strings.ToLower(coreVal)
		if displayCore == "" {
			displayCore = displayTypes[0] // len(dbTypes) == 1: core defaults to it, no prompt ever ran
		}
		fmt.Printf("Databases: %s (core: %s) — see db/db.go (Connect/Close).\n", strings.Join(displayTypes, ", "), displayCore)
		if dbHostVal != "" {
			fmt.Println("Core connection DSN written to .env from the details you gave.")
		} else {
			fmt.Println("Core connection DSN left blank in .env — fill it in yourself, or re-run with -db-host.")
		}
		fmt.Println("Add more connections (and, if needed, a new database type) later with `nexler db add`.")
		fmt.Println("Run `go mod tidy` before building — this pulls in the driver package(s), which needs network access once.")
	}
	fmt.Println("Next steps:")
	fmt.Printf("  cd %s\n", cfg.TargetPath())
	fmt.Println("  go run main.go")
}

func runCreateRoute(route string, args []string) {
	fs := flag.NewFlagSet("create route", flag.ExitOnError)
	module := fs.String("module", "", "module (top-level group) this route belongs to, e.g. purchase")
	submodule := fs.String("submodule", "", "submodule (nested group) this route belongs to, e.g. verify")
	file := fs.String("file", "", "base file name for the generated handler/service/store/model files, e.g. verify; defaults to the route's package name (last of -module/-submodule)")
	name := fs.String("name", "", "override the identifier base used for handler function names, Request/Response types, and OperationIDs (default: derived from -module/-submodule); needed when adding a second, distinct route to an already-scaffolded package whose identifiers would otherwise collide with the first route's")
	service := fs.String("service", "", "reuse an existing service package instead of generating one, as module[/submodule], e.g. purchase or purchase/verify; \"none\" skips generating a service for this route entirely (stop at handler+model)")
	store := fs.String("store", "", "reuse an existing store package instead of generating one, as module[/submodule], e.g. purchase or purchase/verify; \"none\" skips generating a store for this route entirely (stop at handler+model)")
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	protected := fs.Bool("protected", false, "require auth for this route (registers in routes/protected instead of routes/public); when adding method(s) to an already-existing route package, applies only to the newly-added method(s), independently of the rest of the package")
	methods := fs.String("methods", "GET", "comma-separated HTTP methods to generate: GET, POST, PUT, PATCH, DELETE (OPTIONS is automatic and always 200 OK)")
	body := fs.String("body", "json", "request body shape for non-GET methods: json, form, or multipart")
	response := fs.String("response", "json", "response kind for every method: json (response.JSON, default), html (response.HTML), or raw (response.JSONRaw, no {\"data\": ...} envelope)")
	layout := fs.String("layout", "shared", "HTML layout for -response html: shared (default; reuses templates/html/shared/layout.html) or module (this route gets its own templates/html/<module>/layout.html copy)")
	ownRegister := fs.Bool("own-register", false, "scaffold this route as an independent second resource in an already-existing package, with its own Register<name>(mux) function instead of folding into the package's existing Register — requires -file (a name not already used in the package) and -name; see 'nexler help' for details")
	fs.Parse(reorderFlagsFirst(fs, args))

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	moduleVal := *module
	if !set["module"] {
		moduleVal = promptRequired("Module name")
	}

	submoduleVal := *submodule
	if !set["submodule"] {
		submoduleVal = prompt("Submodule (optional)", "")
	}

	fileVal := *file
	if !set["file"] {
		fileVal = prompt("File name (optional, default: package name)", "")
	}

	nameVal := *name

	serviceVal := *service
	storeVal := *store
	if !set["service"] && !set["store"] {
		// Neither flag was touched at all — ask the single yes/no gate
		// instead of unconditionally walking through two reuse prompts
		// (the "have to mention store and service" friction this gate
		// exists to remove), same shape as -db's own "Use a database?"
		// gate. Default true preserves today's behavior for anyone who
		// just accepts every default.
		if promptBool("Generate service and store layers for this route?", true) {
			serviceVal = prompt("Reuse existing service (optional, e.g. purchase/verify)", "")
			storeVal = prompt("Reuse existing store (optional, e.g. purchase/verify)", "")
		} else {
			serviceVal = "none"
			storeVal = "none"
		}
	} else {
		if !set["service"] {
			serviceVal = prompt("Reuse existing service (optional, e.g. purchase/verify; \"none\" to skip)", "")
		}
		if !set["store"] {
			storeVal = prompt("Reuse existing store (optional, e.g. purchase/verify; \"none\" to skip)", "")
		}
	}

	protectedVal := *protected
	if !set["protected"] {
		protectedVal = promptBool("Require auth", protectedVal)
	}

	methodsVal := *methods
	if !set["methods"] {
		methodsVal = prompt("HTTP methods (comma-separated)", methodsVal)
	}

	bodyVal := *body
	if !set["body"] && methodsNeedBody(methodsVal) {
		bodyVal = prompt("Request body shape [json/form/multipart]", bodyVal)
	}

	responseVal := *response
	if !set["response"] {
		responseVal = promptChoice("Response type", []string{"json", "html", "raw"}, responseVal)
	}

	layoutVal := *layout
	if !set["layout"] && responseVal == "html" {
		layoutVal = promptChoice("HTML layout", []string{"module", "shared"}, layoutVal)
	}

	dirVal := *dir
	if !set["dir"] {
		dirVal = prompt("App directory", dirVal)
	}

	if *ownRegister && (fileVal == "" || nameVal == "") {
		fmt.Fprintln(os.Stderr, "nexler: -own-register requires both -file (a name not already used in this package) and -name (to disambiguate this resource's identifiers from the package's existing one(s))")
		os.Exit(1)
	}

	cfg := scaffold.RouteConfig{
		Route:            route,
		Module:           moduleVal,
		Submodule:        submoduleVal,
		FileName:         fileVal,
		IdentName:        nameVal,
		ServiceRef:       serviceVal,
		StoreRef:         storeVal,
		ServiceRequested: set["service"],
		StoreRequested:   set["store"],
		AppDir:           dirVal,
		Protected:        protectedVal,
		Methods:          splitCSV(methodsVal),
		BodyKind:         bodyVal,
		ResponseKind:     responseVal,
		LayoutKind:       layoutVal,
		OwnRegister:      *ownRegister,
	}

	result, err := scaffold.NewRoute(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	group := moduleVal
	if submoduleVal != "" {
		group = moduleVal + "/" + submoduleVal
	}
	kind := "public"
	if protectedVal {
		kind = "protected"
	}

	if result.Created {
		fmt.Printf("Added %s route %s [%s] (%s)\n", kind, route, strings.Join(result.Added, ","), group)
		layers := []string{"handler"}
		var reused []string
		switch serviceVal {
		case "none":
		case "":
			layers = append(layers, "service")
		default:
			reused = append(reused, "service "+serviceVal)
		}
		switch storeVal {
		case "none":
		case "":
			layers = append(layers, "store")
		default:
			reused = append(reused, "store "+storeVal)
		}
		layers = append(layers, "model")
		fmt.Printf("Generated %s files", strings.Join(layers, ", "))
		if len(reused) > 0 {
			fmt.Printf(", reusing %s", strings.Join(reused, " and "))
		}
		fmt.Println(", and wired the route into routes/" + kind + "/" + kind + ".go.")
	} else {
		if len(result.Added) > 0 {
			if result.NewFile != "" && result.NewFile == result.PrimaryFile {
				fmt.Printf("Route %s (%s) — created independent resource %s in the existing package (%s), with its own %s(mux); wired into routes/%s/%s.go alongside the package's existing Register call (%s)\n",
					route, group, result.NewFile, strings.Join(result.Added, ", "), result.RegisterFuncName, kind, kind, kind)
			} else if result.NewFile != "" {
				fmt.Printf("Route %s (%s) already existed — added method(s) %s in a new file %s (Register wiring added to %s) (%s)\n",
					route, group, strings.Join(result.Added, ", "), result.NewFile, result.PrimaryFile, kind)
			} else {
				fmt.Printf("Route %s (%s) already existed — added method(s): %s (%s)\n", route, group, strings.Join(result.Added, ", "), kind)
			}
		} else {
			fmt.Printf("Route %s (%s) already existed — no new methods requested\n", route, group)
		}
		if len(result.Skipped) > 0 {
			fmt.Printf("Already present, left unchanged: %s\n", strings.Join(result.Skipped, ", "))
		}
		if len(result.LayersAdded) > 0 {
			fmt.Printf("Added missing layer(s) to the existing route: %s\n", strings.Join(result.LayersAdded, ", "))
		}
		if result.Note != "" {
			fmt.Println(result.Note)
		}
	}
}

// runCreateLayer implements `nexler create store|service <name>` — see
// internal/scaffold/layer.go for the actual scaffolding logic. kind is
// "store" or "service". <name> addresses the package the same way
// -service/-store's own reuse references do: module[/submodule].
func runCreateLayer(kind string, args []string) {
	fs := flag.NewFlagSet("create "+kind, flag.ExitOnError)
	file := fs.String("file", "", "base file name for the generated file, e.g. verify; defaults to \"<pkgname>"+kind+"\" (e.g. \"appsservice\"/\"appsstore\" for <name> apps)")
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	var store *string
	if kind == "service" {
		store = fs.String("store", "", "link this service to an already-scaffolded store package, as module[/submodule], e.g. purchase or purchase/verify — optional, purely informational (only affects the generated TODO comment)")
	}
	fs.Parse(reorderFlagsFirst(fs, args))

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	name := ""
	if fs.NArg() >= 1 {
		name = fs.Arg(0)
	} else {
		name = promptRequired(strings.ToUpper(kind[:1]) + kind[1:] + " name (module[/submodule], e.g. purchase or purchase/verify)")
	}

	fileVal := *file
	if !set["file"] {
		fileVal = prompt("File name (optional, default: <pkgname>"+kind+")", "")
	}

	dirVal := *dir
	if !set["dir"] {
		dirVal = prompt("App directory", dirVal)
	}

	cfg := scaffold.LayerConfig{
		Kind:     kind,
		Name:     name,
		FileName: fileVal,
		AppDir:   dirVal,
	}
	if kind == "service" {
		storeVal := *store
		if !set["store"] {
			storeVal = prompt("Link to an existing store (optional, e.g. purchase/verify)", "")
		}
		cfg.StoreRef = storeVal
	}

	result, err := scaffold.NewLayer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created standalone %s package: %s\n", kind, result.Path)
	fmt.Println("Not wired into anything — import it by hand from wherever needs it.")
}

// reorderFlagsFirst rewrites args so every flag (and, for flags that take a
// value, its value token) comes before every positional argument, without
// otherwise changing their relative order. flag.FlagSet.Parse stops at the
// first non-flag token by design, so e.g. `create app test -ui` (name
// before flag) would otherwise silently leave -ui unparsed — this lets
// flags appear anywhere on the command line, not just before positionals.
//
// fs must already have every flag defined (fs.Lookup is used to tell
// value-taking flags from boolean ones, via the same IsBoolFlag() method
// the flag package itself uses internally). An unrecognized "-x" is passed
// through as-is so fs.Parse still reports its usual error for it.
func reorderFlagsFirst(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue // -name=value: value already in this token
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // unknown flag; let fs.Parse produce its usual error
		}
		if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bv.IsBoolFlag() {
			continue // boolean flag: no separate value token
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

// splitCSV splits a comma-separated flag value into trimmed, non-empty parts.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// methodsNeedBody reports whether csv (a comma-separated -methods value)
// contains any verb other than GET — used to skip the "Request body
// shape" prompt for GET-only routes, since GET never has a request body
// (see route.go's decodeCall/bodyDescription).
func methodsNeedBody(csv string) bool {
	for _, m := range splitCSV(csv) {
		if strings.ToUpper(strings.TrimSpace(m)) != "GET" {
			return true
		}
	}
	return false
}

func printUsage() {
	fmt.Println(`nexler — scaffolding CLI for the nexler framework

Any flag (and the app name / route path's related settings) left off below is asked for
interactively instead — press Enter to accept the shown default, or pass the flag to
skip the question entirely (e.g. for scripts/CI).

Usage:
  nexler create app <name> [-dir <output-dir>] [-module <module-path>] [-ui] [-auth none|jwt|session|both] [-remember-me] [-db mongo,mysql,postgres,mssql] [-core <type>] [-db-host <host>] [-db-port <port>] [-db-name <name>] [-db-user <user>] [-db-password <password>] [-merge-service-auth] [-multitenant]
      Scaffold a new app with the standard handlers/services/store/models
      layout. Scaffolding itself is always fully self-contained: no
      network access, no git required — every template ships embedded
      inside this binary. (Exception: if -db selects a driver, the
      generated app's very first "go build" needs one "go mod tidy" —
      network access, on your machine, not nexler's — to fetch it; see
      -db below. Without -db, nothing ever changes here.) By default the
      homepage (GET /) is the built-in embedded
      templates/html/home.html. -ui serves it instead from editable,
      disk-based templates/html/home/home.html (plus the shared
      templates/html/shared/layout.html every -response html route also
      falls back to) via response.HTML — the same convention a route
      scaffolded with -response html gets — so the homepage can be edited
      without a rebuild; the embedded home.html/home.css aren't generated
      at all in that case, since nothing serves them.

      -auth (default none) picks what middleware.RequireAuth (used by
      every -protected route) actually enforces: none leaves today's
      unverified stub in place; jwt generates auth/jwt.go (stdlib-only
      HMAC-SHA256 signed bearer tokens — IssueJWT/VerifyJWT — with a
      random secret pre-filled into .env); session generates
      auth/session.go (an in-memory session store + HttpOnly cookie —
      StartSession/SessionFromRequest/EndSession); both generates both,
      and RequireAuth accepts either — e.g. bearer tokens for API
      clients and session cookies for browser/UI clients on the very
      same protected routes. No /login or /logout route is generated —
      call these functions from your own login handler once credentials
      are verified, the same "TODO the business logic" way every other
      generated handler works. -remember-me (only meaningful with
      -auth session or both) adds a rememberMe bool parameter to
      StartSession that swaps its default 24h session lifetime for 30
      days. Sessions are in-memory only — lost on restart, not shared
      across instances; swap the store before scaling beyond one
      instance in production. When -auth is jwt or both AND -db is also
      set, the app additionally gets middleware.RequireServiceAuth — a
      second, wholly separate auth mechanism (checking an X-Api-Key
      header against a new core_services table) for app-to-app calls,
      independent of RequireAuth so a service-only route can never
      accidentally accept an end-user JWT. That X-Api-Key must never be
      embedded in any UI-facing code. core.CreateService/VerifyServiceKey/
      RevokeService (core/services.go) manage it; a companion core_users
      table (core/users.go) is a minimal local record keyed by the same
      UserId as the JWT's "sub" claim (Username/UserRole/UserType/Status
      only — not a profile store). Both tables are provisioned by
      "nexler init db", not here.

      -merge-service-auth (only meaningful with -auth jwt|both and -db;
      default false) folds that same X-Api-Key/X-Api-Secret service-key
      check into RequireAuth itself instead of generating a separate
      RequireServiceAuth — RequireAuth then tries JWT (then session, for
      -auth both), then falls back to the service key, so a
      service/automation caller can hit the exact same -protected routes a
      human user would, rather than needing a service-only route. A
      service-key match still attaches auth.ContextWithService, plus
      auth.ContextWithSubject set to the service's name (purely so
      subject-reading code has something to evaluate — not a claim that a
      service should pass user-level authorization). Off by default: a pure
      API service that wants user-only and service-only routes to stay
      structurally distinct should leave this unset and keep using the
      separate RequireServiceAuth. Retrofit an already-scaffolded app with
      "nexler update -merge-service-auth".

      -multitenant (only meaningful with -auth jwt|session|both; default
      false) threads a tenant Org through auth.ContextWithOrg/Org
      (auth/context.go), StartSession/SessionFromRequest's org parameter
      (auth/session.go, when -auth includes session), and core.User.OrgId
      (core/users.go, when a core database connection AND a JWT-capable
      -auth choice are both present). auth/jwt.go's Claims.Org field and
      IssueJWT's org parameter are already unconditional, so JWT-only apps
      need no further wiring. A service-key-authenticated request (X-Api-
      Secret) never gets an Org attached — a service isn't tied to a
      tenant org. Combining -multitenant with -auth none is an error:
      there's no authenticated subject to attach an Org to. Retrofit an
      already-scaffolded app with "nexler update -multitenant".

      -db (default none) picks which database driver(s) get generated
      support in db/ — any comma-separated subset of mongo, mysql,
      postgres, mssql. This only selects which drivers are compiled in
      (mysql -> github.com/go-sql-driver/mysql, postgres ->
      github.com/jackc/pgx/v5 via its database/sql shim, mssql ->
      github.com/microsoft/go-mssqldb, mongo ->
      go.mongodb.org/mongo-driver/v2) — the actual named connections (any
      number, any of the selected types) are declared purely at runtime
      via {APPNAME}_DB_CONNECTIONS in .env (see the comment nexler writes
      there), so adding or removing a connection later is just an .env
      edit, never a re-scaffold. db.Connect() (called from main at
      startup) opens every declared connection up front, failing fast if
      one can't be reached; db.Close() (wired into a graceful shutdown on
      SIGINT/SIGTERM when -db is set) closes them all. db.SQL(name) and
      db.Mongo(name) fetch a specific connection by name. This only
      covers opening and closing connections — no query builder, ORM, or
      migrations. Since the drivers are real third-party dependencies
      (there's no stdlib option for any of these), run "go mod tidy"
      once before your first build.

      -core picks which -db type is "core" — the app's conventional
      default connection, pre-filled into .env as {APPNAME}_DB_CORE_TYPE
      / _DSN under connection name "core". Every route's store.go TODO
      comment points at db.SQL("core")/db.Mongo("core") accordingly (see
      "nexler create <route>" below). If -db selects only one type, that
      type is core automatically and -core is never needed. If -db
      selects more than one, -core is required to be one of them
      (defaults, when omitted, to mongo if selected, else the first type
      listed).

      -db-host (default blank) lets you give the core connection's real
      host/port/name/user/password right now instead of leaving _DSN
      blank for you to fill in by hand afterward — -db-port/-db-name/
      -db-user/-db-password (all optional, prompted interactively along
      with -db-host when omitted) are only consulted when -db-host is
      actually set; leaving it blank (the default — just press Enter)
      reproduces the original blank-DSN .env output exactly. -db-port
      defaults to the type's conventional port (3306/5432/1433/27017)
      when asked interactively. Password is passed/prompted in plain
      text, never masked — there's no hidden-input prompt in this CLI.

      Example: nexler create app test -ui
      Example: nexler create app test -auth jwt
      Example: nexler create app test -auth both -remember-me
      Example: nexler create app test -db postgres,mongo -core mongo
      Example: nexler create app test -db mongo -db-host localhost -db-name test
      Example: nexler create app test -auth both -db mongo -merge-service-auth
      Example: nexler create app test -auth session -multitenant

  nexler create <route> -module <name> [-submodule <name>] [-protected] [-methods GET,POST] [-body json] [-response json] [-layout shared] [-dir <app-dir>] [-file <name>] [-name <name>] [-own-register]
      Scaffold a route inside an existing app: generates a handler,
      service, store, and model file grouped under module[/submodule].
      The route itself may contain "{name}" path parameter segments (Go
      1.22+ ServeMux wildcards, e.g. /admin/user/{id}/profile, or a
      trailing {name...} catch-all) — every generated handler method
      decodes them via request.DecodePath into the same Request struct as
      its query/body fields (matched by json tag), and they're documented
      as OpenAPI path parameters automatically. A literal "?" or "#" in
      the route is rejected — ServeMux doesn't parse a query string out of
      a route pattern, so use a path parameter instead.
      -methods (default GET) picks which HTTP methods get a handler and a
      Request/Response struct pair each — GET's request struct is
      query-parameters-only, others follow -body (json/form/multipart,
      default json). -response (default json) picks how every method
      writes its result: json calls response.JSON (the {"data": ...}
      envelope); html calls response.HTML(w, r, module, name, title, data),
      which renders templates/html/<module>/<name>.html inside a
      layout.html — both generated once, at scaffold time (not lazily on
      first request), as editable placeholders. -layout (default shared,
      only asked/meaningful when -response html) picks which layout.html:
      shared reuses the single templates/html/shared/layout.html every
      module falls back to if it has no layout of its own; module gives
      this route its own templates/html/<module>/layout.html copy instead.
      A module's own copy always wins over the shared one if both exist.
      OPTIONS is wired automatically for every route and always returns
      200 OK. Public routes (default) are wired into routes/public/
      public.go; -protected wraps each handler with middleware.RequireAuth
      and wires into routes/protected/protected.go instead. main.go itself
      is never touched after the initial
      scaffold. Run from inside the app directory, or pass -dir. If the
      route already exists, this instead adds only the newly-requested
      methods to it (skipping any already registered) rather than
      erroring.
      Handler function names, Request/Response types, and OperationIDs are
      derived from -module/-submodule + verb, not the URL path — adding a
      second, distinct route under the same -module/-submodule as an
      existing one (different path, overlapping verb) fails with an error
      naming the colliding identifier(s); pass -name <value> to override
      the identifier base for this route (e.g. -name New) without changing
      where its files live. -file <name> targets a specific handler/model
      file within the package (default: the package name) — naming a file
      that doesn't exist yet creates a genuine second file for just this
      invocation's method(s), but its mux.HandleFunc/openapi.Register
      wiring still lands in the package's one existing Register, since Go
      only allows one func Register per package.

      -own-register changes that: combined with a -file value that doesn't
      exist yet (and a required -name), it scaffolds that file as a fully
      independent second resource instead — its own Register<name>
      function, own middleware/openapi imports, own service/store/model
      files, wired into the aggregator with an additional call alongside
      the package's existing Register call. Only meaningful when the
      target package already exists. Off by default — every existing
      -file/-name combination keeps behaving exactly as before.

      Example: nexler create /purchase/verify -module purchase -submodule verify -methods GET,POST -body json -protected
      Example: nexler create /home -module home -response html
      Example: nexler create /purchase/new -module purchase -submodule verify -methods POST -name New
      Example: nexler create /admin/providers/sms -module admin -submodule providers -file sms -name Sms -own-register -protected

  nexler db add <name> -type <mongo|mysql|postgres|mssql> [-dir <app-dir>] [-host <host>] [-port <port>] [-dbname <name>] [-user <user>] [-password <password>]
      Adds a new named database connection to an existing app's .env — not
      to be confused with "nexler init db" above (that provisions the core
      schema on an already-declared connection; this declares a new
      connection in the first place). If -type isn't a driver the app was
      originally scaffolded with (e.g. adding mssql to an app created with
      -db mongo), this also retrofits the missing driver support first:
      the per-dialect helper package (mysql/mysql.go etc.), db/sql.go's
      driver import (or a fresh db/sql.go, if this is the app's first SQL
      dialect ever), and — only if the dialect *family* (SQL vs. mongo) is
      new to the app as a whole — db/db.go's Connect/Close cases, inserted
      via marker comments (// nexler:db-connect, // nexler:db-close) so
      any hand edits elsewhere in that file survive. Works even on an app
      that was scaffolded with no -db at all (its first-ever connection).
      <name> must be a valid identifier and can't be "core" — that name is
      reserved for the app's own conventional default connection, which
      this command never touches or replaces; the new connection is
      reachable as db.SQL("<name>")/db.Mongo("<name>") but never becomes
      core. -host (and the other connection-detail flags) work exactly
      like "create app"'s -db-host above — blank leaves the new
      connection's DSN blank for you to fill in by hand.

      Example: nexler db add analytics -type postgres -host localhost -dbname analytics
      Example: nexler db add reports -type mssql -dir ./myapp

  nexler init db [-dir <app-dir>]
      Provisions a generated app's "core" system data on its actual core
      database (see {APPNAME}_DB_CORE_TYPE/_DSN in its .env): core_config
      (backing GetSetting/SetSetting), core_error_log (backing LogError,
      auto-called by middleware.Recover on a panic and by response.Error
      on a 5xx), core_kgate_channels (backing kgate's
      Subscribe/Unsubscribe/ResumeAll channel registry, if "nexler init
      kgate" has been run — see below), core_users, and core_services
      (backing API-key/service-to-service auth, if the app has -auth
      jwt|both — see -auth above) — each a table plus its own insert/
      upsert stored procedure, for mysql/postgres/mssql, or a unique
      index, for mongo. All five are provisioned unconditionally,
      regardless of which optional features the app actually uses. This
      is a separate, explicit step
      — not run automatically by "nexler create app" or by the generated
      app's own startup — since a physical database can be shared by
      several scaffolded apps (provisioning it from every one of them on
      every boot would be redundant) and production setups often run the
      app itself with a low-privilege DB user that can't run DDL at all.
      Every statement is idempotent — safe to run again, including against
      a database another app already provisioned. Requires -db to have
      been set at "nexler create app" time (i.e. .env to actually have a
      core connection to fill in); unlike scaffolding itself, this needs
      real network access to reach the database.

      Example: nexler init db -dir ./myapp

  nexler init kpass [-dir <app-dir>]
      Adds a client for kpass (klivolks' permission-check service) to an
      existing generated app: kpass/kpass.go (Check/Allowed, plus
      UserIDFromRequest when the app has -auth jwt|session|both) and
      KPASS_URL/KPASS_CLIENT_ID/KPASS_API_SECRET (blank, for you to fill
      in) added to .env if not already present. Check(ctx, userID,
      resource, extra) posts {user_id, resource, ...extra} to
      {KPASS_URL}/permission/check/ and returns kpass's full response
      (status, message, access, user_role, user_type) — kept separate
      from Allowed (the plain "is this ALLOW" bool helper) so callers
      needing more, e.g. restricting data fetched based on user_role/
      user_type, call Check directly instead of being forced through a
      yes/no. Unlike "nexler init db", this needs no network access at
      all — it's pure local file scaffolding, safe to run again (won't
      duplicate .env lines, won't overwrite an existing kpass/kpass.go).

      Example: nexler init kpass -dir ./myapp

  nexler init kgate [-dir <app-dir>]
      Adds a client for kgate (klivolks' message broker) to an existing
      generated app: kgate/kgate.go (Subscribe/Unsubscribe/ResumeAll/
      Publish/HandleWebhook/Register) and KGATE_CLIENT_ID/KGATE_WS_SERVER/
      KGATE_HTTP_SERVER/KGATE_ORIGIN/KGATE_WEBHOOK_SECRET (blank, for you
      to fill in) added to .env if not already present. Also auto-wires
      kgate.Register (a POST /webhooks/kgate fallback route) into
      routes/public/public.go, ready to receive immediately.

      Unlike "nexler init kpass", this requires the target app to already
      have a core database connection (-db at "nexler create app" time):
      channel subscriptions are deliberately not static/env-driven —
      Subscribe(ctx, channel) records the channel in the core database and
      starts a background goroutine maintaining a live WebSocket
      connection for it, so a workflow (e.g. order creation) can start
      listening on a new channel mid-run, durable across restarts via
      ResumeAll(ctx) (add this near db.Connect() in your own main.go — not
      wired automatically, since main.go is never touched again after the
      initial scaffold). Every delivered event, whether from a live
      subscription or the /webhooks/kgate fallback (HMAC-SHA256-verified
      against KGATE_WEBHOOK_SECRET, matching kgate's X-Signature header),
      is dispatched to the single generated handleEvent function — edit
      that for your event-processing logic. Needs "go mod tidy" once
      (pulls in github.com/gorilla/websocket, Go's stdlib has no WebSocket
      client) and "nexler init db" (to provision core_kgate_channels)
      before Subscribe/Unsubscribe/ResumeAll will work. Registering this
      app's public /webhooks/kgate URL with kgate's server as this
      client's fallback endpoint is a separate, manual step.

      Example: nexler init kgate -dir ./myapp

  nexler init tenant [-dir <app-dir>]
      Adds a guarded tenantorg listing/delete admin API to an existing
      generated app, for a hub (or similar) app to manage tenants:
      tenant/tenant.go (TenantOrg{ID, Name, Settings, Status, CreatedAt,
      UpdatedAt} — Settings is a free-form organization configuration
      blob; ListTenantOrgs/DeleteTenantOrg only, no generated create/
      update path yet) and handlers/admin/tenants/tenants.go (GET
      /admin/tenants, DELETE /admin/tenants/{id}), wired into
      routes/protected/protected.go automatically. Not the same thing as
      "-multitenant" (see "nexler create app" above) — that threads a
      tenant Org through auth context; this is the actual store of
      tenant-organization records a hub app manages, independent of it.

      Deliberately not part of core/ — whether an app has a multi-tenant
      model at all is provider/business-specific, not something every
      -db app needs — so this is its own opt-in command, same shape as
      "nexler init kgate"/"nexler init kpass". Requires the target app to
      already have a core database connection (-db at "nexler create
      app" time) and a JWT-capable -auth choice (jwt or both) — the
      delete endpoint needs an authenticated caller to gate at all. Both
      routes go through middleware.RegisterTask, every route's guard
      since it registers into an app-wide task registry and — via its
      middleware.PermissionCheck hook — checks authorization once
      "nexler init kpass" has wired it; authenticated but not authorized
      until then. DeleteTenantOrg is a real, hard delete — no undo. Run
      "nexler init db" afterward to provision tenant_orgs on a SQL core
      (Mongo needs no provisioning).

      Example: nexler init tenant -dir ./myapp

  nexler init docker [-dir <app-dir>]
      Adds a multi-stage Dockerfile (build + serve) and a docker-compose.yml
      to an existing generated app. The Dockerfile's golang:<version>-alpine
      build stage always matches the app's actual go.mod "go X.Y" directive
      (read fresh each time, not nexler's own current default), and EXPOSE/
      the compose ports mapping match the app's real {PREFIX}_PORT from
      .env (defaulting to 8080). .env itself is never baked into the image
      — the compose file passes it through via env_file: at container
      runtime instead, so no secret ends up inside an image layer.
      docker-compose.yml is added to .gitignore (created if the app doesn't
      have one yet); Dockerfile is deliberately not gitignored, since "nexler
      init ci" builds from it. Refuses if either file already exists.

      Example: nexler init docker -dir ./myapp

  nexler init ci [-dir <app-dir>] [-registry dockerhub|github]
      Adds .github/workflows/release.yml: builds and pushes a Docker image
      whenever a GitHub Release is published. -registry picks where images
      go — "github" (GitHub Container Registry, ghcr.io, authenticated via
      the automatic GITHUB_TOKEN, no repo secrets needed) or "dockerhub"
      (authenticated via DOCKERHUB_USERNAME/DOCKERHUB_TOKEN secrets you add
      under repo Settings > Secrets and variables > Actions) — prompted
      interactively if not passed. Both generated workflows are fully
      static (no per-app values baked in — GitHub's own
      github.repository_owner/github.event.repository.name/secrets.*
      contexts resolve at workflow-run time), so the file is identical
      across every nexler app regardless of registry choice. Doesn't
      require a Dockerfile to already exist (prints a reminder if one's
      missing rather than refusing) since you may add one by hand or run
      "nexler init docker" afterward. Refuses if release.yml already exists.

      Example: nexler init ci -dir ./myapp -registry github

  nexler init kube [-dir <app-dir>] [-registry dockerhub|github] [-image <ref>] [-namespace <ns>] [-replicas <n>]
      Adds k8s/deployment.yaml: a Secret (every real value from .env),
      a Deployment (references the Secret via envFrom, TCP readiness/
      liveness probes on the app's port, conservative default
      resources.requests/limits), and a ClusterIP Service, as three
      "---"-separated documents in one file. -image sets the image:
      reference directly; otherwise -registry picks how it's derived —
      "github" reads ghcr.io/<owner>/<repo>:latest straight from go.mod's
      module path (erroring if that path isn't github.com/<owner>/<repo>
      shaped — nexler never shells out to git to guess an owner),
      "dockerhub" prompts for your Docker Hub username and derives
      <user>/<repo>:latest — prompted interactively (both -registry
      itself, and the username for dockerhub) if -image/-registry weren't
      passed. -namespace is omitted from every resource by default (so
      kubectl's -n flag / current context decides) unless set explicitly.
      -replicas defaults to 1. k8s/deployment.yaml is added to .gitignore
      (created if the app doesn't have one yet) — unlike docker-compose.yml
      (which only references .env by path at container runtime), the
      Secret document here embeds .env's actual values, since Kubernetes
      has no equivalent way to read a local file at deploy time. Refuses
      if k8s/deployment.yaml already exists.

      Example: nexler init kube -dir ./myapp -registry github
      Example: nexler init kube -dir ./myapp -image ghcr.io/me/myapp:latest -replicas 3

  nexler add <package>[@version] [-dir <app-dir>] [-as <name>]
      Vendors a static asset package from npm into an existing app, without ever giving the
      app itself a package.json or node_modules: nexler shells out to "npm install <package>
      --prefix <scratch-tmp-dir> --no-save --no-audit --no-fund" into a throwaway temp
      directory (not your app directory), then copies the installed package's dist/ contents
      (flattened) — or, if it has no dist/, the whole package minus obvious junk (tests, docs,
      .git, package.json, README*, etc.) — into templates/static/vendors/<name>/. <name>
      defaults to the bare package name (scope stripped, "/" replaced with "-", e.g.
      @popperjs/core -> popperjs-core), overridable with -as. Re-running for the same <name>
      refreshes it (old contents removed first) — no -force flag, since this is a vendored,
      nexler-owned directory, not something you hand-edit in place. Requires npm on PATH and
      real network access — the one other place nexler itself (not just the generated app)
      touches the network, alongside "nexler init db". Since templates/static is embedded into
      the generated app's own binary at its own build time, new assets aren't served until you
      rebuild — and nexler add never wires a <link>/<script> tag into any page itself, so add
      the ones you need to whichever page(s) actually use the package.

      Example: nexler add bootstrap
      Example: nexler add bootstrap@5.3.3 -as bootstrap
      Example: nexler add @popperjs/core

  nexler update [-dir <app-dir>] [-merge-service-auth] [-multitenant]
      Brings an existing generated app's nexler-owned files — ones you generally don't
      hand-edit — up to date with whatever the current nexler binary knows how to generate,
      without needing to coincidentally trigger it via "nexler create <route>" (which already
      runs these same checks, but only as a side effect, and only the ones relevant to the
      route being created). Today this covers openapi/openapi.go (regenerated in full whenever
      it predates an Operation field nexler now sets, e.g. Tags/RespUnwrapped/ClientIdAuth —
      safe, since that file has no per-app content at all) and response/response.go (JSONRaw
      inserted if missing, append-only, never a full rewrite, since that file is realistically
      hand-extended). Extended over time as nexler adds more such checks. Never touches
      handlers/services/store/models, main.go, .env, or templates/html/* — anything you're
      expected to hand-edit is out of scope by design. Prints "updated" or "already up to
      date" per file checked.

      -merge-service-auth is a separate, explicit opt-in step (not one of the
      unconditional checks above): it converts an already-scaffolded -auth
      jwt|both -db app from a separate middleware.RequireServiceAuth to the
      folded-into-RequireAuth form -merge-service-auth on "create app"
      generates (see that flag's own help text above) — removing
      middleware/service_auth.go and regenerating middleware/auth.go.
      Deliberately never runs automatically: merged-vs-separate is a real
      design choice per app, not a one-true-shape bugfix, so nothing flips it
      without being asked. Refuses (naming the file) if
      middleware/service_auth.go has been hand-edited since it was
      generated, rather than silently discarding the changes. A no-op if the
      app isn't eligible (needs -auth jwt|both and -db) or is already
      merged.

      -multitenant is likewise a separate, explicit opt-in step: it threads a
      tenant Org through an already-scaffolded -auth jwt|session|both app's
      auth/context.go (ContextWithOrg/Org), auth/session.go
      (StartSession/SessionFromRequest's org parameter, when the app has
      sessions), middleware/auth.go (attaching Org after a successful JWT or
      session check), and core/users.go (OrgId, when present) — see
      "create app -multitenant" above. Never runs automatically, same
      reasoning as -merge-service-auth: whether an app wants tenant-scoped
      data is a per-app design choice. Refuses (naming the file) if any of
      these files has been hand-edited since it was generated. A no-op if
      the app isn't eligible (needs -auth jwt, session, or both).

      Example: nexler update -dir ./myapp
      Example: nexler update -dir ./myapp -merge-service-auth
      Example: nexler update -dir ./myapp -multitenant

  nexler version
      Print the CLI version.

  nexler help
      Show this message.`)
}
