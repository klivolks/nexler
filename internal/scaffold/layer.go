// This file implements `nexler create store <name>` and `nexler create
// service <name>` — scaffolding a standalone service or store package
// inside an *existing* generated app, independent of any route. Unlike
// `nexler create <route>` (which always ties a service/store to a
// specific handler+model, and wires the route into an aggregator),
// these commands produce a plain, unwired package: nothing imports it,
// nothing calls it, and nexler doesn't track any reference to it
// afterward — a handler, another service, or hand-written code picks it
// up later by importing it directly, exactly like any other Go package.
// This is deliberate: a service/store useful to more than one route (or
// not yet tied to any route at all) shouldn't have to masquerade as
// belonging to one just to get scaffolded.
package scaffold

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LayerConfig holds the parameters for `nexler create store|service <name>`.
type LayerConfig struct {
	// Kind is "service" or "store".
	Kind string
	// Name addresses the package the same way -service/-store's reuse
	// references already do: "module[/submodule]", e.g. "purchase" or
	// "purchase/verify". Required.
	Name string
	// FileName optionally overrides the generated file's base name.
	// Defaults to "<pkgName><kind>" (e.g. "appsservice"/"appsstore" for
	// Name "apps") when empty — self-descriptive since, unlike a route's
	// handler/service/store/model (which live in four differently-named
	// directories and are never confused for one another), a standalone
	// layer's file is more likely to be the only one directly under its
	// directory.
	FileName string
	// StoreRef, only meaningful when Kind == "service", points the new
	// service's TODO comment at an already-scaffolded store package
	// instead of leaving it unlinked — a "module[/submodule]" reference,
	// same as -store on `nexler create <route>`. The referenced package
	// must already exist. Empty (default) leaves the service unlinked to
	// any store — see Standalone's effect on service.go.tmpl.
	StoreRef string
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
}

// LayerResult reports what NewLayer wrote.
type LayerResult struct {
	// Path is the generated file's path, relative to AppDir's parent
	// (i.e. as passed to os.WriteFile).
	Path string
}

// NewLayer scaffolds a standalone service or store package inside
// cfg.AppDir — no handler, no model, no route, no aggregator wiring.
// Reuses the exact same rendering path (writeRouteLayerFile) and
// route_templates/service.go.tmpl / store.go.tmpl `nexler create <route>`
// itself uses, with routeData.Standalone set so the generated TODO
// comments point at `nexler create store|service` instead of a route.
func NewLayer(cfg LayerConfig) (LayerResult, error) {
	if cfg.Kind != "service" && cfg.Kind != "store" {
		return LayerResult{}, fmt.Errorf("unsupported layer kind %q (must be \"service\" or \"store\")", cfg.Kind)
	}
	if cfg.Name == "" {
		return LayerResult{}, fmt.Errorf("a name is required, as module[/submodule], e.g. purchase or purchase/verify")
	}

	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return LayerResult{}, err
	}

	var parts []string
	for _, p := range strings.SplitN(cfg.Name, "/", 2) {
		sp := sanitizeIdent(p)
		if sp == "" {
			return LayerResult{}, fmt.Errorf("%q is invalid — module/submodule must contain at least one letter or digit", cfg.Name)
		}
		parts = append(parts, sp)
	}
	relDir := filepath.Join(parts...)
	relDirSlash := filepath.ToSlash(relDir)
	pkgName := parts[len(parts)-1]
	name := exportedName(pkgName)
	alias := strings.Join(parts, "")
	module := parts[0]

	fileName := pkgName + cfg.Kind
	if cfg.FileName != "" {
		fileName = sanitizeIdent(cfg.FileName)
		if fileName == "" {
			return LayerResult{}, fmt.Errorf("-file must contain at least one letter or digit")
		}
	}

	data := routeData{
		PkgName:     pkgName,
		Name:        name,
		RouteLabel:  "this package",
		ModulePath:  modulePath,
		RelDirSlash: relDirSlash,
		Alias:       alias,
		Module:      module,
		Standalone:  true,
	}

	layer, tmplPath := "services", routeServiceTmpl
	if cfg.Kind == "store" {
		layer, tmplPath = "store", routeStoreTmpl
		coreDBType, hasCoreDB := readCoreDBType(appDir)
		coreDBAccessor := "SQL"
		if coreDBType == "mongo" {
			coreDBAccessor = "Mongo"
		}
		data.HasCoreDB = hasCoreDB
		data.CoreDBAccessor = coreDBAccessor
		data.CoreDBType = coreDBType
	} else {
		storeImportPath, storePkgName, hasStoreRef, _, err := resolveLayerRef(appDir, modulePath, "store", "store", cfg.StoreRef)
		if err != nil {
			return LayerResult{}, err
		}
		data.HasStoreRef = hasStoreRef
		data.StoreImportPath = storeImportPath
		data.StorePkgName = storePkgName
		// A standalone service has no route-scoped store generated
		// alongside it the way a route's own service does by default —
		// HasStore is true only when explicitly linked via -store.
		data.HasStore = hasStoreRef
	}

	path, err := writeRouteLayerFile(appDir, layer, tmplPath, routeTemplateFS, relDir, fileName, data)
	if err != nil {
		return LayerResult{}, err
	}

	return LayerResult{Path: path}, nil
}
