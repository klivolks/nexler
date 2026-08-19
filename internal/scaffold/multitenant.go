// This file implements Multitenant — the explicit, opt-in `nexler update
// -multitenant` retrofit that threads a tenant Org through an already-
// scaffolded -auth jwt|session|both app's auth/context.go, auth/session.go,
// middleware/auth.go, and core/users.go, matching what `nexler create app
// -multitenant` generates (see NewAppConfig.Multitenant and the templates it
// gates). Mirrors MergeServiceAuth's shape in mergeserviceauth.go.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// multitenantContextAddition is the ContextWithOrg/Org block Multitenant
// appends to auth/context.go when missing — fixed, template-variable-free
// text (unlike context.go.tmpl's own {{if}}-gated version, no data is
// needed to render it, so a literal append is safe and exact).
const multitenantContextAddition = `
// orgKeyType is deliberately distinct from subjectKeyType — org is a
// separate piece of context, not a replacement for subject.
type orgKeyType struct{}

var orgKey = orgKeyType{}

// ContextWithOrg returns a copy of ctx carrying org as the authenticated
// subject's tenant organization. Called by middleware.RequireAuth
// alongside ContextWithSubject — application code normally reads it back
// via Org instead of calling this directly.
func ContextWithOrg(ctx context.Context, org string) context.Context {
	return context.WithValue(ctx, orgKey, org)
}

// Org returns r's authenticated subject's tenant organization, as
// attached by middleware.RequireAuth. ok is false for a request that
// never passed through RequireAuth, or one authenticated via a service
// key — a service isn't tied to a tenant org, so RequireAuth never
// attaches one for that credential path.
func Org(r *http.Request) (org string, ok bool) {
	org, ok = r.Context().Value(orgKey).(string)
	return org, ok
}
`

// Multitenant brings appDir's -auth jwt|session|both app up to date with
// -multitenant's Org-propagation design: auth/context.go (ContextWithOrg/
// Org), auth/session.go (StartSession/SessionFromRequest's org parameter,
// only if the app has sessions), middleware/auth.go (attaching Org after a
// successful JWT or session check), and core/users.go (OrgId, only if the
// file already exists — an app without both a core DB connection and a
// JWT-capable -auth choice never has it). A silent no-op if the app was
// scaffolded with -auth none (or before -auth existed): there's no
// authenticated subject to attach an Org to. Never runs `nexler init db` —
// provisioning core_users' org_id column stays a separate, explicit,
// manual step, same precedent as every other core table.
func Multitenant(appDir string) (bool, error) {
	hasJWT, hasSession := detectAuthFiles(appDir)
	if !hasJWT && !hasSession {
		return false, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}

	changed := false

	// 1. auth/context.go: append ContextWithOrg/Org if missing.
	contextPath := filepath.Join(appDir, "auth", "context.go")
	contextRaw, err := os.ReadFile(contextPath)
	if err != nil {
		return changed, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", contextPath, appDir, err)
	}
	if !strings.Contains(string(contextRaw), "ContextWithOrg") {
		content := strings.TrimRight(string(contextRaw), "\n") + "\n" + multitenantContextAddition
		if err := os.WriteFile(contextPath, []byte(content), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	// 2. auth/session.go (only if the app has sessions): upgrade
	// StartSession/SessionFromRequest to carry org, preserving whichever
	// RememberMe shape this app was originally scaffolded with.
	if hasSession {
		sessionPath := filepath.Join(appDir, "auth", "session.go")
		sessionRaw, err := os.ReadFile(sessionPath)
		if err != nil {
			return changed, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", sessionPath, appDir, err)
		}
		envPrefix, err := recoverEnvPrefix(appDir)
		if err != nil {
			return changed, err
		}
		// A session.go already on the multitenant shape (SessionFromRequest
		// returning org) is left alone entirely — comparing it against a
		// freshly-rendered pre-multitenant version would always mismatch
		// and be misread as "hand-rewritten," so this must be checked
		// before the byte-compare below, same as auth/context.go's
		// ContextWithOrg check above.
		if !strings.Contains(string(sessionRaw), "subject, org string, ok bool") {
			rememberMe := strings.Contains(string(sessionRaw), "rememberMeTTL")
			sessionData := struct {
				AppName, EnvPrefix string
				RememberMe         bool
				Multitenant        bool
			}{AppName: filepath.Base(modulePath), EnvPrefix: envPrefix, RememberMe: rememberMe}
			expected, err := renderTemplateFile(templatesRoot+"/auth/session.go.tmpl", sessionData)
			if err != nil {
				return changed, err
			}
			if string(expected) != string(sessionRaw) {
				return changed, fmt.Errorf("%s doesn't match what nexler would generate — has it been hand-rewritten? Reconcile it by hand before re-running with -multitenant", sessionPath)
			}
			sessionData.Multitenant = true
			rendered, err := renderTemplateFile(templatesRoot+"/auth/session.go.tmpl", sessionData)
			if err != nil {
				return changed, err
			}
			if err := os.WriteFile(sessionPath, rendered, 0o644); err != nil {
				return changed, err
			}
			changed = true
		}
	}

	// 3. middleware/auth.go: attach Org after a successful JWT/session
	// check, preserving whichever auth kind and -merge-service-auth shape
	// this app was originally scaffolded with.
	authGoPath := filepath.Join(appDir, "middleware", "auth.go")
	authRaw, err := os.ReadFile(authGoPath)
	if err != nil {
		return changed, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", authGoPath, appDir, err)
	}
	mergeServiceAuth := strings.Contains(string(authRaw), "X-Api-Secret")
	authKind := "session"
	switch {
	case hasJWT && hasSession:
		authKind = "both"
	case hasJWT:
		authKind = "jwt"
	}
	// middleware/auth.go already on the multitenant shape is left alone —
	// see the same reasoning on session.go above.
	if !strings.Contains(string(authRaw), "ContextWithOrg") {
		authData := struct {
			AuthKind, ModulePath string
			MergeServiceAuth     bool
			Multitenant          bool
		}{AuthKind: authKind, ModulePath: modulePath, MergeServiceAuth: mergeServiceAuth}
		expectedAuth, err := renderTemplateFile(templatesRoot+"/middleware/auth.go.tmpl", authData)
		if err != nil {
			return changed, err
		}
		if string(expectedAuth) != string(authRaw) {
			return changed, fmt.Errorf("%s doesn't match what nexler would generate — has it been hand-rewritten? Reconcile it by hand before re-running with -multitenant", authGoPath)
		}
		authData.Multitenant = true
		renderedAuth, err := renderTemplateFile(templatesRoot+"/middleware/auth.go.tmpl", authData)
		if err != nil {
			return changed, err
		}
		if err := os.WriteFile(authGoPath, renderedAuth, 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	// 4. core/users.go (only if it already exists — an app without both a
	// core DB connection and a JWT-capable -auth choice never has it).
	usersPath := filepath.Join(appDir, "core", "users.go")
	usersRaw, err := os.ReadFile(usersPath)
	if err != nil {
		if os.IsNotExist(err) {
			return changed, nil
		}
		return changed, err
	}
	coreDBType, hasCoreDB := readCoreDBType(appDir)
	if !hasCoreDB {
		return changed, fmt.Errorf("%s exists but %s's .env has no core database type — is this a nexler app directory?", usersPath, appDir)
	}
	coreDBAccessor := "SQL"
	if coreDBType == "mongo" {
		coreDBAccessor = "Mongo"
	}
	// core/users.go already carrying OrgId is left alone — same reasoning
	// as session.go/middleware/auth.go above.
	if !strings.Contains(string(usersRaw), "OrgId") {
		coreData := struct {
			ModulePath       string
			CoreDB           string
			CoreDBAccessor   string
			CoreUserGetQuery string
			Multitenant      bool
		}{
			ModulePath:     modulePath,
			CoreDB:         coreDBType,
			CoreDBAccessor: coreDBAccessor,
		}
		if coreDBAccessor != "Mongo" {
			coreData.CoreUserGetQuery = userGetQuerySQL(coreDBType, false)
		}
		expectedUsers, err := renderTemplateFile(templatesRoot+"/core/users.go.tmpl", coreData)
		if err != nil {
			return changed, err
		}
		if string(expectedUsers) != string(usersRaw) {
			return changed, fmt.Errorf("%s doesn't match what nexler would generate — has it been hand-rewritten? Reconcile it by hand before re-running with -multitenant", usersPath)
		}
		coreData.Multitenant = true
		if coreDBAccessor != "Mongo" {
			coreData.CoreUserGetQuery = userGetQuerySQL(coreDBType, true)
		}
		renderedUsers, err := renderTemplateFile(templatesRoot+"/core/users.go.tmpl", coreData)
		if err != nil {
			return changed, err
		}
		if err := os.WriteFile(usersPath, renderedUsers, 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}
