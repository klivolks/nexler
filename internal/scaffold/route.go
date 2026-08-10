// This file implements `nexler create <route> -module <name>
// [-submodule <name>] [-protected] [-methods GET,POST] [-body json]` —
// scaffolding a route's handler, service, store, and model files inside
// an *existing* generated app, and wiring the route into
// routes/public/public.go or routes/protected/protected.go.
//
// Each selected HTTP method gets its own handler function and its own
// Request/Response struct pair in models/ (GET's request is
// query-parameters-only; other methods follow -body). OPTIONS is wired
// automatically for every route via middleware.HandleOptions and is not
// selectable.
//
// If the route's handler package already exists, running this again adds
// only the newly-requested methods to it (see addMethodsToExistingRoute)
// instead of erroring — new handler functions, Register(mux) lines, and
// model structs are inserted before stable marker comments left in the
// generated files ("// nexler:handlers", "// nexler:register",
// "// nexler:models"), the same technique wireAggregator already uses for
// routes/public/public.go and routes/protected/protected.go. This only
// works for files that have those markers — files scaffolded before this
// feature existed will get a clear error explaining how to add the
// marker (or the method) by hand.
//
// There is no route registry or reflection here by design (consistent
// with the rest of nexler): each route gets its own small package with a
// Register(mux) function, and the appropriate routes/ aggregator file is
// edited to import and call it. main.go itself calls routes.Register(mux)
// once and is never touched again.
package scaffold

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// RouteResult reports what NewRoute actually did, so the CLI can print an
// accurate message either way.
type RouteResult struct {
	// Created is true if this was a brand-new route (all four files were
	// generated); false if methods were added to an existing route.
	Created bool
	// Added lists the HTTP methods that were actually added.
	Added []string
	// Skipped lists requested methods that were already registered for
	// this route and were left untouched (only set when Created is false).
	Skipped []string
}

// RouteConfig holds the parameters for scaffolding one route.
type RouteConfig struct {
	// Route is the URL path to register, e.g. "/asd". Must start with "/".
	Route string
	// Module is the top-level grouping for the route, e.g. "purchase".
	Module string
	// Submodule is an optional nested grouping, e.g. "verify".
	Submodule string
	// FileName optionally overrides the generated files' base name
	// (handlers/services/store/models), e.g. "verify" writes
	// handlers/<relDir>/verify.go instead of handlers/<relDir>/<pkgName>.go.
	// Sanitized the same way as Module/Submodule. Only affects the file
	// name on disk — the Go package name, exported handler functions, the
	// models import alias, and the OpenAPI operationId all keep deriving
	// from Module/Submodule as before. Defaults to the route's own package
	// name (the last of Module/Submodule) when empty.
	FileName string
	// ServiceRef, when non-empty, points this route at an already-scaffolded
	// service package instead of generating a new one — a "module[/submodule]"
	// reference addressed the same way Module/Submodule are, e.g. "purchase"
	// or "purchase/verify". The referenced package must already exist.
	// Empty (default) generates a fresh service for this route, as before.
	ServiceRef string
	// StoreRef is the same as ServiceRef, for the store layer.
	StoreRef string
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
	// Protected marks the route as requiring auth. It's registered in
	// routes/protected/protected.go and its handler wraps itself with
	// middleware.RequireAuth. False (the default) registers it in
	// routes/public/public.go instead.
	Protected bool
	// Methods is the set of HTTP methods to generate handlers for, e.g.
	// ["GET", "POST"]. Defaults to ["GET"] if empty. OPTIONS is handled
	// automatically for every route and must not be listed here.
	Methods []string
	// BodyKind describes how non-GET methods' request bodies are shaped:
	// "json" (default), "form", or "multipart". Ignored for GET, which
	// always gets a query-parameter-only request struct.
	BodyKind string
	// ResponseKind describes how every method's response is written:
	// "json" (default), which wraps the Response struct in response's
	// {"data": ...} envelope, or "html", which renders it through
	// response.HTML into templates/html/<module>/<name>.html.
	ResponseKind string
	// LayoutKind picks which layout.html an html-response route's content
	// renders inside: "shared" (default) reuses the single
	// templates/html/shared/layout.html every module falls back to;
	// "module" gives this route its own templates/html/<module>/layout.html
	// copy instead. Ignored when ResponseKind isn't "html".
	LayoutKind string
}

// routeMethod is one HTTP method's worth of generated identifiers,
// precomputed in Go so the templates don't need conditional logic.
type routeMethod struct {
	Verb            string // "GET", "POST", ...
	VerbTitle       string // "Get", "Post", ... — used in identifiers
	RegisterExpr    string // e.g. "HandleXGet" or "middleware.RequireAuth(HandleXGet)"
	BodyDescription string // human-readable, used in model doc comments
	OperationID     string // e.g. "getPing" — used in the openapi.Register call
	ReqContentType  string // e.g. "application/json"; empty for GET (no body)
	DecodeCall      string // e.g. "request.DecodeJSON(r, &req)" — how the handler decodes into its Request struct
	HTMLContentName string // e.g. "verify-content" — the response.HTML content-template name for this method, when ResponseKind is "html"
}

// routeData is what's available to route_templates/*.tmpl placeholders.
type routeData struct {
	PkgName           string // e.g. "verify"
	Name              string // e.g. "Verify" — used in identifiers like HandleVerify
	Route             string // e.g. "/asd"
	ModulePath        string // the app's own module path, from its go.mod
	RelDirSlash       string // e.g. "purchase/verify", forward-slashed for import paths
	Protected         bool   // whether handlers wrap themselves with middleware.RequireAuth
	Alias             string // e.g. "purchaseverify" — used as the models package import alias
	Module            string // e.g. "purchase" — the top-level module, always set regardless of Submodule; used as the OpenAPI operation tag
	HasServiceRef     bool   // true when this route reuses an existing service package instead of generating its own
	ServiceImportPath string // e.g. "example.com/app/services/purchase/verify" — the reused service's import path
	ServicePkgName    string // e.g. "verify" — the reused service's package name
	HasStoreRef       bool   // true when this route reuses an existing store package instead of generating its own
	StoreImportPath   string // e.g. "example.com/app/store/purchase/verify" — the reused store's import path
	StorePkgName      string // e.g. "verify" — the reused store's package name
	HTMLResponse      bool   // whether every method responds via response.HTML instead of response.JSON
	RawResponse       bool   // whether every method responds via response.JSONRaw (no {"data": ...} envelope) instead of response.JSON
	HasCoreDB         bool   // whether the app has a "core" database connection (see readCoreDBType)
	CoreDBAccessor    string // "SQL" or "Mongo" — which db.<Accessor>("core") the store TODO comment points at
	CoreDBType        string // "mongo", "mysql", "postgres", or "mssql" — which package (matching name) the store TODO comment points at
	PathParams        []string // e.g. ["id"] — names of every "{name}" wildcard segment in Route, in order
	HasPathParams     bool     // len(PathParams) > 0
	PathParamArgs     string   // e.g. `"id", "code"` — Go source for request.DecodePath's variadic names argument
	PathParamsCSV     string   // e.g. "id, code" — for doc comments
	Methods           []routeMethod
}

// NewRoute scaffolds handlers/services/store/models files for cfg.Route
// inside cfg.AppDir, then wires the new route into the right routes/
// aggregator. If the route's handler package already exists, it instead
// adds only the requested methods that aren't already registered — see
// addMethodsToExistingRoute.
func NewRoute(cfg RouteConfig) (RouteResult, error) {
	if !strings.HasPrefix(cfg.Route, "/") {
		return RouteResult{}, fmt.Errorf("route %q must start with /", cfg.Route)
	}
	if cfg.Module == "" {
		return RouteResult{}, fmt.Errorf("-module is required")
	}

	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return RouteResult{}, err
	}

	pkgParts := []string{sanitizeIdent(cfg.Module)}
	if cfg.Submodule != "" {
		pkgParts = append(pkgParts, sanitizeIdent(cfg.Submodule))
	}
	for _, p := range pkgParts {
		if p == "" {
			return RouteResult{}, fmt.Errorf("module/submodule must contain at least one letter or digit")
		}
	}

	relDir := filepath.Join(pkgParts...)
	relDirSlash := filepath.ToSlash(relDir)
	pkgName := pkgParts[len(pkgParts)-1]
	name := exportedName(pkgName)
	alias := strings.Join(pkgParts, "")
	module := pkgParts[0]

	fileName := pkgName
	if cfg.FileName != "" {
		fileName = sanitizeIdent(cfg.FileName)
		if fileName == "" {
			return RouteResult{}, fmt.Errorf("-file must contain at least one letter or digit")
		}
	}

	serviceImportPath, servicePkgName, hasServiceRef, err := resolveLayerRef(appDir, modulePath, "service", "services", cfg.ServiceRef)
	if err != nil {
		return RouteResult{}, err
	}
	storeImportPath, storePkgName, hasStoreRef, err := resolveLayerRef(appDir, modulePath, "store", "store", cfg.StoreRef)
	if err != nil {
		return RouteResult{}, err
	}

	pathParams, err := parsePathParams(cfg.Route)
	if err != nil {
		return RouteResult{}, err
	}
	var pathParamArgs []string
	for _, p := range pathParams {
		pathParamArgs = append(pathParamArgs, fmt.Sprintf("%q", p))
	}

	verbs, err := parseMethods(cfg.Methods)
	if err != nil {
		return RouteResult{}, err
	}
	bodyKind := cfg.BodyKind
	if bodyKind == "" {
		bodyKind = "json"
	}
	if err := validateBodyKind(bodyKind); err != nil {
		return RouteResult{}, err
	}
	responseKind := cfg.ResponseKind
	if responseKind == "" {
		responseKind = "json"
	}
	if err := validateResponseKind(responseKind); err != nil {
		return RouteResult{}, err
	}
	htmlResponse := responseKind == "html"
	rawResponse := responseKind == "raw"

	layoutKind := cfg.LayoutKind
	if layoutKind == "" {
		layoutKind = "shared"
	}
	if htmlResponse {
		if err := validateLayoutKind(layoutKind); err != nil {
			return RouteResult{}, err
		}
	}

	var methods []routeMethod
	for _, v := range verbs {
		vt := verbTitle(v)
		handlerName := fmt.Sprintf("Handle%s%s", name, vt)
		registerExpr := handlerName
		if cfg.Protected {
			registerExpr = fmt.Sprintf("middleware.RequireAuth(%s)", handlerName)
		}
		reqContentType := ""
		if v != "GET" {
			reqContentType = openAPIContentType(bodyKind)
		}
		htmlContentName := pkgName + "-content"
		if len(verbs) > 1 {
			htmlContentName = pkgName + "-" + strings.ToLower(v) + "-content"
		}
		methods = append(methods, routeMethod{
			Verb:            v,
			VerbTitle:       vt,
			RegisterExpr:    registerExpr,
			BodyDescription: bodyDescription(v, bodyKind),
			OperationID:     strings.ToLower(v) + name,
			ReqContentType:  reqContentType,
			DecodeCall:      decodeCall(v, bodyKind),
			HTMLContentName: htmlContentName,
		})
	}

	coreDBType, hasCoreDB := readCoreDBType(appDir)
	coreDBAccessor := "SQL"
	if coreDBType == "mongo" {
		coreDBAccessor = "Mongo"
	}

	data := routeData{
		PkgName:           pkgName,
		Name:              name,
		Route:             cfg.Route,
		ModulePath:        modulePath,
		RelDirSlash:       relDirSlash,
		Protected:         cfg.Protected,
		Alias:             alias,
		Module:            module,
		HasServiceRef:     hasServiceRef,
		ServiceImportPath: serviceImportPath,
		ServicePkgName:    servicePkgName,
		HasStoreRef:       hasStoreRef,
		StoreImportPath:   storeImportPath,
		StorePkgName:      storePkgName,
		HTMLResponse:      htmlResponse,
		RawResponse:       rawResponse,
		HasCoreDB:         hasCoreDB,
		CoreDBAccessor:    coreDBAccessor,
		CoreDBType:        coreDBType,
		PathParams:        pathParams,
		HasPathParams:     len(pathParams) > 0,
		PathParamArgs:     strings.Join(pathParamArgs, ", "),
		PathParamsCSV:     strings.Join(pathParams, ", "),
		Methods:           methods,
	}

	if _, err := ensureOpenAPIUpToDate(appDir); err != nil {
		return RouteResult{}, fmt.Errorf("upgrading openapi/openapi.go: %w", err)
	}
	if rawResponse {
		if _, err := ensureResponseJSONRaw(appDir); err != nil {
			return RouteResult{}, fmt.Errorf("upgrading response/response.go for JSONRaw support: %w", err)
		}
	}

	if _, found, err := findMarkedFile(filepath.Join(appDir, "handlers", relDir), handlerMarker); err != nil {
		return RouteResult{}, err
	} else if found {
		return addMethodsToExistingRoute(appDir, modulePath, data, cfg)
	}

	type routeFile struct {
		layer    string // "handlers", "services", "store", "models"
		tmplPath string
		fs       embed.FS
	}
	files := []routeFile{{"handlers", routeHandlerTmpl, routeTemplateFS}}
	if !hasServiceRef {
		files = append(files, routeFile{"services", routeServiceTmpl, routeTemplateFS})
	}
	if !hasStoreRef {
		files = append(files, routeFile{"store", routeStoreTmpl, routeTemplateFS})
	}
	files = append(files, routeFile{"models", routeModelTmpl, routeTemplateFS})

	var written []string
	for _, f := range files {
		destDir := filepath.Join(appDir, f.layer, relDir)
		destFile := filepath.Join(destDir, fileName+".go")

		if _, err := os.Stat(destFile); err == nil {
			return RouteResult{}, fmt.Errorf("%s already exists — pick a different -file name (or -module/-submodule), or edit it directly", destFile)
		}

		raw, err := f.fs.ReadFile(f.tmplPath)
		if err != nil {
			return RouteResult{}, fmt.Errorf("reading embedded template %s: %w", f.tmplPath, err)
		}
		content, err := processFile(f.tmplPath, raw, data)
		if err != nil {
			return RouteResult{}, fmt.Errorf("rendering %s: %w", f.tmplPath, err)
		}
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return RouteResult{}, fmt.Errorf("creating %s: %w", destDir, err)
		}
		if err := os.WriteFile(destFile, content, 0o644); err != nil {
			return RouteResult{}, fmt.Errorf("writing %s: %w", destFile, err)
		}
		written = append(written, destFile)
	}

	if htmlResponse {
		if err := writeHTMLTemplates(appDir, relDirSlash, methods, layoutKind); err != nil {
			return RouteResult{}, fmt.Errorf("generated %s, but could not write its HTML templates: %w", strings.Join(written, ", "), err)
		}
	}

	group := "public"
	if cfg.Protected {
		group = "protected"
	}
	if err := wireAggregator(appDir, group, modulePath+"/handlers/"+relDirSlash, alias); err != nil {
		// The handler/service/store/model files are already written and
		// are valid Go on their own — failing to auto-wire the aggregator
		// isn't fatal, so surface it as guidance instead of rolling back.
		return RouteResult{}, fmt.Errorf("generated %s, but could not wire routes/%s/%s.go automatically: %w\nAdd manually in that file:\n  import %s %q\n  %s.Register(mux)",
			strings.Join(written, ", "), group, group, err, alias, modulePath+"/handlers/"+relDirSlash, alias)
	}

	return RouteResult{Created: true, Added: verbs}, nil
}

// defaultHTMLLayout is written by writeSharedHTMLLayout to
// templates/html/shared/layout.html the first time any html-response route
// (or the -ui homepage) is scaffolded, if no shared layout exists yet — or,
// for a route scaffolded with -layout module, by writeHTMLTemplates
// instead, to that route's own templates/html/<module>/layout.html. It
// wraps {{.Content}} — the already-rendered, already-escaped content
// template — and pulls in the app's default stylesheet/script.
// response.HTML (the generated app's runtime helper) only ever reads
// these files; nexler itself owns creating them, at `nexler create` time,
// not at request time.
const defaultHTMLLayout = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<link rel="stylesheet" href="/static/css/styles.css">
</head>
<body>
{{.Content}}
<script src="/static/js/site.js"></script>
</body>
</html>
`

// defaultHTMLContent is written by writeHTMLTemplates to
// templates/html/<module>/<name>.html for each html-response method, the
// first time that content file doesn't exist yet. It's a placeholder
// meant to be edited, not shipped as-is.
const defaultHTMLContent = `<h1>{{.Title}}</h1>
<p>TODO: replace this placeholder content.</p>
`

// writeHTMLTemplates ensures a content file per method in methods exists on
// disk inside appDir's templates/html/<relDirSlash>/, for a route
// scaffolded (or extended) with -response html, plus a layout.html: by
// default (layoutKind "shared") the single app-wide
// templates/html/shared/layout.html, via writeSharedHTMLLayout; with
// layoutKind "module", this route's own templates/html/<relDirSlash>/
// layout.html copy instead. Existing files are left untouched — only
// missing ones are created, so re-running `nexler create <route>` (e.g. to
// add a method, or to switch -layout later) never clobbers hand-edited
// templates.
func writeHTMLTemplates(appDir, relDirSlash string, methods []routeMethod, layoutKind string) error {
	dir := filepath.Join(appDir, "templates", "html", filepath.FromSlash(relDirSlash))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if layoutKind == "module" {
		if err := writeIfMissing(filepath.Join(dir, "layout.html"), defaultHTMLLayout); err != nil {
			return err
		}
	} else if err := writeSharedHTMLLayout(appDir); err != nil {
		return err
	}
	for _, m := range methods {
		if err := writeIfMissing(filepath.Join(dir, m.HTMLContentName+".html"), defaultHTMLContent); err != nil {
			return err
		}
	}
	return nil
}

// writeSharedHTMLLayout ensures templates/html/shared/layout.html exists
// inside root (an app directory) — the single layout every module falls
// back to at runtime (see response.HTML's resolveHTMLFile) unless it has
// its own override copy. Only missing files are created — never clobbers
// a hand-edited layout.
func writeSharedHTMLLayout(root string) error {
	dir := filepath.Join(root, "templates", "html", "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	return writeIfMissing(filepath.Join(dir, "layout.html"), defaultHTMLLayout)
}

// writeIfMissing writes content to path only if path doesn't already exist.
func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// addMethodsToExistingRoute adds only the methods in data.Methods that
// aren't already registered for this route, to the already-existing
// handler and model files — leaving services/store untouched (they're
// generic, not per-method) and skipping aggregator wiring entirely
// (the route package is already imported and registered there).
//
// It refuses to mix -protected with an existing route that was created
// the other way, since a single handler package can only be registered
// in one of routes/public or routes/protected.
func addMethodsToExistingRoute(appDir, modulePath string, data routeData, cfg RouteConfig) (RouteResult, error) {
	existingProtected, found, err := detectExistingProtection(appDir, modulePath, data.RelDirSlash)
	if err == nil && found && existingProtected != cfg.Protected {
		want := "public (omit -protected)"
		if existingProtected {
			want = "protected (pass -protected)"
		}
		return RouteResult{}, fmt.Errorf("this route was originally created as %s — mixing protected and public within one route package isn't supported", want)
	}

	handlerPath, found, err := findMarkedFile(filepath.Join(appDir, "handlers", filepath.FromSlash(data.RelDirSlash)), handlerMarker)
	if err != nil {
		return RouteResult{}, err
	}
	if !found {
		return RouteResult{}, fmt.Errorf("no existing handler file found containing the %q marker under handlers/%s — it may predate this feature; add the marker manually or edit the route directly", handlerMarker, data.RelDirSlash)
	}
	modelPath, found, err := findMarkedFile(filepath.Join(appDir, "models", filepath.FromSlash(data.RelDirSlash)), modelMarker)
	if err != nil {
		return RouteResult{}, err
	}
	if !found {
		return RouteResult{}, fmt.Errorf("no existing model file found containing the %q marker under models/%s — it may predate this feature; add the marker manually or edit the route directly", modelMarker, data.RelDirSlash)
	}

	handlerRaw, err := os.ReadFile(handlerPath)
	if err != nil {
		return RouteResult{}, fmt.Errorf("reading %s: %w", handlerPath, err)
	}
	handlerContent := strings.ReplaceAll(string(handlerRaw), "\r\n", "\n")

	var newMethods []routeMethod
	var skipped []string
	for _, m := range data.Methods {
		marker := fmt.Sprintf("%q", m.Verb+" "+data.Route)
		if strings.Contains(handlerContent, marker) {
			skipped = append(skipped, m.Verb)
			continue
		}
		newMethods = append(newMethods, m)
	}
	if len(newMethods) == 0 {
		return RouteResult{}, fmt.Errorf("method(s) %s already registered for this route", strings.Join(skipped, ", "))
	}
	data.Methods = newMethods

	if data.HTMLResponse {
		layoutKind := cfg.LayoutKind
		if layoutKind == "" {
			layoutKind = "shared"
		}
		if err := validateLayoutKind(layoutKind); err != nil {
			return RouteResult{}, err
		}
		if err := writeHTMLTemplates(appDir, data.RelDirSlash, newMethods, layoutKind); err != nil {
			return RouteResult{}, fmt.Errorf("could not write HTML templates for the new method(s): %w", err)
		}
	}

	handlerContent, err = insertFragment(handlerContent, routeHandlerMethodsFragment, data,
		handlerMarker,
		fmt.Sprintf("could not find the handler-insertion marker in %s — it may predate this feature. Add a line `// nexler:handlers (do not remove this marker)` right before the `// Register wires...` comment, or add the new method's handler manually.", handlerPath))
	if err != nil {
		return RouteResult{}, err
	}
	handlerContent, err = insertFragment(handlerContent, routeRegisterMethodsFragment, data,
		"\t// nexler:register (do not remove this marker)",
		fmt.Sprintf("could not find the register-insertion marker in %s — it may predate this feature. Add a line `\t// nexler:register (do not remove this marker)` right before Register's closing brace, or wire the new method manually.", handlerPath))
	if err != nil {
		return RouteResult{}, err
	}
	if err := os.WriteFile(handlerPath, []byte(handlerContent), 0o644); err != nil {
		return RouteResult{}, fmt.Errorf("writing %s: %w", handlerPath, err)
	}

	modelRaw, err := os.ReadFile(modelPath)
	if err != nil {
		return RouteResult{}, fmt.Errorf("reading %s: %w", modelPath, err)
	}
	modelContent := strings.ReplaceAll(string(modelRaw), "\r\n", "\n")
	modelContent, err = insertFragment(modelContent, routeModelMethodsFragment, data,
		modelMarker,
		fmt.Sprintf("could not find the model-insertion marker in %s — it may predate this feature. Add a line `// nexler:models (do not remove this marker)` at the end of the file, or add the new structs manually.", modelPath))
	if err != nil {
		return RouteResult{}, err
	}
	if err := os.WriteFile(modelPath, []byte(modelContent), 0o644); err != nil {
		return RouteResult{}, fmt.Errorf("writing %s: %w", modelPath, err)
	}

	var added []string
	for _, m := range newMethods {
		added = append(added, m.Verb)
	}
	return RouteResult{Created: false, Added: added, Skipped: skipped}, nil
}

// handlerMarker and modelMarker are the stable marker comments left in
// generated handler/model files — insertFragment anchors new method/model
// insertions on them, and findMarkedFile (below) reuses the same text to
// recognize an already-scaffolded route file regardless of its base name
// (the default pkgName-derived name, a -file override, or a hand-renamed
// file), instead of assuming a fixed file name.
const (
	handlerMarker = "// nexler:handlers (do not remove this marker)"
	modelMarker   = "// nexler:models (do not remove this marker)"
)

// findMarkedFile scans dir's top-level *.go files for one whose content
// contains marker, returning its path. found is false if dir doesn't
// exist or no file contains marker — not an error, since that just means
// no nexler-managed route file exists there yet (a brand-new route) or
// the directory holds only hand-written files (e.g. helper files added
// alongside a route's own handler.go, which won't contain the marker).
func findMarkedFile(dir, marker string) (path string, found bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), marker) {
			return p, true, nil
		}
	}
	return "", false, nil
}

// dirHasGoFile reports whether dir contains at least one top-level *.go
// file — used by resolveLayerRef to confirm a -service/-store reference
// actually points at an already-scaffolded package before wiring a route's
// comments to it. A non-existent dir simply reports false, not an error.
func dirHasGoFile(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// resolveLayerRef parses a "module[/submodule]" reference passed to
// -service/-store, addressed the exact same way -module/-submodule
// themselves are (sanitizeIdent per segment, joined into a directory under
// layer — "services" or "store"). has is false when ref is empty, meaning
// "generate a fresh file for this route" (today's default behavior) rather
// than reusing one. A non-empty ref that doesn't point at an
// already-scaffolded package is a clear error, not a silent no-op — a typo
// here would otherwise leave a route's TODO comment pointing at nothing.
func resolveLayerRef(appDir, modulePath, flagName, layer, ref string) (importPath, pkgName string, has bool, err error) {
	if ref == "" {
		return "", "", false, nil
	}
	var refParts []string
	for _, p := range strings.SplitN(ref, "/", 2) {
		sp := sanitizeIdent(p)
		if sp == "" {
			return "", "", false, fmt.Errorf("-%s %q is invalid — module/submodule must contain at least one letter or digit", flagName, ref)
		}
		refParts = append(refParts, sp)
	}
	refRelDir := filepath.Join(refParts...)
	dir := filepath.Join(appDir, layer, refRelDir)
	if !dirHasGoFile(dir) {
		return "", "", false, fmt.Errorf("-%s %q points at %s, but no package exists there yet — scaffold it first, or omit -%s to generate a new one here", flagName, ref, filepath.Join(layer, refRelDir), flagName)
	}
	return modulePath + "/" + layer + "/" + filepath.ToSlash(refRelDir), refParts[len(refParts)-1], true, nil
}

// insertFragment renders the embedded fragment template at fragmentPath
// against data, and inserts the result immediately before marker in
// content. marker itself is left in place, so this can be called again
// later for further methods.
func insertFragment(content, fragmentPath string, data routeData, marker, missingMarkerErr string) (string, error) {
	if !strings.Contains(content, marker) {
		return "", errors.New(missingMarkerErr)
	}
	raw, err := routeTemplateFS.ReadFile(fragmentPath)
	if err != nil {
		return "", fmt.Errorf("reading embedded fragment %s: %w", fragmentPath, err)
	}
	rendered, err := render(fragmentPath, raw, data)
	if err != nil {
		return "", fmt.Errorf("rendering fragment %s: %w", fragmentPath, err)
	}
	return strings.Replace(content, marker, string(rendered)+"\n"+marker, 1), nil
}

// detectExistingProtection checks whether a route's handler package is
// currently imported by routes/public/public.go or
// routes/protected/protected.go, so addMethodsToExistingRoute can refuse
// a mismatched -protected flag instead of producing an inconsistent
// registration.
func detectExistingProtection(appDir, modulePath, relDirSlash string) (protected bool, found bool, err error) {
	importPath := fmt.Sprintf("%q", modulePath+"/handlers/"+relDirSlash)

	if raw, readErr := os.ReadFile(filepath.Join(appDir, "routes", "public", "public.go")); readErr == nil {
		if strings.Contains(string(raw), importPath) {
			return false, true, nil
		}
	}
	if raw, readErr := os.ReadFile(filepath.Join(appDir, "routes", "protected", "protected.go")); readErr == nil {
		if strings.Contains(string(raw), importPath) {
			return true, true, nil
		}
	}
	return false, false, nil
}

// openAPIUpToDateMarkers lists every Operation field openapi.go.tmpl has
// gained since its original version — ensureOpenAPIUpToDate regenerates
// the file unless ALL of them are present, not just the newest one: an app
// can pick up these fields independently of each other depending on
// exactly when it was last regenerated (e.g. a hand-maintained app that
// added its own RespUnwrapped before Tags existed, or vice versa) — a
// single-marker check would wrongly consider such a file current. Add the
// new field's name here whenever openapi.go.tmpl gains another one.
var openAPIUpToDateMarkers = []string{"Tags", "RespUnwrapped", "ClientIdAuth"}

// ensureOpenAPIUpToDate rewrites appDir/openapi/openapi.go from the current
// embedded template if it predates any Operation field nexler now expects
// to set (see openAPIUpToDateMarkers), so a route created with this nexler
// version doesn't emit a struct literal referencing a field that doesn't
// exist yet in an app scaffolded (or last regenerated) by an older nexler.
// Safe to fully regenerate (not marker-patch): openapi.go.tmpl has no
// per-app templating at all — its rendered output is byte-identical across
// every app at a given nexler version — so there's nothing app-specific
// that a full rewrite could clobber.
func ensureOpenAPIUpToDate(appDir string) (bool, error) {
	path := filepath.Join(appDir, "openapi", "openapi.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No openapi package in this app — nothing to upgrade.
			return false, nil
		}
		return false, err
	}
	upToDate := true
	for _, marker := range openAPIUpToDateMarkers {
		if !strings.Contains(string(raw), marker) {
			upToDate = false
			break
		}
	}
	if upToDate {
		return false, nil
	}
	tmplPath := templatesRoot + "/openapi/openapi.go.tmpl"
	content, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return false, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	rendered, err := render(tmplPath, content, nil)
	if err != nil {
		return false, fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// responseMarker is the stable anchor ensureResponseJSONRaw inserts
// JSONRaw before, in apps scaffolded with this feature. responseErrorAnchor
// is the fallback anchor for apps scaffolded before the marker existed —
// still self-healing without a re-scaffold, since func Error's signature
// has been stable since response.go.tmpl's very first version.
const (
	responseMarker      = "// nexler:response (do not remove this marker)"
	responseErrorAnchor = "func Error(w http.ResponseWriter"
	jsonRawFunc         = `
// JSONRaw writes v directly (no {"data": ...} envelope) with the given
// status code — for routes with a response contract that predates the
// envelope convention (e.g. an existing non-Go client), where JSON's
// wrapping isn't an option. Prefer JSON for anything without that
// constraint, so the envelope stays the norm.
func JSONRaw(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
`
)

// ensureResponseJSONRaw inserts JSONRaw into appDir/response/response.go if
// it's missing — needed for a route created with -response raw against an
// app scaffolded before this feature (or before JSONRaw was unconditional).
// Deliberately append-only, never a full-file regeneration like
// ensureOpenAPIUpToDate uses for openapi.go: unlike openapi.go ("no file on
// disk to keep in sync," pure generated infra), response.go is realistically
// hand-extended — ctrl-svc's own JSONRaw was itself a hand-addition before
// this feature existed — so a blind overwrite could silently delete an
// unrelated hand-written helper living in the same file.
func ensureResponseJSONRaw(appDir string) (bool, error) {
	path := filepath.Join(appDir, "response", "response.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("%s does not exist — is %s a nexler app directory?", path, appDir)
		}
		return false, err
	}
	content := string(raw)
	if strings.Contains(content, "JSONRaw") {
		return false, nil
	}

	anchor := responseMarker
	if !strings.Contains(content, anchor) {
		anchor = responseErrorAnchor
		if !strings.Contains(content, anchor) {
			return false, fmt.Errorf("could not find an insertion point for JSONRaw in %s (has it been hand-rewritten?) — add the following manually:\n%s", path, jsonRawFunc)
		}
	}
	content = strings.Replace(content, anchor, jsonRawFunc+"\n"+anchor, 1)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// wireAggregator inserts an import for importPath (aliased as alias) and a
// call to its Register(mux) into routes/<group>/<group>.go (group is
// "public" or "protected"), anchored on the fixed markers left by
// templates/routes/public/public.go.tmpl and .../protected/protected.go.tmpl.
// importPath is the caller's to build — a route package's own
// modulePath+"/handlers/"+relDirSlash, or (for a non-route aggregator like
// kgate's webhook) any other package with its own Register(mux) func.
func wireAggregator(appDir, group, importPath, alias string) error {
	aggPath := filepath.Join(appDir, "routes", group, group+".go")

	raw, err := os.ReadFile(aggPath)
	if err != nil {
		return fmt.Errorf("reading %s (are you in a nexler app directory?): %w", aggPath, err)
	}
	// Normalize CRLF to LF in case this file was generated before the
	// render() fix, or hand-edited with an editor that uses CRLF — the
	// anchor matching below depends on exact LF byte sequences.
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")

	if strings.Contains(content, `"`+importPath+`"`) {
		return fmt.Errorf("%s already imports %s", aggPath, importPath)
	}

	content, err = insertImport(content, alias, importPath)
	if err != nil {
		return fmt.Errorf("%s: %w (has it been hand-edited?)", aggPath, err)
	}

	const registerAnchor = "\t// nexler:routes (do not remove this marker)"
	if !strings.Contains(content, registerAnchor) {
		return fmt.Errorf("could not find the registration anchor in %s (has it been hand-edited?)", aggPath)
	}
	newRegister := fmt.Sprintf("\t%s.Register(mux)\n%s", alias, registerAnchor)
	content = strings.Replace(content, registerAnchor, newRegister, 1)

	return os.WriteFile(aggPath, []byte(content), 0o644)
}

// insertImport adds `alias "importPath"` to content's single import block,
// just before its closing paren.
//
// The anchor is the block's opening ("import (\n") and closing ("\n)")
// delimiters, not the stdlib "net/http" line itself — unlike the
// register-call marker below, which stays put after every insertion, the
// import block's own last line changes shape on every call (it's whatever
// import was added previously), so anchoring on specific import text would
// only ever match once. Finding "\n)" is safe here because it's the first
// closing paren on its own line in the file — nothing before the import
// block could contain one, and the next candidate (the closing paren of
// Register's signature) is never alone on its own line.
//
// The first inserted import gets a blank line before it, separating it
// from the stdlib "net/http" line the way gofmt groups stdlib vs. local
// imports; later insertions stack directly under the previous one.
func insertImport(content, alias, importPath string) (string, error) {
	const openMarker = "import (\n"
	openIdx := strings.Index(content, openMarker)
	if openIdx == -1 {
		return "", errors.New("could not find the import block")
	}
	blockStart := openIdx + len(openMarker)

	closeOffset := strings.Index(content[blockStart:], "\n)")
	if closeOffset == -1 {
		return "", errors.New("could not find the end of the import block")
	}
	insertAt := blockStart + closeOffset

	prefix := "\n"
	if strings.TrimSpace(content[blockStart:insertAt]) == `"net/http"` {
		prefix = "\n\n"
	}
	line := fmt.Sprintf("%s\t%s %q", prefix, alias, importPath)

	return content[:insertAt] + line + content[insertAt:], nil
}

// readModulePath extracts the module path from appDir/go.mod's leading
// "module ..." line.
func readModulePath(appDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(appDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("reading go.mod (run this from inside a nexler app directory, or pass -dir): %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("no module declaration found in go.mod")
}

// readCoreDBType looks for a "..._DB_CORE_TYPE=<value>" line in appDir's
// .env (the env-var prefix varies per app, so this matches on the fixed
// suffix rather than needing to know it) and returns that value plus
// whether one was found at all. Missing .env, or no such line (an app
// scaffolded without -db), just means ok is false — not an error, since
// most apps don't have a core database and that's expected.
func readCoreDBType(appDir string) (dbType string, ok bool) {
	raw, err := os.ReadFile(filepath.Join(appDir, ".env"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "_DB_CORE_TYPE=")
		if idx == -1 {
			continue
		}
		value := strings.TrimSpace(line[idx+len("_DB_CORE_TYPE="):])
		if value == "" {
			return "", false
		}
		return value, true
	}
	return "", false
}

// sanitizeIdent lowercases s and strips everything but letters and
// digits, producing a valid, idiomatic Go package name fragment.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// exportedName title-cases s for use in exported identifiers, e.g.
// "verify" -> "Verify".
func exportedName(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// pathParamNameRe validates a "{name}" path-parameter segment's name —
// a legal Go identifier, since it ends up matched against a Request
// struct's json tags and passed as a Go string literal to
// request.DecodePath.
var pathParamNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parsePathParams extracts every "{name}" segment from route, in path
// order, validating each and rejecting duplicates. These are Go 1.22+
// ServeMux wildcards — Register (handler.go.tmpl) passes route through to
// mux.HandleFunc verbatim and net/http already routes them natively; this
// only extracts the names so the generated handler can also decode them
// into the Request struct (via request.DecodePath, alongside query/body
// fields) and so openapi.go can document them as path parameters.
//
// A trailing "{name...}" (matches the rest of the path, e.g. for a
// catch-all) is accepted too, with its "..." stripped from the returned
// name — it must be the last segment, same restriction net/http itself
// enforces.
//
// "?" or "#" anywhere in route is rejected outright: a literal query
// string or fragment isn't a wildcard net/http's ServeMux understands —
// it would just become part of the literal path pattern — so a route
// that looks like it carries one (e.g. a leftover "/admin/user/?id=u1"
// from before path parameters existed) is almost certainly a mistake;
// the error steers toward {name} instead.
func parsePathParams(route string) ([]string, error) {
	if strings.ContainsAny(route, "?#") {
		return nil, fmt.Errorf("route %q must not contain a literal %q — net/http's ServeMux doesn't parse query strings out of route patterns, it would just become part of the literal path; use a path parameter instead, e.g. /admin/user/{id}/profile", route, route[strings.IndexAny(route, "?#"):])
	}

	segments := strings.Split(route, "/")
	var names []string
	seen := map[string]bool{}
	for i, seg := range segments {
		if !strings.Contains(seg, "{") && !strings.Contains(seg, "}") {
			continue
		}
		if len(seg) < 2 || seg[0] != '{' || seg[len(seg)-1] != '}' {
			return nil, fmt.Errorf("route %q has a malformed path parameter segment %q — the whole segment must be {name} or {name...}", route, seg)
		}
		name := seg[1 : len(seg)-1]
		if strings.HasSuffix(name, "...") {
			if i != len(segments)-1 {
				return nil, fmt.Errorf("route %q: {%s} must be the last path segment", route, name)
			}
			name = strings.TrimSuffix(name, "...")
		}
		if !pathParamNameRe.MatchString(name) {
			return nil, fmt.Errorf("route %q: invalid path parameter name %q — must start with a letter or underscore and contain only letters, digits, and underscores", route, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("route %q: duplicate path parameter {%s}", route, name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// allowedVerbs are the HTTP methods `nexler create <route>` can generate
// handlers for. OPTIONS is deliberately excluded — it's wired
// automatically for every route via middleware.HandleOptions.
var allowedVerbs = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// parseMethods normalizes and validates the -methods list, defaulting to
// ["GET"] when empty.
func parseMethods(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{"GET"}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range raw {
		v := strings.ToUpper(strings.TrimSpace(m))
		if v == "" {
			continue
		}
		if v == "OPTIONS" {
			return nil, fmt.Errorf("OPTIONS is handled automatically for every route — omit it from -methods")
		}
		if !allowedVerbs[v] {
			return nil, fmt.Errorf("unsupported method %q (supported: GET, POST, PUT, PATCH, DELETE)", v)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-methods must include at least one method")
	}
	return out, nil
}

// validateBodyKind checks -body against the supported request-body shapes.
func validateBodyKind(kind string) error {
	switch kind {
	case "json", "form", "multipart":
		return nil
	default:
		return fmt.Errorf("unsupported -body %q (supported: json, form, multipart)", kind)
	}
}

// validateResponseKind checks -response against the supported response
// shapes.
func validateResponseKind(kind string) error {
	switch kind {
	case "json", "html", "raw":
		return nil
	default:
		return fmt.Errorf("unsupported -response %q (supported: json, html, raw)", kind)
	}
}

// validateLayoutKind checks -layout against the supported layout choices.
func validateLayoutKind(kind string) error {
	switch kind {
	case "shared", "module":
		return nil
	default:
		return fmt.Errorf("unsupported -layout %q (supported: shared, module)", kind)
	}
}

// verbTitle title-cases an HTTP method for use in identifiers, e.g.
// "GET" -> "Get", "DELETE" -> "Delete".
func verbTitle(verb string) string {
	if verb == "" {
		return verb
	}
	return string(verb[0]) + strings.ToLower(verb[1:])
}

// bodyDescription is the human-readable phrase used in generated model
// doc comments, describing where a method's request data comes from.
func bodyDescription(verb, bodyKind string) string {
	if verb == "GET" {
		return "query-parameter payload"
	}
	switch bodyKind {
	case "form":
		return "form-encoded request payload"
	case "multipart":
		return "multipart form-data request payload (files are read separately, via r.MultipartForm.File)"
	default:
		return "JSON request payload"
	}
}

// openAPIContentType maps a -body kind to its OpenAPI media type, used
// when a route registers itself with the runtime openapi package.
func openAPIContentType(bodyKind string) string {
	switch bodyKind {
	case "form":
		return "application/x-www-form-urlencoded"
	case "multipart":
		return "multipart/form-data"
	default:
		return "application/json"
	}
}

// decodeCall returns the request package call a generated handler uses to
// decode into its Request struct: DecodeQuery for GET (which never has a
// body), otherwise whichever of DecodeJSON/DecodeForm/DecodeMultipart
// matches -body. Precomputed here, like the rest of routeMethod, so
// handler.go.tmpl and handler_methods.tmpl just emit it verbatim instead
// of branching on verb/bodyKind themselves.
func decodeCall(verb, bodyKind string) string {
	if verb == "GET" {
		return "request.DecodeQuery(r, &req)"
	}
	switch bodyKind {
	case "form":
		return "request.DecodeForm(r, &req)"
	case "multipart":
		return "request.DecodeMultipart(r, &req, request.DefaultMaxMemory)"
	default:
		return "request.DecodeJSON(r, &req)"
	}
}
