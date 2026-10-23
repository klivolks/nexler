// This file implements ensureServiceCallerContext — the `nexler update`
// retrofit that lets a service caller (X-Api-Secret) say who it is acting
// for (X-Acting-Subject, written to the audit log) and, when its
// core_services record allows it, which tenant org the call is for
// (X-Org-Id). A workflow engine running tasks in other services needs both.
// Mirrors what create app now generates: auth.ContextWithActingSubject/
// ActingSubject, core.ServiceActsForOrgs/SetServiceActsForOrgs, the
// acts_for_orgs field, middleware/service_caller.go, its call in
// RequireAuth/RequireServiceAuth, and acting_subject in the audit entry.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const actingSubjectContextAddition = `
// actingSubjectKeyType keeps the acting user distinct from the
// authenticated subject: a service authenticates, the acting subject is
// only who it says it is working for.
type actingSubjectKeyType struct{}

var actingSubjectKey = actingSubjectKeyType{}

// ContextWithActingSubject returns a copy of ctx carrying subject as the
// user a service caller is acting for (its X-Acting-Subject header).
func ContextWithActingSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, actingSubjectKey, subject)
}

// ActingSubject returns the user a service caller said it is acting for.
// It is informational (written to the audit log), never an authenticated
// identity: permission checks still run against the service itself.
func ActingSubject(r *http.Request) (subject string, ok bool) {
	subject, ok = r.Context().Value(actingSubjectKey).(string)
	return subject, ok && subject != ""
}
`

const serviceActsForOrgsMongo = `
// ServiceActsForOrgs reports whether the service with this core_services ID
// may name the tenant org it calls for (X-Org-Id) — e.g. a workflow engine
// running a task for a tenant. False for an unknown or inactive service.
func ServiceActsForOrgs(ctx context.Context, id string) (bool, error) {
	coll, ok := serviceCollection()
	if !ok {
		return false, fmt.Errorf("core: no \"core\" database connection (has db.Connect() run yet?)")
	}
	objID, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return false, nil
	}
	doc, err := mongo.GetOne[serviceDoc](ctx, coll, serviceDoc{Base: common.Base{ID: objID}})
	if err != nil {
		return false, nil
	}
	return doc.Status == "active" && doc.ActsForOrgs, nil
}

// SetServiceActsForOrgs allows or stops service name naming the tenant org
// it calls for (see ServiceActsForOrgs).
func SetServiceActsForOrgs(ctx context.Context, name string, acts bool) error {
	coll, ok := serviceCollection()
	if !ok {
		return fmt.Errorf("core: no \"core\" database connection (has db.Connect() run yet?)")
	}
	doc, err := mongo.GetOne[serviceDoc](ctx, coll, serviceDoc{Name: name})
	if err != nil {
		return fmt.Errorf("core: service %q not found", name)
	}
	doc.ActsForOrgs = acts
	doc.UpdatedAt = time.Now().UTC()
	return mongo.Set(ctx, coll, serviceDoc{Name: name}, doc)
}
`

const serviceActsForOrgsSQL = `
// ServiceActsForOrgs reports whether a service may name the tenant org it
// calls for (X-Org-Id). Always false on a SQL core: core_services has no
// column for it yet.
func ServiceActsForOrgs(ctx context.Context, id string) (bool, error) {
	return false, nil
}

// SetServiceActsForOrgs is not supported on a SQL core yet (see
// ServiceActsForOrgs).
func SetServiceActsForOrgs(ctx context.Context, name string, acts bool) error {
	return fmt.Errorf("core: acts-for-orgs is not supported on a SQL core yet")
}
`

// Call-site anchors: the generated text before this retrofit, and after.
const (
	mergedServiceCallOld   = "\t\t\t\tctx = auth.ContextWithSubject(ctx, id)\n\t\t\t\tnext(w, r.WithContext(ctx))\n"
	mergedServiceCallNew   = "\t\t\t\tctx = auth.ContextWithSubject(ctx, id)\n\t\t\t\tif ctx, ok = serviceCallerContext(ctx, w, r, id); ok {\n\t\t\t\t\tnext(w, r.WithContext(ctx))\n\t\t\t\t}\n"
	separateServiceCallOld = "\t\tnext(w, r.WithContext(auth.ContextWithService(r.Context(), id)))\n"
	separateServiceCallNew = "\t\tif ctx, ok := serviceCallerContext(auth.ContextWithService(r.Context(), id), w, r, id); ok {\n\t\t\tnext(w, r.WithContext(ctx))\n\t\t}\n"
	auditDetailsOld        = "\t\t\t_ = core.LogAudit(r.Context(), subject, actorType, action, pathValues, map[string]any{\n\t\t\t\t\"correlation_id\": correlationID,\n\t\t\t\t\"session_id\":     r.Header.Get(\"X-Session-Id\"),\n\t\t\t})\n"
	auditDetailsNew        = "\t\t\tdetails := map[string]any{\n\t\t\t\t\"correlation_id\": correlationID,\n\t\t\t\t\"session_id\":     r.Header.Get(\"X-Session-Id\"),\n\t\t\t}\n\t\t\tif acting, ok := auth.ActingSubject(r); ok {\n\t\t\t\tdetails[\"acting_subject\"] = acting\n\t\t\t}\n\t\t\t_ = core.LogAudit(r.Context(), subject, actorType, action, pathValues, details)\n"
)

var serviceDocLastUsedRe = regexp.MustCompile("(?m)^(\\s*LastUsedAt\\s+\\*time\\.Time\\s+`bson:\"last_used_at,omitempty\"`\\n)\\}")

// ensureServiceCallerContext applies the retrofit described in this file's
// doc comment. A silent no-op for an app without service-key auth (no
// core/services.go). Each piece is skipped when already present; a call
// site that doesn't match the generated text is an error naming the file.
func ensureServiceCallerContext(appDir string) (bool, error) {
	servicesPath := filepath.Join(appDir, "core", "services.go")
	servicesRaw, err := os.ReadFile(servicesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	contextPath := filepath.Join(appDir, "auth", "context.go")
	contextRaw, err := os.ReadFile(contextPath)
	if err != nil {
		return false, err
	}
	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	changed := false

	// auth/context.go
	ctxContent := string(contextRaw)
	if !strings.Contains(ctxContent, "func ContextWithActingSubject(") {
		if !strings.Contains(ctxContent, "func ContextWithService(") {
			return changed, fmt.Errorf("%s has no ContextWithService — run nexler update's service-auth check first", contextPath)
		}
		ctxContent = strings.TrimRight(ctxContent, "\n") + "\n" + actingSubjectContextAddition
		if err := writeGoFile(contextPath, ctxContent); err != nil {
			return changed, err
		}
		changed = true
	}

	// core/services.go
	svc := string(servicesRaw)
	svcChanged := false
	isMongo := strings.Contains(svc, "type serviceDoc struct")
	if isMongo && !strings.Contains(svc, "acts_for_orgs") {
		loc := serviceDocLastUsedRe.FindStringSubmatchIndex(svc)
		if loc == nil {
			return changed, fmt.Errorf("%s: serviceDoc doesn't end with its generated LastUsedAt field — add `ActsForOrgs bool \x60bson:\"acts_for_orgs,omitempty\"\x60` to serviceDoc by hand", servicesPath)
		}
		insertAt := loc[3]
		svc = svc[:insertAt] + "\tActsForOrgs bool `bson:\"acts_for_orgs,omitempty\"`\n" + svc[insertAt:]
		svcChanged = true
	}
	if !strings.Contains(svc, "func ServiceActsForOrgs(") {
		add := serviceActsForOrgsSQL
		if isMongo {
			add = serviceActsForOrgsMongo
		}
		svc = strings.TrimRight(svc, "\n") + "\n" + add
		svcChanged = true
	}
	if svcChanged {
		if err := writeGoFile(servicesPath, svc); err != nil {
			return changed, err
		}
		changed = true
	}

	// middleware/service_caller.go
	wrote, err := writeServiceCaller(appDir, modulePath, strings.Contains(ctxContent, "func ContextWithOrg("), false)
	if err != nil {
		return changed, err
	}
	changed = changed || wrote

	// Call sites in RequireAuth (merged service auth) and RequireServiceAuth.
	for _, site := range []struct{ file, old, new string }{
		{filepath.Join(appDir, "middleware", "auth.go"), mergedServiceCallOld, mergedServiceCallNew},
		{filepath.Join(appDir, "middleware", "service_auth.go"), separateServiceCallOld, separateServiceCallNew},
	} {
		raw, err := os.ReadFile(site.file)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return changed, err
		}
		content := string(raw)
		if !strings.Contains(content, "core.VerifyServiceKey") || strings.Contains(content, "serviceCallerContext(") {
			continue
		}
		if !strings.Contains(content, site.old) {
			return changed, fmt.Errorf("%s: the service-key branch doesn't match the generated text — has it been hand-edited? Pass the request context through serviceCallerContext (middleware/service_caller.go) before calling next, by hand", site.file)
		}
		if err := os.WriteFile(site.file, []byte(strings.ReplaceAll(content, site.old, site.new)), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	// acting_subject in the audit entry.
	taskPath := filepath.Join(appDir, "middleware", "task.go")
	taskRaw, err := os.ReadFile(taskPath)
	if err == nil && !strings.Contains(string(taskRaw), "acting_subject") && strings.Contains(string(taskRaw), "func auditWrap(") {
		content := string(taskRaw)
		if !strings.Contains(content, auditDetailsOld) {
			return changed, fmt.Errorf("%s: auditWrap's core.LogAudit call doesn't match the generated text — add details[\"acting_subject\"] from auth.ActingSubject(r) by hand", taskPath)
		}
		if err := os.WriteFile(taskPath, []byte(strings.Replace(content, auditDetailsOld, auditDetailsNew, 1)), 0o644); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil && !os.IsNotExist(err) {
		return changed, err
	}
	return changed, nil
}

// writeServiceCaller writes middleware/service_caller.go from its template
// when missing, or — with upgradeToMultitenant — when the existing copy
// predates X-Org-Id handling (an app turned multitenant later). It never
// touches a copy that already handles X-Org-Id.
func writeServiceCaller(appDir, modulePath string, multitenant, upgradeToMultitenant bool) (bool, error) {
	p := filepath.Join(appDir, "middleware", "service_caller.go")
	raw, err := os.ReadFile(p)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return false, err
	case upgradeToMultitenant && !strings.Contains(string(raw), "X-Org-Id"):
	default:
		return false, nil
	}
	data := struct {
		ModulePath  string
		Multitenant bool
	}{modulePath, multitenant}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, writeTemplateFile(templatesRoot+"/middleware/service_caller.go.tmpl", p, data)
}

// normalizeServiceCallSites rewrites the pre-serviceCallerContext service-key
// call sites into their current form, so a retrofit comparing a file against
// the current template doesn't mistake an untouched older file for a
// hand-edited one.
func normalizeServiceCallSites(content string) string {
	content = strings.ReplaceAll(content, mergedServiceCallOld, mergedServiceCallNew)
	return strings.ReplaceAll(content, separateServiceCallOld, separateServiceCallNew)
}

// contextRawForCaller reads auth/context.go, or returns nil when it can't.
func contextRawForCaller(appDir string) []byte {
	raw, _ := os.ReadFile(filepath.Join(appDir, "auth", "context.go"))
	return raw
}

// coreDBAccessorFor maps a core DB type to the CoreDBAccessor value the
// templates branch on.
func coreDBAccessorFor(coreDBType string) string {
	if coreDBType == "mongo" {
		return "Mongo"
	}
	return "SQL"
}
