// This file implements `nexler update [-dir <app-dir>]` — a discoverable
// command for bringing an existing generated app's nexler-owned files
// (ones users generally don't hand-edit) up to date with whatever the
// current nexler binary knows how to generate, without needing to
// coincidentally trigger it as a side effect of `nexler create <route>`.
//
// This is a thin registry around retrofit functions that already exist
// for other reasons — ensureOpenAPIUpToDate and ensureResponseJSONRaw
// (route.go) were both originally built to run silently inside NewRoute;
// ensureAuthSubjectContext (route.go) is the same idea for
// middleware.RequireAuth's context-attached subject (see "Authentication"
// in CLAUDE.md), ensureJWTClaims (route.go) brings auth/jwt.go's
// Claims up to RFC 7519 (sub/exp/iat, plus Name/Org/UserType/UserRole),
// ensureResponseHTMLUpgrade (route.go) extracts HTML's rendering into
// composeHTML and adds HTMLError/Unauthorised plus optional header/
// sidebar/footer partials and Subject/Path on the page template data,
// ensureMongoEmbeddedFilterFix (route.go) fixes structToBSON silently
// dropping an anonymously embedded filter field (e.g. store/common.Base's
// ID) instead of flattening it to _id, ensureStoreCommon (route.go) adds
// store/common.Base itself if missing, and ensureKgateResumeAll (kgate.go)
// patches kgate/kgate.go's Register to auto-resume recorded channels on
// startup. Update just runs these functions directly, unconditionally,
// and reports what happened. Deliberately never touches anything a
// developer is expected to hand-edit — handlers/services/store/models,
// main.go, .env, templates/html/*, kgate.go's handleEvent — only ever the
// same narrow set of files/regions those functions already know how to
// safely upgrade (full regeneration for pure generated infra like
// openapi.go, middleware/auth.go, and auth/jwt.go; append-only or
// surgical-anchor patching for files that are realistically hand-extended,
// like response.go, mongo.go, and kgate.go).
package scaffold

// UpdateResult reports which of Update's checks changed something vs.
// were already current.
type UpdateResult struct {
	Applied []string
	Current []string
}

// updateCheck is one named retrofit — apply reports whether it changed
// anything. Extend updateChecks whenever nexler adds another such
// retrofit function, the same way openAPIUpToDateMarkers documents
// extending its own narrower list.
type updateCheck struct {
	name  string
	apply func(appDir string) (bool, error)
}

var updateChecks = []updateCheck{
	{"openapi/openapi.go", ensureOpenAPIUpToDate},
	{"response/response.go (JSONRaw)", ensureResponseJSONRaw},
	{"response/response.go (HTML composition: partials, HTMLError/Unauthorised)", ensureResponseHTMLUpgrade},
	{"auth: subject in request context", ensureAuthSubjectContext},
	{"auth: RFC 7519 JWT claims (sub/exp/iat)", ensureJWTClaims},
	{"db: InsertID helpers", ensureInsertIDHelpers},
	{"db: mongo database name from DSN", ensureMongoDatabaseName},
	{"mongo: embedded-field filter fix", ensureMongoEmbeddedFilterFix},
	{"mongo: Update (partial patch)", ensureMongoUpdate},
	{"store: common.Base helper", ensureStoreCommon},
	{"config: SwaggerEnabled toggle", ensureSwaggerToggle},
	{"kgate: resume subscriptions on startup (Register)", ensureKgateResumeAll},
	{"kgate: webhook OpenAPI docs + startup test subscribe", ensureKgateOpenAPIAndTestSubscribe},
	{"auth: API-key (service-to-service) auth", ensureServiceAuth},
	{"auth: admin API for core_users/core_services", ensureAdminRoutes},
	{"config: API base path (openapi servers)", ensureAPIBasePath},
}

// Update runs every registered check against appDir in order, stopping at
// the first error (matching NewRoute's own fail-fast style rather than
// best-effort-continue). Unlike NewRoute's narrow gating — which only
// touches response.go when the route being created actually uses
// -response raw — Update runs every check unconditionally: its whole job
// is making every nexler-owned primitive current, the same "every app
// gets it, no flag gating" precedent JSONRaw's own generation-time
// behavior already set.
func Update(appDir string) (UpdateResult, error) {
	if appDir == "" {
		appDir = "."
	}

	if _, err := readModulePath(appDir); err != nil {
		return UpdateResult{}, err
	}

	var result UpdateResult
	for _, check := range updateChecks {
		changed, err := check.apply(appDir)
		if err != nil {
			return result, err
		}
		if changed {
			result.Applied = append(result.Applied, check.name)
		} else {
			result.Current = append(result.Current, check.name)
		}
	}
	return result, nil
}
