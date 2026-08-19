// This file implements ensureServiceAuth — the `nexler update` retrofit
// for API-key (service-to-service) auth: core/users.go, core/services.go,
// middleware/service_auth.go, and auth/context.go's
// ContextWithService/Service pair. See core/services.go.tmpl and
// middleware/service_auth.go.tmpl for what these actually do; this file
// only brings an app scaffolded before they existed up to date with them.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// serviceAuthContextAddition is the ContextWithService/Service block
// ensureServiceAuth appends to auth/context.go when missing — fixed,
// template-variable-free text (unlike context.go.tmpl's own
// {{if}}-gated version, no data is needed to render it, so a literal
// append is safe and exact).
const serviceAuthContextAddition = `
// serviceKeyType is deliberately a distinct type from subjectKeyType —
// a service-authenticated request (middleware.RequireServiceAuth) and a
// user-authenticated one (middleware.RequireAuth) must never be
// confusable with each other at the type level.
type serviceKeyType struct{}

var serviceKey = serviceKeyType{}

// ContextWithService returns a copy of ctx carrying name as the calling
// service's identity (its core_services Name). Called by
// middleware.RequireServiceAuth once a request's X-Api-Key passes
// core.VerifyServiceKey — application code normally reads it back via
// Service instead of calling this directly.
func ContextWithService(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, serviceKey, name)
}

// Service returns r's calling service's name, as attached by
// middleware.RequireServiceAuth. ok is false for a request that never
// passed through RequireServiceAuth.
func Service(r *http.Request) (name string, ok bool) {
	name, ok = r.Context().Value(serviceKey).(string)
	return name, ok
}
`

// ensureServiceAuth brings an app scaffolded before API-key
// (service-to-service) auth existed up to date: writes
// core/users.go/services.go and middleware/service_auth.go if missing,
// and appends ContextWithService/Service to auth/context.go if missing.
// A silent no-op for an app that doesn't have both a core database
// connection and a JWT-capable -auth choice (jwt|both) — same precedent
// as every other ensure* retrofit skipping a feature the app was never
// eligible for. Does not re-run `nexler init db` — provisioning
// core_users/core_services stays a separate, explicit, manual step, same
// precedent as core_kgate_channels.
func ensureServiceAuth(appDir string) (bool, error) {
	coreDBType, hasCoreDB := readCoreDBType(appDir)
	hasJWT, _ := detectAuthFiles(appDir)
	if !hasCoreDB || !hasJWT {
		return false, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}

	coreDBAccessor := "SQL"
	if coreDBType == "mongo" {
		coreDBAccessor = "Mongo"
	}
	coreData := struct {
		ModulePath             string
		CoreDB                 string
		CoreDBAccessor         string
		CoreUserGetQuery       string
		CoreServiceVerifyQuery string
		CoreServiceGetQuery    string
	}{
		ModulePath:     modulePath,
		CoreDB:         coreDBType,
		CoreDBAccessor: coreDBAccessor,
	}
	if coreDBAccessor != "Mongo" {
		coreData.CoreUserGetQuery = userGetQuerySQL(coreDBType)
		coreData.CoreServiceVerifyQuery = serviceVerifyQuerySQL(coreDBType)
		coreData.CoreServiceGetQuery = serviceGetQuerySQL(coreDBType)
	}

	changed := false

	usersPath := filepath.Join(appDir, "core", "users.go")
	if _, err := os.Stat(usersPath); os.IsNotExist(err) {
		if err := writeTemplateFile(templatesRoot+"/core/users.go.tmpl", usersPath, coreData); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	servicesPath := filepath.Join(appDir, "core", "services.go")
	if _, err := os.Stat(servicesPath); os.IsNotExist(err) {
		if err := writeTemplateFile(templatesRoot+"/core/services.go.tmpl", servicesPath, coreData); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	// An app already on the -merge-service-auth design (folded into
	// middleware/auth.go's own RequireAuth — see MergeServiceAuth in
	// mergeserviceauth.go) never gets middleware/service_auth.go written
	// back here: that file's whole capability already lives in auth.go, so
	// resurrecting it on every plain `nexler update` would silently
	// reintroduce dead/duplicate service-key-checking code into a merged
	// app. Detected the same way MergeServiceAuth itself detects merged
	// state — middleware/auth.go already checking X-Api-Secret.
	authGoPath := filepath.Join(appDir, "middleware", "auth.go")
	authGoRaw, err := os.ReadFile(authGoPath)
	if err != nil {
		return changed, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", authGoPath, appDir, err)
	}
	mergedServiceAuth := strings.Contains(string(authGoRaw), "X-Api-Secret")

	middlewarePath := filepath.Join(appDir, "middleware", "service_auth.go")
	if !mergedServiceAuth {
		if _, err := os.Stat(middlewarePath); os.IsNotExist(err) {
			mwData := struct{ ModulePath string }{ModulePath: modulePath}
			if err := writeTemplateFile(templatesRoot+"/middleware/service_auth.go.tmpl", middlewarePath, mwData); err != nil {
				return changed, err
			}
			changed = true
		} else if err != nil {
			return changed, err
		}
	}

	contextPath := filepath.Join(appDir, "auth", "context.go")
	raw, err := os.ReadFile(contextPath)
	if err != nil {
		return changed, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", contextPath, appDir, err)
	}
	if !strings.Contains(string(raw), "ContextWithService") {
		content := strings.TrimRight(string(raw), "\n") + "\n" + serviceAuthContextAddition
		if err := os.WriteFile(contextPath, []byte(content), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}

// ensureAdminRoutes brings an app scaffolded before the generated
// core_users/core_services admin API existed up to date: writes
// handlers/admin/users/users.go and handlers/admin/services/services.go
// if missing, and wires each into routes/protected/protected.go if not
// already imported. Same eligibility gate as ensureServiceAuth (a core
// database connection + a JWT-capable -auth choice) — these routes call
// core.CreateUser/ListServices/etc. directly, so neither makes sense
// without both. A silent no-op otherwise, same precedent as every other
// ensure* retrofit. Does not re-run `nexler init db`.
func ensureAdminRoutes(appDir string) (bool, error) {
	_, hasCoreDB := readCoreDBType(appDir)
	hasJWT, _ := detectAuthFiles(appDir)
	if !hasCoreDB || !hasJWT {
		return false, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	data := struct{ ModulePath string }{ModulePath: modulePath}

	changed := false

	usersPath := filepath.Join(appDir, "handlers", "admin", "users", "users.go")
	if _, err := os.Stat(usersPath); os.IsNotExist(err) {
		if err := writeTemplateFile(templatesRoot+"/handlers/admin/users/users.go.tmpl", usersPath, data); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	servicesPath := filepath.Join(appDir, "handlers", "admin", "services", "services.go")
	if _, err := os.Stat(servicesPath); os.IsNotExist(err) {
		if err := writeTemplateFile(templatesRoot+"/handlers/admin/services/services.go.tmpl", servicesPath, data); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	protectedPath := filepath.Join(appDir, "routes", "protected", "protected.go")
	protectedRaw, err := os.ReadFile(protectedPath)
	if err != nil {
		return changed, fmt.Errorf("reading %s: %w", protectedPath, err)
	}
	protectedContent := string(protectedRaw)

	// wireAggregator itself errors (rather than silently no-op'ing) if the
	// import is already present, since that's a programmer-error signal
	// for its usual caller (a fresh `nexler create <route>` invocation
	// wiring in a genuinely new package). A retrofit re-run is exactly the
	// case where "already wired" is the expected, common outcome, so it's
	// checked for here instead of treated as an error.
	usersImport := modulePath + "/handlers/admin/users"
	if !strings.Contains(protectedContent, `"`+usersImport+`"`) {
		if err := wireAggregator(appDir, "protected", usersImport, "adminusers"); err != nil {
			return changed, err
		}
		changed = true
		protectedRaw, err = os.ReadFile(protectedPath)
		if err != nil {
			return changed, fmt.Errorf("reading %s: %w", protectedPath, err)
		}
		protectedContent = string(protectedRaw)
	}

	servicesImport := modulePath + "/handlers/admin/services"
	if !strings.Contains(protectedContent, `"`+servicesImport+`"`) {
		if err := wireAggregator(appDir, "protected", servicesImport, "adminservices"); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}
