// This file implements `nexler init tenant [-dir <app-dir>]` — adding an
// independent, opt-in TenantOrg store + admin API (list/delete) to an
// *existing* generated app, for a hub (or similar) app to manage tenants.
//
// Deliberately NOT part of core/ (see tenant_templates/tenant.go.tmpl's
// own doc comment): whether an app has a multi-tenant model at all is a
// provider/business-specific decision, not something every -db app
// needs — unlike core_config/core_error_log, which are unconditional for
// every -db app. This is the same "separate, explicit opt-in command"
// shape as `nexler init kgate`/`nexler init kpass`, not a `create app`
// flag.
//
// Like `nexler init kgate`, this needs the target app to already have a
// core database connection (-db at `nexler create app` time) — there's
// nowhere for ListTenantOrgs/DeleteTenantOrg to read/write otherwise.
// Like the core_users/core_services admin API, the generated admin
// routes also need a JWT-capable -auth choice, since the delete endpoint
// has to have an authenticated caller to gate at all.
//
// Unlike `nexler init db`, this needs no live network connection at all:
// it's pure local file scaffolding, same as `nexler create <route>`.
// Provisioning tenant_orgs' schema (SQL only — Mongo needs none, see
// initdb.go) is still done by `nexler init db`, conditional on
// tenant/tenant.go existing on disk.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
)

// TenantConfig holds the parameters for `nexler init tenant`.
type TenantConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
}

// tenantData is what's available to tenant_templates/*.tmpl placeholders.
type tenantData struct {
	ModulePath     string
	CoreDB         string
	CoreDBAccessor string
}

// NewTenant scaffolds tenant/tenant.go and handlers/admin/tenants/tenants.go
// inside cfg.AppDir, and wires the latter into routes/protected/protected.go.
func NewTenant(cfg TenantConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return err
	}

	coreDBType, hasCoreDB := readCoreDBType(appDir)
	if !hasCoreDB {
		return fmt.Errorf("%s has no core database connection (no _DB_CORE_TYPE in .env) — tenant_orgs needs one: re-scaffold with -db, or run \"nexler create app\" with -db in the first place", filepath.Join(appDir, ".env"))
	}
	hasJWT, _ := detectAuthFiles(appDir)
	if !hasJWT {
		return fmt.Errorf("%s has no JWT-capable auth (auth/jwt.go) — the generated admin API needs an authenticated caller to gate: re-scaffold with -auth jwt or -auth both", filepath.Join(appDir, "auth"))
	}

	tenantFile := filepath.Join(appDir, "tenant", "tenant.go")
	if _, err := os.Stat(tenantFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", tenantFile)
	}
	handlersFile := filepath.Join(appDir, "handlers", "admin", "tenants", "tenants.go")
	if _, err := os.Stat(handlersFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", handlersFile)
	}

	coreDBAccessor := "SQL"
	if coreDBType == "mongo" {
		coreDBAccessor = "Mongo"
	}
	data := tenantData{ModulePath: modulePath, CoreDB: coreDBType, CoreDBAccessor: coreDBAccessor}

	if err := writeTenantFile(tenantFile, tenantTmpl, data); err != nil {
		return err
	}
	if err := writeTenantFile(handlersFile, tenantHandlersTmpl, data); err != nil {
		return err
	}

	tenantsImport := modulePath + "/handlers/admin/tenants"
	if err := wireAggregator(appDir, "protected", tenantsImport, "admintenants"); err != nil {
		return fmt.Errorf("generated %s and %s, but could not wire routes/protected/protected.go automatically: %w\nAdd manually in that file:\n  import admintenants %q\n  admintenants.Register(mux)",
			tenantFile, handlersFile, err, tenantsImport)
	}

	return nil
}

// writeTenantFile renders tmpl (from tenantTemplateFS) with data and
// writes it to destFile, creating any needed directories.
func writeTenantFile(destFile, tmpl string, data tenantData) error {
	raw, err := tenantTemplateFS.ReadFile(tmpl)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", tmpl, err)
	}
	content, err := processFile(tmpl, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", tmpl, err)
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(destFile), err)
	}
	return os.WriteFile(destFile, content, 0o644)
}
