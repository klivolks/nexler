# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

`nexler` is a scaffolding CLI (like `django-admin startproject` / `rails new`) for a Go web framework of the same name. It does not run applications; it generates the handlers/services/store/models layout that apps built on nexler follow.

- **Scaffolding is pure local file I/O.** Every template is embedded via `go:embed` (`internal/scaffold/templates.go`). Everything under `internal/scaffold` (driven by `create app`, `create <route>`, `init kpass|kgate|tenant|docker|ci|kube`, `db add`, `update`) does no network access and never shells out to git or any other process. Prompting always lives in `cmd/nexler`, never in `internal/scaffold`.
- **`-db` is a scaffold-time choice with a build-time consequence**, not an exception: the generated app gets real third-party driver dependencies and needs one `go mod tidy` (network, on the user's machine) before its first build. Nexler itself still never touches the network to produce the app.
- **Two real exceptions**, both self-contained in `cmd/nexler` and deliberately outside `internal/scaffold`: `nexler init db` (live database connection, provisions the "core" schema) and `nexler add` (live `npm install`). Both are explicit, separately invoked commands, never run implicitly by `create app`/`create <route>`.

## Commands

```
go build -o nexler.exe ./cmd/nexler   # build the CLI
go run ./cmd/nexler <args>            # run without building
go vet ./...                          # static checks
```

There are no automated tests or Makefile. Verify changes by building and running the CLI against a scratch directory:

```
go run ./cmd/nexler create app demo -dir /tmp/scratch
cd /tmp/scratch/demo && go build ./...
go run ./cmd/nexler create /purchase/verify -module purchase -submodule verify -methods GET,POST -body json
```

CLI usage (`nexler help` prints the authoritative text with every flag explained):

```
nexler create app <name> [-dir <output-dir>] [-module <module-path>] [-ui] [-auth none|jwt|session|both] [-remember-me] [-db mongo,mysql,postgres,mssql] [-core <type>] [-db-host <host>] [-db-port <port>] [-db-name <name>] [-db-user <user>] [-db-password <password>] [-merge-service-auth] [-multitenant]
nexler create <route> -module <name> [-submodule <name>] [-protected] [-callable] [-methods GET,POST] [-body json] [-response json] [-layout shared] [-dir <app-dir>] [-file <name>] [-name <name>] [-own-register]
nexler create store <name> [-file <name>] [-dir <app-dir>]
nexler create service <name> [-file <name>] [-dir <app-dir>] [-store <name>]
nexler db add <name> -type <mongo|mysql|postgres|mssql> [-dir <app-dir>] [-host <host>] [-port <port>] [-dbname <name>] [-user <user>] [-password <password>]
nexler init db [-dir <app-dir>]
nexler init kpass [-dir <app-dir>]
nexler init kgate [-dir <app-dir>]
nexler init tenant [-dir <app-dir>]
nexler init docker [-dir <app-dir>]
nexler init ci [-dir <app-dir>] [-registry dockerhub|github]
nexler init kube [-dir <app-dir>] [-registry dockerhub|github] [-image <ref>] [-namespace <ns>] [-replicas <n>]
nexler add <package>[@version] [-dir <app-dir>] [-as <name>]
nexler update [-dir <app-dir>] [-merge-service-auth] [-multitenant] [-opt-out <name>[,<name>]] [-opt-in <name>[,<name>]]
nexler version
nexler help
```

### Interactive prompting (`cmd/nexler/prompt.go`)

Any flag not passed on the command line is asked for interactively, Enter accepting the default shown in the usage text. Passing every flag (scripts/CI) is fully non-interactive. `runCreateApp`/`runCreateRoute` use `fs.Visit` after `fs.Parse` to tell which flags were actually set, and prompt only for the rest.

- `-ui`/`-response` prompt as "ui/api?" (`promptUIOrAPI`); `-auth` as an N-way choice (`promptChoice`).
- `-db` is gated behind `promptBool("Use a database?", false)` (default no database); only if yes does it ask which type(s) via the free-text `prompt()` (comma-separated, default `"mongo"`, same pattern as `-methods`).
- `-core` prompts only when `-db` selected more than one type.
- Service/store layers: a single `promptBool("Generate service and store layers for this route?", true)`, asked only when neither `-service` nor `-store` was passed; "no" sets both to `"none"`.
- `-name` and `-file` are not proactively prompted beyond `-file`'s own optional prompt.
- **Never ask a question whose answer can't matter**: skip the body-shape prompt for GET-only routes, remember-me when `-auth` has no session, core-database when only one `-db` type, `-layout` unless `-response html`, and every `-db-*` prompt after `-db-host` when host is blank.
- There is no masked-input helper: the password prompt is plain visible `prompt()` and its label says so.

## Documentation site (`docs/`)

`docs/` (`index.html`, `css/theme.css`, `js/site.js`, `assets/logo.png`) is a standalone static site with no build step and no dependency on the rest of the repo. It publishes the end-user guide: installation, every command and flag, the generated-app architecture, and a running changelog. It is meant for klivolks.com once Nexler ships; until then it is opened as a local file. Its design tokens (`docs/css/theme.css`: brand blue `#22A7F0`, navy `#0E2A44`, the `0 32px 0 32px` signature corner radius, Space Grotesk/Inter/JetBrains Mono) are adapted from the klivolks.com marketing theme so it reads as one family.

**Keep it current.** Whenever a command, flag, default, or documented behavior changes, update the matching section of `docs/index.html` in the same change and append a dated entry to the Changelog section (`#changelog`), newest first. Source every fact from the Go code (`cmd/nexler/*.go` flag definitions and `printUsage()`, plus the relevant `internal/scaffold/*.go`), not from this file's prose, which can drift. Re-check the usage block above when flags change.

Known drift to fix separately: `docs/index.html` documents `DELETE /admin/users/{id}` and `DELETE /admin/services/{name}`, but neither exists in code.

## Releasing

Releasing the CLI itself is deliberately **not** a tag-push pipeline. It fires only when a human publishes a GitHub Release targeting the `release` branch (Releases → Draft a new release → pick/create a tag → **Target: release** → Publish).

- `.github/workflows/release.yml` listens for `release: types: [published]`. The `goreleaser` job's `if: github.event.release.target_commitish == 'release'` skips the whole pipeline for any other branch (both installer jobs cascade-skip via `needs: goreleaser`), so a release cut from `main` or a feature branch does nothing, silently and safely.
- `goreleaser` (`.goreleaser.yml`): cross-compiles windows/darwin × amd64/arm64, checksums, auto-generated changelog, and uploads onto the already-existing release (`release.mode: keep-existing`; `replace_existing_artifacts: true` lets a re-run overwrite).
- Two more jobs build and attach installers to the same release: `installer/windows/nexler.iss` (Inno Setup, `windows-latest`) and `installer/macos/build-pkg.sh` (`pkgbuild`/`productbuild`, `macos-latest`). See each `installer/*/BUILDING.md` for local build/test steps and limitations. No code-signing cert exists yet, so both hit first-run OS friction (SmartScreen, Gatekeeper).
- Local dev builds are unaffected. `cliVersion` in `cmd/nexler/main.go` is a package-level `var` so release builds override it with `-ldflags "-X main.cliVersion=<version>"`; a plain `go build` keeps the literal fallback.

### Artifact signing (cosign)

Every job cosign-signs what it uploads using a **key pair** (deliberately not keyless/OIDC).

- `goreleaser` signs `checksums.txt` only (`.goreleaser.yml` `signs:`); verifying it and checking each archive's sha256 covers all four archives. Each installer job signs its own installer (`nexler-setup-*.exe`, `Nexler.pkg`), since those aren't in `checksums.txt`.
- Output is a `<file>.bundle` Sigstore bundle, **not** a bare `.sig`: cosign v3's `sign-blob` dropped `--output-signature`/`--output-certificate` for `--bundle`. Signing logs to the public Rekor transparency log, so it needs network access; that is expected, not a bug to suppress.
- `cosign.key` and its password are **not** in the repo (generated locally with `cosign generate-key-pair`, gitignored). CI reads two repository secrets with deliberately nexler-specific names, because the key is this project's own and not shared across repos:
  - `COSIGN_NEXLER_KEY`: full contents of `cosign.key`.
  - `NEXLER_PASSWORD`: its password, mapped at the `env:` step level to the literal `COSIGN_PASSWORD` cosign looks for. Each job deletes its written-out `cosign.key` right after signing.
- `cosign.pub` **is** committed at the repo root. Verify with:

```
cosign verify-blob --bundle checksums.txt.bundle --key cosign.pub checksums.txt
cosign verify-blob --bundle nexler-setup-1.2.3.exe.bundle --key cosign.pub nexler-setup-1.2.3.exe
```

  then check each archive's `sha256sum` against `checksums.txt`.

### Licence and icons

- MIT (`LICENSE`, repo root). Both installers show it as a wizard page (`nexler.iss` `LicenseFile=`; `distribution.xml.tmpl` `<license>`); `build-pkg.sh` copies it with `README.md`/`welcome.html` into the macOS resources.
- `installer/nexler-icon.svg` is Nexler's own app icon (an "N" monogram), distinct from `internal/scaffold/templates/templates/static/logo.svg`, which is the starter logo scaffolded into generated apps. `installer/windows/nexler.ico` and `installer/macos/nexler-1024.png` are regenerated from it via `build/gen-icons` (see each `BUILDING.md`).
- The Windows wizard images (`installer/windows/wizard-image.png` 192x386, `wizard-small-image.png` 76x80, for `WizardStyle=modern`, set by `WizardImageFile`/`WizardSmallImageFile`) come from `installer/wizard-banner.svg`/`wizard-small.svg`, each authored at its exact aspect ratio. `build/gen-icons` accepts a `<width>x<height>` size argument to rasterize these without distortion.

## Architecture

### Cross-cutting rules

These apply everywhere below; individual sections only note exceptions.

- **Primitives only.** Nexler generates building blocks, not business flows: no `/login`/`/logout`, no CRUD routes for services beyond the admin API, no automatic permission wiring into `RequireAuth`, no `<link>`/`<script>` tags for vendored assets. Handlers carry informed `// TODO` comments instead of guessed-at calls.
- **No breaking changes to generated APIs.** New helpers are additive (`InsertID` beside `Insert`, `Update` beside `Set`); never change an existing signature or remove a function.
- **Zero third-party dependencies in generated apps**, except where unavoidable: `-db` drivers and `init kgate`'s `github.com/gorilla/websocket`. Each needs one `go mod tidy` in the generated app. Prefer stdlib (hand-rolled HS256 JWT, hand-rolled `.env` loader, `net/http` apiclient). No CDN at runtime.
- **Never clobber a hand-edit.** Commands that write a hand-editable file refuse if it exists (`kpass/kpass.go`, `kgate/kgate.go`, `services/kpass|kgate`, route layer files, `k8s/deployment.yaml`, Docker/CI files). Content files (HTML templates) are written once via `writeIfMissing`, never lazily recreated at runtime.
- **Detect state from disk, not stored flags.** Later commands infer an app's shape from files: `detectAuthFiles` (`auth/jwt.go`/`auth/session.go`), `readCoreDBType` (scans `.env` for the `_DB_CORE_TYPE=` suffix), `recoverEnvPrefix` (scans for the unconditional `_HOST=` line, so it works on a `-db`-less app), `dialectEnabled`, `tenantOrgEnabled`, `dirHasGoFile`.
- **Marker comments and literal anchors, never Go reparsing.** Insertions anchor on fixed marker comments or known-exact text. A missing marker/anchor is an error naming the file and the exact text to add by hand; never guess a location.
- **`main.go` is scaffolded once and never edited again.** New routes are wired by editing the aggregators. The single exception is the `// nexler:tasks-sync` marker (Task Registry retrofit).
- **Env var naming.** App settings are `{APPNAME}_`-prefixed (`envPrefix` in `scaffold.go`: uppercase letters, keep digits, everything else `_`; e.g. `-module my-app` → `MY_APP_`). Shared vendor credentials (`KPASS_*`, `KGATE_*`) are deliberately not prefixed.

### `nexler update` retrofit conventions (`internal/scaffold/update.go`)

`nexler update [-dir]` brings an already-scaffolded app up to the current generated shape through the checks registered in `updateChecks`.

- **Best-effort.** Every check runs regardless of earlier failures; failures are reported in `UpdateResult.Failed []FailedCheck`. Checks are independent (each anchors on its own known text, never panics or shares state). The one real ordering dependency, the kgate chain, degrades safely on a missing anchor.
- **Silent no-op when not eligible** (feature never enabled for the app) or already current.
- **Three patch styles, chosen per file:**
  - *Append-only* for wholly new code that touches nothing existing (`ensureInsertIDHelpers`, `ensureMongoUpdate`, `ensureResponseJSONRaw`'s `JSONRaw`, `ContextWithService`).
  - *Narrow literal-anchor replacement* of a known-exact body with zero per-app templating, erroring (naming the file and the snippet to add by hand) if the body was hand-rewritten. Used for files developers realistically extend: `response.go`, `mongo.go`, `kgate.go`, `config.go`, `home.go`, `openapi.go`, `db/db.go`.
  - *Full regeneration* only for pure generated infra whose content is fully derived from app config (`middleware/auth.go`, guarded by a `func RequireAuth` sanity check), accepting that a hand edit there is discarded.
- **Write-if-missing** for present-or-absent files (`store/common`, `services/kpass`, `task/task.go`, admin handlers).
- **Since nexler never runs `gofmt` on output** and real apps get reformatted by ordinary tooling, anchors must recognise `gofmt`-style text as "already current" (`ensureMongoEmbeddedFilterFix` accepts both the `gofmt` form and the original malformed-whitespace form). Templates themselves must be `gofmt`-clean.
- **Opt-in changes are flags, not checks.** `updateChecks` converge every eligible app to one canonical shape; a per-app design choice (`-merge-service-auth`, `-multitenant`, `-opt-out`/`-opt-in`) only happens when asked.
- **Never re-runs `nexler init db`**; table provisioning stays a separate manual step. `.env` keys are appended only if missing, never overwriting a present value.
- **Does not rewrite already-generated route files.** Only newly added methods pick up new wrapping, so an older app can legitimately mix old-style and new-style route registrations.
- `apiclient/apiclient.go` and `core/apiclientlog.go` are not updated by `nexler update`: copy them from a fresh scaffold.

Registered retrofits by area (function → what it adds):

| Area | Checks |
|---|---|
| Auth | `ensureAuthSubjectContext` (adds `auth/context.go`, regenerates `middleware/auth.go`), `ensureJWTClaims`, `ensureServiceAuth` and `ensureAdminRoutes` (`serviceauth.go`), `ensureServiceCallerContext` (`servicecaller.go`) |
| Response/OpenAPI/config | `ensureResponseJSONRaw`, `ensureResponseHTMLUpgrade`, `ensureOpenAPIUpToDate`, `ensureSwaggerToggle`, `ensureAPIBasePath` |
| Mongo/SQL helpers | `ensureInsertIDHelpers`, `ensureMongoDatabaseName`, `ensureMongoUpdate`, `ensureMongoFilterExpression`, `ensureMongoEmbeddedFilterFix`, `ensureStoreCommon` |
| kpass | `ensureKpassService`, `ensureKpassPermissionHook` |
| kgate (in this order) | `ensureKgateResumeAll` → `ensureKgateOpenAPIAndTestSubscribe` → `ensureKgateSharedConnection` → `ensureKgatePublishEncoding` → `ensureKgateResilientDelivery` → `ensureKgateWebhookDispatch` → `ensureKgateServiceExtraction` |
| Task Registry | `ensureTaskRegistry`, `ensureTaskSyncAfterRoutes`, and `taskmetadata.go`'s `ensureOpenAPITaskFields`, `ensureCoreTaskMetadata`, `ensureTaskCallableFromDB`, `ensureOperationTaskFields` |

### Two embedded template trees, two code paths

- `internal/scaffold/templates/` (`templateFS`): the full skeleton for a new app (`main.go`, `routes/`, `middleware/`, `openapi/`, `handlers/home/`, static assets). Driven by `scaffold.NewApp` in `scaffold.go`, walking with `fs.WalkDir`.
- `internal/scaffold/route_templates/` (`routeTemplateFS`): per-route stubs (handler/service/store/model), `handler_file.tmpl` (a secondary handler file with no `Register`), and three fragment templates (`handler_methods.tmpl`, `register_methods.tmpl`, `model_methods.tmpl`) for extending an existing route. Driven by `scaffold.NewRoute` / `addToExistingRoute` in `route.go`.
- Further trees for `init` commands: `kpass_templates/` (`kpassTemplateFS`), `kgate_templates/` (`kgateTemplateFS`), `tenant_templates/`.

Rendering rules (`processFile`, `render`, `destPath` in `scaffold.go`):

- Only `*.tmpl` files go through `text/template`; everything else is copied verbatim. Generated Go can contain a literal `{{` (e.g. `[]map[string][]string{{"a": nil}}`) that would otherwise be misparsed.
- A placeholder meant for the generated app's own runtime templates is written as `` {{"{{.Title}}"}} `` inside a `.tmpl`.
- `gomod.tmpl` → `go.mod` (a real `go.mod` inside `templates/` would be a module boundary and break `go:embed`). `dotenv.tmpl` → `.env` (`go:embed` silently excludes names starting with `.` or `_`).
- All rendered output is normalised to LF, because route wiring depends on exact-byte anchor matching.
- Optional parts are skipped inside `NewApp`'s walk: `fs.SkipDir` for whole directories (`auth/`, `db/`, `core/`, `mongo/`, `mysql/`, ...) and per-file skips (`auth/jwt.go.tmpl` vs `session.go.tmpl`, the embedded homepage under `-ui`, `middleware/service_auth.go.tmpl` when merged).

### Generated app layout

Fixed layered structure (mirrors kGate's proven split):

```
handlers/    HTTP handlers: decode request, (TODO) call service, write response
services/    business logic, orchestration
store/       persistence
models/      request/response and domain structs (struct-tag validated)
middleware/  auth, logging, recovery, RegisterTask
routes/      routes.go (calls public+protected Register once from main.go);
             routes/public/public.go, routes/protected/protected.go are aggregators
openapi/     in-process registry reflecting Request/Response json tags into
             /openapi.json at request time (no file on disk to keep in sync)
request/     decode helpers          response/   JSON envelope + HTML helpers
apiclient/   Get/Post/Put/Delete helpers for calling other HTTP APIs
task/        in-process Task Registry every route registers with
config/      loads {APPNAME}_ env vars (+ optional .env) at package-init time
.env         HOST/PORT, SWAGGER_ENABLED, JWT_SECRET (-auth jwt|both), and the
             "core" DB connection (-db)
auth/        only for -auth jwt|session|both
db/          only for -db (any subset of mongo|mysql|postgres|mssql)
mongo/ mysql/ postgres/ mssql/   per-dialect helper packages, per -db type
store/common/                    shared Mongo Base struct (mongo only)
core/        only with -db: Config, error log, kgate channel registry,
             tasks/audit log, users and service API keys (a session log is not built)
kpass/ kgate/ tenant/ k8s/       only added by the matching `nexler init ...`
```

`main.go` calls `routes.Register(mux)` once. New routes are added purely by editing the aggregators, which `nexler create <route>` does automatically.

### Request decoding and response envelope

- Each handler method decodes with one `request` call, precomputed as `routeMethod.DecodeCall` (`decodeCall` in `route.go`): `DecodeQuery` for GET (always query-only, regardless of `-body`), otherwise `DecodeJSON`/`DecodeForm`/`DecodeMultipart` per `-body`.
- `DecodeForm`/`DecodeMultipart`/`DecodeQuery`/`DecodePath` populate exported fields by reflection, matched on each field's `json` tag (the same tag `openapi` reads). Supported: string/bool/int/uint/float and slices of those; a `*multipart.FileHeader` field receives an uploaded file of the same name.
- Default (`-response json`) envelope: `response.JSON(w, status, v)` → `{"data": v}`; `response.Error(w, r, status, msg)` → `{"error": msg}`. `Error` takes the request because on a `-db` app it auto-logs every 5xx via `core.LogError`, best-effort, as `middleware.Recover` does for a recovered panic. 4xx is never logged.
- `openapi.Spec` documents the envelope: a `RespType` schema is nested under `data` in the `"200"` response, and every operation references a shared `Error` component as `"default"`.
- **Keep three things in sync** when any changes: `request`'s decode helpers, `response`'s envelope shape, and `openapi.go.tmpl`'s documented shape.
- Handlers only decode then respond. The service call stays a `// TODO: call <pkg>'s service with req` comment, because `services/<pkg>.go`'s single `Do<Name>()` isn't parameterised per HTTP method.

### The `apiclient` package

Always generated (pure stdlib, no flag). `Get(ctx, url, headers)`, `Post(ctx, url, headers, body)`, `Put(...)`, `Delete(...)` return `(*Response, error)`. It is the outbound direction (calling other services); `request`/`response` handle this app's own traffic.

- **Synchronous by design.** The internal `do()` calls the package-level, swappable `Client` (`&http.Client{Timeout: 120 * time.Second}`), then `defer resp.Body.Close()` and `io.ReadAll`s the body before returning. Callers see only `*Response{StatusCode, Header, Body []byte}` plus `.JSON(v any) error`, never an `*http.Response` they could forget to close; the connection returns to the pool.
- **Body encoding** (`Post`/`Put`): `[]byte`/`string` sent verbatim with no `Content-Type` guess; anything else `json.Marshal`ed with `Content-Type: application/json` unless `headers` sets one; `nil` means no body. `headers` (`map[string]string`, nil ok) is applied after the inferred type, so an explicit header wins.
- **Failure reporting.** `apiclient.OnFailure` (package-level hook, nil by default) is called by `do()` for every call without a usable answer: transport/body-read error (status 0) or any status ≥ 400. For ≥ 400 the body first passes through `apiclient.CompactBody`, which reduces an HTML error page (e.g. ASP.NET's unhandled-exception page) to its title and request id.
- On a `-db` app, `core/apiclientlog.go.tmpl` installs the hook in `init()` and writes each failure via `core.LogError` (message `api: METHOD host/path returned STATUS`, details = reply capped at 1000 characters, route = the path, never the query string). An app without `-db` has no hook.
- `do()` also logs slow (over `SlowCall`, default 1s) or failed calls to stdout.
- **Route every external call through `apiclient`**; a raw `http.Client` is not covered.

### Runtime configuration (`config`, `.env`)

- `config.C` (`config/config.go`, `var C = load()`, ready before `main()`) holds `Host`/`Port` (`{APPNAME}_HOST`/`_PORT`), `SwaggerEnabled`, `APIBasePath`, and `JWTSecret` when applicable. `main.go` listens on `config.C.Addr()`.
- **Extend `Config`/`load()` for any new env-driven setting**; don't add another mechanism. Parsing helpers: `getEnvOr` (strings) and `getEnvBoolOr` (`"true"/"1"`, `"false"/"0"`, else fallback); a new type gets its own `getEnv<Type>Or`.
- `.env` is read by the hand-rolled `loadDotEnv` (`bufio.Scanner` over `KEY=VALUE`). Real environment variables always win; `.env` is a local default, not an override. Empty `HOST` means all interfaces; default `PORT=8080`.
- **`{APPNAME}_SWAGGER_ENABLED`** (default `true`) gates `GET /swagger` (`templates/swagger.html`) and `GET /openapi.json` (`HandleOpenAPI`, reflecting `openapi.Spec()` per request). Both routes stay registered; the check lives **inside each handler** (`HandleOpenAPI`, `HandleSwagger`) and writes `http.NotFound`. Not registering them would not 404: `GET /` is a subtree pattern in `ServeMux`, so the request would fall through to the homepage with a 200.
  - The non-`-ui` static homepage (`templates/html/home.html`) hides its "View API Docs" button client-side: an inline script fetches `/openapi.json` and hides the button on a non-OK response or network error. This is hand-editable content, so `nexler update` does not retrofit it.
- **`{APPNAME}_API_BASE_PATH`** (no default) documents the prefix an external gateway mounts the service under. When set, `openapi.Spec("<AppName>", config.C.APIBasePath)` adds a relative `servers[0].url`. Purely documentational; route matching is unaware of it.
- Dynamic, runtime-mutable settings (e.g. timezone) are a documented future direction only (`config.go.tmpl` doc comment), not implemented.

### Authentication (`-auth none|jwt|session|both`, `-remember-me`)

`middleware.RequireAuth` wraps every `-protected` route; `middleware/auth.go.tmpl` branches on `.AuthKind`.

- `none` (default): the original stub. It checks that an `X-Api-Scret` header and a `Bearer` token were sent and verifies neither.
- `jwt`: `auth/jwt.go` with `IssueJWT`/`VerifyJWT`, a minimal stdlib HS256 JWT (`crypto/hmac` + `crypto/sha256`; deliberately not JWE). `RequireAuth` verifies `Authorization: Bearer <token>`.
- `session`: `auth/session.go`, an **in-memory** store (`map[string]sessionEntry` behind a mutex) with `StartSession` (an `HttpOnly`/`SameSite=Lax` cookie named `{{.EnvPrefix}}_session`), `SessionFromRequest`, `EndSession`. Sessions are lost on restart and not shared across instances; swap the map for a real store before scaling.
- `both`: both files; `RequireAuth` tries the bearer token, then the session cookie.

Details:

- `RequireAuth` attaches the subject (userID) via `auth.ContextWithSubject` (`auth/context.go`, generated whenever `AuthKind != "none"`). Protected handlers read it with `auth.Subject(r) (subject string, ok bool)`.
- The JWT secret (`generateJWTSecret`, 32 bytes of `crypto/rand`) is generated once per `create app` and threaded through `templateData.JWTSecret` into `config.go.tmpl` (`Config.JWTSecret`, `{{.EnvPrefix}}_JWT_SECRET`) and `dotenv.tmpl`.
- `-remember-me` adds a `rememberMe bool` parameter to `StartSession`, swapping the 24h lifetime for 30 days (one cookie, one lifetime toggle). Silently ignored without session auth.
- `-multitenant` threads an Org through the auth context (`auth.ContextWithOrg`, `core.User.OrgId`). It is unrelated to `nexler init tenant`.
- `core/users.go` (`core.GetUser`/`CreateUser`/`SetUserStatus`/`ListUsers`, `core_users`) is a **minimal local record, not a profile store**: `UserId` equals `auth.Subject(r)` / the JWT `sub`, plus `Username`/`UserRole`/`UserType` and `Status` (a local active/disabled switch). A disabled user's unexpired JWT is still valid; enforcing `Status` is a separate check your own code must make. `CreateUser` is not called automatically from `RequireAuth`; call it from your registration/login flow.

### API-key (service-to-service) auth (`middleware.RequireServiceAuth`)

Generated for apps with `-auth jwt|both` **and** `-db`. `middleware/service_auth.go.tmpl` checks the `X-Api-Secret` header against `core_services`.

- **Independent of `RequireAuth` by default.** A route is user-facing (`-protected`) or service-only (`RequireServiceAuth`), never both through one check, so a service-only route can never accept an end-user JWT.
- **The `X-Api-Secret` must never be embedded in UI-facing code** (browser JS, mobile binary). Treat it like a database credential. This is a documented discipline, not a runtime check.
- `core/services.go`: `CreateService(ctx, name) (key, err)` generates a random 32-byte hex key, stores only its SHA-256 hash, and returns the plaintext **exactly once**. `VerifyServiceKey(ctx, key) (name, ok, err)` looks up by hash and keeps its signature. `RevokeService(ctx, name)` sets `Status` to `"revoked"`. Also `ListServices`/`GetService` (metadata only; `Service` has no key field).
- On success the service name is attached via `auth.ContextWithService` / read with `auth.Service(r)`, a distinct context key from the subject.
- Schema for `core_users`/`core_services` is provisioned by `nexler init db` for every `-db` app regardless of `-auth`. Only the Go code is gated on `-auth jwt|both`.

#### `-merge-service-auth`

Opt-in (same eligibility). Folds the `X-Api-Secret` check into `RequireAuth` itself, tried after JWT (and session for `both`), so service callers can hit the same `-protected` routes; `middleware/service_auth.go` is not generated. A matching key attaches both `ContextWithService` and `ContextWithSubject` (the service's name). Default `false`, deliberately: a pure API service that wants user-only and service-only routes structurally distinct should keep the separate middleware.

Retrofit with `nexler update -merge-service-auth` (`MergeServiceAuth` in `mergeserviceauth.go`): ensures the service-auth files exist, byte-compares an existing `middleware/service_auth.go` against the current template before removing it (refusing, naming the file, if hand-edited), then regenerates `middleware/auth.go` merged. `ensureServiceAuth` is merge-aware and never writes `service_auth.go` back on a merged app.

#### Admin API for `core_users`/`core_services`

Same eligibility. Hand-authored, one self-contained file each (not through `NewRoute`):

- `handlers/admin/users/users.go.tmpl`: `GET /admin/users` (no pagination), `POST /admin/users` (create/upsert), `GET /admin/users/{id}`, `PATCH /admin/users/{id}/status`.
- `handlers/admin/services/services.go.tmpl`: `GET /admin/services`, `POST /admin/services` (plaintext key in the response exactly once), `GET /admin/services/{name}`, `POST /admin/services/{name}/revoke`.

Rules:

- Every handler goes through `middleware.RegisterTask` with `requireAuth` true and **no role check**: authentication, not authorisation. `PermissionCheck` is nil until `nexler init kpass` sets it; until then any authenticated caller may use these routes. Both files' doc comments say so.
- Wired into `routes/protected/protected.go` by `wireAggregator` right after `NewApp`'s walk. `ensureAdminRoutes` pre-checks the import substring and skips, because `wireAggregator` itself errors on an already-present import.
- **Opt-out:** `nexler update -opt-out admin-users|admin-services` (`optout.go`) records the choice in a committed `nexler.json` at the app root (`{"optOut": [...]}`, unknown keys preserved). `ensureAdminRoutes`, `ensureServiceAuth` and `MergeServiceAuth` then never recreate `handlers/admin/users` + `core/users.go` (`admin-users`) or `handlers/admin/services` (`admin-services`). Opting out deletes nothing; `-opt-in` removes the name.

### Database connections (`-db mongo,mysql,postgres,mssql`)

Scoped to connection lifecycle only (open at startup, close at shutdown). No query builder, ORM or migrations.

- **Which drivers are compiled in** is the `-db` value. It governs which `db/*.go.tmpl` files are walked and which blank imports `db/sql.go.tmpl` emits: `github.com/go-sql-driver/mysql`, `github.com/jackc/pgx/v5/stdlib` (pgx purely as a `database/sql` driver), `github.com/microsoft/go-mssqldb` (driver name `"sqlserver"`, see `sqlDriverName`), and `go.mongodb.org/mongo-driver/v2` (own file, `db/mongo.go.tmpl`).
- **Which named connections exist** is pure `.env`/runtime config, never a re-scaffold: `{APPNAME}_DB_CONNECTIONS` lists names; each has `_TYPE` (must be a compiled-in type) and `_DSN`.
- `db/db.go.tmpl`: `Connect()` (called from `main()`, fails fast on any connection) dispatches to `openSQL`/`openMongo`; `Close()` calls `closeSQL()`/`closeMongo()` and joins errors with `errors.Join`. `db.SQL(name)`/`db.Mongo(name)` read mutex-guarded maps.
- `main.go.tmpl` branches on `.HasDB`: with `-db` it gains `db.Connect()` and graceful shutdown (SIGINT/SIGTERM → `http.Server.Shutdown` → `db.Close()`); without it, plain `http.ListenAndServe`.
- `gomod.tmpl` deliberately has no `require` block (nexler can't verify current driver versions). The post-create output tells the user to run `go mod tidy`.

#### The "core" connection and `store/`

- Every `-db` app gets a connection named `"core"` pre-populated in `.env` (`_DB_CONNECTIONS=core`, `_DB_CORE_TYPE`, `_DB_CORE_DSN`). `-core <type>` picks which type (`resolveCoreDB`): automatic with one type, else `mongo` if selected, else the first listed. `.env` stays the only source of truth for connections.
- `-db-host`/`-db-port`/`-db-name`/`-db-user`/`-db-password` build a real DSN (`buildDSN` + `userinfo` in `scaffold.go`): mysql `user:pass@tcp(host:port)/dbname`; postgres `postgres://user:pass@host:port/dbname` (adds `?sslmode=disable` when the password is blank); mssql `sqlserver://user:pass@host:port?database=dbname` (blank credentials mean Windows/Integrated auth); mongo `mongodb://user:pass@host:port/dbname`. A blank host reproduces the blank `_DB_CORE_DSN=` exactly.
- `store/<route>/<pkg>.go.tmpl` renders an informed TODO naming the package matching the core type (`routeData.CoreDBType`, e.g. `mysql.Conn("core")...`). **Do not generate a package-level var/helper calling `db.SQL`/`mongo.Conn`**: package-level vars initialise before `db.Connect()` runs and would always return `ok == false`. Without `-db` the original stub text is kept.

#### `nexler db add`

`internal/scaffold/db.go` → `cmd/nexler/db.go`. Adds a named connection to an existing app and can retrofit a driver type the app wasn't scaffolded with. It never opens a live connection (unlike `nexler init db`); it edits `.env` and, when retrofitting, Go source.

- `<name>` cannot be `"core"` and must match `^[A-Za-z][A-Za-z0-9_]*$`; rejected, never sanitised, since sanitising would break hand-written `db.SQL("<name>")` call sites. The connection is never promoted to core.
- `-host` etc. behave like `create app`'s `-db-host` etc.
- `setEnvLine` upserts any key (replace in place or append). `ensureEnvVars` only appends blank placeholders for a fixed key list, so it can't be used here.
- **Retrofitting a dialect** writes code before `.env` (a crash self-heals into "dialect already enabled"): the helper package always; for mongo a fresh `db/mongo.go`; for SQL either a patched import block in the existing `db/sql.go` (`insertImport` from `route.go`) or a fresh `db/sql.go`.
- `db/db.go` needs touching only when the dialect *family* (SQL vs mongo) is new: rendered fresh if the app had no `-db`, otherwise patched via the `// nexler:db-connect` (before `Connect()`'s `default:`) and `// nexler:db-close` (before `Close()`'s `return errors.Join(errs...)`) markers. Marker insertion, not regeneration, because developers plausibly hand-edit this file.

#### The `mongo` package (`mongo/mongo.go.tmpl`, `HasMongo`)

`db/mongo.go` exposes only the raw `*mongo.Client`. `mongo/` adds simplified helpers on top.

- Addressing is a method chain: `mongo.Conn(name).Database(name).Collection(name)`.
- Operations are **free generic functions taking the `Collection`** (`mongo.Get[User](ctx, coll, filter)`), because Go methods cannot introduce type parameters and generic decoding into the caller's struct was required.
- `Get`/`GetOne`/`Set`/`Update`/`Delete` take `filter any`, the caller's struct (`filterToBSON`): non-zero fields AND together; a `[]T` ORs each element's AND-group (`$or`). Keys come from the `bson` tag, else the lowercased field name. Zero-valued fields are never part of a filter.
- `Set` upserts the **one** matching document (`ReplaceOne` with upsert); `Delete` removes the **one** matching document. `Insert` inserts. `Aggregate` takes a `driver.Pipeline` and is the escape hatch.
- **`InsertID[T](ctx, coll, doc) (bson.ObjectID, T, error)`**: locates `T`'s `_id` field by `bson` tag (recursing into anonymous embedded structs), generates `bson.NewObjectID()` if zero, inserts, and returns it. With no such field it delegates to `Insert` and returns a zero id, never an error.
- **`Update[T](ctx, coll, filter, patch) error`**: builds `$set` from `patch`'s non-zero fields via `structToBSON` and calls `UpdateOne`. Refuses an empty filter and an empty patch. No Mongo-native operators (`$inc`, `$push`); drop to `db.Mongo(name)` for those.
- **`Filter{And, Or []any; Eq map[string]any}`**: composes nested AND/OR out of struct filters, slices and other `Filter`s; `Eq` includes explicit `key:value` pairs verbatim, zero values included. A single `Filter` sets exactly one of `And`/`Or`/`Eq`. `filterToBSON` type-switches on `Filter`/`*Filter` first, so it is a drop-in `filter any`. An app's own pre-existing `Filter` type is never overwritten (`ensureMongoFilterExpression` no-ops).
- `EnsureUniqueIndex(ctx, coll, field)` (partial index scoped to documents where `field` exists, so unset optional fields don't collide) and `EnsureTTLIndex(ctx, coll, field, seconds)`; both safe on every startup.
- **`structToBSON` flattens anonymous embedded fields** into the top-level filter, so `T{Base: common.Base{ID: id}}` builds `{"_id": id}`. Direct-fields-only handling would build `{"base": ...}` and break every by-ID lookup.
- **`store/common/common.go.tmpl`** (`HasMongo`): `Base{ID bson.ObjectID}` tagged `bson:"_id,omitempty"`, embedded anonymously (tagged `bson:",inline"`) in every Mongo domain struct.
- No transactions, no query builder beyond struct filters.

#### The `mysql`/`postgres`/`mssql` packages

**Three fully independent packages** (`templates/{mysql,postgres,mssql}/*.go.tmpl`), each with its own copy of every helper: independence over DRY, deliberately, because placeholders, upsert and id-return differ per dialect.

```go
t := mysql.Conn("core").Table("users")            // or postgres./mssql.
users, err := mysql.Get[User](ctx, t, User{Active: true})
one, err := mysql.GetOne[User](ctx, t, User{ID: id})
err = mysql.Insert(ctx, t, User{ID: id, Name: "x"})
err = mysql.Update(ctx, t, User{ID: id}, User{Name: "y"})  // filter, then patch
err = mysql.Delete(ctx, t, User{ID: id})
rows, err := mysql.Query[Report](ctx, mysql.Conn("core"), "select * from tbl_y")
rows, err = mysql.Call[Report](ctx, mysql.Conn("core"), "my_proc", arg1, arg2)
```

- Same struct-filter convention as `mongo`; columns come from the `db` tag.
- **`Insert` vs `Update` is intentionally asymmetric**: `Insert` writes every mapped field, zeros included; `Update`'s patch uses non-zero fields only. `Update`/`Delete` refuse an empty filter. No `Set`/upsert (no portable single-statement upsert).
- `Query`/`Call` are raw-SQL escape hatches (`Call` supports a single result set).

| | placeholder | `Call` | `InsertID` read-back |
|---|---|---|---|
| mysql | `?` | `CALL proc(?, ?, ...)` | `LastInsertId()` |
| postgres | `$1, $2, ...` | `CALL proc($1, ...)`; functions aren't `CALL`-able, use `Query("SELECT * FROM my_func($1)", ...)` | `RETURNING <col>` |
| mssql | `@p1, @p2, ...` | no `EXEC`/`CALL` text: a bare procedure name is an RPC call, so `Call` is `Query[T](ctx, conn, proc, args...)` | `OUTPUT INSERTED.<col>` |

- `scanRows[T]` matches `rows.Columns()` against `db`-tagged fields; unmatched columns are discarded. `Get`/`GetOne` build an explicit column list, not `SELECT *`.
- No injection risk: only values flow through placeholders; table/column/procedure names come from struct tags and literal arguments authored at compile time.
- **`InsertID[T](ctx, t, doc) (int64, T, error)`**: follows the server-assigned auto-increment integer convention nexler's own schemas use (not client UUIDs). `findIntPKField` locates the field mapping to column `"id"` with an integer kind. When zero it is omitted from the column list (`insertColumnsSkipping`) and read back per the table above; a non-zero value is written and echoed. With no integer `"id"` field it delegates to `Insert` and returns `0`. Composite and non-integer keys aren't supported.

#### The `core` package and `nexler init db`

`core/` (gated on `hasDB`) is app-wide system data behind a stable function API. "Engine-independent" means stable Go function signatures, **not** a Go `interface`: the engine is fixed per app at scaffold time.

- `core/config.go`: `core.GetSetting(ctx, key)` / `core.SetSetting(ctx, key, value)`. Mongo core calls the `mongo` package. SQL core: `GetSetting` is a parameterised `SELECT` (text precomputed by `configGetQuerySQL`); `SetSetting` calls the `core_config_upsert` stored procedure via `{{.CoreDB}}.Call`, a dialect-native upsert (`ON DUPLICATE KEY UPDATE` / `ON CONFLICT DO UPDATE` / `MERGE`) that is safe because nexler owns the schema.
- Also: `core/errorlog.go` (`core.LogError`), `core/kgate_channels.go` (`AddKgateChannel`/`RemoveKgateChannel`/`ListKgateChannels`, generated for every `-db` app), `core/users.go`/`services.go` (also gated on `-auth`), `core/tasks.go`/`audit.go` (Task Registry), `core/apiclientlog.go`.
- **Provisioning is never done by `create app` or app startup.** `nexler init db [-dir]` (`cmd/nexler/initdb.go`) reads the app's `.env` for `{PREFIX}_DB_CORE_TYPE`/`_DSN` and creates `core_config`, `core_error_log`, `core_kgate_channels` (`kgateChannelStatements`: `core_kgate_channel_add`/`_remove` procedures, or a unique Mongo index on `channel`), `core_users`, `core_services`, `core_tasks`, `core_audit_log`, and `tenant_orgs` only if `tenant/tenant.go` exists. Reasons: a database may be shared by several apps, and production apps often run with a DB user that cannot run DDL.
- Every statement is idempotent (`IF NOT EXISTS` / `CREATE OR REPLACE PROCEDURE` / `CREATE OR ALTER PROCEDURE` / unique indexes), so re-running is always safe. Unused tables are zero-cost, so they are provisioned unconditionally.
- This is why all four drivers are unconditional entries in nexler's own `go.mod`.

### `nexler init kpass`: permission-check integration

`cmd/nexler/initkpass.go` → `scaffold.NewKpass` (`kpass.go`). Pure local scaffolding. kpass is a permission-check service with no user profile store.

- Writes `kpass/kpass.go` and appends blank `KPASS_URL`, `KPASS_CLIENT_ID`, `KPASS_API_SECRET` to `.env` if missing (`ensureEnvVars`/`envHasKey`).
- `kpass.Check(ctx, userID, resource, extra)` posts `{user_id, resource, ...extra}` to `{KPASS_URL}/permission/check/` via `apiclient`, with client id/secret as headers, and returns the full `Result{Status, Message, Access, UserRole, UserType}`. It is deliberately not collapsed to a bool; `Allowed(result) bool` is the separate simple helper.
- `Authorise(ctx, code) (*AuthoriseResult, error)`: `GET {KPASS_URL}/api/v1/authorise` with `code`, `client_id`, and `api_secret` (only when set) as query params. Decodes a flat camelCase body into `AuthoriseResult{UserId, UserType, UserName, UserRole, OrgId}` (all strings). It checks `resp.StatusCode != http.StatusOK` before decoding, because this endpoint has no in-body error signal.
- `UserIDFromRequest(r) (userID, ok)` is generated only when the app has real `-auth`; it is a thin wrapper over `auth.Subject(r)`.
- `services/kpass/kpass.go` (`kpass_service.go.tmpl`): the hand-editable `CheckAccess(ctx, userID, resource, extra) (bool, error)` wrapper handlers should call, giving app authorisation logic (caching, overrides, logging) one home.
- `Check`/`Allowed` are not wired into `RequireAuth`. The Task Registry's `PermissionCheck` hook **is** wired: `NewKpass` writes `middleware/permission_kpass.go` (an `init()` setting `PermissionCheck` to `services/kpass.CheckAccess`) when `middleware/task.go` exists.

### `nexler init kgate`: message-broker integration

`cmd/nexler/initkgate.go` → `scaffold.NewKgate` (`kgate.go`). Pure local scaffolding, but **requires a core database connection** (checked via `readCoreDBType`), because channel subscriptions are deliberately dynamic, not env-driven.

Subscriptions:

- `Subscribe(ctx, channel) error` records the channel in the core registry (`core.AddKgateChannel`) and adds it to the shared connection, returning immediately; safe inside a request handler. `Unsubscribe` removes it from the set and registry but does not close the connection (kgate has no per-channel unsubscribe frame). `ResumeAll(ctx)` seeds the set from the registry.
- **One shared WebSocket for all channels**: kgate's gateway allows one live connection per `X-Client-Id`; several evict each other in endless reconnect churn.
- State: `channels map[string]struct{}` behind `subMu`; `connMu` guards the single `*websocket.Conn` (`active`) and serialises writes (gorilla permits one concurrent writer); a `started` bool makes `ensureLoopStarted` a one-time gate. The loop (`subscribeLoop`/`subscribeOnce`) re-sends a subscribe frame for every channel on each (re)connect, so repeat `Subscribe`/`ResumeAll` calls are no-ops.
- Every channel dispatches to a **single** `handleEvent(ctx, channel, payload json.RawMessage) error`, because a restart can recover channel names but not handler closures.

Wiring:

- `Register(mux)` is inserted into `routes/public/public.go` via `wireAggregator(appDir, group, importPath, alias)`. It registers `POST /webhooks/kgate`, documents it with `openapi.Register` (`ReqType: webhookEvent{}`, `ReqContentType: "application/json"`, no `RespType`, `Protected: false`), and starts `ResumeAll(context.Background())` plus `OnStartup` in a background goroutine, logging rather than failing. `main.go` is not touched.
- `HandleWebhook` verifies `X-Signature` (HMAC-SHA256 of the raw body, keyed by `KGATE_WEBHOOK_SECRET`, constant-time compare) before dispatching. Registering the app's public `/webhooks/kgate` URL with kgate is a manual, out-of-band step, and the doc comment says so.
- `Publish(ctx, channel, payload)`: `apiclient.Post` to `{KGATE_HTTP_SERVER}/messenger/publish` with `X-Client-Id` and `Origin`. `publishBody.Payload` is always a JSON-encoded string on the wire.
- Env vars (blank, appended if missing): `KGATE_CLIENT_ID`, `KGATE_WS_SERVER` (the **full** WebSocket URL, dialled as-is), `KGATE_HTTP_SERVER` (base URL), `KGATE_ORIGIN` (no automatic derivation), `KGATE_WEBHOOK_SECRET`.

Resilient delivery:

- Package-level `logger` (`log/slog`, JSON to stdout, `component=kgate`). Logs never include a payload, only channel and message_id.
- Bounded 15s dial timeout (`context.WithTimeout` + `DialContext`), with `X-Client-Id`/`Origin` headers.
- Ping/pong keepalive: 5s ping period, 15s read deadline (kgate sockets die after about 10s of silence).
- `runSubscribeOnce` wraps `subscribeOnce` in `recover()`; `dispatchEvent` recovers a panic from `handleEvent`. `subscribeLoop` retries with capped exponential backoff (1s → 30s).
- An event is acked only after `handleEvent` succeeds. `permanentError`/`permanentf`/`isPermanent` mark failures redelivery cannot fix: acked anyway but logged loudly. Transient failures stay unacked so kgate may redeliver.
- `unwrapPayload` undoes kgate's double encoding (the payload is itself a JSON-encoded string) before `handleEvent`.
- `HandleWebhook` goes through `dispatchEvent`: permanent error → 400, any other error → 500.

Business logic lives in `services/kgate`:

- `kgate/kgate.go` is pure, stable infrastructure exposing two function-variable hooks: `EventHandler` (default `defaultEventHandler`, a no-op) and `OnStartup` (default `defaultOnStartup`, a `Subscribe(ctx, "test")` smoke test that is safe to leave or remove).
- `services/kgate/kgate.go` (`kgate_service.go.tmpl`, written once, hand-editable): `Init(ctx)` (startup subscriptions) and `HandleEvent(ctx, channel, payload) error`. Its `init()` sets `kgateclient.EventHandler = HandleEvent` and `kgateclient.OnStartup = Init`. This is dependency injection to avoid an import cycle: `kgate` must never import `services/kgate`.
- `wireBlankImport` (`route.go`) adds `import _ "{modulePath}/services/kgate"` to `routes/public/public.go` so that `init()` runs. `wireAggregator` can't be reused, since it always pairs an import with a `Register(mux)` call.
- `ensureKgateServiceExtraction` requires `handleEvent` to match one of two known pristine shapes **exactly** (the current `unwrapPayload` stub or the older bare `// TODO: process the event`), else it errors with manual migration steps. Never guess how to relocate hand-written event logic. All kgate retrofits stay well away from `handleEvent`.

### `nexler init kube`: Kubernetes manifest

`cmd/nexler/initkube.go` → `scaffold.NewKube` (`kube.go`). Writes `k8s/deployment.yaml` (refuses if present) with three documents:

- a `Secret` (`<app>-env`, `stringData` from every real `KEY=VALUE` line in `.env`, via `readEnvVars`);
- a `Deployment` (`envFrom: secretRef`, `tcpSocket` readiness/liveness probes on the `.env` port because no generated app exposes a health route, conservative default `resources`);
- a `ClusterIP` `Service` on the same port.

Rules:

- The `image:` must be concrete at scaffold time. `scaffold.ResolveKubeImage(appDir, image, registry, dockerHubUser)` is non-interactive: `-image` wins; `-registry github` derives `ghcr.io/<owner>/<repo>:latest` from `go.mod`'s module path (`readModulePath`), erroring if it isn't `github.com/<owner>/<repo>`-shaped (never shelling out to `git remote`); `-registry dockerhub` derives `<user>/<repo>:latest`, with `<user>` prompted in `cmd/nexler/initkube.go` (`promptRequired`).
- `appServiceName(appDir)` (`sanitizeIdent(filepath.Base(appDir))`, shared with `docker.go`) names every resource, the selector label and the Docker Hub repo.
- `-namespace` is omitted from metadata when blank; `-replicas` defaults to `1`.
- `k8s/deployment.yaml` is added to `.gitignore` (`ensureGitignoreLine`): it embeds real secret values and must never be committed.
- `init ci`'s GitHub Actions workflows are fully static and resolve image names at workflow run time from GitHub context variables.

### `nexler init tenant`: TenantOrg admin API

`cmd/nexler/inittenant.go` → `scaffold.NewTenant` (`tenant.go`), template tree `tenant_templates/`. Adds a guarded tenant listing/delete admin API for a hub-style app. **Not the `-multitenant` flag**: that threads an Org through requests; this stores tenant-organisation records. The two don't reference each other.

- Deliberately not part of `core/` and not a `create app` flag: having a tenant model is a business decision. Requires a core database and a JWT-capable `-auth`, erroring clearly otherwise.
- `tenant/tenant.go`: `TenantOrg{ID, Name, Settings map[string]any, Status, CreatedAt, UpdatedAt}`. `Settings` is a free-form configuration blob. Only `ListTenantOrgs`/`DeleteTenantOrg` exist; there is no Create/Update, so `ID` is caller-assigned. `DeleteTenantOrg` is a real hard delete, unlike `core.RevokeService`'s soft status flip.
- `handlers/admin/tenants/tenants.go`: `GET /admin/tenants`, `DELETE /admin/tenants/{id}`, wired into `routes/protected/protected.go`, both through `middleware.RegisterTask` with `requireAuth` true.
- `tenant_orgs` is a plain table for SQL cores (no procedures); Mongo needs no provisioning.

### `nexler add`: vendoring static assets from npm

`cmd/nexler/add.go`, entirely outside `internal/scaffold`. Vendors a package into `templates/static/vendors/<name>/` of an existing app. Not scaffolding: no embedded template, real network and `os/exec`.

- Runs `npm install <package> --prefix <scratch-tmp-dir> --no-save --no-audit --no-fund` in an `os.MkdirTemp` directory, streaming npm's output live. The app never gets a `package.json` or `node_modules`.
- `parsePackageSpec` accepts only `name`, `name@version`, `@scope/name`, `@scope/name@version`; git/tarball/URL specs, `npm:` aliases and bare `user/repo` are rejected up front.
- `selectSource`: if the package has `dist/`, copy its contents flattened and unfiltered. Otherwise copy the package directory minus junk (`isJunkEntry`): directories `node_modules`, `.git`, `.github`, `test(s)`, `__tests__`, `docs`, `example(s)`; files `package.json`, `package-lock.json`, `.npmignore`, `.gitignore`, `.gitattributes`, `.editorconfig`, `.babelrc`, `.npmrc`, `tsconfig.json`, and anything case-insensitively prefixed `README`/`CHANGELOG`/`LICENSE`/`CONTRIBUTING`/`HISTORY`/`AUTHORS`/`NOTICE`.
- `copyTree` uses `filepath.WalkDir` and copies bytes verbatim. **Do not reuse `processFile`/`render`**: a minified bundle can contain a literal `{{`. Symlinks are skipped.
- `<name>` defaults to the bare package name with scope stripped and `/` → `-` (`defaultVendorName`, e.g. `@popperjs/core` → `popperjs-core`), overridable with `-as`, validated against `vendorNameRe` (`^[A-Za-z0-9][A-Za-z0-9._-]*$`).
- **Re-running always refreshes**: `os.RemoveAll(destDir)` then recreate, no `-force`. Accepted trade-off: a failure mid-copy leaves a half-copied directory with the old version gone.
- `templates/static` is embedded into the generated app at its own build time, so a vendored package isn't served until the app is rebuilt; the success message says so.

### HTML responses (`-response html`)

A route with `-response html` calls `response.HTML(w, r, module, name, title, data)`. `module` is the route's `RelDirSlash`; `name` is `routeMethod.HTMLContentName` (`<name>-content`, or `<name>-<verb>-content` when one invocation generates several methods), keyed off the identifier base (`strings.ToLower(name)`), so `-name` also disambiguates content files.

- **Read from disk on every request, not from `templateFS`.** `resolveHTMLFile(module, filename)` checks `templates/html/<module>/<filename>`, then `templates/html/shared/<filename>`. Files are written at scaffold time; a missing file is a clear error naming both paths, never a recreated placeholder.
- `-layout shared` (default) uses `templates/html/shared/layout.html` (`writeSharedHTMLLayout`); `-layout module` gives the route its own copy, which wins at request time. `writeHTMLTemplates` runs from both `NewRoute` and `addToExistingRoute` and only adds what is missing. Placeholders: `defaultHTMLContent`/`defaultHTMLLayout`.
- The default layout references `/static/css/styles.css` and `/static/js/site.js`, which are embedded and served by `templates.RegisterStatic`.
- Content is rendered first with `html/template` (auto-escaped `{{.Title}}`/`{{.Data}}`), then embedded as `template.HTML` into the layout's `{{.Content}}`. There is no `{{define "content"}}` convention.
- Page data also carries `{{.Subject}}` (`auth.Subject(r)`; `""` on `-auth none`) and `{{.Path}}`.
- `composeHTML(r, module, name, title, data) ([]byte, error)` is shared by `HTML` (200), `HTMLError(w, r, module, status, title, data)` and `Unauthorised(w, r, module, data)` (403), which render `error-content.html`/`unauthorised-content.html` and fall back to the JSON envelope if the page can't render. A ≥ 500 goes through `core.LogError` on `-db` apps.
- Optional partials `header.html`/`sidebar.html`/`footer.html` resolve through the same hierarchy and are injected as `{{.Header}}`/`{{.Sidebar}}`/`{{.Footer}}` by `renderOptionalPartial`; a missing one renders as an empty string, not an error.
- `create app -ui` serves `GET /` this way (`response.HTML(w, r, "home", "home", ...)`, `writeAppUIHomepage`, always the shared layout) and skips generating the embedded `templates/html/home.html` and `static/css/home.css`. Without `-ui` the homepage is `templates.ServePage("home.html")`.
- OpenAPI: `Operation.RespContentType = "text/html"` makes the `"200"` response a plain string body.

### Route wiring via marker comments

Markers later invocations anchor on:

- `// nexler:routes` in `routes/public/public.go` / `routes/protected/protected.go`: route imports and `Register(mux)` calls (`wireAggregator`).
- `// nexler:handlers` in a handler file: new per-method handler functions. May appear in several files per package.
- `// nexler:register` in a handler file: new `mux.HandleFunc`/`openapi.Register` calls. The file holding it is the package's **primary** file; by default exactly one per package, since Go allows one `func Register`.
- `// nexler:models` in a model file: new Request/Response struct pairs.
- Elsewhere: `// nexler:db-connect`, `// nexler:db-close` (`db/db.go`), `// nexler:tasks-sync` (`main.go`).

Extending an existing route (`addToExistingRoute`, chosen when a file with `// nexler:register` exists under `handlers/<relDir>`):

- Adds only newly requested methods, skipping ones already registered. Detection is an exact `"<VERB> <route>"` string in the **primary** file only; checking other files could let a duplicate pattern through and panic `mux.HandleFunc` at runtime.
- Leaves `services/`/`store/` untouched unless a layer is explicitly requested.
- **`-file`** names the handler/model file targeted (default the package name). If it exists (with `// nexler:handlers`), methods are appended. If not, a new handler+model pair is rendered from `handler_file.tmpl` (no `Register`, no `middleware`/`openapi` imports, trailing `// nexler:handlers`). Registration wiring always goes into the primary file.
- The aggregator (`public` vs `protected`) is decided at the package's first creation and never changes. `-protected` on a later call governs only the new methods, so a package can mix protected and public methods. `detectExistingProtection` only powers an informational `RouteResult.Note`.
- All checks run before any file is written: no partial writes.

#### `-own-register`: a second independent resource in a package

With `-file` (a new name) and `-name`, `-own-register` creates an independent resource instead of folding into the primary: its own `Register<Name>` (same `handler.go.tmpl`, via `{{ .RegisterFuncName }}`, default `"Register"`), imports, markers, and model/service/store files (`addOwnRegisterResource`). `wireAggregatorAdditionalCall` adds one more `<alias>.Register<Name>(mux)` under the existing import. Example shape: `email` and `sms` sharing `handlers/admin/providers`, with `RegisterSms`.

- Primary-file resolution checks `-file`'s own target for `// nexler:register` first, before any directory scan, so a later method for `sms` lands in `sms.go`'s `RegisterSms`.
- A directory with two or more independent resources, given a new `-file` and **no** `-own-register`, is a hard error listing every existing resource.

#### `-name`: cross-invocation identifier collisions

Identifiers (`routeMethod.HandlerName`/`ReqTypeName`/`RespTypeName`/`OperationID`) are built from `-module`/`-submodule` + verb, computed once in `NewRoute` and referenced by the templates. The URL never feeds an identifier. A second route with the same module/submodule/verb would therefore duplicate declarations or `OperationID`s.

- `detectIdentifierCollisions` runs before any write, checking the concatenated content of **every** `.go` file under `handlers/<relDir>` and `models/<relDir>` (`concatGoFiles`), and aborts naming every colliding identifier. It also guards `-own-register`'s `Register<Name>`.
- The fix is `-name` (`RouteConfig.IdentName`), overriding only the identifier base, not file/package location. E.g. adding `/purchase/new` beside `/purchase/verify` (both POST) needs `-name New` → `HandleNewPost`/`PostNewRequest`/`"postNew"`.

### Optional service/store layers

- `-service`/`-store` (`resolveLayerRef`) accept a reference to reuse an existing package, or `"none"` to skip the layer. `routeData.HasService`/`HasStore` drive the three-way TODO wording in `handler.go.tmpl`/`handler_methods.tmpl`/`service.go.tmpl`, so comments never point at a layer that doesn't exist.
- **Adding a layer later**: re-run `nexler create <route>` with `-service`/`-store` passed explicitly (even empty, meaning "generate fresh"). This is gated on `RouteConfig.ServiceRequested`/`StoreRequested` (from `flag.FlagSet.Visit`), **not** the resolved value: a scripted `-methods POST` with neither flag must never start generating layers a handler-only route never had. `addToExistingRoute` now requires "a new method *or* a layer to add".
- `HasService`/`HasStore` are re-detected from disk (`dirHasGoFile`) and flipped to `true` before method fragments render when a retrofit happens in the same call. Files are written by the shared `writeRouteLayerFile`. Result: `RouteResult.LayersAdded`.

#### Standalone layers (`nexler create store|service <name>`)

`internal/scaffold/layer.go` `NewLayer`, dispatched from `runCreateLayer`. Scaffolds a plain unwired package: no handler, model, route or aggregator wiring, and nothing tracks it afterwards.

- `<name>` is `module[/submodule]`, parsed like a reuse reference (`sanitizeIdent` per segment).
- `-file` overrides the file base name; the default is `<pkgName><kind>` (`services/apps/appsservice.go`, `store/apps/appsstore.go`). A route's own layers keep plain `<pkgName>.go`.
- Reuses `service.go.tmpl`/`store.go.tmpl` with `routeData.RouteLabel` (`"this package"` when `Route` is empty) and `routeData.Standalone` (points the "no store linked" TODO at `nexler create store ...`).
- `nexler create service <name> -store <ref>` links the TODO to an existing store (informational). A standalone service without `-store` gets `HasStore: false`.

### Per-route code generation (`route.go`)

- `RouteConfig` → `routeData`/`routeMethod` precompute every identifier in Go (handler names, `RegisterExpr`, `OperationID`, content types, doc phrasing) so templates just range over `.Methods` with minimal conditionals.
- `routeMethod.Protected` is tracked per method and feeds both `RegisterExpr` and `openapi.Register`'s `Protected`.
- GET always gets a query-only request struct; other methods follow `-body json|form|multipart`. `-response json|html` is route-level (`routeData.HTMLResponse`).
- OPTIONS is wired automatically via `middleware.HandleOptions` (always 200) and cannot be requested; `parseMethods` rejects it.
- `-module`/`-submodule` are sanitised to letters/digits (`sanitizeIdent`). The import alias is the concatenation (e.g. package `verify`, alias `purchaseverify`, files under `purchase/verify/`).

### The Task Registry (`middleware.RegisterTask`, `task` package)

Baseline infrastructure generated for every app (`task/task.go.tmpl`, `middleware/task.go.tmpl`), ported from ctrl-svc's Task Registry. Every generated route, including the admin and tenant APIs, is wired through:

```go
middleware.RegisterTask(action string, requireAuth bool, reqType, respType any, pathParams ...string) func(http.HandlerFunc) http.HandlerFunc
```

- `task/task.go` is an in-process registry of route contracts, not a job queue: `Task{Name, RequireAuth, PathParams, ReqType, RespType, Executor}` with `Register`/`Get`/`All`/`Run`. `Executor` is nil for auto-registered routes; `Run` works only for a task registered with a real `Executor`.
- `RegisterTask` calls `task.Register` (idempotent) each time a package's `Register(mux)` runs, then wraps the handler: `middleware.RequireAuth` when `requireAuth`, then `PermissionCheck` if set, then a best-effort audit log via `core.LogAudit` (core-database apps only) recording the caller (`auth.Service` takes precedence over `auth.Subject`), `X-Correlation-Id` (echoed or generated), `X-Session-Id`, and path parameter values.
- `RegisterExpr` is the whole precomputed call. `taskActionName(module, name, pkgName, verb)` derives the registry key `"<module>.<resource>.<verb>"` (e.g. `"purchase.verify.post"`, `"admin.sms.post"`); `task.Task.Name` is safe to hand-edit. Hand-authored admin files use the same convention (`"admin.users.list"`, `"admin.services.revoke"`).
- **`middleware.PermissionCheck`** (`func(ctx, subject, action string) (bool, error)`, nil by default): when set, `RegisterTask` denies with 403 on `false`/error. Set by `nexler init kpass` through `middleware/permission_kpass.go`, so `middleware/task.go` is never rewritten.

Core-database apps:

- `core/tasks.go`: `core.SyncTasks` mirrors `task.All()` into `core_tasks`, using the exported `openapi.JSONSchema` for `ReqSchema`/`RespSchema`. `core/audit.go`: `core.LogAudit` writes `core_audit_log`.
- `main.go` calls `core.SyncTasks` right **after** `routes.Register(mux)` (routes register their tasks inside `Register`; calling it before syncs nothing), best-effort and logged, behind the `// nexler:tasks-sync` marker.
- `pruneStaleTasks` removes this service's own entries for tasks it no longer registers. It matches on `service`, never touches another service's entries or entries without `service`, and does nothing when the registry is empty.

Calling a task over HTTP:

- `core_tasks` also records `service` (app name), `method`, `path`, `base_path` (`{APPNAME}_API_BASE_PATH`) and `callable` (Mongo core only; the SQL `core_task_upsert` procedure keeps its original columns).
- `RegisterTask` doesn't know a route's method/path, so generated `openapi.Register` calls carry `Task: "<action>"` (plus `Callable: true` with `nexler create <route> -callable`). `core.SyncTasks` joins task to operation by that name (`operationFor`), falling back to the one operation with identical `ReqType`/`RespType`.
- A service-key caller may send `X-Acting-Subject` (audit `acting_subject`, `auth.ActingSubject`) and, on a `-multitenant` app, `X-Org-Id`, honoured only when its `core_services` doc has `acts_for_orgs` (`core.ServiceActsForOrgs`/`SetServiceActsForOrgs`), else 403. Both are handled in `middleware/service_caller.go` (`serviceCallerContext`).
- **`callable` is seeded, not enforced**: `SyncTasks` looks up the task's existing `core_tasks` entry and keeps its stored `callable`; code only sets it for a task with no entry (so a database change survives restarts). Mongo core only. Retrofit: `ensureTaskCallableFromDB` (`taskmetadata.go`, runs after `ensureCoreTaskMetadata`).
- These retrofits insert or patch by anchor and never fully regenerate, because `openapi.go` is hand-extended in some apps.

### Dynamic path parameters (`{name}` segments)

Go 1.22+'s `ServeMux` routes `"METHOD /path"` patterns with `{name}` and a trailing `{name...}` natively, so `cfg.Route` is passed to `mux.HandleFunc` verbatim.

- `parsePathParams` (`route.go`) extracts each `{name}` at scaffold time: must be a legal Go identifier (`pathParamNameRe`), no duplicates, `{name...}` only as the last segment. It fills `routeData.PathParams`/`HasPathParams`/`PathParamArgs` (a Go source literal like `"id", "code"`)/`PathParamsCSV`.
- A literal `?` or `#` in a route is rejected with an error pointing at `{name}`: `ServeMux` would treat it as literal path text and never match.
- Every handler method for such a route calls `request.DecodePath(r, &req, <names...>)` right after its primary decode (guarded by `{{if $.HasPathParams}}`, all methods). It reads `r.PathValue(name)` and uses the same `populate` helper, so a path parameter matches the Request struct by `json` tag (one `ID string \`json:"id"\`` field receives either source).
- Models never auto-generate fields (`// TODO: add fields.`); the Request struct's doc comment names the route's path parameters and points at `request.DecodePath`.
- OpenAPI: `pathParamNames` parses `op.Path` and documents each as `in: "path", required: true` for every method. For GET, `queryParams` takes a skip-set of those names so a field isn't listed as both path and query.
