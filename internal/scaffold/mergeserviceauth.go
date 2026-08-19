// This file implements MergeServiceAuth — the explicit, opt-in `nexler
// update -merge-service-auth` retrofit that converts an already-scaffolded
// app from a separate middleware.RequireServiceAuth to the folded-into-
// RequireAuth design `nexler create app -merge-service-auth` generates (see
// middleware/auth.go.tmpl and NewAppConfig.MergeServiceAuth). Deliberately
// NOT part of updateChecks (update.go): every check there converges every
// eligible app to one canonical current shape, but merged-vs-separate
// service auth is a real per-app design choice (a pure API service may
// deliberately want user-only and service-only routes to stay structurally
// distinct — see ctrl-svc's own still-current separate design) — so nothing
// flips it without being asked explicitly.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MergeServiceAuth converts appDir from a separate middleware.
// RequireServiceAuth to service-key auth folded into RequireAuth itself.
// Same eligibility as ensureServiceAuth (a core database connection AND a
// JWT-capable -auth choice) — a silent no-op otherwise. Also a no-op if
// appDir is already merged (middleware/auth.go already checks X-Api-Secret
// and there's no separate middleware/service_auth.go left to remove).
//
// Ensures core/users.go, core/services.go, and auth/context.go's
// ContextWithService/Service pair exist first (same as ensureServiceAuth —
// an app that predates API-key auth entirely needs these regardless of
// merged-vs-separate), then, if middleware/service_auth.go exists, verifies
// its content still matches exactly what the current (unmerged)
// service_auth.go.tmpl would render for this app before removing it —
// refusing instead if it's been hand-edited, same "has it been
// hand-rewritten?" precedent ensureAuthSubjectContext already uses for
// middleware/auth.go — and finally regenerates middleware/auth.go in
// merged form.
func MergeServiceAuth(appDir string) (bool, error) {
	coreDBType, hasCoreDB := readCoreDBType(appDir)
	hasJWT, hasSession := detectAuthFiles(appDir)
	if !hasCoreDB || !hasJWT {
		return false, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}

	authGoPath := filepath.Join(appDir, "middleware", "auth.go")
	authRaw, err := os.ReadFile(authGoPath)
	if err != nil {
		return false, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", authGoPath, appDir, err)
	}
	alreadyMerged := strings.Contains(string(authRaw), "X-Api-Secret")

	serviceAuthPath := filepath.Join(appDir, "middleware", "service_auth.go")
	hasServiceAuthFile := true
	if _, statErr := os.Stat(serviceAuthPath); os.IsNotExist(statErr) {
		hasServiceAuthFile = false
	} else if statErr != nil {
		return false, statErr
	}

	if alreadyMerged && !hasServiceAuthFile {
		return false, nil
	}

	changed := false

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
		Multitenant            bool
	}{
		ModulePath:     modulePath,
		CoreDB:         coreDBType,
		CoreDBAccessor: coreDBAccessor,
	}
	if coreDBAccessor != "Mongo" {
		coreData.CoreUserGetQuery = userGetQuerySQL(coreDBType, false)
		coreData.CoreServiceVerifyQuery = serviceVerifyQuerySQL(coreDBType)
		coreData.CoreServiceGetQuery = serviceGetQuerySQL(coreDBType)
	}

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

	contextPath := filepath.Join(appDir, "auth", "context.go")
	contextRaw, err := os.ReadFile(contextPath)
	if err != nil {
		return changed, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", contextPath, appDir, err)
	}
	if !strings.Contains(string(contextRaw), "ContextWithService") {
		content := strings.TrimRight(string(contextRaw), "\n") + "\n" + serviceAuthContextAddition
		if err := os.WriteFile(contextPath, []byte(content), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	if hasServiceAuthFile {
		mwData := struct{ ModulePath string }{ModulePath: modulePath}
		expected, err := renderTemplateFile(templatesRoot+"/middleware/service_auth.go.tmpl", mwData)
		if err != nil {
			return changed, err
		}
		actual, err := os.ReadFile(serviceAuthPath)
		if err != nil {
			return changed, err
		}
		if string(actual) != string(expected) {
			return changed, fmt.Errorf("%s doesn't match what nexler would generate — has it been hand-rewritten? Remove or reconcile it by hand before re-running with -merge-service-auth", serviceAuthPath)
		}
		if err := os.Remove(serviceAuthPath); err != nil {
			return changed, err
		}
		changed = true
	}

	authKind := "session"
	switch {
	case hasJWT && hasSession:
		authKind = "both"
	case hasJWT:
		authKind = "jwt"
	}
	authData := struct {
		AuthKind, ModulePath string
		MergeServiceAuth     bool
	}{AuthKind: authKind, ModulePath: modulePath, MergeServiceAuth: true}
	rendered, err := renderTemplateFile(templatesRoot+"/middleware/auth.go.tmpl", authData)
	if err != nil {
		return changed, err
	}
	if string(rendered) != string(authRaw) {
		if err := os.WriteFile(authGoPath, rendered, 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}

// renderTemplateFile renders the embedded template at tmplPath with data,
// returning the bytes without writing them anywhere — the read-only
// counterpart to writeTemplateFile, for callers that need to compare
// against or reuse rendered output rather than write it directly.
func renderTemplateFile(tmplPath string, data any) ([]byte, error) {
	raw, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	rendered, err := render(tmplPath, raw, data)
	if err != nil {
		return nil, fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	return rendered, nil
}
