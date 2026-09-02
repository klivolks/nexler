# Nexler Core Checklist

Tracks reusable patterns discovered in nexler's own live scaffolded apps —
`comms` (UI-based) and `ctrl-svc` (API-based), both under `D:\Services\` — that
have since diverged from what `nexler create app`/`nexler create <route>`
actually generate. Mirrors the shape of those apps' own `comms-checklist.md`/
`ctrl-svc-checklist.md`, but from nexler's side: this file tracks nexler's own
templates against what its downstream apps have proven out in practice.

Not every divergence belongs here — a lot of what changed in comms/ctrl-svc is
their own business logic (jobs, providers, licensing, ...) with nothing to port
back. This file is only for changes to *nexler-owned, generated infrastructure*
(middleware, `response`/`request`, `mongo`/`mysql`/etc., `core`, ...) that are
either a genuine bug fix or a pattern independently reinvented in more than one
app — a signal it belongs in the scaffold itself rather than being re-solved
per app.

Starting with `0.5.7`, new sections here are headed by nexler's own release
version (the real version record is its git tags — latest released is
`v0.5.6-beta` — and `docs/index.html`'s dated changelog), not a date. Earlier
date-headed sections below are left exactly as originally written.

## Starting state (2026-08-19)

Two ports were already in flight as uncommitted edits in this repo when this
checklist was written:

- `internal/scaffold/templates/middleware/service_auth.go.tmpl` — the
  `X-Api-Key` → `X-Api-Secret` header rename (matching what both comms and
  ctrl-svc now send). Superseded by item 1 below: the merged-auth branch of
  `middleware/auth.go.tmpl` carries this same rename forward, and
  `service_auth.go.tmpl` itself is now conditionally skipped entirely when
  `-merge-service-auth` is used — but the plain (unmerged) `-auth jwt|both`
  path still goes through this file, so the rename here stays live and
  correct for that path.
- `internal/scaffold/templates/response/response.go.tmpl` — the
  `resolveHTMLFile` hierarchy-walk (a module's HTML content resolves by
  walking up its own path segments before falling back to `shared/`, not just
  a flat module-or-shared check). This is the first half of item 3 below —
  still incomplete (partials, `HTMLError`/`Unauthorised`, `Subject`/`Path` on
  the page template data aren't ported yet).

## Ready to port

1. `[x]` **Merge service-key auth into `RequireAuth`** — opt-in
   `-merge-service-auth` flag on `nexler create app` (only meaningful with
   `-auth jwt|both` and `-db`). Folds `X-Api-Secret` service-key auth
   (`core.VerifyServiceKey`) into `RequireAuth` itself as a fallback after
   JWT/session, instead of a separate `RequireServiceAuth` — so a
   service/automation caller can hit the exact same protected routes a human
   user would. Source: `comms`'s merged `middleware/auth.go` (see its own
   `comms-checklist.md`'s Auth Model note for why it moved off the split
   design). Deliberately **not** the new default — `ctrl-svc` still uses the
   separate-middleware design on purpose (a pure API service with a clean
   user-vs-service route split), so this stays opt-in both at scaffold time
   (`-merge-service-auth`) and at retrofit time (`nexler update
   -merge-service-auth`). Implemented 2026-08-19: `cmd/nexler/main.go` (new
   flag + prompt + usage text), `internal/scaffold/scaffold.go`
   (`NewAppConfig`/`templateData` field + skip-switch),
   `internal/scaffold/templates/middleware/auth.go.tmpl` (merged jwt/both
   branches), `internal/scaffold/route.go` (`ensureAuthSubjectContext`'s
   render data struct needed the new field — always `false` there, since
   that path only ever regenerates an app predating `ContextWithSubject`
   entirely, long before this feature existed), `internal/scaffold/
   serviceauth.go` (`ensureServiceAuth` made merge-aware, so a plain
   `nexler update` never resurrects `middleware/service_auth.go` on an
   already-merged app — found via the idempotency test below),
   `internal/scaffold/mergeserviceauth.go` (new — the opt-in retrofit),
   `cmd/nexler/update.go` (new flag). Docs: `docs/index.html` (flag
   reference x2, auth-model section, changelog) and `CLAUDE.md`'s "API-key
   (service-to-service) auth" section. Verified: `go build`/`go vet` clean;
   scaffolded merged/unmerged/jwt-only-merged apps and confirmed `go build`
   succeeds after `go mod tidy`, output is gofmt-clean, and the unmerged
   default path is unaffected; retrofitted a plain app via `nexler update
   -merge-service-auth`, confirmed it's idempotent on a second run,
   confirmed a plain `nexler update` (no flag) leaves a merged app alone,
   and confirmed a hand-edited `middleware/service_auth.go` is refused
   rather than silently discarded.
2. `[x]` **`mongo.go.tmpl`'s `structToBSON` embedded-field bug.** Only
   `findObjectIDField`/`InsertID` recursed into anonymous embedded struct
   fields before — `Get`/`GetOne`/`Set`/`Delete`'s filter builder
   (`structToBSON`, `internal/scaffold/templates/mongo/mongo.go.tmpl`) did
   not, so a filter like `T{Base: common.Base{ID: id}}` built
   `{"base": ...}` instead of `{"_id": id}` and every by-ID lookup silently
   404s. Ported comms's fix (flattens anonymous embedded fields into the
   same top-level filter via `maps.Copy`) verbatim. Implemented 2026-08-19:
   `internal/scaffold/templates/mongo/mongo.go.tmpl` (template fix + `maps`
   import), `internal/scaffold/route.go` (new `ensureMongoEmbeddedFilterFix`
   retrofit — an exact literal-text anchor replacement, since
   `structToBSON`'s body has zero per-app templating, same technique
   `ensureKgateResumeAll` already uses for `kgate.go`'s `Register`),
   registered in `update.go`'s `updateChecks`. Verified with a white-box
   Go test (`common.Base{ID: id}` as a filter now produces exactly
   `{"_id": id}`) and an end-to-end retrofit test against an app scaffolded
   with the pre-fix binary.
3. `[x]` **`response.go.tmpl` HTML composition upgrade.** The
   `resolveHTMLFile` hierarchy-walk (see "Starting state" above) plus the
   rest of comms's evolution, all ported: optional
   `header.html`/`sidebar.html`/`footer.html` partials (resolved the same
   hierarchy-walk way, composed into `layout.html`, missing anywhere in the
   hierarchy simply renders as empty, not an error); `HTMLError`/
   `Unauthorised` (render a module's `error-content.html`/
   `unauthorised-content.html` instead of the JSON envelope, falling back to
   JSON if the page itself can't render); `Subject`/`Path` on the page
   template data. Two adaptations comms didn't need: `Subject` (from
   `auth.Subject(r)`) is gated on `-auth` not being `none` — the field is
   always present on the page data, just always `""` for a no-auth app —
   and the `HasDB`-gated `core.LogError` call on a >=500 mirrors `Error`'s
   own existing gate. HTML's own rendering was extracted into a new
   `composeHTML`, shared by `HTML`/`HTMLError`/`Unauthorised` so they can
   never drift. Implemented 2026-08-19:
   `internal/scaffold/templates/response/response.go.tmpl` (full rewrite of
   everything from `resolveHTMLFile` down), `internal/scaffold/route.go`
   (new `ensureResponseHTMLUpgrade` retrofit — re-renders the current
   template to a string with this app's inferred `HasDB`/`AuthKind`
   (`readCoreDBType`/`detectAuthFiles`, same disk-inference precedent
   `ensureServiceAuth` already set), then splices just the new HTML body and
   new-functions block into the existing file, coexisting with
   `ensureResponseJSONRaw`'s own insertion at the same `responseMarker`),
   registered in `update.go`'s `updateChecks`. Verified with a white-box Go
   test (`composeHTML` renders correctly with and without partials,
   `HTMLError` falls back to JSON when no page exists) and end-to-end
   retrofit tests against apps scaffolded with the pre-fix binary, covering
   both a full `-auth both -db mongo` app and a minimal `-auth none` app
   with no DB — including confirming a hand-edited `HTML`/`structToBSON` is
   refused rather than silently overwritten, and idempotency on a second
   `nexler update` run.
4. `[x]` **A generated `store/common/common.go.tmpl`** for `-db mongo` apps —
   `type Base struct { ID bson.ObjectID \`bson:"_id,omitempty"\` }`, meant to
   be embedded anonymously in every Mongo domain struct. Both
   `D:\Services\comms\store\common\common.go` and
   `D:\Services\ctrl-svc\store\common\common.go` independently hand-built an
   identical package with the same doc-comment shape — two independent
   reinventions of the same shape was exactly the signal this belonged in
   the scaffold itself instead of being re-solved per app. Implemented
   2026-08-19: new `internal/scaffold/templates/store/common/common.go.tmpl`
   (a generic port, not tied to either app's specific domain), a new
   `store`-directory gate in `scaffold.go`'s `NewApp` walk (mongo-only, same
   as the existing `mongo/` gate), new `ensureStoreCommon` retrofit in
   `route.go` (writes the file only if missing — `Base` has no versioned
   content to bring up to date, just present-or-absent — gated on
   `mongo/mongo.go` existing), registered in `update.go`'s `updateChecks`.
5. `[x]` **`-own-register`: an independent second resource in one package.**
   `D:\Services\comms\handlers\admin\providers` has `email.go` (scaffolded
   via `nexler create <route>`, carries the nexler markers, `func
   Register`) and `sms.go` (hand-written afterward, its own `func
   RegisterSms` — Go only allows one `func Register` per package — no
   markers at all), wired from `routes/protected/protected.go` via one
   shared import but two consecutive calls. The identical split repeats at
   services/store/models. `comms-checklist.md`'s own note: "kept the
   nexler markers in `handlers/admin/providers/email.go` untouched since no
   nexler scaffold for this route existed to append to" — real manual work
   filling a gap. Implemented 2026-08-19 as a new opt-in flag on `nexler
   create <route>`, `-own-register` (combined with a not-yet-existing
   `-file` and a required `-name`): scaffolds an independent second
   resource — own `Register<Name>`, own `middleware`/`openapi` imports, own
   service/store/model files — wired into the aggregator with an
   additional call, reproducing the comms shape exactly as a supported,
   idempotent, re-invokable feature instead of a one-off hand edit. See
   `CLAUDE.md`'s "A second independent resource in an existing package
   (`-own-register`)" section for the full design (primary-file resolution,
   `wireAggregatorAdditionalCall`, the deliberately-hard-error ambiguous
   case). Touched: `cmd/nexler/main.go`, `internal/scaffold/route.go`
   (`RouteConfig.OwnRegister`, `routeData.RegisterFuncName`,
   `addToExistingRoute`'s primary-file resolution, `findAllMarkedFiles`,
   `wireAggregatorAdditionalCall`/`insertAggregatorCall`,
   `detectIdentifierCollisions`'s new parameter, `addOwnRegisterResource`),
   `internal/scaffold/route_templates/handler.go.tmpl` (one new
   `RegisterFuncName` field, no new template file needed). Verified: `go
   build`/`go vet` clean; reproduced the comms `email`+`sms` shape
   end-to-end on a scratch app (own `RegisterSms`, `email.go` byte-for-byte
   untouched, correct aggregator wiring, `go build` succeeds); confirmed a
   later method addition to `sms` (no `-own-register`) correctly extends
   `RegisterSms`, never `email.go`'s `Register`; confirmed both new
   error-out safety nets (`-own-register` without `-file`/`-name`; a bare
   new `-file` against a package with 2+ independent resources); confirmed
   the classic pre-existing fold-into-primary `-file`/`-name` behavior is
   completely unaffected; confirmed `nexler update` behaves normally
   against a package with two `Register<X>` functions.

## 0.5.7

1. `[x]` **Task Registry, ported as baseline infrastructure.** Ports
   `ctrl-svc`'s Task Registry (see the Watch List entry this supersedes,
   below) into nexler itself — resolving both decisions that entry left
   open. New `task/task.go.tmpl` (in-process registry — `Task{Name,
   RequireAuth, PathParams, ReqType, RespType, Executor}`,
   `Register`/`Get`/`All`/`Run`; `Executor` is nil for every
   auto-registered route — `Run` is the deliberately secondary,
   exceptional path) and `middleware/task.go.tmpl`
   (`RegisterTask(action, requireAuth, reqType, respType, pathParams...)`
   — one call replacing the old bare `middleware.RequireAuth(handlerName)`
   wrap, wiring in audit logging and a permission-check hook) are
   generated unconditionally for every app, same as `apiclient/` — this
   is the "baseline, not opt-in" decision, made concrete: every route
   `internal/scaffold/route.go`'s `NewRoute` builds now emits
   `middleware.RegisterTask(...)` as its `RegisterExpr` (a new
   `taskActionName(module, name, pkgName, verb)` helper builds the dotted
   `"<module>.<resource>.<verb>"` registry key), and the two hand-authored
   admin-API templates (`handlers/admin/users`, `handlers/admin/services`)
   were updated to match. On a core-database app, new `core/tasks.go.tmpl`
   (`core.SyncTasks`, mirroring the registry into `core_tasks` via a new
   exported `openapi.JSONSchema`, factored out of the package's existing
   unexported `jsonSchema`) and `core/audit.go.tmpl` (`core.LogAudit`,
   writing `core_audit_log`) are generated too, and `main.go.tmpl` calls
   `core.SyncTasks` at startup (best-effort, behind a new `//
   nexler:tasks-sync` marker — the first marker `main.go` has ever
   needed, despite being otherwise "never edited again after scaffold").
   Resolves the second open decision (permission wiring conditional on
   kpass) via a hook, not a hardcoded call: `middleware.PermissionCheck`
   is a nil-by-default package var; `nexler init kpass` (`NewKpass`) now
   also writes `middleware/permission_kpass.go`, wiring it to
   `services/kpass.CheckAccess`, whenever the target app already has
   `middleware/task.go` — so a fresh app never needs `middleware/task.go`
   itself rewritten later, unlike `ctrl-svc`'s own hand-built
   `middleware/permission.go`, which the Watch List entry already flagged
   as having drifted from calling `services/kpass.CheckAccess` in the
   first place. `nexler update` picks up an app scaffolded before this
   existed via two new checks, `ensureTaskRegistry` (writes
   `task/task.go`/`middleware/task.go` unconditionally, `core/tasks.go`/
   `core/audit.go` plus the `main.go` patch when the app has a core
   database) and `ensureKpassPermissionHook` (writes the kpass wiring file
   for an app that already has both `kpass/kpass.go` and
   `middleware/task.go`) — deliberately does **not** rewrite
   `RegisterExpr` in already-generated route files, since that's static Go
   source baked in at each route's own creation time; only a newly-added
   method on an existing app picks up the new wrapping. Implemented
   2026-08-31: `internal/scaffold/templates/task/task.go.tmpl` (new),
   `internal/scaffold/templates/middleware/task.go.tmpl` (new),
   `internal/scaffold/templates/core/tasks.go.tmpl` (new),
   `internal/scaffold/templates/core/audit.go.tmpl` (new),
   `internal/scaffold/templates/openapi/openapi.go.tmpl` (new exported
   `JSONSchema`), `internal/scaffold/templates/main.go.tmpl`
   (`core.SyncTasks` call + marker), `internal/scaffold/route.go`
   (`RegisterExpr` construction, new `taskActionName`),
   `internal/scaffold/templates/handlers/admin/{users,services}/*.go.tmpl`
   (`RegisterTask` wrapping), `internal/scaffold/taskregistry.go` (new —
   `ensureTaskRegistry`), `internal/scaffold/kpass.go`
   (`writeKpassPermissionHook`/`ensureKpassPermissionHook`),
   `internal/scaffold/kpass_templates/permission_kpass.go.tmpl` (new),
   `internal/scaffold/update.go` (both new checks registered). Verified:
   `go build`/`go vet` clean on nexler itself; scaffolded apps across the
   flag matrix (`-auth none|jwt|session|both`, no `-db`/mongo/mysql/
   postgres, `-merge-service-auth`) and confirmed each builds and vets
   clean after `go mod tidy`; confirmed a fresh route's generated
   `RegisterTask(...)` call has the expected action name and path-param
   wiring; confirmed `GET`/`DELETE` against a running scaffolded app's
   admin routes 401 without a bearer token and `/openapi.json` documents
   both operations; confirmed the `nexler update` retrofit restores
   deleted Task Registry files/main.go patch and is idempotent on a
   second run; confirmed `nexler init kpass` wires
   `middleware/permission_kpass.go` and the app still builds.
2. `[x]` **`nexler init tenant`: independent TenantOrg admin API (list +
   delete).** A guarded `tenantorg` listing/delete API for a `hub` (or
   similar) app to manage tenants — deliberately **not** part of `core/`
   (unlike `core_config`/`core_error_log`, unconditional for every `-db`
   app): whether an app has a multi-tenant model at all is
   provider/business-specific, so this is a new, separate, opt-in `nexler
   init tenant [-dir <app-dir>]` command, the same shape as `nexler init
   kgate`/`nexler init kpass`, not a `create app` flag — and not to be
   confused with the already-shipped, unrelated `-multitenant` flag (Org
   propagation through auth context; see the Watch List entry below),
   which this doesn't touch. Requires the target app to already have a
   core database connection (same eligibility as `nexler init kgate`) and
   a JWT-capable `-auth` choice (same eligibility as the
   `core_users`/`core_services` admin API), since the delete endpoint
   needs an authenticated caller to gate at all. New `tenant/tenant.go`
   (`TenantOrg{ID, Name, Settings map[string]any, Status, CreatedAt,
   UpdatedAt}` — `Settings` is a free-form organization
   configuration/settings blob, this package's whole reason for existing;
   `ListTenantOrgs`/`DeleteTenantOrg` only — no generated Create/Update
   path yet, so `ID` is a caller-assigned string, not
   nexler-generated) and `handlers/admin/tenants/tenants.go` (`GET
   /admin/tenants`, `DELETE /admin/tenants/{id}`, both wrapped in the new
   `middleware.RegisterTask` from item 1 above — the same "guard is left
   for the developer to extend via kpass" caveat as the
   `core_users`/`core_services` admin API, now backed by a concrete,
   working `PermissionCheck` hook instead of just a comment).
   `DeleteTenantOrg` is a real, hard delete — no undo, unlike
   `core.RevokeService`'s soft status-flip. `nexler init db` provisions
   `tenant_orgs` (SQL cores only — a plain table, no stored procedures,
   since List/Delete need none; Mongo needs no provisioning at all, since
   it has no unique-index requirement here and creates the collection
   lazily), conditional on `tenant/tenant.go` existing on disk, since
   `nexler init tenant` itself never touches a live database connection.
   Implemented 2026-08-31: `internal/scaffold/tenant_templates/
   tenant.go.tmpl` (new), `internal/scaffold/tenant_templates/
   tenant_handlers.go.tmpl` (new), `internal/scaffold/templates.go`
   (`tenantTemplateFS` embed), `internal/scaffold/tenant.go` (new —
   `NewTenant`), `cmd/nexler/inittenant.go` (new), `cmd/nexler/initdb.go`
   (`runInit`'s dispatch, `tenantOrgEnabled`, `tenantOrgStatements`, the
   Mongo/SQL provisioning branches). Verified: `go build`/`go vet` clean;
   scaffolded a mongo-core and a mysql-core app, ran `init tenant` on
   each, confirmed both build clean after `go mod tidy`; confirmed the
   collision guard correctly refuses `init tenant` against an app that
   already has `handlers/admin/tenants/tenants.go` from an unrelated
   route; confirmed clear errors for an app missing `-db` or a
   JWT-capable `-auth`; confirmed `nexler init db` provisions `core_tasks`/
   `core_audit_log`/`tenant_orgs` against a live local Mongo instance
   without error and is idempotent on a second run.

## Watch list

Seen once, or still unfinished even in the one app that has it — not
actionable yet, but worth revisiting if a third app repeats the pattern (or
the one app that has it finishes it).

- **Multi-tenant `Org` propagation.** Comms threads kpass's `OrgId` all the
  way through: `auth.ContextWithOrg`/`Org`, `StartSession`'s new `org`
  parameter, `core.User.OrgId`. Nexler's own `auth/jwt.go.tmpl` already
  carries a `Claims.Org` field (from an earlier pass), but nothing threads it
  through session/context yet. This is plausibly generalizable, but it's also
  specifically shaped by comms's kpass-multi-tenant model — wait for a second
  app to need the same shape before generalizing it into the scaffold.
- ~~**`ctrl-svc`'s Task Registry**~~ — ported into nexler itself as baseline
  infrastructure, see `## 0.5.7` item 1 above, which also resolves both
  decisions this entry originally left open. Left below for the full
  history of what `ctrl-svc` proved out first — supersedes the old
  `middleware.Audit` +
  `core.LogAudit` entry above (Audit is now one component of this larger
  pattern, not a standalone port). Built and proven out end-to-end in
  `ctrl-svc` (2026-08-30): `task/task.go` (in-process registry —
  `Task{Name, RequireAuth, AccountIDParam, ReqType, RespType, Executor}`,
  `Register`/`Get`/`All`/`Run`), `middleware/task.go`
  (`RegisterTask(action, requireAuth, reqType, respType, accountIDParam...)`
  — one call replacing the old three-deep
  `RequireAuth(Permission(...)(Audit(...)(...)))` nesting, including
  `X-Correlation-Id`/`X-Session-Id` and actor-type detection via
  `auth.Subject`/`auth.Service`), and `core/tasks.go` (mirrors the registry
  into a new `core_tasks` Mongo collection at startup via `core.SyncTasks`,
  storing each task's `ReqSchema`/`RespSchema` — built via a new exported
  `openapi.JSONSchema`, reusing `/openapi.json`'s own reflection instead of
  re-deriving it — so provisioning/workflow tooling can list ctrl-svc's
  tasks and their request/response shapes straight from Mongo without the
  binary running). `ReqType`/`RespType` matter because a task is normally
  invoked through the API itself (`task.Run`, direct in-process dispatch, is
  the deliberately secondary/exceptional path) — the registry's real job is
  describing each task's API contract to permission-check/audit/workflow/
  provisioning/AI tooling, not enabling non-HTTP execution.

  Two decisions were made for when this was actually ported into nexler's own
  templates — see `## 0.5.7` item 1 above for exactly how each was resolved:

  1. **Baseline, not opt-in** — unlike `init kpass`/`init kgate`, this
     should ship as part of what `nexler create`/`nexler init` generate by
     default for every new app, not a separate `init task` command. Means
     `route_templates/handler.go.tmpl` and `route_templates/
     register_methods.tmpl` need to emit `middleware.RegisterTask(...)`
     instead of the current bare `middleware.RequireAuth(handlerName)` (see
     `internal/scaffold/route.go`'s `registerExpr` construction), and
     `templates/main.go.tmpl` needs a `core.SyncTasks` startup call. This
     touches nexler's full flag matrix — `AuthKind` (`none`/`jwt`/`session`/
     `both`), `MergeServiceAuth`, `Multitenant`, `HasCoreDB`/
     `CoreDBAccessor` (all visible today in `templates/middleware/
     auth.go.tmpl`'s conditionals) — a future implementer needs to work out
     what `RegisterTask`/`Audit`/`Task` even mean under `-auth none` (no
     subject at all) or with no Mongo core DB (no `core.LogAudit`/
     `core.SyncTasks` target to write to — same precondition `core/
     kgate_channels.go.tmpl`/`core/config.go.tmpl` already have), not just
     the `-auth jwt -db mongo` shape ctrl-svc happens to be.
  2. **Permission wiring is conditional on kpass.** The scaffolded
     `RegisterTask`'s permission check should call
     `services/kpass.CheckAccess(ctx, subject, action, nil)` — nexler's own
     documented per-app wrapper (`kpass_templates/kpass_service.go.tmpl`),
     not the raw `kpass.Check` client — whenever `nexler init kpass` has
     already been run for that app; `RegisterTask`'s baseline (no kpass yet)
     is just `RequireAuth` + `Audit` + registry capture, no permission gate.
- **Standardized error envelope / pagination conventions.** `ctrl-svc-
  endpoint-contracts.md` proposes a `{error: {code, message, details}}` shape
  and consistent `page`/`pageSize`/`total` pagination across list endpoints —
  but these are still `(proposed — not yet implemented)` even in ctrl-svc
  itself. Nothing to port until ctrl-svc (or another app) actually builds it.
