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
// only the newly-requested methods to it (see addToExistingRoute)
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
	// LayersAdded lists which of "service"/"store" were retrofitted onto
	// an already-existing route this call (only set when Created is
	// false) — see RouteConfig.ServiceRequested/StoreRequested.
	LayersAdded []string
	// Note is an optional informational message about something worth
	// telling the caller that isn't an error — e.g. that the newly-added
	// method(s) have a different auth requirement than the rest of an
	// already-existing route package (only set when Created is false).
	Note string
	// NewFile is the base file name (e.g. "verify.go") of a brand-new
	// secondary handler+model file pair this invocation created inside an
	// already-existing route package — set only when addToExistingRoute
	// wrote a fresh file pair for -file's value rather than appending to
	// one that already existed. Empty when Created is true (the whole
	// package, including its one file, is new — see Created) or when this
	// invocation appended to an already-existing file instead.
	NewFile string
	// PrimaryFile is the base file name (e.g. "provisioning.go") of the
	// package's Register-holding file — set whenever Created is false and
	// at least one method was added, so the caller can report where the
	// new method(s)' mux.HandleFunc/openapi.Register wiring actually
	// landed, independently of which file (NewFile, or -file's existing
	// target) their handler function(s)/model(s) landed in.
	PrimaryFile string
	// RegisterFuncName is the exact Go function name this invocation's
	// mux-wiring landed in — "Register" for the ordinary fold-into-existing
	// case (NewFile != PrimaryFile, or Created), or "Register"+Name (e.g.
	// "RegisterSms") when RouteConfig.OwnRegister created an independent
	// second resource (NewFile == PrimaryFile) — set so the CLI can report
	// the exact generated identifier without re-deriving it.
	RegisterFuncName string
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
	// from Module/Submodule (or IdentName below, when set) as before.
	// Defaults to the route's own package name (the last of
	// Module/Submodule) when empty.
	//
	// When Module/Submodule already name an existing route package, a
	// FileName that names a file that doesn't exist yet in that package
	// creates a genuine second handler+model file pair for this
	// invocation's method(s) (see addToExistingRoute) — the package's
	// Go package name, handlers, and everything else about it are
	// unaffected, but its code now spans more than one file. A FileName
	// that matches an already-existing file appends to it instead, same as
	// today. By default, either way, this invocation's mux.HandleFunc/
	// openapi.Register wiring lands in the package's one primary
	// (Register-holding) file, since Go permits only one func Register per
	// package — unless OwnRegister is set, in which case a brand-new
	// FileName instead becomes its own independent primary file with its
	// own Register<Name> function (see OwnRegister).
	FileName string
	// IdentName optionally overrides the identifier base normally derived
	// from Module/Submodule — used to build every handler function name,
	// Request/Response type name, and OperationID for this route (e.g.
	// "New" here means HandleNewPost/PostNewRequest/"postnew" instead of
	// the module-derived HandlePurchasePost/etc.). Sanitized the same way
	// as Module/Submodule. Unlike FileName, this does NOT affect where the
	// generated code lives on disk — package, directory, and models import
	// alias all still derive from Module/Submodule as before. This exists
	// for adding a second, distinct route to an already-scaffolded package
	// (see addToExistingRoute) whose Module/Submodule-derived
	// identifiers would otherwise collide with the first route's; leave
	// empty (the default) for the common case of one route per package.
	IdentName string
	// ServiceRef, when non-empty, points this route at an already-scaffolded
	// service package instead of generating a new one — a "module[/submodule]"
	// reference addressed the same way Module/Submodule are, e.g. "purchase"
	// or "purchase/verify". The referenced package must already exist.
	// Empty (default) generates a fresh service for this route, as before.
	// The literal value "none" skips the service layer entirely — no
	// generation, no reuse; a route can stop at handler + model.
	ServiceRef string
	// StoreRef is the same as ServiceRef, for the store layer.
	StoreRef string
	// ServiceRequested records whether -service was explicitly passed on
	// this invocation (even as an empty string), independent of what it
	// resolved to. Only consulted when adding to an already-existing route
	// (see addToExistingRoute): distinguishes "the user asked me to
	// (re)consider the service layer this time" — which can retrofit a
	// missing service onto an existing handler-only route — from "the user
	// didn't mention it, leave whatever's there alone". Ignored for a
	// brand-new route, where ServiceRef's own value already fully decides
	// behavior. Set by the CLI via flag.FlagSet.Visit.
	ServiceRequested bool
	// StoreRequested is the same as ServiceRequested, for the store layer.
	StoreRequested bool
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
	// Protected marks the newly-created-or-added method(s) as requiring
	// auth: each one's handler wraps itself with middleware.RequireAuth.
	// For a brand-new route package, it also decides which aggregator file
	// the package is registered in (routes/protected/protected.go if true,
	// routes/public/public.go if false — the default). For a route package
	// that already exists, it only affects the method(s) this invocation
	// adds — independently of whatever protection the package's existing
	// methods already have, and without moving the package to a different
	// aggregator file. See routeMethod.Protected.
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
	// OwnRegister, when true, scaffolds this route as an independent second
	// (third, ...) resource inside an already-existing package directory —
	// its own handler file with its own Register<Name> function (Go allows
	// only one func Register per package, so a second one needs a distinct
	// name), own openapi/middleware wiring, and an additional
	// <alias>.Register<Name>(mux) call in the aggregator alongside the
	// package's existing Register call — instead of folding this
	// invocation's method(s) into the package's single existing Register,
	// which is what a plain -file (without OwnRegister) still does. Only
	// meaningful when FileName names a file that doesn't already exist in
	// an already-existing route package, and requires IdentName to be set
	// (to disambiguate this resource's identifiers from the package's
	// existing one(s) — see addOwnRegisterResource). False (default)
	// preserves every existing -file/-name behavior exactly.
	OwnRegister bool
}

// routeMethod is one HTTP method's worth of generated identifiers,
// precomputed in Go so the templates don't need conditional logic.
type routeMethod struct {
	Verb            string // "GET", "POST", ...
	VerbTitle       string // "Get", "Post", ... — used in identifiers
	HandlerName     string // e.g. "HandleXGet" — the bare handler function name, before any middleware.RequireAuth wrapping. Single source of truth for this identifier: templates reference it directly instead of re-deriving "Handle"+Name+VerbTitle themselves, and addToExistingRoute's collision check matches against it exactly.
	ReqTypeName     string // e.g. "GetXRequest" — same single-source-of-truth reasoning as HandlerName, for the Request struct type
	RespTypeName    string // e.g. "GetXResponse" — same, for the Response struct type
	RegisterExpr    string // e.g. "HandleXGet" or "middleware.RequireAuth(HandleXGet)"
	Protected       bool   // whether this specific method wraps itself with middleware.RequireAuth — same value RegisterExpr was built from
	BodyDescription string // human-readable, used in model doc comments
	OperationID     string // e.g. "getPing" — used in the openapi.Register call
	ReqContentType  string // e.g. "application/json"; empty for GET (no body)
	DecodeCall      string // e.g. "request.DecodeJSON(r, &req)" — how the handler decodes into its Request struct
	HTMLContentName string // e.g. "verify-content" — the response.HTML content-template name for this method, when ResponseKind is "html"
}

// routeData is what's available to route_templates/*.tmpl placeholders.
type routeData struct {
	PkgName           string   // e.g. "verify"
	Name              string   // e.g. "Verify" — used in identifiers like HandleVerify
	// RegisterFuncName is the name of this file's mux-wiring function —
	// "Register" for every package's original/only resource (the default,
	// rendered identically to before this field existed), or "Register"+Name
	// for a second independent resource added via -own-register (see
	// addOwnRegisterResource) — Go allows only one func Register per
	// package, so a second one needs a distinct name, matching the
	// "RegisterSms" convention a hand-built app already established.
	RegisterFuncName string
	Route             string   // e.g. "/asd" — empty for a standalone service/store, not tied to any route
	RouteLabel        string   // descriptive text for service.go.tmpl/store.go.tmpl's TODO comments: Route when set, else "this package"
	Standalone        bool     // true when this service/store was generated via `nexler create service|store` (NewLayer), not as part of a route
	ModulePath        string   // the app's own module path, from its go.mod
	RelDirSlash       string   // e.g. "purchase/verify", forward-slashed for import paths
	Protected         bool     // whether handlers wrap themselves with middleware.RequireAuth
	Alias             string   // e.g. "purchaseverify" — used as the models package import alias
	Module            string   // e.g. "purchase" — the top-level module, always set regardless of Submodule; used as the OpenAPI operation tag
	HasServiceRef     bool     // true when this route reuses an existing service package instead of generating its own
	ServiceImportPath string   // e.g. "example.com/app/services/purchase/verify" — the reused service's import path
	ServicePkgName    string   // e.g. "verify" — the reused service's package name
	HasService        bool     // whether this route has a service layer at all — ref'd or generated (false only when the service layer was explicitly skipped)
	HasStoreRef       bool     // true when this route reuses an existing store package instead of generating its own
	StoreImportPath   string   // e.g. "example.com/app/store/purchase/verify" — the reused store's import path
	StorePkgName      string   // e.g. "verify" — the reused store's package name
	HasStore          bool     // whether this route has a store layer at all — ref'd or generated (false only when the store layer was explicitly skipped)
	HTMLResponse      bool     // whether every method responds via response.HTML instead of response.JSON
	RawResponse       bool     // whether every method responds via response.JSONRaw (no {"data": ...} envelope) instead of response.JSON
	HasCoreDB         bool     // whether the app has a "core" database connection (see readCoreDBType)
	CoreDBAccessor    string   // "SQL" or "Mongo" — which db.<Accessor>("core") the store TODO comment points at
	CoreDBType        string   // "mongo", "mysql", "postgres", or "mssql" — which package (matching name) the store TODO comment points at
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
// addToExistingRoute.
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

	if cfg.IdentName != "" {
		identName := sanitizeIdent(cfg.IdentName)
		if identName == "" {
			return RouteResult{}, fmt.Errorf("-name must contain at least one letter or digit")
		}
		name = exportedName(identName)
	}

	serviceImportPath, servicePkgName, hasServiceRef, skipService, err := resolveLayerRef(appDir, modulePath, "service", "services", cfg.ServiceRef)
	if err != nil {
		return RouteResult{}, err
	}
	storeImportPath, storePkgName, hasStoreRef, skipStore, err := resolveLayerRef(appDir, modulePath, "store", "store", cfg.StoreRef)
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
		protected := cfg.Protected
		reqContentType := ""
		if v != "GET" {
			reqContentType = openAPIContentType(bodyKind)
		}
		// Based on name (the identifier base — -name's value when set, else
		// derived from pkgName), not pkgName directly: name is already the
		// one thing -name exists to disambiguate whenever two routes would
		// otherwise collide in the same package (see detectIdentifierCollisions),
		// so keying HTMLContentName off it too means it can never collide in
		// any case nexler currently allows to proceed. sanitizeIdent already
		// lowercases, so strings.ToLower(name) equals pkgName exactly
		// whenever -name isn't used — no behavior change for the common
		// single-route case.
		htmlContentName := strings.ToLower(name) + "-content"
		if len(verbs) > 1 {
			htmlContentName = strings.ToLower(name) + "-" + strings.ToLower(v) + "-content"
		}
		methods = append(methods, routeMethod{
			Verb:            v,
			VerbTitle:       vt,
			HandlerName:     handlerName,
			ReqTypeName:     vt + name + "Request",
			RespTypeName:    vt + name + "Response",
			RegisterExpr:    registerExpr,
			Protected:       protected,
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
		RegisterFuncName:  "Register",
		Route:             cfg.Route,
		RouteLabel:        cfg.Route,
		ModulePath:        modulePath,
		RelDirSlash:       relDirSlash,
		Protected:         cfg.Protected,
		Alias:             alias,
		Module:            module,
		HasServiceRef:     hasServiceRef,
		ServiceImportPath: serviceImportPath,
		ServicePkgName:    servicePkgName,
		HasService:        hasServiceRef || !skipService,
		HasStoreRef:       hasStoreRef,
		StoreImportPath:   storeImportPath,
		StorePkgName:      storePkgName,
		HasStore:          hasStoreRef || !skipStore,
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

	if _, found, err := findMarkedFile(filepath.Join(appDir, "handlers", relDir), registerMarker); err != nil {
		return RouteResult{}, err
	} else if found {
		return addToExistingRoute(appDir, modulePath, relDir, fileName, data, cfg)
	}

	type routeFile struct {
		layer    string // "handlers", "services", "store", "models"
		tmplPath string
		fs       embed.FS
	}
	files := []routeFile{{"handlers", routeHandlerTmpl, routeTemplateFS}}
	if !hasServiceRef && !skipService {
		files = append(files, routeFile{"services", routeServiceTmpl, routeTemplateFS})
	}
	if !hasStoreRef && !skipStore {
		files = append(files, routeFile{"store", routeStoreTmpl, routeTemplateFS})
	}
	files = append(files, routeFile{"models", routeModelTmpl, routeTemplateFS})

	var written []string
	for _, f := range files {
		destFile, err := writeRouteLayerFile(appDir, f.layer, f.tmplPath, f.fs, relDir, fileName, data)
		if err != nil {
			return RouteResult{}, err
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

	return RouteResult{Created: true, Added: verbs, RegisterFuncName: "Register"}, nil
}

// writeRouteLayerFile renders tmplPath (from fs) against data and writes it
// to appDir/layer/relDir/fileName.go, erroring if that file already
// exists — the same read-embedded/render/mkdir/write shape NewRoute's own
// per-file loop used before this was extracted, now shared with the
// existing-route layer retrofit in addToExistingRoute. Returns the
// path written.
func writeRouteLayerFile(appDir, layer, tmplPath string, fs embed.FS, relDir, fileName string, data routeData) (string, error) {
	destDir := filepath.Join(appDir, layer, relDir)
	destFile := filepath.Join(destDir, fileName+".go")

	if _, err := os.Stat(destFile); err == nil {
		return "", fmt.Errorf("%s already exists — pick a different -file name (or -module/-submodule), or edit it directly", destFile)
	}

	raw, err := fs.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	content, err := processFile(tmplPath, raw, data)
	if err != nil {
		return "", fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", destDir, err)
	}
	if err := os.WriteFile(destFile, content, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", destFile, err)
	}
	return destFile, nil
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

// addToExistingRoute adds the methods in data.Methods that aren't already
// registered for this route to an already-existing route package —
// leaving services/store untouched (they're generic, not per-method) and
// skipping aggregator wiring entirely (the route package is already
// imported and registered there, and which aggregator file that is never
// changes after a route's first creation).
//
// A package's handler/model code can now span more than one file: fileName
// (from -file, defaulting to the package name) names the specific
// handler/model file this invocation targets. If that file already exists
// (and carries handlerMarker/modelMarker), the new method(s) are appended
// to it, same as before this feature existed. If it doesn't exist yet, a
// brand-new handler+model file pair is created holding just the new
// method(s) — this is what makes a second, distinct -file value on an
// already-scaffolded package actually produce a second file. Either way,
// the new method(s)' mux.HandleFunc/openapi.Register wiring always lands
// in the package's single primary file (the one file containing
// registerMarker — Go permits only one func Register per package), never
// in a secondary file.
//
// Each newly-added method wraps itself with middleware.RequireAuth (or
// not) according to this invocation's own cfg.Protected, independently of
// whichever methods already exist in the package — so a package can end
// up with a mix of protected and public methods (see routeMethod.Protected,
// consumed per-method by handler.go.tmpl/register_methods.tmpl). Mixing is
// not an error: detectExistingProtection is used only to build an
// informational RouteResult.Note when this invocation's cfg.Protected
// differs from the package's original aggregator classification.
func addToExistingRoute(appDir, modulePath, relDir, fileName string, data routeData, cfg RouteConfig) (RouteResult, error) {
	existingProtected, existingProtectionFound, err := detectExistingProtection(appDir, modulePath, data.RelDirSlash)
	if err != nil {
		existingProtectionFound = false
	}

	handlersDir := filepath.Join(appDir, "handlers", filepath.FromSlash(data.RelDirSlash))
	modelsDir := filepath.Join(appDir, "models", filepath.FromSlash(data.RelDirSlash))

	// Resolve fileName's own target handler/model files directly, first —
	// never via a directory-wide marker scan — so a -file value naming a
	// file that doesn't exist yet is recognized as "create a new file"
	// rather than silently redirected to whichever file a scan happens to
	// find first, and so a -file value that already has its own
	// registerMarker (this package's original primary, or an earlier
	// -own-register resource — see OwnRegister) is always treated as ITS
	// OWN primary, never misattributed to a different marked file in the
	// same directory (the root cause of the bug this direct-first
	// resolution fixes).
	targetHandlerPath := filepath.Join(handlersDir, fileName+".go")
	targetModelPath := filepath.Join(modelsDir, fileName+".go")

	var primaryPath, primaryContent string
	targetRaw, targetStatErr := os.ReadFile(targetHandlerPath)
	targetHasOwnRegister := targetStatErr == nil && strings.Contains(string(targetRaw), registerMarker)

	switch {
	case targetHasOwnRegister:
		primaryPath = targetHandlerPath
		primaryContent = strings.ReplaceAll(string(targetRaw), "\r\n", "\n")
	case cfg.OwnRegister:
		switch {
		case targetStatErr == nil:
			return RouteResult{}, fmt.Errorf("%s already exists but has no Register function of its own — it's a secondary file already folded into a different resource's Register; pick a different -file name to create a new independent resource, or omit -own-register to add method(s) to it as-is", targetHandlerPath)
		case !os.IsNotExist(targetStatErr):
			return RouteResult{}, targetStatErr
		}
		return addOwnRegisterResource(appDir, modulePath, relDir, fileName, data, cfg)
	default:
		matches, err := findAllMarkedFiles(handlersDir, registerMarker)
		if err != nil {
			return RouteResult{}, err
		}
		switch len(matches) {
		case 0:
			return RouteResult{}, fmt.Errorf("no existing handler file found containing the %q marker under handlers/%s — it may predate this feature; add the marker manually or edit the route directly", registerMarker, data.RelDirSlash)
		case 1:
			primaryPath = matches[0]
		default:
			names := make([]string, len(matches))
			for i, m := range matches {
				names[i] = filepath.Base(m)
			}
			return RouteResult{}, fmt.Errorf("handlers/%s already has more than one independent resource (%s), and -file %q doesn't match any of them — target one of them directly via -file, or add a new one with -own-register (and -name)", data.RelDirSlash, strings.Join(names, ", "), fileName)
		}
		primaryRaw, err := os.ReadFile(primaryPath)
		if err != nil {
			return RouteResult{}, fmt.Errorf("reading %s: %w", primaryPath, err)
		}
		primaryContent = strings.ReplaceAll(string(primaryRaw), "\r\n", "\n")
	}

	targetIsPrimary := targetHandlerPath == primaryPath

	var targetHandlerContent string
	targetExists := false
	switch {
	case targetIsPrimary:
		targetHandlerContent, targetExists = primaryContent, true
	default:
		raw, statErr := os.ReadFile(targetHandlerPath)
		switch {
		case statErr == nil && strings.Contains(string(raw), handlerMarker):
			targetHandlerContent, targetExists = strings.ReplaceAll(string(raw), "\r\n", "\n"), true
		case statErr == nil:
			return RouteResult{}, fmt.Errorf("%s already exists but doesn't contain the %q marker — it may predate this feature or be hand-written; pick a different -file name, or add the marker manually if it should be extended", targetHandlerPath, handlerMarker)
		case !os.IsNotExist(statErr):
			return RouteResult{}, statErr
		}
	}

	var targetModelContent string
	modelExists := false
	if raw, statErr := os.ReadFile(targetModelPath); statErr == nil {
		if !strings.Contains(string(raw), modelMarker) {
			return RouteResult{}, fmt.Errorf("%s already exists but doesn't contain the %q marker — it may predate this feature or be hand-written; pick a different -file name, or add the marker manually if it should be extended", targetModelPath, modelMarker)
		}
		targetModelContent, modelExists = strings.ReplaceAll(string(raw), "\r\n", "\n"), true
	} else if !os.IsNotExist(statErr) {
		return RouteResult{}, statErr
	}
	if targetExists != modelExists {
		return RouteResult{}, fmt.Errorf("inconsistent state: %s exists=%v but %s exists=%v — this handler/model pair should always be created or extended together; reconcile manually before re-running", targetHandlerPath, targetExists, targetModelPath, modelExists)
	}

	// Detect service/store presence from disk, not from this invocation's
	// flag resolution — a plain follow-up call (no -service/-store passed)
	// must reflect what's actually there, not silently assume "generated"
	// or "skipped" from stale/default flag values. Overrides data.HasService/
	// HasStore (initially set from THIS call's resolveLayerRef result) so
	// any newly-inserted methods' TODO comments below are accurate.
	hasServiceOnDisk := dirHasGoFile(filepath.Join(appDir, "services", relDir))
	hasStoreOnDisk := dirHasGoFile(filepath.Join(appDir, "store", relDir))
	data.HasService = data.HasServiceRef || hasServiceOnDisk
	data.HasStore = data.HasStoreRef || hasStoreOnDisk

	// A layer is only retrofitted when THIS invocation explicitly asked
	// about it (ServiceRequested/StoreRequested, set from flag.Visit) —
	// never inferred from a blank/default value, so a plain
	// `nexler create <route> -methods POST` never silently generates a
	// service/store an existing handler-only route never had.
	addService := cfg.ServiceRequested && !data.HasServiceRef && !skipLayerRef(cfg.ServiceRef) && !hasServiceOnDisk
	addStore := cfg.StoreRequested && !data.HasStoreRef && !skipLayerRef(cfg.StoreRef) && !hasStoreOnDisk

	// Reflect this call's own retrofit in data *before* rendering the
	// method-insertion fragment below, so a method added in the same
	// invocation that adds the missing layer gets the accurate TODO
	// wording immediately, not the stale "no service/store" text.
	if addService {
		data.HasService = true
	}
	if addStore {
		data.HasStore = true
	}

	// The "already registered" dedup marker only ever appears inside
	// Register's own Summary: "VERB /route" line, which only ever lives in
	// the primary file — so this must always check primaryContent, never
	// targetHandlerContent: checking a secondary file here would silently
	// let a duplicate route/verb slip through and panic mux.HandleFunc on
	// a duplicate pattern at runtime.
	var newMethods []routeMethod
	var skipped []string
	for _, m := range data.Methods {
		marker := fmt.Sprintf("%q", m.Verb+" "+data.Route)
		if strings.Contains(primaryContent, marker) {
			skipped = append(skipped, m.Verb)
			continue
		}
		newMethods = append(newMethods, m)
	}
	if len(newMethods) == 0 && !addService && !addStore {
		return RouteResult{}, fmt.Errorf("method(s) %s already registered for this route", strings.Join(skipped, ", "))
	}

	// Collisions are checked package-wide, not just against the file this
	// invocation happens to be touching — a package can now span several
	// handler/model files, so a colliding identifier might live in any of
	// them.
	allHandlerContent, err := concatGoFiles(handlersDir)
	if err != nil {
		return RouteResult{}, err
	}
	allModelContent, err := concatGoFiles(modelsDir)
	if err != nil {
		return RouteResult{}, err
	}
	if err := detectIdentifierCollisions(allHandlerContent, allModelContent, newMethods, "", data.Route); err != nil {
		return RouteResult{}, err
	}
	data.Methods = newMethods

	var added []string
	newFileCreated := false
	if len(newMethods) > 0 {
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

		if targetExists {
			targetHandlerContent, err = insertFragment(targetHandlerContent, routeHandlerMethodsFragment, data,
				handlerMarker,
				fmt.Sprintf("could not find the handler-insertion marker in %s — it may predate this feature. Add a line `// nexler:handlers (do not remove this marker)` right before the `// Register wires...` comment (or the file's end, for a secondary file), or add the new method's handler manually.", targetHandlerPath))
			if err != nil {
				return RouteResult{}, err
			}
			if targetIsPrimary {
				primaryContent = targetHandlerContent
			} else if err := os.WriteFile(targetHandlerPath, []byte(targetHandlerContent), 0o644); err != nil {
				return RouteResult{}, fmt.Errorf("writing %s: %w", targetHandlerPath, err)
			}

			targetModelContent, err = insertFragment(targetModelContent, routeModelMethodsFragment, data,
				modelMarker,
				fmt.Sprintf("could not find the model-insertion marker in %s — it may predate this feature. Add a line `// nexler:models (do not remove this marker)` at the end of the file, or add the new structs manually.", targetModelPath))
			if err != nil {
				return RouteResult{}, err
			}
			if err := os.WriteFile(targetModelPath, []byte(targetModelContent), 0o644); err != nil {
				return RouteResult{}, fmt.Errorf("writing %s: %w", targetModelPath, err)
			}
		} else {
			if _, err := writeRouteLayerFile(appDir, "handlers", routeHandlerFileTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
				return RouteResult{}, err
			}
			if _, err := writeRouteLayerFile(appDir, "models", routeModelTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
				return RouteResult{}, err
			}
			newFileCreated = true
		}

		// Register wiring always targets the primary file, regardless of
		// which file the new handler function(s)/model(s) above landed in.
		primaryContent, err = insertFragment(primaryContent, routeRegisterMethodsFragment, data,
			registerMarker,
			fmt.Sprintf("could not find the register-insertion marker in %s — it may predate this feature. Add a line `\t// nexler:register (do not remove this marker)` right before Register's closing brace, or wire the new method manually.", primaryPath))
		if err != nil {
			return RouteResult{}, err
		}
		if err := os.WriteFile(primaryPath, []byte(primaryContent), 0o644); err != nil {
			return RouteResult{}, fmt.Errorf("writing %s: %w", primaryPath, err)
		}

		for _, m := range newMethods {
			added = append(added, m.Verb)
		}
	}

	var layersAdded []string
	if addService {
		if _, err := writeRouteLayerFile(appDir, "services", routeServiceTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
			return RouteResult{}, fmt.Errorf("could not add a service layer to this existing route: %w", err)
		}
		layersAdded = append(layersAdded, "service")
	}
	if addStore {
		if _, err := writeRouteLayerFile(appDir, "store", routeStoreTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
			return RouteResult{}, fmt.Errorf("could not add a store layer to this existing route: %w", err)
		}
		layersAdded = append(layersAdded, "store")
	}

	var note string
	if existingProtectionFound && existingProtected != cfg.Protected && len(added) > 0 {
		existingLabel, addedLabel := "public", "protected"
		if existingProtected {
			existingLabel, addedLabel = "protected", "public"
		}
		note = fmt.Sprintf("note: this route package was already registered as %s; the newly added method(s) (%s) are %s instead — only those handlers wrap themselves with middleware.RequireAuth, the rest of the package is untouched", existingLabel, strings.Join(added, ", "), addedLabel)
	}

	result := RouteResult{Created: false, Added: added, Skipped: skipped, LayersAdded: layersAdded, Note: note, RegisterFuncName: "Register"}
	if newFileCreated {
		result.NewFile = fileName + ".go"
	}
	if len(added) > 0 {
		result.PrimaryFile = filepath.Base(primaryPath)
	}
	return result, nil
}

// addOwnRegisterResource scaffolds fileName as a brand-new, independent
// resource inside relDir's already-existing package: its own primary-shaped
// handler file (own Register<Name> function, own middleware/openapi
// imports, own handlerMarker/registerMarker — rendered from the very same
// handler.go.tmpl a package's first resource uses, just with
// data.RegisterFuncName set to something other than "Register"), its own
// model file, and — unless this invocation's own -service/-store skip or
// reuse them — its own service/store files, all living alongside (never
// touching) the package's existing resource's files. Finally wires an
// additional <alias>.Register<Name>(mux) call into the aggregator alongside
// the package's existing Register call (see wireAggregatorAdditionalCall).
//
// Dispatched from addToExistingRoute when RouteConfig.OwnRegister is set
// and fileName doesn't already name a file with its own registerMarker —
// by that point the caller has already confirmed fileName's target handler
// file doesn't exist yet, so every write below is a fresh file, never an
// overwrite. data is this invocation's own routeData exactly as NewRoute
// built it (Methods/HasServiceRef/HasStoreRef/etc. all reflect only this
// invocation's own flags — not the package's existing resource's), so this
// mirrors NewRoute's brand-new-package path almost exactly, just writing
// into an already-existing package directory instead of a fresh one.
func addOwnRegisterResource(appDir, modulePath, relDir, fileName string, data routeData, cfg RouteConfig) (RouteResult, error) {
	handlersDir := filepath.Join(appDir, "handlers", filepath.FromSlash(data.RelDirSlash))
	modelsDir := filepath.Join(appDir, "models", filepath.FromSlash(data.RelDirSlash))

	data.RegisterFuncName = "Register" + data.Name

	// Collisions are checked package-wide (every existing file in the
	// directory, from any earlier resource), same reasoning as
	// addToExistingRoute's own check — plus the new resource's own
	// Register<Name> function name, which only this path can ever
	// introduce.
	allHandlerContent, err := concatGoFiles(handlersDir)
	if err != nil {
		return RouteResult{}, err
	}
	allModelContent, err := concatGoFiles(modelsDir)
	if err != nil {
		return RouteResult{}, err
	}
	if err := detectIdentifierCollisions(allHandlerContent, allModelContent, data.Methods, data.RegisterFuncName, data.Route); err != nil {
		return RouteResult{}, err
	}

	if data.HTMLResponse {
		layoutKind := cfg.LayoutKind
		if layoutKind == "" {
			layoutKind = "shared"
		}
		if err := validateLayoutKind(layoutKind); err != nil {
			return RouteResult{}, err
		}
		if err := writeHTMLTemplates(appDir, data.RelDirSlash, data.Methods, layoutKind); err != nil {
			return RouteResult{}, fmt.Errorf("could not write HTML templates for the new resource: %w", err)
		}
	}

	if _, err := writeRouteLayerFile(appDir, "handlers", routeHandlerTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
		return RouteResult{}, err
	}
	// !data.HasServiceRef && data.HasService is exactly "this invocation's
	// own -service resolved to 'generate fresh'" — the same condition
	// NewRoute's brand-new-package path uses (there expressed directly as
	// !hasServiceRef && !skipService, which data.HasService/HasServiceRef
	// already encode: HasService is hasServiceRef || !skipService). Doesn't
	// consult what's already on disk for the package's OTHER resource(s)
	// the way addToExistingRoute's own on-disk detection does — an existing
	// resource's services/<relDir>/email.go must never be mistaken for this
	// new resource already having a service of its own.
	if !data.HasServiceRef && data.HasService {
		if _, err := writeRouteLayerFile(appDir, "services", routeServiceTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
			return RouteResult{}, err
		}
	}
	if !data.HasStoreRef && data.HasStore {
		if _, err := writeRouteLayerFile(appDir, "store", routeStoreTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
			return RouteResult{}, err
		}
	}
	if _, err := writeRouteLayerFile(appDir, "models", routeModelTmpl, routeTemplateFS, relDir, fileName, data); err != nil {
		return RouteResult{}, err
	}

	// This invocation's own -protected decides which aggregator file this
	// resource is wired into, independently of wherever the package's
	// existing resource was wired — so email (public) and sms (-protected)
	// can legitimately live in different aggregator files while sharing
	// one handlers/services/store/models package.
	group := "public"
	if cfg.Protected {
		group = "protected"
	}
	importPath := modulePath + "/handlers/" + data.RelDirSlash
	if err := wireAggregatorAdditionalCall(appDir, group, importPath, data.Alias, data.RegisterFuncName); err != nil {
		return RouteResult{}, fmt.Errorf("generated the new resource's files, but could not wire routes/%s/%s.go automatically: %w\nAdd manually in that file:\n  %s.%s(mux)",
			group, group, err, data.Alias, data.RegisterFuncName)
	}

	var added []string
	for _, m := range data.Methods {
		added = append(added, m.Verb)
	}

	newFile := fileName + ".go"
	return RouteResult{
		Added:            added,
		NewFile:          newFile,
		PrimaryFile:      newFile,
		RegisterFuncName: data.RegisterFuncName,
	}, nil
}

// detectIdentifierCollisions checks whether any of methods' generated
// identifiers (handler function, Request/Response types, OperationID)
// already exist in handlerContent/modelContent — nexler builds every one of
// these purely from -module/-submodule/-name and the HTTP verb, never from
// the URL path (see routeMethod.HandlerName/ReqTypeName/RespTypeName/
// OperationID, all built once in NewRoute's per-verb loop), so a second
// `nexler create` invocation targeting a *different* route but the same
// -module/-submodule/verb collides by construction: the per-method
// already-registered check above only looks for this exact route's own
// "<VERB> <route>" marker, so a different route sails past it and would
// otherwise have its handler/type/OperationID silently appended
// (Go-legal-but-duplicate at best, a silent /openapi.json corruption at
// worst) right alongside the first route's. Must run before any file is
// written — Go forbids duplicate top-level declarations, and there's no
// clean way to "undo" a partial insertion. route is only used to phrase the
// error; the identifiers themselves are matched via the exact literal text
// the templates emit, so this only fires on a real collision, never a
// same-route re-run (already filtered out by the caller before this runs).
// Since a package can now span more than one handler/model file (see
// addToExistingRoute), handlerContent/modelContent are normally the
// concatenated content of every .go file in handlers/<relDir> and
// models/<relDir> respectively (via concatGoFiles) — a colliding
// identifier can live in any file in the package, not just the one this
// invocation happens to be reading or writing.
//
// registerFuncName, when non-empty, additionally checks that function name
// itself against handlerContent — used by addOwnRegisterResource (see
// OwnRegister) to catch two separate -own-register calls accidentally
// reusing the same -name, producing the same "Register<Name>" twice in one
// package. Pass "" (the ordinary fold-into-existing-primary path, which
// never introduces a new Register function) to skip this check.
func detectIdentifierCollisions(handlerContent, modelContent string, methods []routeMethod, registerFuncName, route string) error {
	var collisions []string
	if registerFuncName != "" && strings.Contains(handlerContent, "func "+registerFuncName+"(") {
		collisions = append(collisions, fmt.Sprintf("register function %s", registerFuncName))
	}
	for _, m := range methods {
		if strings.Contains(handlerContent, "func "+m.HandlerName+"(") {
			collisions = append(collisions, fmt.Sprintf("handler function %s", m.HandlerName))
		}
		if strings.Contains(modelContent, "type "+m.ReqTypeName+" struct") {
			collisions = append(collisions, fmt.Sprintf("request type %s", m.ReqTypeName))
		}
		if strings.Contains(modelContent, "type "+m.RespTypeName+" struct") {
			collisions = append(collisions, fmt.Sprintf("response type %s", m.RespTypeName))
		}
		if strings.Contains(handlerContent, fmt.Sprintf("OperationID: %q,", m.OperationID)) {
			collisions = append(collisions, fmt.Sprintf("OperationID %q", m.OperationID))
		}
	}
	if len(collisions) == 0 {
		return nil
	}
	return fmt.Errorf("route %q would generate identifier(s) that already exist in this package for what nexler can only assume is a different route: %s — nexler derives handler/type/OperationID names purely from -module/-submodule/-name and the HTTP verb, never the URL path, so two distinct routes sharing those collide; re-run with a distinct -name to disambiguate this route's identifiers, or place it under its own -submodule", route, strings.Join(collisions, ", "))
}

// skipLayerRef reports whether ref is the "none" sentinel — resolveLayerRef's
// own skip signal, re-derivable from the raw RouteConfig.ServiceRef/StoreRef
// string without another resolveLayerRef call (which would also re-validate
// an existing reuse reference unnecessarily).
func skipLayerRef(ref string) bool {
	return ref == "none"
}

// handlerMarker and modelMarker are the stable marker comments left in
// generated handler/model files — insertFragment anchors new method/model
// insertions on them, and findMarkedFile (below) reuses the same text to
// recognize an already-scaffolded route file regardless of its base name
// (the default pkgName-derived name, a -file override, or a hand-renamed
// file), instead of assuming a fixed file name. A package can now span
// several handler files (see addToExistingRoute) — handlerMarker appears in
// every one of them, so it can no longer be used to find "the" handler
// file. registerMarker exists for that: Go allows only one func Register
// per package, so exactly one file in handlers/<relDir> ever contains it —
// that file is the "primary" file, where every method's mux.HandleFunc/
// openapi.Register wiring lives regardless of which file its handler
// function itself is defined in.
const (
	handlerMarker  = "// nexler:handlers (do not remove this marker)"
	modelMarker    = "// nexler:models (do not remove this marker)"
	registerMarker = "\t// nexler:register (do not remove this marker)"
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

// findAllMarkedFiles is findMarkedFile's plural sibling: instead of
// stopping at the first match (which silently picks an arbitrary one once
// more than one file in dir carries marker — the scenario -own-register
// makes possible, see addToExistingRoute), it returns every match, so the
// caller can tell "exactly one, unambiguous" apart from "more than one,
// needs a caller-supplied -file to disambiguate" and error accordingly
// instead of guessing.
func findAllMarkedFiles(dir, marker string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var matches []string
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
			matches = append(matches, p)
		}
	}
	return matches, nil
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

// concatGoFiles reads and concatenates every top-level *.go file in dir —
// used by addToExistingRoute to scan an entire package (which may now span
// several handler or model files) for identifier collisions, instead of
// just the one file a given invocation happens to be reading/writing. A
// non-existent dir simply returns "", nil — same non-error treatment
// dirHasGoFile/findMarkedFile give "nothing here yet".
func concatGoFiles(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", filepath.Join(dir, e.Name()), err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// resolveLayerRef parses a "module[/submodule]" reference passed to
// -service/-store, addressed the exact same way -module/-submodule
// themselves are (sanitizeIdent per segment, joined into a directory under
// layer — "services" or "store"). has is false when ref is empty, meaning
// "generate a fresh file for this route" (today's default behavior) rather
// than reusing one. ref == "none" is the third state — skip is true and
// has is false, meaning "don't generate this layer at all, and don't
// reuse anything either" (see the "Authentication"-style optional-layer
// docs on RouteConfig.ServiceRef/StoreRef). A non-empty ref that isn't
// "none" and doesn't point at an already-scaffolded package is a clear
// error, not a silent no-op — a typo here would otherwise leave a route's
// TODO comment pointing at nothing.
func resolveLayerRef(appDir, modulePath, flagName, layer, ref string) (importPath, pkgName string, has, skip bool, err error) {
	if ref == "" {
		return "", "", false, false, nil
	}
	if ref == "none" {
		return "", "", false, true, nil
	}
	var refParts []string
	for _, p := range strings.SplitN(ref, "/", 2) {
		sp := sanitizeIdent(p)
		if sp == "" {
			return "", "", false, false, fmt.Errorf("-%s %q is invalid — module/submodule must contain at least one letter or digit", flagName, ref)
		}
		refParts = append(refParts, sp)
	}
	refRelDir := filepath.Join(refParts...)
	dir := filepath.Join(appDir, layer, refRelDir)
	if !dirHasGoFile(dir) {
		return "", "", false, false, fmt.Errorf("-%s %q points at %s, but no package exists there yet — scaffold it first, or omit -%s to generate a new one here", flagName, ref, filepath.Join(layer, refRelDir), flagName)
	}
	return modulePath + "/" + layer + "/" + filepath.ToSlash(refRelDir), refParts[len(refParts)-1], true, false, nil
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
// routes/protected/protected.go, so addToExistingRoute can note
// when this invocation's -protected setting differs from the package's
// original aggregator classification. Informational only — mixed
// protection within one package is supported (see routeMethod.Protected).
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
var openAPIUpToDateMarkers = []string{"Tags", "RespUnwrapped", "ClientIdAuth", "basePath"}

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

// responseHTMLOldFunc is the exact byte-for-byte body of HTML in every
// nexler-generated response.go before the composeHTML extraction (see
// ensureResponseHTMLUpgrade) — zero per-app templating, so identical
// across every app. Used both to detect "not yet upgraded" and as the
// literal anchor replaced by the new, much shorter composeHTML-calling
// body. Only the function body is anchored, not its doc comment above —
// deliberately: the comment is prose, more likely to have been reformatted
// by an editor, and leaving it as-is on retrofit is a purely cosmetic
// staleness, not a correctness issue.
const responseHTMLOldFunc = `func HTML(w http.ResponseWriter, r *http.Request, module, name, title string, data any) {
	contentPath, err := resolveHTMLFile(module, name+".html")
	if err != nil {
		Error(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	contentSrc, err := os.ReadFile(contentPath)
	if err != nil {
		Error(w, r, http.StatusInternalServerError, fmt.Sprintf("reading %s: %v", contentPath, err))
		return
	}
	layoutPath, err := resolveHTMLFile(module, "layout.html")
	if err != nil {
		Error(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	layoutSrc, err := os.ReadFile(layoutPath)
	if err != nil {
		Error(w, r, http.StatusInternalServerError, fmt.Sprintf("reading %s: %v", layoutPath, err))
		return
	}

	page := struct {
		Title string
		Data  any
	}{title, data}

	contentTmpl, err := template.New(name).Parse(string(contentSrc))
	if err != nil {
		Error(w, r, http.StatusInternalServerError, fmt.Sprintf("parsing %s: %v", contentPath, err))
		return
	}
	var content bytes.Buffer
	if err := contentTmpl.Execute(&content, page); err != nil {
		Error(w, r, http.StatusInternalServerError, fmt.Sprintf("rendering %s: %v", contentPath, err))
		return
	}

	layoutTmpl, err := template.New("layout").Parse(string(layoutSrc))
	if err != nil {
		Error(w, r, http.StatusInternalServerError, fmt.Sprintf("parsing %s: %v", layoutPath, err))
		return
	}
	var out bytes.Buffer
	if err := layoutTmpl.Execute(&out, struct {
		Title   string
		Content template.HTML
	}{title, template.HTML(content.String())}); err != nil {
		Error(w, r, http.StatusInternalServerError, fmt.Sprintf("rendering %s: %v", layoutPath, err))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(out.Bytes())
}`

// ensureResponseHTMLUpgrade brings an app scaffolded before HTML's
// rendering was extracted into composeHTML up to date: adds
// composeHTML/renderOptionalPartial (optional header/sidebar/footer
// partials, and Subject/Path on the page template data) and the new
// HTMLError/Unauthorised functions, and rewrites HTML's own body to call
// composeHTML instead of duplicating its rendering logic inline — see
// response.go.tmpl's own doc comments for what each of these does.
//
// Deliberately not a full-file regeneration (same reasoning
// ensureResponseJSONRaw already gives for response.go specifically being
// hand-extension-prone): re-renders the current response.go.tmpl to a
// string (never written to disk directly) purely to obtain the exact,
// correctly-conditioned (on this app's own HasDB/-auth) new HTML body and
// new-functions text, then splices just those two pieces into the
// existing file — same append-before-responseMarker precedent
// ensureResponseJSONRaw's own JSONRaw insertion already established,
// coexisting safely with it. HasDB/-auth are inferred from disk
// (readCoreDBType/detectAuthFiles), the same way ensureServiceAuth and
// this session's own MergeServiceAuth already do, since a retrofit
// function is never handed the original NewAppConfig.
func ensureResponseHTMLUpgrade(appDir string) (bool, error) {
	path := filepath.Join(appDir, "response", "response.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("%s does not exist — is %s a nexler app directory?", path, appDir)
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "func composeHTML") {
		return false, nil
	}
	if !strings.Contains(content, responseHTMLOldFunc) {
		return false, fmt.Errorf("%s's HTML function doesn't match what nexler generated (has it been hand-rewritten?) — add composeHTML/renderOptionalPartial/HTMLError/Unauthorised by hand (see response.go.tmpl), and have HTML call composeHTML, or restore it from a fresh scaffold and reapply your changes", path)
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	_, hasCoreDB := readCoreDBType(appDir)
	hasJWT, hasSession := detectAuthFiles(appDir)
	authKind := "none"
	if hasJWT || hasSession {
		authKind = "jwt"
	}
	data := struct {
		ModulePath string
		HasDB      bool
		AuthKind   string
	}{ModulePath: modulePath, HasDB: hasCoreDB, AuthKind: authKind}

	tmplPath := templatesRoot + "/response/response.go.tmpl"
	tmplRaw, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return false, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	rendered, err := render(tmplPath, tmplRaw, data)
	if err != nil {
		return false, fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	renderedStr := string(rendered)

	newHTMLStart := strings.Index(renderedStr, "func HTML(w http.ResponseWriter")
	if newHTMLStart == -1 {
		return false, errors.New("internal error: rendered response.go.tmpl is missing HTML's new body")
	}
	newHTMLBodyEnd := strings.Index(renderedStr[newHTMLStart:], "\n}\n")
	if newHTMLBodyEnd == -1 {
		return false, errors.New("internal error: could not find the end of HTML's new body")
	}
	newHTMLFunc := renderedStr[newHTMLStart : newHTMLStart+newHTMLBodyEnd+2]

	const newFuncsStart = "\n// HTMLError renders"
	funcsStart := strings.Index(renderedStr, newFuncsStart)
	if funcsStart == -1 {
		return false, errors.New("internal error: rendered response.go.tmpl is missing the expected HTMLError block")
	}
	funcsEnd := strings.Index(renderedStr, "\n"+responseMarker)
	if funcsEnd == -1 || funcsEnd < funcsStart {
		return false, fmt.Errorf("internal error: rendered response.go.tmpl is missing the expected %q marker", responseMarker)
	}
	newFuncsBlock := strings.Trim(renderedStr[funcsStart:funcsEnd], "\n") + "\n"

	content = strings.Replace(content, responseHTMLOldFunc, newHTMLFunc, 1)

	anchor := responseMarker
	if !strings.Contains(content, anchor) {
		anchor = responseErrorAnchor
		if !strings.Contains(content, anchor) {
			return false, fmt.Errorf("could not find an insertion point for the new functions in %s (has it been hand-rewritten?)", path)
		}
	}
	content = strings.Replace(content, anchor, newFuncsBlock+"\n"+anchor, 1)

	if (hasJWT || hasSession) && !strings.Contains(content, `"`+modulePath+`/auth"`) {
		content = strings.Replace(content, "\t\"strings\"\n", "\t\"strings\"\n\n\t\""+modulePath+"/auth\"\n", 1)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ensureAuthSubjectContext brings an app scaffolded before RequireAuth
// started attaching the verified subject (userID) to the request context
// up to date: it adds auth/context.go if missing (ContextWithSubject/
// Subject — see auth/context.go.tmpl) and, if middleware/auth.go predates
// this feature, fully regenerates it from the current template. Apps
// scaffolded with -auth none (or before -auth existed at all) have
// neither auth/jwt.go nor auth/session.go — detectAuthFiles reports both
// false, and there's nothing to upgrade.
//
// middleware/auth.go is regenerated wholesale rather than marker-patched
// (contrast ensureResponseJSONRaw's append-only insertion above): unlike
// response.go, RequireAuth has no established hand-customization pattern,
// and its content is fully derived from AuthKind/ModulePath alone — the
// same "pure generated infra, no per-app free text" reasoning
// ensureOpenAPIUpToDate already relies on for openapi.go. A sanity check
// (the file still contains "func RequireAuth") guards against silently
// overwriting a file that's been hand-rewritten beyond recognition —
// that case errors out instead, same tone as ensureResponseJSONRaw's own
// "has it been hand-rewritten?" fallback.
func ensureAuthSubjectContext(appDir string) (bool, error) {
	hasJWT, hasSession := detectAuthFiles(appDir)
	if !hasJWT && !hasSession {
		return false, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}

	_, hasCoreDB := readCoreDBType(appDir)
	authKind := "session"
	switch {
	case hasJWT && hasSession:
		authKind = "both"
	case hasJWT:
		authKind = "jwt"
	}

	contextPath := filepath.Join(appDir, "auth", "context.go")
	addedContext := false
	if _, err := os.Stat(contextPath); os.IsNotExist(err) {
		// HasCoreDB/AuthKind are needed because context.go.tmpl's
		// ContextWithService/Service block is gated on them
		// ({{- if and .HasCoreDB (or (eq .AuthKind "jwt") (eq .AuthKind
		// "both")) }}); Multitenant is always false here for the same
		// reason MergeServiceAuth is below — this path only regenerates an
		// app old enough to predate ContextWithSubject entirely, long
		// before -multitenant could exist.
		data := struct {
			AppName, ModulePath, AuthKind string
			HasCoreDB, Multitenant        bool
		}{
			AppName:    filepath.Base(modulePath),
			ModulePath: modulePath,
			AuthKind:   authKind,
			HasCoreDB:  hasCoreDB,
		}
		tmplPath := templatesRoot + "/auth/context.go.tmpl"
		if err := writeTemplateFile(tmplPath, contextPath, data); err != nil {
			return false, err
		}
		addedContext = true
	} else if err != nil {
		return false, err
	}

	authGoPath := filepath.Join(appDir, "middleware", "auth.go")
	raw, err := os.ReadFile(authGoPath)
	if err != nil {
		return addedContext, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", authGoPath, appDir, err)
	}
	content := string(raw)
	if strings.Contains(content, "ContextWithSubject") {
		return addedContext, nil
	}
	if !strings.Contains(content, "func RequireAuth") {
		return addedContext, fmt.Errorf("%s doesn't look like a generated RequireAuth (no \"func RequireAuth\" found) — has it been hand-rewritten? Regenerate it manually to attach the subject via auth.ContextWithSubject, or restore it from a fresh scaffold and reapply your changes", authGoPath)
	}

	// MergeServiceAuth/Multitenant are always false here: this path only
	// regenerates an app whose middleware/auth.go predates
	// ContextWithSubject entirely (see this function's own doc comment) —
	// long before -merge-service-auth/-multitenant existed, so there's no
	// merged/multitenant state to preserve. An app that's already on either
	// design already contains ContextWithSubject and so never reaches this
	// regeneration branch at all.
	data := struct {
		AuthKind, ModulePath string
		MergeServiceAuth     bool
		Multitenant          bool
	}{AuthKind: authKind, ModulePath: modulePath}
	tmplPath := templatesRoot + "/middleware/auth.go.tmpl"
	tmplRaw, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return addedContext, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	rendered, err := render(tmplPath, tmplRaw, data)
	if err != nil {
		return addedContext, fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	if err := os.WriteFile(authGoPath, rendered, 0o644); err != nil {
		return addedContext, err
	}
	return true, nil
}

// ensureJWTClaims brings an app scaffolded before Claims followed RFC
// 7519 up to date: it fully regenerates auth/jwt.go from the current
// template if the file predates the IssuedAt (iat) claim. Apps scaffolded
// with -auth none/session only (no JWT at all) simply have no auth/jwt.go
// — silently skipped, same as ensureAuthSubjectContext skipping -auth
// none.
//
// Regenerated wholesale, not marker-patched (contrast
// ensureResponseJSONRaw's append-only insertion): jwt.go.tmpl's own doc
// comment invites hand-added Claims fields ("Add fields as your app needs
// them"), same as response.go inviting hand-added helpers — but unlike
// JSONRaw, this change also rewrites IssueJWT's signature and Claims'
// existing fields, which can't be expressed as a pure append. A sanity
// check (the file must still contain "func IssueJWT") guards against
// silently overwriting a file that's been hand-rewritten beyond
// recognition — that case errors out instead, same tone as
// ensureAuthSubjectContext's own "has it been hand-rewritten?" guard on
// middleware/auth.go. The trade-off, same one documented there: any
// hand-added Claims field or IssueJWT customization is silently
// discarded by the regeneration.
func ensureJWTClaims(appDir string) (bool, error) {
	path := filepath.Join(appDir, "auth", "jwt.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No JWT auth in this app — nothing to upgrade.
			return false, nil
		}
		return false, err
	}
	content := string(raw)
	if strings.Contains(content, "IssuedAt") {
		return false, nil
	}
	if !strings.Contains(content, "func IssueJWT") {
		return false, fmt.Errorf("%s doesn't look like a generated jwt.go (no \"func IssueJWT\" found) — has it been hand-rewritten? Regenerate it manually to add RFC 7519's sub/exp/iat claims, or restore it from a fresh scaffold and reapply your changes", path)
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	data := struct{ AppName, ModulePath string }{
		AppName:    filepath.Base(modulePath),
		ModulePath: modulePath,
	}
	tmplPath := templatesRoot + "/auth/jwt.go.tmpl"
	tmplRaw, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return false, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	rendered, err := render(tmplPath, tmplRaw, data)
	if err != nil {
		return false, fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ensureMongoDatabaseName brings an app scaffolded before nexler correctly
// derived a Mongo connection's database name from its own DSN up to date.
// Previously db/mongo.go never retained the database name embedded in a
// connection's DSN at all, and every "core" package file (core/config.go,
// core/errorlog.go, core/kgate_channels.go) hardcoded the literal database
// name "core" instead — so both `nexler init db` and the running app
// always read/wrote a Mongo database literally named "core", silently
// ignoring whatever database the DSN's own path actually named (e.g.
// mongodb://host:27017/mydb). This regenerates db/mongo.go (if present)
// and, for an app whose core connection is mongo, the three core/*.go
// files that used to reference the hardcoded name.
//
// Full regeneration, not marker-patched: db/mongo.go.tmpl has zero
// per-app templating at all (same reasoning ensureOpenAPIUpToDate already
// relies on for openapi.go), and the core/*.go files are pure generated
// infra with no established hand-customization pattern (same reasoning
// ensureAuthSubjectContext relies on for middleware/auth.go). A sanity
// check per file guards against silently overwriting one that's been
// hand-rewritten beyond recognition — that case errors out instead,
// naming the file and asking the developer to reconcile it by hand.
func ensureMongoDatabaseName(appDir string) (bool, error) {
	changed := false

	mongoGoPath := filepath.Join(appDir, "db", "mongo.go")
	raw, err := os.ReadFile(mongoGoPath)
	if err != nil && !os.IsNotExist(err) {
		return changed, err
	}
	if err == nil {
		content := string(raw)
		if !strings.Contains(content, "MongoDatabaseName") {
			if !strings.Contains(content, "func Mongo(") {
				return changed, fmt.Errorf("%s doesn't look like a generated db/mongo.go (no \"func Mongo(\" found) — has it been hand-rewritten? Regenerate it manually to add MongoDatabaseName (deriving the database name from the connection's own DSN instead of a hardcoded literal), or restore it from a fresh scaffold and reapply your changes", mongoGoPath)
			}
			tmplPath := templatesRoot + "/db/mongo.go.tmpl"
			tmplRaw, err := templateFS.ReadFile(tmplPath)
			if err != nil {
				return changed, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
			}
			rendered, err := render(tmplPath, tmplRaw, nil)
			if err != nil {
				return changed, fmt.Errorf("rendering %s: %w", tmplPath, err)
			}
			if err := os.WriteFile(mongoGoPath, rendered, 0o644); err != nil {
				return changed, err
			}
			changed = true
		}
	}

	coreDBType, ok := readCoreDBType(appDir)
	if !ok || coreDBType != "mongo" {
		return changed, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return changed, err
	}
	data := struct{ AppName, ModulePath, CoreDBAccessor string }{
		AppName:        filepath.Base(modulePath),
		ModulePath:     modulePath,
		CoreDBAccessor: "Mongo",
	}

	coreFiles := []struct {
		file, tmpl, marker string
	}{
		{"config.go", "core/config.go.tmpl", "func configCollection("},
		{"errorlog.go", "core/errorlog.go.tmpl", "func errorLogCollection("},
		{"kgate_channels.go", "core/kgate_channels.go.tmpl", "func kgateChannelCollection("},
	}
	for _, cf := range coreFiles {
		path := filepath.Join(appDir, "core", cf.file)
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return changed, err
		}
		content := string(raw)
		if strings.Contains(content, "MongoDatabaseName") {
			continue
		}
		if !strings.Contains(content, cf.marker) {
			return changed, fmt.Errorf("%s doesn't look like a generated file (no %q found) — has it been hand-rewritten? Regenerate it manually to read the database name via db.MongoDatabaseName instead of the hardcoded literal \"core\", or restore it from a fresh scaffold and reapply your changes", path, cf.marker)
		}
		tmplPath := templatesRoot + "/" + cf.tmpl
		tmplRaw, err := templateFS.ReadFile(tmplPath)
		if err != nil {
			return changed, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
		}
		rendered, err := render(tmplPath, tmplRaw, data)
		if err != nil {
			return changed, fmt.Errorf("rendering %s: %w", tmplPath, err)
		}
		if err := os.WriteFile(path, rendered, 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}

// mongoStructToBSONOldLoop/mongoStructToBSONNewLoop are the exact
// byte-for-byte text of structToBSON's field loop in mongo/mongo.go,
// before and after the embedded-field-flattening fix (see
// mongo.go.tmpl) — structToBSON has zero per-app templating, so its
// rendered text is identical across every app, making an exact literal
// replacement (rather than a full-file regeneration) both safe and
// precise, the same "narrow, anchor-based patch of an exact known body"
// precedent ensureKgateResumeAll already established for kgate.go's
// Register.
const (
	mongoStructToBSONOldLoop = `	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if fv.IsZero() {
			continue
		}
		key := bsonFieldName(f)
		if key == "-" {
			continue
		}
		out[key] = fv.Interface()
	}`
	mongoStructToBSONNewLoop = `	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if f.Anonymous && fv.Kind() == reflect.Struct {
			// An embedded field (e.g. a shared store/common.Base
			// contributing _id) is flattened into this same top-level
			// filter rather than nested under its own key — matching how
			// the Mongo driver's own default (non-inline) BSON encoding
			// treats an embedded struct, and how Set/InsertID already
			// expect callers to build filters (T{Base: common.Base{ID: id}}).
			nested, err := structToBSON(fv)
			if err != nil {
				return nil, err
			}
			maps.Copy(out, nested)
			continue
		}
		if fv.IsZero() {
			continue
		}
		key := bsonFieldName(f)
		if key == "-" {
			continue
		}
		out[key] = fv.Interface()
	}`
)

// ensureMongoEmbeddedFilterFix brings an app scaffolded before
// structToBSON flattened anonymously embedded struct fields (see
// mongo.go.tmpl's own doc comment, and store/common.Base below) up to
// date: a filter like T{Base: common.Base{ID: id}} used to silently build
// {"base": ...} instead of {"_id": id}, breaking every by-ID lookup for
// any type embedding a shared base struct. A silent no-op if
// mongo/mongo.go doesn't exist (app wasn't scaffolded with -db mongo).
// Errors, naming the file, if structToBSON's loop doesn't match the exact
// text this checks for — i.e. it's been hand-rewritten — rather than
// silently overwriting it.
func ensureMongoEmbeddedFilterFix(appDir string) (bool, error) {
	path := filepath.Join(appDir, "mongo", "mongo.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "maps.Copy(out, nested)") {
		return false, nil
	}
	if !strings.Contains(content, mongoStructToBSONOldLoop) {
		return false, fmt.Errorf("%s's structToBSON doesn't match what nexler generated (has it been hand-rewritten?) — add the embedded-field-flattening branch by hand (see mongo.go.tmpl's own structToBSON), or restore it from a fresh scaffold and reapply your changes", path)
	}
	content = strings.Replace(content, mongoStructToBSONOldLoop, mongoStructToBSONNewLoop, 1)
	if !strings.Contains(content, "\n\t\"maps\"\n") {
		content = strings.Replace(content, "\t\"fmt\"\n", "\t\"fmt\"\n\t\"maps\"\n", 1)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ensureStoreCommon brings an app scaffolded with -db mongo before
// store/common.Base existed up to date: writes store/common/common.go if
// missing. A silent no-op if mongo/mongo.go doesn't exist (app wasn't
// scaffolded with -db mongo) or store/common/common.go already exists —
// never overwrites, matching kpass.go's own "never clobber a possibly
// hand-edited file" precedent (Base has no versioned content to bring up
// to date, just present-or-absent).
func ensureStoreCommon(appDir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(appDir, "mongo", "mongo.go")); err != nil {
		return false, nil
	}
	path := filepath.Join(appDir, "store", "common", "common.go")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := writeTemplateFile(templatesRoot+"/store/common/common.go.tmpl", path, nil); err != nil {
		return false, err
	}
	return true, nil
}

// swaggerConfigFieldAnchor/swaggerConfigLoadAnchor are fixed,
// template-variable-free substrings of every generated config/config.go —
// identical regardless of EnvPrefix/AuthKind — that ensureSwaggerToggle
// anchors its Config-struct-field and load()-line insertions on.
// swaggerGetEnvOrBody is getEnvOr's complete, equally fixed body, used
// both to detect it (so it's only ever added once) and as the insertion
// point for getEnvBoolOr right after it.
const (
	swaggerConfigFieldAnchor = "\tPort string "
	swaggerConfigLoadAnchor  = "\t\tPort: getEnvOr("
	swaggerGetEnvOrBody      = "func getEnvOr(key, fallback string) string {\n\tif v := os.Getenv(key); v != \"\" {\n\t\treturn v\n\t}\n\treturn fallback\n}"
	swaggerGetEnvBoolOrCode  = "\n\nfunc getEnvBoolOr(key string, fallback bool) bool {\n\tswitch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {\n\tcase \"true\", \"1\":\n\t\treturn true\n\tcase \"false\", \"0\":\n\t\treturn false\n\tdefault:\n\t\treturn fallback\n\t}\n}"
	// swaggerHomeMuxLineOriginal/Patched: home.go's Register wires
	// GET /swagger directly to templates.ServePage("swagger.html")
	// originally — retrofitted to the new named HandleSwagger handler
	// instead, which is what actually enforces the toggle (see below).
	swaggerHomeMuxLineOriginal = `mux.HandleFunc("GET /swagger", templates.ServePage("swagger.html"))`
	swaggerHomeMuxLinePatched  = `mux.HandleFunc("GET /swagger", HandleSwagger)`
	// swaggerHandleOpenAPISig anchors on HandleOpenAPI's fixed function
	// signature line (its body isn't fixed — it embeds the app's own name
	// via openapi.Spec("<AppName>")) — the guard clause is inserted right
	// after the opening brace, so it never needs to match the body.
	swaggerHandleOpenAPISig  = "func HandleOpenAPI(w http.ResponseWriter, r *http.Request) {\n"
	swaggerHandleOpenAPIGuard = "\tif !config.C.SwaggerEnabled {\n\t\thttp.NotFound(w, r)\n\t\treturn\n\t}\n"
	// swaggerHandleSwaggerCode is the new handler ensureSwaggerToggle
	// inserts right before HandleOpenAPI — GET /swagger's toggle check
	// has to live in a real handler function too, not just at
	// registration time, for the same "GET /" subtree-pattern reason
	// documented on HandleOpenAPI's own doc comment in home.go.tmpl.
	swaggerHandleSwaggerCode = "// HandleSwagger serves the bundled Swagger UI page, gated on\n// config.C.SwaggerEnabled the same way and for the same reason as\n// HandleOpenAPI above.\nfunc HandleSwagger(w http.ResponseWriter, r *http.Request) {\n\tif !config.C.SwaggerEnabled {\n\t\thttp.NotFound(w, r)\n\t\treturn\n\t}\n\ttemplates.ServePage(\"swagger.html\")(w, r)\n}\n\n"
)

// ensureSwaggerToggle brings an app scaffolded before Config gained
// SwaggerEnabled up to date: adds the field + getEnvBoolOr helper +
// load() wiring to config/config.go, wraps handlers/home/home.go's
// /swagger and /openapi.json registrations in
// "if config.C.SwaggerEnabled { ... }" (adding the config import if
// missing), and appends {PREFIX}_SWAGGER_ENABLED=true to .env — but only
// when that key isn't already present, same as ensureEnvVars: a
// production deployment may have deliberately set it to false, and this
// must never silently reset that back to true.
//
// Every anchor is a fixed, template-variable-free substring (identical
// across every app regardless of EnvPrefix/AuthKind/-ui), so one literal
// match works everywhere — same fail-loud-on-mismatch precedent as every
// other ensure* retrofit: a hand-rewritten config.go/home.go errors out
// naming the exact snippet to add by hand, instead of being silently
// corrupted.
func ensureSwaggerToggle(appDir string) (bool, error) {
	changed := false

	prefix, err := recoverEnvPrefix(appDir)
	if err != nil {
		return changed, err
	}

	configPath := filepath.Join(appDir, "config", "config.go")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return changed, fmt.Errorf("reading %s: %w", configPath, err)
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.Contains(content, "SwaggerEnabled") {
		fieldIdx := strings.Index(content, swaggerConfigFieldAnchor)
		loadIdx := strings.Index(content, swaggerConfigLoadAnchor)
		if fieldIdx == -1 || loadIdx == -1 {
			return changed, fmt.Errorf("%s doesn't look like a generated config.go (missing the expected Port field/load() lines) — has it been hand-rewritten? Add manually: a `SwaggerEnabled bool` field to Config, and `SwaggerEnabled: getEnvBoolOr(%q, true),` to load()'s returned Config literal", configPath, prefix+"_SWAGGER_ENABLED")
		}

		fieldInsertAt := fieldIdx + strings.Index(content[fieldIdx:], "\n") + 1
		fieldLine := "\tSwaggerEnabled bool // " + prefix + "_SWAGGER_ENABLED — enables /swagger and /openapi.json; default true, set to false in production\n"
		content = content[:fieldInsertAt] + fieldLine + content[fieldInsertAt:]

		// Re-find loadIdx: the field insertion above may have shifted it.
		loadIdx = strings.Index(content, swaggerConfigLoadAnchor)
		loadInsertAt := loadIdx + strings.Index(content[loadIdx:], "\n") + 1
		loadLine := "\t\tSwaggerEnabled: getEnvBoolOr(\"" + prefix + "_SWAGGER_ENABLED\", true),\n"
		content = content[:loadInsertAt] + loadLine + content[loadInsertAt:]

		if !strings.Contains(content, "func getEnvBoolOr") {
			bodyIdx := strings.Index(content, swaggerGetEnvOrBody)
			if bodyIdx == -1 {
				return changed, fmt.Errorf("%s: getEnvOr doesn't match its known original body (has it been hand-rewritten?) — add getEnvBoolOr manually:%s", configPath, swaggerGetEnvBoolOrCode)
			}
			insertAt := bodyIdx + len(swaggerGetEnvOrBody)
			content = content[:insertAt] + swaggerGetEnvBoolOrCode + content[insertAt:]
		}

		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	homePath := filepath.Join(appDir, "handlers", "home", "home.go")
	homeRaw, err := os.ReadFile(homePath)
	if err != nil {
		return changed, fmt.Errorf("reading %s: %w", homePath, err)
	}
	homeContent := strings.ReplaceAll(string(homeRaw), "\r\n", "\n")
	if !strings.Contains(homeContent, "config.C.SwaggerEnabled") {
		sigIdx := strings.Index(homeContent, swaggerHandleOpenAPISig)
		if sigIdx == -1 || !strings.Contains(homeContent, swaggerHomeMuxLineOriginal) {
			return changed, fmt.Errorf("%s doesn't look like a generated home.go (missing the expected HandleOpenAPI signature or GET /swagger registration) — has it been hand-rewritten? Add manually: a config.C.SwaggerEnabled check (http.NotFound on false) at the top of HandleOpenAPI, a new HandleSwagger handler doing the same before serving swagger.html, and wire Register's GET /swagger to HandleSwagger instead of templates.ServePage(\"swagger.html\") directly", homePath)
		}

		// Insert the guard clause right after HandleOpenAPI's opening
		// brace — must happen before the HandleSwagger insertion below,
		// which is anchored on this same signature line and would
		// otherwise shift sigIdx.
		guardAt := sigIdx + len(swaggerHandleOpenAPISig)
		homeContent = homeContent[:guardAt] + swaggerHandleOpenAPIGuard + homeContent[guardAt:]

		// Insert the new HandleSwagger function immediately before
		// HandleOpenAPI's doc comment/signature (re-found: the guard
		// insertion above shifted it).
		sigIdx = strings.Index(homeContent, swaggerHandleOpenAPISig)
		docStart := strings.LastIndex(homeContent[:sigIdx], "// HandleOpenAPI")
		insertAt := sigIdx
		if docStart != -1 {
			insertAt = docStart
		}
		homeContent = homeContent[:insertAt] + swaggerHandleSwaggerCode + homeContent[insertAt:]

		homeContent = strings.Replace(homeContent, swaggerHomeMuxLineOriginal, swaggerHomeMuxLinePatched, 1)

		modulePath, err := readModulePath(appDir)
		if err != nil {
			return changed, err
		}
		configImport := modulePath + "/config"
		if !strings.Contains(homeContent, "\""+configImport+"\"") {
			homeContent, err = insertImport(homeContent, "", configImport)
			if err != nil {
				return changed, fmt.Errorf("%s: adding %q import: %w", homePath, configImport, err)
			}
		}

		if err := os.WriteFile(homePath, []byte(homeContent), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	envPath := filepath.Join(appDir, ".env")
	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		return changed, fmt.Errorf("reading %s: %w", envPath, err)
	}
	envContent := strings.ReplaceAll(string(envRaw), "\r\n", "\n")
	envKey := prefix + "_SWAGGER_ENABLED"
	if !envHasKey(envContent, envKey) {
		var b strings.Builder
		b.WriteString(envContent)
		if len(envContent) > 0 && !strings.HasSuffix(envContent, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(envKey + "=true\n")
		if err := os.WriteFile(envPath, []byte(b.String()), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}

// apiBasePathHomeSpecOld/apiBasePathHomeSpecOpen anchor ensureAPIBasePath's
// patch of home.go's HandleOpenAPI: openapi.Spec is always called as
// openapi.Spec("<AppName>") before this feature — the app name itself isn't
// a fixed anchor (it varies per app), so the match is done in two steps:
// find the fixed openapi.Spec(" prefix, then the next ") after it closes
// the string literal.
const apiBasePathHomeSpecOpen = `openapi.Spec("`

// ensureAPIBasePath brings an app scaffolded before Config gained
// APIBasePath up to date: adds the field + load() wiring to
// config/config.go (reusing the same Port anchors ensureSwaggerToggle
// already established, so this doesn't depend on ensureSwaggerToggle having
// run first), turns home.go's single-arg openapi.Spec("<AppName>") call
// into the two-arg openapi.Spec("<AppName>", config.C.APIBasePath) form,
// and appends {PREFIX}_API_BASE_PATH= (blank — this setting has no default)
// to .env, only when that key isn't already present.
//
// Registered in update.go's updateChecks after ensureOpenAPIUpToDate and
// ensureSwaggerToggle, so by the time this runs, openapi/openapi.go's Spec
// already takes two arguments (ensureOpenAPIUpToDate regenerates it — see
// the "basePath" marker in openAPIUpToDateMarkers) and home.go already
// imports config (ensureSwaggerToggle adds it if missing).
func ensureAPIBasePath(appDir string) (bool, error) {
	changed := false

	prefix, err := recoverEnvPrefix(appDir)
	if err != nil {
		return changed, err
	}

	configPath := filepath.Join(appDir, "config", "config.go")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return changed, fmt.Errorf("reading %s: %w", configPath, err)
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.Contains(content, "APIBasePath") {
		fieldIdx := strings.Index(content, swaggerConfigFieldAnchor)
		loadIdx := strings.Index(content, swaggerConfigLoadAnchor)
		if fieldIdx == -1 || loadIdx == -1 {
			return changed, fmt.Errorf("%s doesn't look like a generated config.go (missing the expected Port field/load() lines) — has it been hand-rewritten? Add manually: an `APIBasePath string` field to Config, and `APIBasePath: os.Getenv(%q),` to load()'s returned Config literal", configPath, prefix+"_API_BASE_PATH")
		}

		fieldInsertAt := fieldIdx + strings.Index(content[fieldIdx:], "\n") + 1
		fieldLine := "\tAPIBasePath string // " + prefix + "_API_BASE_PATH — path prefix the external gateway mounts this service under (e.g. \"/api/v1\"); empty means openapi.json advertises no prefix (direct access). Purely documentational — does not affect actual route registration/matching.\n"
		content = content[:fieldInsertAt] + fieldLine + content[fieldInsertAt:]

		// Re-find loadIdx: the field insertion above may have shifted it.
		loadIdx = strings.Index(content, swaggerConfigLoadAnchor)
		loadInsertAt := loadIdx + strings.Index(content[loadIdx:], "\n") + 1
		loadLine := "\t\tAPIBasePath: os.Getenv(\"" + prefix + "_API_BASE_PATH\"),\n"
		content = content[:loadInsertAt] + loadLine + content[loadInsertAt:]

		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	homePath := filepath.Join(appDir, "handlers", "home", "home.go")
	homeRaw, err := os.ReadFile(homePath)
	if err != nil {
		return changed, fmt.Errorf("reading %s: %w", homePath, err)
	}
	homeContent := strings.ReplaceAll(string(homeRaw), "\r\n", "\n")
	if !strings.Contains(homeContent, "config.C.APIBasePath") {
		openIdx := strings.Index(homeContent, apiBasePathHomeSpecOpen)
		if openIdx == -1 {
			return changed, fmt.Errorf("%s doesn't look like a generated home.go (no openapi.Spec(\"...\") call found) — has it been hand-rewritten? Add config.C.APIBasePath as Spec's second argument manually", homePath)
		}
		closeRel := strings.Index(homeContent[openIdx:], "\")")
		if closeRel == -1 {
			return changed, fmt.Errorf("%s: openapi.Spec(\" call has no closing \") — has it been hand-rewritten?", homePath)
		}
		closeIdx := openIdx + closeRel + 2 // position right after the closing ")
		call := homeContent[openIdx:closeIdx]
		newCall := call[:len(call)-1] + ", config.C.APIBasePath)"
		homeContent = homeContent[:openIdx] + newCall + homeContent[closeIdx:]

		if err := os.WriteFile(homePath, []byte(homeContent), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	envPath := filepath.Join(appDir, ".env")
	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		return changed, fmt.Errorf("reading %s: %w", envPath, err)
	}
	envContent := strings.ReplaceAll(string(envRaw), "\r\n", "\n")
	envKey := prefix + "_API_BASE_PATH"
	if !envHasKey(envContent, envKey) {
		var b strings.Builder
		b.WriteString(envContent)
		if len(envContent) > 0 && !strings.HasSuffix(envContent, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(envKey + "=\n")
		if err := os.WriteFile(envPath, []byte(b.String()), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}

// insertIDRetrofit describes one InsertID retrofit target: the
// dialect's directory/file name (mongo/mongo.go, mysql/mysql.go, ...)
// and the exact source to append when the file exists but doesn't yet
// define InsertID.
type insertIDRetrofit struct {
	dir  string
	code string
}

var insertIDRetrofits = []insertIDRetrofit{
	{"mongo", mongoInsertIDCode},
	{"mysql", mysqlInsertIDCode},
	{"postgres", postgresInsertIDCode},
	{"mssql", mssqlInsertIDCode},
}

// ensureInsertIDHelpers appends InsertID (and its supporting unexported
// helpers) to whichever of appDir's mongo/mysql/postgres/mssql packages
// already exist and don't yet define it. An app not scaffolded with a
// given dialect at all simply has no such file — silently skipped, same
// as ensureAuthSubjectContext skipping -auth none. Append-only, like
// ensureResponseJSONRaw's JSONRaw insertion above: InsertID is wholly
// new code that doesn't touch or replace anything already in these
// files, so (unlike ensureAuthSubjectContext's middleware/auth.go full
// regen) there's no hand-edit-clobbering risk to guard against — nothing
// to sanity-check before appending.
func ensureInsertIDHelpers(appDir string) (bool, error) {
	changed := false
	for _, r := range insertIDRetrofits {
		path := filepath.Join(appDir, r.dir, r.dir+".go")
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return changed, err
		}
		content := string(raw)
		if strings.Contains(content, "func InsertID") {
			continue
		}
		content = strings.TrimRight(content, "\n") + "\n" + r.code
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

const mongoInsertIDCode = `
// InsertID inserts doc as a new document into coll, same as Insert, but
// also returns the document's _id. doc's _id field is located by its
// bson tag (a field tagged bson:"_id" anywhere in T, including inside an
// anonymously embedded struct — the shape a hand-defined "base" struct
// every model embeds would take, e.g. a Base struct holding just an ID
// bson.ObjectID field tagged bson:"_id,omitempty") and, if it's still
// its zero value, populated with a freshly generated bson.NewObjectID()
// before inserting — unlike a bare Insert, where the driver still
// assigns an _id server-side but the caller's own doc value never sees
// it, since Go passes doc by value. If T has no such field at all,
// InsertID behaves exactly like Insert and returns a zero ObjectID.
func InsertID[T any](ctx context.Context, coll Collection, doc T) (bson.ObjectID, T, error) {
	idField, ok := findObjectIDField(reflect.ValueOf(&doc).Elem())
	if !ok {
		err := Insert(ctx, coll, doc)
		return bson.ObjectID{}, doc, err
	}
	if idField.Interface().(bson.ObjectID).IsZero() {
		idField.Set(reflect.ValueOf(bson.NewObjectID()))
	}
	_, err := coll.coll.InsertOne(ctx, doc)
	return idField.Interface().(bson.ObjectID), doc, err
}

// findObjectIDField locates the bson.ObjectID-typed field mapped to "_id"
// within v (a struct), recursing into anonymous embedded struct fields so
// an embedded base/common struct's _id field is found the same way a
// direct field would be. Returns the field itself (settable, since v must
// be addressable) and whether one was found.
func findObjectIDField(v reflect.Value) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if f.Anonymous && fv.Kind() == reflect.Struct {
			if found, ok := findObjectIDField(fv); ok {
				return found, true
			}
			continue
		}
		if bsonFieldName(f) == "_id" && fv.Type() == reflect.TypeOf(bson.ObjectID{}) {
			return fv, true
		}
	}
	return reflect.Value{}, false
}
`

const mysqlInsertIDCode = `
// InsertID inserts doc as a new row into t, same as Insert, but also
// returns its primary key. The primary key is T's field mapped (by db
// tag, or its lowercased field name) to column "id" and holding a Go
// integer kind — the server-assigned auto-increment convention nexler's
// own generated schemas use (see cmd/nexler/initdb.go). When that
// field's current value is zero, it's omitted from the INSERT entirely
// (letting MySQL assign it) and populated from the driver's
// LastInsertId() afterward; a caller-supplied non-zero value is written
// and echoed back unchanged. If T has no such field, InsertID behaves
// exactly like Insert and returns 0.
func InsertID[T any](ctx context.Context, t Table, doc T) (int64, T, error) {
	v := reflect.ValueOf(&doc).Elem()
	pk, pkCol, ok := findIntPKField(v)
	if !ok {
		err := Insert(ctx, t, doc)
		return 0, doc, err
	}

	omitPK := pk.Int() == 0
	cols, placeholders, args, err := insertColumnsSkipping(doc, pkCol, omitPK)
	if err != nil {
		return 0, doc, err
	}
	query := "INSERT INTO " + t.name + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	res, err := t.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, doc, err
	}
	if !omitPK {
		return pk.Int(), doc, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, doc, err
	}
	pk.SetInt(id)
	return id, doc, nil
}

// insertColumnsSkipping is insertColumns, but omits skipCol entirely when
// omit is true — used by InsertID to leave an auto-increment primary key
// column out of the INSERT so MySQL assigns it.
func insertColumnsSkipping(doc any, skipCol string, omit bool) (cols, placeholders []string, args []any, err error) {
	v := reflect.ValueOf(doc)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, nil, nil, fmt.Errorf("mysql: doc must be a struct, got %s", v.Kind())
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col := dbFieldName(f)
		if col == "-" {
			continue
		}
		if omit && col == skipCol {
			continue
		}
		cols = append(cols, col)
		placeholders = append(placeholders, "?")
		args = append(args, v.Field(i).Interface())
	}
	return cols, placeholders, args, nil
}

// findIntPKField locates T's primary key field — the one mapped (by db
// tag, or its lowercased field name) to column "id" and holding a Go
// integer kind, the convention nexler's own generated schemas use for a
// server-assigned auto-increment primary key (see cmd/nexler/initdb.go).
// Returns the field itself (settable, since v must be addressable), its
// column name, and whether one was found.
func findIntPKField(v reflect.Value) (reflect.Value, string, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col := dbFieldName(f)
		if col != "id" {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return fv, col, true
		}
	}
	return reflect.Value{}, "", false
}
`

const postgresInsertIDCode = `
// InsertID inserts doc as a new row into t, same as Insert, but also
// returns its primary key. The primary key is T's field mapped (by db
// tag, or its lowercased field name) to column "id" and holding a Go
// integer kind — the server-assigned auto-increment (SERIAL) convention
// nexler's own generated schemas use (see cmd/nexler/initdb.go). When
// that field's current value is zero, it's omitted from the INSERT
// entirely (letting the SERIAL sequence assign it) and populated via a
// RETURNING clause afterward; a caller-supplied non-zero value is
// written and echoed back the same way (RETURNING confirms whatever
// value the row ended up with either way). If T has no such field,
// InsertID behaves exactly like Insert and returns 0.
func InsertID[T any](ctx context.Context, t Table, doc T) (int64, T, error) {
	v := reflect.ValueOf(&doc).Elem()
	pk, pkCol, ok := findIntPKField(v)
	if !ok {
		err := Insert(ctx, t, doc)
		return 0, doc, err
	}

	omitPK := pk.Int() == 0
	cols, placeholders, args, err := insertColumnsSkipping(doc, pkCol, omitPK)
	if err != nil {
		return 0, doc, err
	}
	query := "INSERT INTO " + t.name + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ") RETURNING " + pkCol
	var id int64
	if err := t.db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
		return 0, doc, err
	}
	pk.SetInt(id)
	return id, doc, nil
}

// insertColumnsSkipping is insertColumns, but omits skipCol entirely when
// omit is true — used by InsertID to leave an auto-increment (SERIAL)
// primary key column out of the INSERT so PostgreSQL assigns it.
func insertColumnsSkipping(doc any, skipCol string, omit bool) (cols, placeholders []string, args []any, err error) {
	v := reflect.ValueOf(doc)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, nil, nil, fmt.Errorf("postgres: doc must be a struct, got %s", v.Kind())
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col := dbFieldName(f)
		if col == "-" {
			continue
		}
		if omit && col == skipCol {
			continue
		}
		cols = append(cols, col)
		args = append(args, v.Field(i).Interface())
		placeholders = append(placeholders, "$"+strconv.Itoa(len(args)))
	}
	return cols, placeholders, args, nil
}

// findIntPKField locates T's primary key field — the one mapped (by db
// tag, or its lowercased field name) to column "id" and holding a Go
// integer kind, the convention nexler's own generated schemas use for a
// server-assigned auto-increment primary key (see cmd/nexler/initdb.go).
// Returns the field itself (settable, since v must be addressable), its
// column name, and whether one was found.
func findIntPKField(v reflect.Value) (reflect.Value, string, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col := dbFieldName(f)
		if col != "id" {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return fv, col, true
		}
	}
	return reflect.Value{}, "", false
}
`

const mssqlInsertIDCode = `
// InsertID inserts doc as a new row into t, same as Insert, but also
// returns its primary key. The primary key is T's field mapped (by db
// tag, or its lowercased field name) to column "id" and holding a Go
// integer kind — the server-assigned auto-increment (IDENTITY)
// convention nexler's own generated schemas use (see
// cmd/nexler/initdb.go). When that field's current value is zero, it's
// omitted from the INSERT entirely (letting the IDENTITY column assign
// it) and populated via an OUTPUT clause afterward (go-mssqldb doesn't
// support LastInsertId() reliably, so OUTPUT is the dialect-native
// mechanism here, same role RETURNING plays for postgres); a
// caller-supplied non-zero value is written and echoed back the same
// way. If T has no such field, InsertID behaves exactly like Insert and
// returns 0.
func InsertID[T any](ctx context.Context, t Table, doc T) (int64, T, error) {
	v := reflect.ValueOf(&doc).Elem()
	pk, pkCol, ok := findIntPKField(v)
	if !ok {
		err := Insert(ctx, t, doc)
		return 0, doc, err
	}

	omitPK := pk.Int() == 0
	cols, placeholders, args, err := insertColumnsSkipping(doc, pkCol, omitPK)
	if err != nil {
		return 0, doc, err
	}
	query := "INSERT INTO " + t.name + " (" + strings.Join(cols, ", ") + ") OUTPUT INSERTED." + pkCol + " VALUES (" + strings.Join(placeholders, ", ") + ")"
	var id int64
	if err := t.db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
		return 0, doc, err
	}
	pk.SetInt(id)
	return id, doc, nil
}

// insertColumnsSkipping is insertColumns, but omits skipCol entirely when
// omit is true — used by InsertID to leave an auto-increment (IDENTITY)
// primary key column out of the INSERT so SQL Server assigns it.
func insertColumnsSkipping(doc any, skipCol string, omit bool) (cols, placeholders []string, args []any, err error) {
	v := reflect.ValueOf(doc)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, nil, nil, fmt.Errorf("mssql: doc must be a struct, got %s", v.Kind())
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col := dbFieldName(f)
		if col == "-" {
			continue
		}
		if omit && col == skipCol {
			continue
		}
		cols = append(cols, col)
		args = append(args, v.Field(i).Interface())
		placeholders = append(placeholders, "@p"+strconv.Itoa(len(args)))
	}
	return cols, placeholders, args, nil
}

// findIntPKField locates T's primary key field — the one mapped (by db
// tag, or its lowercased field name) to column "id" and holding a Go
// integer kind, the convention nexler's own generated schemas use for a
// server-assigned auto-increment primary key (see cmd/nexler/initdb.go).
// Returns the field itself (settable, since v must be addressable), its
// column name, and whether one was found.
func findIntPKField(v reflect.Value) (reflect.Value, string, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col := dbFieldName(f)
		if col != "id" {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return fv, col, true
		}
	}
	return reflect.Value{}, "", false
}
`

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

	content, err = insertAggregatorCall(content, alias, "Register")
	if err != nil {
		return fmt.Errorf("%s: %w (has it been hand-edited?)", aggPath, err)
	}

	return os.WriteFile(aggPath, []byte(content), 0o644)
}

// wireAggregatorAdditionalCall is wireAggregator's tolerant sibling, for a
// second (third, ...) independent resource sharing an already-wired
// package (see RouteConfig.OwnRegister / addOwnRegisterResource). Unlike
// wireAggregator, it does NOT error when importPath is already imported —
// that's the expected case here, since the package's first resource
// already wired the import. It only adds the import if genuinely missing
// (e.g. this resource is -protected while the package's existing resource
// is public, so it belongs in the *other* aggregator file) via the same
// insertImport helper, then inserts "<alias>.<funcName>(mux)" before the
// // nexler:routes marker — idempotently: a no-op if that exact call line
// is already present, so re-running the same `nexler create` invocation
// never double-registers.
func wireAggregatorAdditionalCall(appDir, group, importPath, alias, funcName string) error {
	aggPath := filepath.Join(appDir, "routes", group, group+".go")

	raw, err := os.ReadFile(aggPath)
	if err != nil {
		return fmt.Errorf("reading %s (are you in a nexler app directory?): %w", aggPath, err)
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")

	if !strings.Contains(content, `"`+importPath+`"`) {
		content, err = insertImport(content, alias, importPath)
		if err != nil {
			return fmt.Errorf("%s: %w (has it been hand-edited?)", aggPath, err)
		}
	}

	callLine := fmt.Sprintf("\t%s.%s(mux)\n", alias, funcName)
	if strings.Contains(content, callLine) {
		return os.WriteFile(aggPath, []byte(content), 0o644)
	}

	content, err = insertAggregatorCall(content, alias, funcName)
	if err != nil {
		return fmt.Errorf("%s: %w (has it been hand-edited?)", aggPath, err)
	}

	return os.WriteFile(aggPath, []byte(content), 0o644)
}

// insertAggregatorCall inserts "<alias>.<funcName>(mux)" immediately before
// the // nexler:routes marker in content — the shared splice both
// wireAggregator and wireAggregatorAdditionalCall use, parameterized by
// funcName instead of a hardcoded "Register" so a second independent
// resource's differently-named Register<X> can be wired the same way.
func insertAggregatorCall(content, alias, funcName string) (string, error) {
	const registerAnchor = "\t// nexler:routes (do not remove this marker)"
	if !strings.Contains(content, registerAnchor) {
		return "", errors.New("could not find the registration anchor")
	}
	newRegister := fmt.Sprintf("\t%s.%s(mux)\n%s", alias, funcName, registerAnchor)
	return strings.Replace(content, registerAnchor, newRegister, 1), nil
}

// insertImport adds `alias "importPath"` to content's single import block,
// just before its closing paren. alias may be "" for a bare import (e.g.
// a stdlib package referenced by its own package name) — the added line
// omits the alias entirely rather than leaving a stray blank one.
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
	var line string
	if alias == "" {
		line = fmt.Sprintf("%s\t%q", prefix, importPath)
	} else {
		line = fmt.Sprintf("%s\t%s %q", prefix, alias, importPath)
	}

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
