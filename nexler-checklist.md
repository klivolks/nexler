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
- **`ctrl-svc`'s `middleware.Audit` + `core.LogAudit`.** A clean, genuinely
  reusable cross-cutting concern (`X-Correlation-Id`/`X-Session-Id`,
  actor-type detection via `auth.Subject`/`auth.Service`, best-effort logging
  of any successful mutating call) — but a sizable standalone feature (a new
  `core` file, `nexler init db` schema changes, its own docs section), not a
  small port. Deserves its own future task, not a bundled line item here.
- **Standardized error envelope / pagination conventions.** `ctrl-svc-
  endpoint-contracts.md` proposes a `{error: {code, message, details}}` shape
  and consistent `page`/`pageSize`/`total` pagination across list endpoints —
  but these are still `(proposed — not yet implemented)` even in ctrl-svc
  itself. Nothing to port until ctrl-svc (or another app) actually builds it.
