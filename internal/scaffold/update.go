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
// ID) instead of flattening it to _id, ensureMongoFilterExpression
// (route.go) backfills mongo.go's Filter{And, Or, Eq} composable filter
// expressions and EnsureUniqueIndex/EnsureTTLIndex for an app scaffolded
// before they existed, ensureStoreCommon (route.go) adds
// store/common.Base itself if missing, ensureKgateResumeAll (kgate.go)
// patches kgate/kgate.go's Register to auto-resume recorded channels on
// startup, ensureKgateSharedConnection (kgate.go) patches Subscribe/
// Unsubscribe/ResumeAll to multiplex every channel over a single shared
// WebSocket connection instead of dialing one per channel,
// ensureKgatePublishEncoding/ensureKgateResilientDelivery/
// ensureKgateWebhookDispatch (kgate.go) bring an app up to kgate's
// structured-logging/keepalive/dispatchEvent revision (see "nexler init
// kgate" in CLAUDE.md), ensureKgateServiceExtraction (kgate.go) moves
// handleEvent's real implementation and Register's startup-subscription
// decision into the one-time-written services/kgate package, and
// ensureKpassService (kpass.go) writes services/kpass if missing. Update
// runs these functions directly, unconditionally, best-effort (a failing
// check doesn't stop the rest — see Update's own doc comment), and
// reports what happened. Deliberately never touches anything a
// developer is expected to hand-edit — handlers/services/store/models,
// main.go, .env, templates/html/*, kgate.go's handleEvent,
// services/kgate/*, services/kpass/* — only ever the same narrow set of
// files/regions those functions already know how to safely upgrade (full
// regeneration for pure generated infra like openapi.go,
// middleware/auth.go, and auth/jwt.go; append-only or surgical-anchor
// patching for files that are realistically hand-extended, like
// response.go, mongo.go, and kgate.go).
package scaffold

// UpdateResult reports which of Update's checks changed something, were
// already current, or failed outright.
type UpdateResult struct {
	Applied []string
	Current []string
	Failed  []FailedCheck
}

// FailedCheck names one updateCheck that returned an error, and that
// error — e.g. an anchor-based check refusing to touch a file it can't
// positively identify as either its known-old or known-current shape.
type FailedCheck struct {
	Name string
	Err  error
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
	{"mongo: Filter{And, Or, Eq} + EnsureUniqueIndex/EnsureTTLIndex", ensureMongoFilterExpression},
	{"mongo: Update (partial patch)", ensureMongoUpdate},
	{"store: common.Base helper", ensureStoreCommon},
	{"config: SwaggerEnabled toggle", ensureSwaggerToggle},
	{"kgate: resume subscriptions on startup (Register)", ensureKgateResumeAll},
	{"kgate: webhook OpenAPI docs + startup test subscribe", ensureKgateOpenAPIAndTestSubscribe},
	{"kgate: single shared WebSocket connection (was one per channel)", ensureKgateSharedConnection},
	{"kgate: publish payload JSON-encoding + Origin header", ensureKgatePublishEncoding},
	{"kgate: resilient delivery (structured logging, keepalive, dispatchEvent)", ensureKgateResilientDelivery},
	{"kgate: webhook dispatch via dispatchEvent", ensureKgateWebhookDispatch},
	{"kgate: extract event handling into services/kgate", ensureKgateServiceExtraction},
	{"kpass: generate services/kpass wrapper if missing", ensureKpassService},
	{"auth: API-key (service-to-service) auth", ensureServiceAuth},
	{"auth: admin API for core_users/core_services", ensureAdminRoutes},
	{"config: API base path (openapi servers)", ensureAPIBasePath},
	{"task: Task Registry (task/task.go, middleware/task.go, core.SyncTasks)", ensureTaskRegistry},
	{"kpass: wire middleware.PermissionCheck to services/kpass.CheckAccess", ensureKpassPermissionHook},
}

// Update runs every registered check against appDir, best-effort: a check
// that returns an error (e.g. an anchor-based check refusing to touch a
// file it can't positively identify — see ensureMongoEmbeddedFilterFix)
// is recorded in result.Failed and the loop continues, rather than
// aborting the rest of the registry. This is deliberate, not
// NewRoute's own fail-fast style: the checks are independent (each
// anchors on its own known text and returns a plain error, never panics
// or shares mutable state), so one check being unable to recognize a
// hand-edited or hand-rewritten file must never block every other,
// unrelated check from applying. The one place a real ordering
// dependency exists — the kgate retrofit chain, where a later check
// anchors on an earlier one's own patched output — degrades safely on
// its own: if an earlier link didn't apply, the later one just can't
// find its anchor either and fails with its own clear error, it doesn't
// corrupt anything. Unlike NewRoute's narrow gating — which only touches
// response.go when the route being created actually uses -response raw —
// Update runs every check unconditionally: its whole job is making every
// nexler-owned primitive current, the same "every app gets it, no flag
// gating" precedent JSONRaw's own generation-time behavior already set.
//
// The returned error is non-nil only when Update couldn't even start
// (e.g. appDir isn't a valid app directory at all) — per-check failures
// never surface as a top-level error, only via result.Failed, since a
// partial-success result is the whole point of best-effort-continue.
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
			result.Failed = append(result.Failed, FailedCheck{Name: check.name, Err: err})
			continue
		}
		if changed {
			result.Applied = append(result.Applied, check.name)
		} else {
			result.Current = append(result.Current, check.name)
		}
	}
	return result, nil
}
