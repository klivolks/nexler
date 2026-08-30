# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`nexler` is a scaffolding CLI (like `django-admin startproject` or `rails new`) for a Go web
framework of the same name. It does not run applications itself — it generates the
handlers/services/store/models project layout that apps built on nexler follow. Every template
file is embedded into the binary via `go:embed` (see `internal/scaffold/templates.go`), so
scaffolding itself — everything under `internal/scaffold`, driven by `create app`/`create <route>`/
`init kpass`/`init kgate` — is pure local file I/O: no network access, no shelling out to git or
any other external process. `-db` (see "Database connections" below) is a *scaffold-time* choice
with a *build-time* consequence, not an exception to this: the generated app gets real
third-party driver dependencies in its `go.mod`, needing one `go mod tidy` (network access, on the
user's machine) before its first build — nexler itself still never touches the network to produce
the app.

Two commands are real exceptions, where nexler's own process does impure work beyond local files —
both self-contained in `cmd/nexler`, deliberately outside `internal/scaffold`, so its "pure,
embed-only" scaffolding claim above stays literally true: `nexler init db` (a live database
connection, to provision the "core" schema — see "`nexler init db`" below) and `nexler add` (a
live `npm install`, to vendor a static asset package — see "`nexler add`" below). Both are
explicit, separately-invoked commands — never run implicitly by `create app`/`create <route>`.

## Commands

```
go build -o nexler.exe ./cmd/nexler   # build the CLI
go run ./cmd/nexler <args>            # run without building
go vet ./...                          # static checks
```

There are no automated tests or Makefile in this repo currently — verify changes by building and
running the CLI against a scratch directory, e.g.:

```
go run ./cmd/nexler create app demo -dir /tmp/scratch
cd /tmp/scratch/demo && go build ./...
go run ./cmd/nexler create /purchase/verify -module purchase -submodule verify -methods GET,POST -body json
```

CLI usage (also printed by `nexler help`):

```
nexler create app <name> [-dir <output-dir>] [-module <module-path>] [-ui] [-auth none|jwt|session|both] [-remember-me] [-db mongo,mysql,postgres,mssql] [-core <type>]
nexler create <route> -module <name> [-submodule <name>] [-dir <app-dir>] [-protected] [-methods GET,POST] [-body json] [-response json]
nexler create store <name> [-file <name>] [-dir <app-dir>]
nexler create service <name> [-file <name>] [-dir <app-dir>] [-store <name>]
nexler db add <name> -type <mongo|mysql|postgres|mssql> [-dir <app-dir>] [-host] [-port] [-dbname] [-user] [-password]
nexler add <package>[@version] [-dir <app-dir>] [-as <name>]
nexler version
```

## Documentation site (`docs/`)

`docs/` (`index.html`, `css/theme.css`, `js/site.js`, `assets/logo.png`) is a standalone, static
HTML/CSS/JS site — no build step, no dependency on anything else in this repo — that publishes
Nexler's end-user guide: installation, every command and flag, the generated-app architecture, and
a running changelog. It exists to be published to klivolks.com once Nexler ships; until then it's
just opened as a local file. Its design (tokens in `docs/css/theme.css`: brand blue `#22A7F0`, navy
`#0E2A44`, the `0 32px 0 32px` signature corner radius, Space Grotesk/Inter/JetBrains Mono) is
deliberately adapted from the klivolks.com marketing site's own theme, so it reads as one family
with the rest of klivolks.com rather than a generic docs template.

**Keep it current.** Whenever a command, flag, default, or documented behavior changes in this
repo, update the matching section of `docs/index.html` in the same change, and append a dated entry
to the Changelog section (`#changelog`) describing what changed — newest entry first. Source every
fact from the actual Go code (`cmd/nexler/*.go`'s flag definitions and `printUsage()`, plus the
relevant `internal/scaffold/*.go`), not from this file's own prose, which can and has drifted out of
sync with the CLI (e.g. the compact usage block above omits `init db`/`init kpass`/`init kgate`/
`init docker`/`init ci`/`update` entirely, and is missing several `create app`/`create <route>`
flags — don't propagate that same gap into `docs/`).

## Releasing

Building/releasing the nexler CLI itself (not any app it scaffolds) is deliberately **not** a raw
tag-push pipeline — it only fires when a human publishes a GitHub Release targeting the `release`
branch specifically (Releases → Draft a new release → pick/create a tag → **Target: release** →
Publish release). `.github/workflows/release.yml` listens for `release: types: [published]`, and
its `goreleaser` job's `if: github.event.release.target_commitish == 'release'` skips the *entire*
pipeline (both installer jobs cascade-skip via their `needs: goreleaser`) for a release drafted
against any other branch — so cutting a release from `main` or a feature branch by mistake does
nothing, silently and safely, rather than half-publishing artifacts.

Once that gate passes: `goreleaser` (`.goreleaser.yml` — cross-compiles windows/darwin ×
amd64/arm64, checksums, an auto-generated changelog) uploads onto the release that already exists
(the human just published it — `release.mode: keep-existing` is what makes goreleaser attach
rather than try to create a competing one for the same tag; `replace_existing_artifacts: true`
lets a re-run after a failure overwrite instead of erroring), then two more jobs build and attach
the platform installers to that same release: `installer/windows/nexler.iss` (Inno Setup, on
`windows-latest`) and `installer/macos/build-pkg.sh` (`pkgbuild`/`productbuild`, on
`macos-latest`) — see each `installer/*/BUILDING.md` for local build/test instructions and known
limitations (no code-signing cert exists yet, so both installers hit first-run OS friction —
SmartScreen on Windows, a Gatekeeper block on macOS). Local dev builds are unaffected by any of
this: `go build -o nexler.exe ./cmd/nexler` still works exactly as documented above;
`cmd/nexler/main.go`'s `cliVersion` is a package-level `var` specifically so release builds can
override it via `-ldflags "-X main.cliVersion=<version>"` while a plain `go build` keeps its
literal fallback.

### Artifact signing (cosign)

Every job also [cosign](https://github.com/sigstore/cosign)-signs what it uploads, using a
key-pair (not keyless/OIDC signing — a deliberate choice for a small project, no Fulcio/OIDC
identity-provider wiring needed): the `goreleaser` job signs `checksums.txt`
(`.goreleaser.yml`'s `signs:` — signing just the checksum file, not each of the 4 raw archives
individually, is the standard cosign+goreleaser pattern, since verifying it and then checking each
archive's own sha256 against it transitively covers everything with one signature); each installer
job signs its own installer directly (`nexler-setup-*.exe`, `Nexler.pkg`), since those are built
outside goreleaser and aren't covered by `checksums.txt` at all. Output is a `<file>.bundle`
Sigstore bundle (signature + public-key hint + a Rekor transparency-log entry, all in one JSON
document) — **not** a bare `.sig` file: cosign v3's `sign-blob` dropped the old
`--output-signature`/`--output-certificate` flags entirely in favor of `--bundle`, which is also
why signing requires real network access (it logs to the public Rekor transparency log by
default) — reasonable and expected for an open-source project's release artifacts, not a bug to
suppress.

`cosign.key` (the private key) and its password are **not** in this repo — generated once locally
(`cosign generate-key-pair`) and deliberately gitignored (see `.gitignore`'s comment). CI reads
them from two GitHub Actions repository secrets, already created (Settings → Secrets and
variables → Actions), named with a nexler-specific prefix/suffix rather than a generic `COSIGN_*`
— deliberate, since this project's signing key is its own, not shared with any other repo/app
under the same account (see the security tradeoff discussion this was decided from: a shared
cross-project key means one leak compromises every project signed with it):
- `COSIGN_NEXLER_KEY` — the full contents of the local `cosign.key` file, pasted as-is.
- `NEXLER_PASSWORD` — the password that encrypts it. Read into each job's environment under the
  literal name `COSIGN_PASSWORD` (renamed at the `env:` step level, not the secret name itself) —
  cosign specifically looks for a variable with that exact name to auto-decrypt the key without an
  interactive prompt; each job also deletes its own written-out `cosign.key` file immediately after
  signing, defense-in-depth on top of the runner already being ephemeral.

`cosign.pub` (the public key) **is** committed at the repo root — it's what anyone verifying a
downloaded release needs, and has no secrecy requirement. To verify, e.g., the checksums:

```
cosign verify-blob --bundle checksums.txt.bundle --key cosign.pub checksums.txt
```

(then check each downloaded archive's own `sha256sum` against the matching line in
`checksums.txt`) — or, for an installer, `cosign verify-blob --bundle nexler-setup-1.2.3.exe.bundle
--key cosign.pub nexler-setup-1.2.3.exe` directly.

Licensed MIT (`LICENSE`, repo root) — both installers show it as its own wizard page
(`nexler.iss`'s `LicenseFile=`; `distribution.xml.tmpl`'s `<license>`), and `build-pkg.sh` copies it
alongside `README.md`/`welcome.html` into the macOS build's resources. `installer/nexler-icon.svg`
is Nexler's own app icon (a plain "N" monogram, deliberately distinct from
`internal/scaffold/templates/templates/static/logo.svg`, which is the generic starter logo
scaffolded *into apps built with* nexler, not nexler itself) — `installer/windows/nexler.ico` and
`installer/macos/nexler-1024.png` are both regenerated from it via `build/gen-icons` (see each
`installer/*/BUILDING.md`'s "Regenerating..." section). The Windows wizard's own banner/corner
images (`installer/windows/wizard-image.png`, `wizard-small-image.png` — Inno's recommended
192x386/76x80 for `WizardStyle=modern`, set via `nexler.iss`'s `WizardImageFile`/
`WizardSmallImageFile`) come from two more dedicated sources, `installer/wizard-banner.svg`/
`wizard-small.svg`, each authored at its own exact target aspect ratio rather than stretching the
square app icon into a non-square slot — `build/gen-icons` was extended (its `<width>x<height>`
size argument form) specifically to rasterize these non-square shapes without distortion.

Every flag above is optional in a stronger sense than usual: any flag not passed on the command
line is asked for interactively instead (`cmd/nexler/prompt.go`), Enter accepting the same default
shown in this usage text — so passing every flag (scripts/CI) is fully non-interactive, and passing
none walks through a Q&A. `runCreateApp`/`runCreateRoute` use `fs.Visit` after `fs.Parse` to tell
which flags were actually set vs left at their zero value, and only prompt for the ones that
weren't. `-ui`/`-response` prompt as "ui/api?" (`promptUIOrAPI`); `-auth` prompts as an N-way
choice (`promptChoice`); `-db` is gated behind its own `promptBool("Use a database?", false)` —
defaulting to *no* database, since basic/`-ui` apps don't need one — and only asks which type(s)
(the plain free-text `prompt()` helper, comma-separated, same pattern as `-methods`, defaulting to
`"mongo"`) if that gate is answered yes; `-core` (which `-db` type is the app's default
connection) only prompts when `-db` selected more than one type — automatic otherwise. The
request-body-shape prompt (routes), the remember-me prompt, and the core-database prompt
(both apps) are all skipped entirely when they wouldn't apply (GET-only methods; `-auth`
not including session; only one `-db` type selected) — never ask a question whose answer
can't matter.

## Architecture

### Two embedded template trees, two code paths

- `internal/scaffold/templates/` (embedded as `templateFS`) — the full skeleton for a brand-new
  app: `main.go`, `routes/`, `middleware/`, `openapi/`, `handlers/home/`, static assets. Driven by
  `scaffold.NewApp` in `scaffold.go`, walking the tree with `fs.WalkDir` and rendering every
  `*.tmpl` file through `text/template`.
- `internal/scaffold/route_templates/` (embedded as `routeTemplateFS`) — per-route stubs
  (handler/service/store/model), `handler_file.tmpl` (a secondary handler file with no `Register`
  func — see "Idempotent route wiring via marker comments" below), plus three *fragment*
  templates (`handler_methods.tmpl`, `register_methods.tmpl`, `model_methods.tmpl`) used to
  extend an existing route. Driven by `scaffold.NewRoute` / `addToExistingRoute` in `route.go`.

Only files ending in `.tmpl` are parsed by `text/template` (see `processFile` in `scaffold.go`);
everything else is copied through verbatim. This matters because generated Go source can contain
a literal `{{` (e.g. `[]map[string][]string{{"a": nil}}`) that would otherwise be misparsed as a
template action. `gomod.tmpl` is special-cased to `go.mod` on output — a real `go.mod` can't live
inside `templates/` because the Go toolchain would treat it as a module boundary and break the
`go:embed` of the nexler CLI itself. `dotenv.tmpl` is likewise special-cased to `.env` — a literal
`.env.tmpl` would never be embedded in the first place, since `go:embed` silently excludes any file
whose name starts with `.` (or `_`). Both special cases live in `destPath` in `scaffold.go`.

All rendered output is normalized to LF regardless of source line endings (`render()` in
`scaffold.go`), because the route-wiring logic below depends on exact-byte anchor matching.

### Generated app layout

Apps produced by `nexler create app` follow a fixed layered structure (mirrors "kGate"'s proven
split, per the `scaffold` package doc comment):

```
handlers/    HTTP handlers — decode request, (TODO) call service, write response
services/    business logic, orchestration
store/       persistence via the Store interface
models/      request/response and domain structs (struct-tag validated)
middleware/  auth, logging, recovery, etc.
routes/      routes.go (calls public+protected Register once from main.go)
             routes/public/public.go, routes/protected/protected.go — aggregators
openapi/     in-process OpenAPI registry, reflects Request/Response struct field
             (json) tags into /openapi.json at request time — no file on disk to
             keep in sync
request/     decode helpers a handler calls into its Request struct
response/    JSON envelope helpers a handler calls to write its result
apiclient/   fetch/axios-style helpers (Get/Post/Put/Delete) for calling other
             HTTP APIs — see "The apiclient package" below
config/      loads {APPNAME}_-prefixed env vars (+ optional .env) once, at
             package-init time — see "Runtime configuration" below
.env         editable starting point for the app's env vars (HOST/PORT, and
             JWT_SECRET if -auth jwt|both, and a pre-populated "core" DB
             connection — DB_CONNECTIONS/DB_CORE_TYPE/DB_CORE_DSN — if -db
             is set)
auth/        only generated for -auth jwt|session|both — see "Authentication"
             below
db/          only generated for -db mongo|mysql|postgres|mssql (any subset)
             — see "Database connections" below
core/        only generated when -db is set — app-wide system data (Config,
             error log, and the kgate channel registry today; session log/
             API keys planned) — see "The core package" below
kpass/       only added by `nexler init kpass`, run separately after
             `create app` — see "nexler init kpass" below
kgate/       only added by `nexler init kgate`, run separately after
             `create app` (needs -db) — see "nexler init kgate" below
```

`main.go` calls `routes.Register(mux)` once and is **never edited again** after the initial
scaffold — new routes are added purely by editing `routes/public/public.go` or
`routes/protected/protected.go`, which `nexler create <route>` does automatically.

### Request decoding and response envelope

Every generated handler method decodes into its Request struct via one `request` package call —
precomputed per method as `routeMethod.DecodeCall` in `route.go` (`decodeCall`), so
`handler.go.tmpl`/`handler_methods.tmpl` just emit it rather than branching on verb/body kind:
`DecodeQuery` for GET (always query-params-only, regardless of `-body`), else whichever of
`DecodeJSON`/`DecodeForm`/`DecodeMultipart` matches `-body`. `DecodeForm`/`DecodeMultipart`/
`DecodeQuery` populate a struct's exported fields via reflection, matched by each field's `json`
tag (the same tag `openapi`'s schema reflection reads) — string/bool/int/uint/float kinds and
slices of those are supported; a `*multipart.FileHeader` field is populated from an uploaded file
of the same name instead.

Every handler writes its result via the `response` package. By default (`-response json`, every
existing route) that means the JSON envelope: `response.JSON(w, status, v)` → `{"data": v}`,
`response.Error(w, r, status, msg)` → `{"error": msg}`. `Error` takes the request (not just the
response writer) because on a `-db` app it also auto-logs every 5xx it writes to the core database
via `core.LogError`, best-effort — the same logging `middleware.Recover` already does for a
recovered panic (see "Database connections" below). 4xx responses (e.g. the standard 400 a handler
writes on a decode/validation failure) are never logged — that's the client's fault, not the
server's. `openapi.Spec` documents the envelope shape: a `RespType`'s schema is nested under `data`
in the `"200"` response, and every operation gets a shared `Error` component schema referenced as
its `"default"` response — see the `schemas["Error"]` entry and the `data`-wrapping in
`openapi.go.tmpl`'s response-building block. Keep these three in sync if either changes: `request`'s
decode helpers, `response`'s envelope shape, and `openapi.go.tmpl`'s documented shape.

Handlers only decode-then-respond; the actual service call is left as a `// TODO: call <pkg>'s
service with req` comment, since `services/<pkg>.go`'s single `Do<Name>()` function isn't
parameterized per HTTP method (unlike handlers/models) — wiring it in is a separate, not-yet-done
step.

### The `apiclient` package

`apiclient/apiclient.go.tmpl` provides fetch/axios-style helpers for calling other HTTP
APIs — `Get(ctx, url, headers)`, `Post(ctx, url, headers, body)`, `Put(ctx, url, headers,
body)`, `Delete(ctx, url, headers)`, all returning `(*Response, error)`. Unlike
`request`/`response` (decoding/writing *this* app's own HTTP traffic), `apiclient` is for
the reverse direction — calling out to other services, the `apiclient` layer
`scaffold` package's own doc comment already names alongside `store`/`broker`. Always
generated (pure stdlib `net/http`, no third-party dependency), so unlike `db`/`auth`
there's no flag gating it — every app gets it.

Every call is synchronous by design ("await" semantics): the shared internal `do()`
builds the request, calls the package-level `Client` (`&http.Client{Timeout: 120 *
time.Second}`, swappable), then `defer resp.Body.Close()` + `io.ReadAll`s the body
*before* returning — the caller only ever sees a `*Response{StatusCode, Header, Body
[]byte}` (plus a `.JSON(v any) error` decode helper), never an `*http.Response` it could
forget to close. This is what "honour close connection after data is received" means in
Go terms: the call blocks until the body is fully drained and the underlying connection
is released back to `Client`'s pool for keep-alive reuse, rather than leaving that to a
caller-managed `Close()` that's easy to forget or defer too late.

`Post`/`Put`'s `body any` is encoded before sending: `[]byte`/`string` are sent verbatim
(no `Content-Type` guess); anything else is `json.Marshal`-ed with `Content-Type:
application/json` set unless `headers` already sets one; `nil` means no body. `headers`
(a plain `map[string]string`, nil is fine) is applied after any inferred `Content-Type`,
so an explicit header always wins over the inferred one.

### Runtime configuration (`config`, `.env`)

`main.go` gets its listen address from `config.C.Addr()` instead of a hardcoded
`const addr`. `config.C` (`config/config.go`, package-level `var C = load()`, so it's
ready before `main()` runs with no explicit call needed) currently holds `Host`/`Port`
(read from two env vars namespaced per-app as `{APPNAME}_HOST`/`{APPNAME}_PORT`, e.g. `-module
my-app` → `MY_APP_HOST`/`MY_APP_PORT` — the prefix is computed once, at scaffold time, by
`envPrefix` in `scaffold.go` (uppercases letters, keeps digits, replaces everything else with
`_`) and baked into `config.go.tmpl`/`dotenv.tmpl` as `{{ .EnvPrefix }}`) plus `SwaggerEnabled`
(see below). This is meant to grow: the package doc comment on `config.go.tmpl` says more
settings (flags, etc.) land here later as `Config` gains fields — don't reach for a different
mechanism for the next env-driven setting, extend `Config`/`load()` instead. `getEnvOr`
(strings) and `getEnvBoolOr` (`"true"/"1"` → true, `"false"/"0"` → false, anything else/empty →
the fallback) are the two parsing helpers `load()` uses; a future non-string/non-bool setting
gets its own `getEnv<Type>Or` alongside them, same pattern.

`.env` (scaffolded at the app root, `{APPNAME}_HOST=` / `{APPNAME}_PORT=8080` — empty `HOST`
means "all interfaces", preserving the old hardcoded `:8080` default) is read by `config.go`'s
hand-rolled `loadDotEnv` (`bufio.Scanner` over `KEY=VALUE` lines — no third-party dependency,
consistent with the generated `go.mod` having none). A key already present in the real
environment is never overwritten by `.env` — real env vars always win, so `.env` is just a
committed-or-not local default, not an override mechanism.

**`{APPNAME}_SWAGGER_ENABLED`** (`Config.SwaggerEnabled`, default `true`, pre-populated in
`.env`) gates `GET /swagger` (the bundled Swagger UI, `templates/swagger.html`) and
`GET /openapi.json` (`HandleOpenAPI`, which reflects `openapi.Spec()` fresh on every request) —
set to `false` in production to 404 both. Both routes are still *registered* unconditionally in
`handlers/home/home.go`'s `Register`; the toggle is checked **inside each handler**
(`HandleOpenAPI`, and a new `HandleSwagger` wrapping `templates.ServePage("swagger.html")`),
writing `http.NotFound` when disabled — deliberately not "just don't register them," because
`GET /` (the homepage route, registered by every app regardless of `-ui`) is a *subtree* pattern
in Go's enhanced `net/http.ServeMux`: it matches any path with no more specific registration, so
an unregistered `/swagger`/`/openapi.json` wouldn't 404 at all, it would silently fall through to
the homepage handler (a `200` with the wrong body) — confirmed by hitting a real disabled-Swagger
app before landing on the handler-level-guard design. Every other route's own registration is
untouched either way. The non-`-ui` static homepage (`templates/html/home.html`, served
verbatim via `templates.ServePage` — not re-rendered per request, so it can't check
`config.C.SwaggerEnabled` server-side the way a Go template could) hides its "View API
Docs" button client-side instead: a small inline `<script>` `fetch("/openapi.json")`s on
page load and sets the button's `display: none` on a non-OK response or a network error
— no external JS dependency, consistent with this template tree's "no CDN" precedent.
This is a change to the static HTML content itself, not to `home.go.tmpl`'s Go logic, so
it's **not** part of `ensureSwaggerToggle`'s retrofit below — `templates/html/home.html`
is a hand-editable content file once scaffolded (same "written once, not lazily
recreated" precedent as every other homepage/`-response html` content file), so an
already-scaffolded app's copy is left untouched by `nexler update`; only a freshly
scaffolded app gets the script. An app scaffolded before this setting existed picks it
up via `nexler update`
(`ensureSwaggerToggle` in `route.go`, registered in `update.go`'s `updateChecks`) — patches
`config.go`/`home.go` via the same fixed-literal-anchor technique `ensureKgateResumeAll` already
established (see "`nexler init kgate`" below), and appends `{PREFIX}_SWAGGER_ENABLED=true` to
`.env` **only if that key is missing** — never overwriting an already-present value, since a
production deployment may have deliberately set it to `false`.

**`{APPNAME}_API_BASE_PATH`** (`Config.APIBasePath`, no default — stays empty unless an operator
sets it) documents the path prefix an external gateway mounts this service under (e.g. an app
reverse-proxied at `/api/my-service`). When non-empty, `openapi.Spec` (`openapi/openapi.go.tmpl`)
adds a relative `servers[0].url` entry to the generated document — relative, not a full
`https://host/...` URL, since Swagger UI resolves a relative server URL against the page's own
origin, so the same spec works correctly whether fetched from a dev host or through the
production gateway; only the path prefix needs documenting, not the host. Purely documentational
— it has no effect on `mux.HandleFunc`'s own route registration/matching, which is unaware of any
prefix. An app scaffolded before this setting existed picks it up via `nexler update`
(`ensureAPIBasePath` in `route.go`) — same fixed-literal-anchor technique as `ensureSwaggerToggle`
for `config.go` (reusing its own `Port` field/load()-line anchors, so it doesn't depend on
`ensureSwaggerToggle` having run first), plus turning `home.go`'s single-argument
`openapi.Spec("<AppName>")` call into the two-argument
`openapi.Spec("<AppName>", config.C.APIBasePath)` form, and appending
`{PREFIX}_API_BASE_PATH=` (blank) to `.env` only if that key is missing.

### Authentication (`-auth none|jwt|session|both`, `-remember-me`)

`middleware.RequireAuth` — what every `-protected` route wraps its handlers in — is
generated differently depending on the app-creation-time `-auth` choice
(`middleware/auth.go.tmpl` branches on `.AuthKind`, same `{{if}}`-per-mode style
`handler.go.tmpl` already uses for `HTMLResponse`):

- `none` (default): functionally identical to the original stub (only its doc comment
  now points at `-auth`) — checks that an `X-Api-Key` header and a `Bearer` token were
  *sent*, never verifies either. Existing apps/behavior are untouched by this feature.
- `jwt`: generates `auth/jwt.go.tmpl` → `auth/jwt.go` — `IssueJWT`/`VerifyJWT`, a
  minimal, correct HS256 JWT (stdlib-only: `crypto/hmac` + `crypto/sha256`, no
  dependency — deliberately *not* JWE, which would mean either hand-rolled encryption
  or breaking nexler's zero-dependency precedent). `RequireAuth` checks the
  `Authorization: Bearer <token>` header via `VerifyJWT`.
- `session`: generates `auth/session.go.tmpl` → `auth/session.go` — an **in-memory**
  session store (`map[string]sessionEntry` behind a mutex) plus `StartSession`
  (issues an `HttpOnly`/`SameSite=Lax` cookie named `{{.EnvPrefix}}_session`),
  `SessionFromRequest`, `EndSession`. Same documented scope limitation as
  `store.go.tmpl`'s stub: sessions are lost on restart and aren't shared across
  instances — swap the map for a real store before scaling beyond one instance.
  `RequireAuth` checks the session cookie via `SessionFromRequest`.
- `both`: generates *both* `auth/jwt.go` and `auth/session.go`; `RequireAuth` tries the
  bearer token first, falls back to the session cookie — so the same protected route
  serves API clients (bearer) and browser/UI clients (session cookie) at once.

Whichever branch verifies the caller, `RequireAuth` attaches the resulting subject
(userID) to the request's `context.Context` via `auth.ContextWithSubject`
(`auth/context.go.tmpl` → `auth/context.go`, generated whenever `AuthKind != "none"` —
unconditionally alongside whichever of `jwt.go`/`session.go` is present, not per-file
gated like they are, since it's needed regardless of which mechanism is active) before
calling the wrapped handler. Any protected handler reads it back with
`auth.Subject(r) (subject string, ok bool)` — the general-purpose way to get the
authenticated userID inside a protected route, independent of `nexler init kpass` (see
`UserIDFromRequest` below, now a thin wrapper over this same function).

An app scaffolded before this feature existed doesn't get it automatically — retrofit
it with `nexler update [-dir <app-dir>]` (`internal/scaffold/update.go`'s
`ensureAuthSubjectContext`, registered in its `updateChecks` alongside
`ensureOpenAPIUpToDate`/`ensureResponseJSONRaw`). It detects the app's auth mechanism
the same way `nexler init kpass`'s `detectAuthFiles` does (whether `auth/jwt.go`/
`auth/session.go` exist on disk — a no-op, not an error, for an `-auth none` app or
one predating `-auth` entirely), adds `auth/context.go` if missing, and — if
`middleware/auth.go` predates `ContextWithSubject` — fully regenerates it from the
current template. Full regeneration (not a marker-based patch like
`ensureResponseJSONRaw` uses for `response.go`) is deliberate: `RequireAuth` has no
established hand-customization pattern and its content is fully derived from
`AuthKind`/`ModulePath` alone, the same "pure generated infra, no per-app free text"
reasoning `ensureOpenAPIUpToDate` already relies on for `openapi.go` — the trade-off
being that a hand-edited `RequireAuth` (extra logging, custom error messages) would be
silently discarded. A sanity check (the existing file must still contain `func
RequireAuth`) guards against that being *catastrophic* — an unrecognized file errors
out instead of being overwritten, naming the file and asking the developer to
reconcile it by hand.

`scaffold.go`'s `NewApp` picks which `auth/*.go.tmpl` files actually get walked: the
whole `auth/` directory is skipped via `fs.SkipDir` when `AuthKind == "none"`; otherwise
whichever of `jwt.go.tmpl`/`session.go.tmpl` isn't needed is skipped per-file (same
technique `-ui` already uses to skip the unused embedded homepage) — `context.go.tmpl`
has no such per-file skip, since both auth kinds need it. The JWT secret
(`generateJWTSecret`, `crypto/rand`, 32 bytes) is generated once per `nexler create app`
call and threaded through `templateData.JWTSecret` into both `config.go.tmpl` (a new
conditional `Config.JWTSecret` field, env var `{{.EnvPrefix}}_JWT_SECRET`) and
`dotenv.tmpl` (pre-filled, so a `-auth jwt`/`both` app works with zero manual setup).

`-remember-me` only has an effect when `-auth` includes session: it adds a `rememberMe
bool` parameter to `StartSession` that swaps its default 24h lifetime for 30 days — the
simple form (one cookie, one lifetime toggle), not a separate rotating remember-token.
It's silently ignored (not an error) when `-auth` is `none`/`jwt`, same as `-body` being
irrelevant-but-harmless for GET-only routes.

Deliberately **not** generated: any `/login`/`/logout` route. `-auth` only wires the
primitives + `RequireAuth`; you call `auth.IssueJWT`/`auth.StartSession` from your own
login handler (scaffolded separately via `nexler create /login -module ...`) once
credentials are verified — matching every other generated handler's `// TODO: call
<pkg>'s service` style, rather than generating a business-flavored route.

### API-key (service-to-service) auth (`middleware.RequireServiceAuth`)

Apps with `-auth jwt|both` and `-db` set also get a second, wholly separate auth
mechanism for app-to-app calls: `middleware.RequireServiceAuth` (`middleware/
service_auth.go.tmpl`), checking the `X-Api-Secret` header — nexler's existing convention
for this concept (the `-auth none` stub in `middleware/auth.go.tmpl` already checks for
an `X-Api-Key` header's presence, unverified — a separate, older stub convention, not
literally the same header name as this real check) — against a new `core_services`
table, backed by `core/services.go.tmpl`'s `CreateService`/`VerifyServiceKey`/
`RevokeService`. Deliberately **independent** of `RequireAuth` by default, not a third
fallback alongside JWT/session the way `-auth both` already tries JWT then session on
the same route: a service-only route must never be able to accept an end-user JWT just
because it happens to share middleware with a user-facing one. A route is either
user-facing (`-protected`, `RequireAuth`) or service-only (`RequireServiceAuth`) — never
both via one combined check, unless `-merge-service-auth` opts in (see below). **The
`X-Api-Secret` it validates must never be embedded in any UI-facing code** (browser JS,
a mobile app binary) — treat it the same as a database credential; nexler has no way to
enforce this at the framework level, so it's a documented discipline, not a runtime
check.

`core.CreateService(ctx, name) (key string, err error)` generates a random 32-byte key
(`crypto/rand`, hex-encoded), stores only its SHA-256 hash in `core_services`, and
returns the plaintext key **exactly once** — it is never stored or retrievable again.
`VerifyServiceKey(ctx, key) (name string, ok bool, err error)` hashes an incoming key and
looks it up by that hash (never the plaintext); `RequireServiceAuth` calls this and, on
success, attaches the calling service's `Name` to the request context via a **new**
`auth.ContextWithService`/`auth.Service(r)` pair — a distinct context key from
`ContextWithSubject`/`Subject`, so a service-authenticated request and a
user-authenticated one can never be confused at the type level. `RevokeService(ctx,
name)` sets `Status` to `"revoked"`; `VerifyServiceKey` rejects a revoked service's key
from that point on. Same "primitives only" precedent as `-auth`'s missing `/login`
route: nexler generates no CRUD routes for creating/listing/revoking services — call
these functions from your own code (an internal admin handler, a one-off script, etc.).

`core/users.go.tmpl` (`core.GetUser`/`CreateUser`/`SetUserStatus`, `core_users` table)
is a **minimal local record**, explicitly *not* a profile store: `UserId` is the same
value as `auth.Subject(r)` / the JWT `sub` claim (documented in the generated code, not
just implied), plus `Username`/`UserRole`/`UserType` (for future org-wide/subsection-
scoped data access) and `Status` (a local active/disabled kill-switch — a disabled
user's existing, unexpired JWT is still cryptographically valid, so enforcing `Status`
is a deliberate, separate check your own middleware/handlers must call; it is not
automatic). Real user profile data (name, email, etc.) stays wherever it already lives —
another app or table, whether or not that app uses kpass (see "`nexler init kpass`"
below — kpass itself is confirmed to be a pure permission-check service with no user
profile store of its own). `CreateUser` is **not** auto-called from `RequireAuth` on
every JWT verification (that would add a DB write/read to every authenticated request)
— call it from your own registration/login flow once a user's identity is known.

Both tables' schema is provisioned by `nexler init db` (see below) **unconditionally**
for every `-db` app, regardless of `-auth` choice — same "zero-cost-when-unused"
precedent `core_kgate_channels` already established. Only the *Go code* using them
(`core/users.go`, `core/services.go`, `middleware/service_auth.go`, plus
`ContextWithService`/`Service` in `auth/context.go`) is gated on `-auth jwt|both` — a
new per-file skip in `scaffold.go`'s `NewApp`, alongside the existing whole-`core/`-
directory `HasCoreDB` gate. An app scaffolded before this existed picks it up via
`nexler update` (`ensureServiceAuth` in `internal/scaffold/serviceauth.go`) — writes
the three new files if missing (full-file render, same "pure generated infra" reasoning
as `ensureAuthSubjectContext`) and append-only-adds `ContextWithService`/`Service` to
`auth/context.go` if missing; a silent no-op for an app without both a core DB
connection and a JWT-capable `-auth` choice, same precedent as every other retrofit
skipping a feature the app was never eligible for. Never re-runs `nexler init db` itself
— table provisioning stays a separate, explicit, manual step.

#### Folding service auth into `RequireAuth` (`-merge-service-auth`)

`-merge-service-auth` (new `create app` flag, same eligibility as service auth itself —
`-auth jwt|both` + `-db`) is the opt-in alternative to the separate-middleware design
above: instead of generating `middleware/service_auth.go.tmpl` at all, its
`X-Api-Secret` check is folded directly into `middleware/auth.go.tmpl`'s own
`RequireAuth`, tried after JWT (and after session too, for `-auth both`) — so a
service/automation caller can hit the exact same `-protected` routes a human user
would, instead of a route needing a structurally separate service-only variant of
itself. `templateData.MergeServiceAuth` (from `NewAppConfig.MergeServiceAuth`) drives
both a new branch inside `middleware/auth.go.tmpl` (a merged "jwt" branch and a merged
"both" branch, alongside the existing unmerged ones — no new branch for "session" or
"none", since merging isn't eligible there either) and a skip on
`middleware/service_auth.go.tmpl` itself in `scaffold.go`'s `NewApp`. A matching
service key still attaches both `auth.ContextWithService` and
`auth.ContextWithSubject` (set to the service's name) — same reasoning as the unmerged
design's own `Subject`-for-RBAC note, just inlined into one middleware instead of two.

Deliberately **not** the new default: this is a real per-app design tradeoff, not a
one-true-shape improvement — a pure API service that wants user-only and service-only
routes to stay structurally distinct (so a service key can never reach a route meant
only for end users, or vice versa) should leave it unset and keep the separate
`RequireServiceAuth`. Default `false` preserves today's behavior exactly for anyone who
doesn't pass it.

An already-scaffolded app opts in via `nexler update -merge-service-auth`
(`MergeServiceAuth` in `internal/scaffold/mergeserviceauth.go`) — a deliberately
separate, explicit flag on the `update` command, **not** one of the unconditional
`updateChecks` (`update.go`): every check there converges every eligible app to one
canonical current shape, but merged-vs-separate isn't that kind of fix, so nothing
flips it without being asked. It ensures `core/users.go`/`core/services.go`/
`auth/context.go`'s `ContextWithService` exist (same as `ensureServiceAuth`, for an app
that predates API-key auth entirely), then — if `middleware/service_auth.go` exists —
byte-compares it against what the current template would render for this app before
removing it, refusing instead (naming the file) if it's been hand-edited, same
"has it been hand-rewritten?" precedent `ensureAuthSubjectContext` already uses; finally
regenerates `middleware/auth.go` in merged form. A silent no-op if the app is already
merged, or was never eligible. `ensureServiceAuth` itself is merge-aware too — it never
writes `middleware/service_auth.go` back for an app whose `middleware/auth.go` already
checks `X-Api-Secret` inline, so a plain `nexler update` (no flag) can never resurrect
the separate file on an already-merged app.

#### Admin API for `core_users`/`core_services`

Same eligibility gate as above (`-auth jwt|both` + `-db`): every such app also gets a
Swagger-documented admin API, hand-authored (not routed through `route.go`'s `NewRoute`
machinery, unlike a `nexler create <route>` output — these are infrastructure endpoints
with real, from-day-one field definitions, not generic `-methods` TODO stubs, so — like
`handlers/home/home.go.tmpl` — each is one self-contained file rather than the usual
handlers/services/store/models four-layer split):

- `handlers/admin/users/users.go.tmpl`: `GET /admin/users` (list, `core.ListUsers` — no
  pagination this release), `POST /admin/users` (create/upsert, `core.CreateUser`),
  `GET /admin/users/{id}` (`core.GetUser`), `PATCH /admin/users/{id}/status`
  (`core.SetUserStatus`).
- `handlers/admin/services/services.go.tmpl`: `GET /admin/services` (list, `core.
  ListServices` — metadata only, `Service` has no key field at all so a key can never
  leak through this endpoint by construction), `POST /admin/services` (`core.
  CreateService` — the plaintext key is in the JSON response body exactly once, same
  "never retrievable again" contract as `core.CreateService` itself), `GET /admin/
  services/{name}` (`core.GetService` — new, metadata-only, added alongside `ListUsers`/
  `ListServices` specifically to support these endpoints), `POST /admin/services/{name}/
  revoke` (`core.RevokeService`).

Every handler wraps only in `middleware.RequireAuth` — **no role/authorization check**.
This is deliberate, not an oversight: `RequireAuth` confirms the caller is
*authenticated*, not that they're *allowed* to manage other users/services. Both
generated files' package doc comments say so explicitly and point at `kpass.Check` (once
`nexler init kpass` has been run) as the expected way to add that check per-app —
permission enforcement is intentionally left out of nexler's own generated code, same
"primitives only" precedent as everywhere else, rather than nexler guessing at an
authorization scheme every app would actually want.

Wired into `routes/protected/protected.go` at `NewApp` time via `route.go`'s
`wireAggregator` — the exact same post-hoc-wiring mechanism `kgate`/`kpass` already use,
called right after `NewApp`'s main `fs.WalkDir` pass (same point `cfg.UI`'s
`writeAppUIHomepage` call already runs), since `wireAggregator` needs the aggregator file
to already exist on disk. Retrofits via `ensureAdminRoutes` (`serviceauth.go`, alongside
`ensureServiceAuth`) — same eligibility gate, writes either handler file if missing, and
wires each into `protected.go` **only if not already imported**: `wireAggregator` itself
*errors* (not a silent no-op) on an already-present import, since for its usual caller
(a fresh `nexler create <route>`) that's a real programmer-error signal — a retrofit
re-run is exactly the case where "already wired" is the expected, common outcome, so
`ensureAdminRoutes` pre-checks with the same substring test `wireAggregator` uses
internally and skips the call rather than treating it as a failure.

### Database connections (`-db mongo,mysql,postgres,mssql`)

`-db` is deliberately scoped to connection lifecycle only — opening at startup, closing
at shutdown. No query builder, ORM, or migrations; call methods directly on the
`*sql.DB`/`*mongo.Client` that `db.SQL`/`db.Mongo` return. It's also the **first** nexler
feature needing real third-party dependencies: there's no stdlib driver for MongoDB or
any SQL server, unlike the JWT work, where hand-rolling was the safe stdlib-only choice.

Two separate decisions, both made at `nexler create app` time:
- **Which driver types are compiled in** — `-db`'s value, any comma-separated subset of
  `mongo`/`mysql`/`postgres`/`mssql`. This governs which `db/*.go.tmpl` files
  `scaffold.go`'s `NewApp` walks (same per-file skip technique `-auth` already uses for
  `auth/jwt.go.tmpl`/`auth/session.go.tmpl` — the whole `db/` dir is `fs.SkipDir`-skipped
  when `-db` is empty) and which blank driver imports `db/sql.go.tmpl` emits:
  `github.com/go-sql-driver/mysql` (mysql), `github.com/jackc/pgx/v5/stdlib` (postgres —
  used purely as a `database/sql` driver via pgx's stdlib shim, not its richer native
  API, so one manager handles all three SQL flavors uniformly), `github.com/microsoft/go-mssqldb`
  (mssql, registers as driver name `"sqlserver"` — see `sqlDriverName` in `sql.go.tmpl`),
  and `go.mongodb.org/mongo-driver/v2` (mongo, architecturally separate from
  `database/sql` — its own file, `db/mongo.go.tmpl`).
- **Which named connections actually exist, and where they point** — deliberately *not*
  decided at scaffold time. This is pure `.env`/runtime config, by design (so adding or
  removing a connection is never a re-scaffold): `{APPNAME}_DB_CONNECTIONS` lists
  comma-separated connection names; each name gets its own `_TYPE` (must be one of the
  types selected via `-db`) and `_DSN`. `db.Connect()` (called from `main()` at startup)
  reads this convention and fails fast — returns an error immediately — if any listed
  connection can't be opened, rather than failing lazily on first query.

`db/db.go.tmpl` is the orchestrator (generated whenever `-db` is non-empty): `Connect()`
dispatches each declared connection to `openSQL`/`openMongo` by its `_TYPE`; `Close()`
calls `closeSQL()`/`closeMongo()` (whichever exist) and joins any errors via
`errors.Join`. `db/sql.go.tmpl` and `db/mongo.go.tmpl` each keep their own
`map[string]*sql.DB`/`map[string]*mongo.Client` behind a mutex, populated by
`open*`/read by `SQL(name)`/`Mongo(name)`/drained by `close*`.

`main.go.tmpl` branches on `.HasDB`: when `-db` is set, `main.go` gains `db.Connect()` at
startup (fatal on error) and a graceful-shutdown sequence (SIGINT/SIGTERM →
`http.Server.Shutdown` → `db.Close()`) that doesn't exist otherwise — today's `main.go`
has no shutdown path at all, nothing for "close a connection" to hook into, so this is
the minimum needed to make `Close()` meaningful; without `-db`, `main.go` is generated
exactly as it always has been (plain `http.ListenAndServe`, no signal handling).

Because the driver packages are real dependencies, `gomod.tmpl` is deliberately left
**unchanged** (no hand-written `require` block — nexler can't verify from inside itself
which exact driver versions are currently valid/unyanked, and a wrong pinned version is
worse than none). The CLI's post-create output tells the user to run `go mod tidy`
before their first build; that command resolves + pins real current versions and writes
`go.sum` correctly, which is also exactly why nexler itself never touches the network to
produce a `-db` app — only the generated app's one-time dependency fetch does.

#### The "core" connection and `store/`

Every `-db`-enabled app gets one connection pre-populated under the conventional name
`"core"` — `.env`'s `{APPNAME}_DB_CONNECTIONS=core` plus `_DB_CORE_TYPE`/`_DB_CORE_DSN`
(type filled in; DSN either left blank for the user, or built for real — see below —
depending on whether `-db-host` was given), instead of the empty starter it used to
be. Which `-db` type is core is a **scaffold-time decision** (`-core <type>`, new flag
on `create app`): automatic when `-db` selects exactly one type, otherwise required —
defaults to `mongo` if selected, else the first type listed — resolved by
`resolveCoreDB` in `scaffold.go`. This is deliberately *not* dynamic/DB-backed: `.env`
stays the only source of truth for what connections exist (see above); "core" is just
the name every app's primary connection gets by convention, nothing more.

**Real DSNs, optionally, at scaffold time.** `-db-host`/`-db-port`/`-db-name`/`-db-user`/
`-db-password` (new `create app` flags) let `_DB_CORE_DSN` be a real, working connection
string instead of always-blank — `scaffold.go`'s `buildDSN` (dialect switch: mysql's
`user:pass@tcp(host:port)/dbname`, postgres's `postgres://user:pass@host:port/dbname`
— defaulting to `?sslmode=disable` whenever password is blank, since pgx's own default
of `sslmode=prefer` would still connect via graceful fallback against a local no-TLS
server but pays an extra negotiation round trip for the ambiguity — mssql's
`sqlserver://user:pass@host:port?database=dbname`, mongo's `mongodb://user:pass@host:port/
dbname`) plus `userinfo` (renders the `user:pass@` prefix, or `user@`, or nothing at all
when both are blank — confirmed against each driver's actual documented grammar, not
assumed, including the mssql-specific wrinkle that blank credentials there fall back to
Windows/Integrated auth rather than meaning "no auth," since SQL Server has no such
concept). **Every prompt after `-db-host` is gated on host actually being non-blank** —
hitting Enter through the flow (or never passing any of these flags) reproduces the
original always-blank `_DB_CORE_DSN=` output exactly, so this is purely additive, never a
behavior change for anyone who'd rather fill `.env` in by hand. Password is prompted via
the same plain-**visible** `prompt()` every other value uses — there's no masked-input
helper anywhere in `cmd/nexler/prompt.go`, and the prompt label says so explicitly rather
than silently doing something a user might assume is hidden.

This is what makes `store/<route>/<pkg>.go.tmpl` — previously a disconnected 3-line
stub referencing a "Store interface" that doesn't exist anywhere in code — actually
useful: when the target app has a core connection, `route.go`'s `NewRoute` reads it
back out of the app's `.env` (`readCoreDBType`, a lightweight text scan for a
`_DB_CORE_TYPE=` line — the `{PREFIX}_` part varies per app, so it matches on the fixed
suffix, same style as `detectExistingProtection`'s substring search) and
`store.go.tmpl` renders an *informed* TODO pointing at whichever of the `mongo`/
`mysql`/`postgres`/`mssql` packages (see below) matches the core connection's actual
type — instead of the old generic stub. Deliberately **not** generating a package-level
`var`/helper that actually calls `db.SQL`/`mongo.Conn` — package-level vars initialize
before `main()` runs, i.e. before `db.Connect()` has been called, so that would
silently always return `ok == false`; a doc-comment pointer sidesteps the trap
entirely, and matches how `services/<pkg>.go` is already an informed TODO rather than a
guessed-at API. An app scaffolded without `-db` still gets today's original stub text,
unchanged.

Dynamic, runtime-mutable settings (e.g. timezone — something that needs to change
without a restart, unlike `.env`/`config.C`, which loads once at startup) are a
documented future direction only (see `config.go.tmpl`'s doc comment) — not
implemented.

#### `nexler db add`: connections (and dialects) after the fact

`nexler db add <name> -type <mongo|mysql|postgres|mssql> [-dir <app-dir>] [-host] [-port]
[-dbname] [-user] [-password]` (`internal/scaffold/db.go` → `cmd/nexler/db.go`) adds a new
named connection to an *already-scaffolded* app — the thing `-db`'s own doc comment above
already flags as "pure `.env`/runtime config," now with a command to do it instead of a
hand edit, plus the ability to retrofit a driver type the app wasn't originally
scaffolded with. Deliberately **not** named anything with "init db" in it — `nexler init
db` already means something different (provisioning the core schema on a live database
connection, a real network operation); this command never opens a live connection at all,
it only edits `.env` and, when retrofitting, writes/patches Go source. `<name>` can't be
`"core"` (reserved for the app's own conventional default connection, which this command
never touches — the added connection is reachable as `db.SQL("<name>")`/
`db.Mongo("<name>")` but never promoted to core) and must match `^[A-Za-z][A-Za-z0-9_]*$`
— rejected outright rather than silently sanitized, since a mismatched sanitization would
break hand-written `db.SQL("<name>")` call sites. `-host`/etc. work exactly like `create
app`'s `-db-host`/etc. above — blank leaves the new connection's DSN blank.

`recoverEnvPrefix` (not `route.go`'s `readCoreDBType`) recovers the app's `{PREFIX}` by
scanning for the *unconditional* `_HOST=` line rather than `_DB_CORE_TYPE=` — deliberate,
since `db add` needs to work on a `-db`-less app too (adding its very first database),
which has no `_DB_CORE_TYPE` line to scan for at all. `setEnvLine` is new — `kpass.go`'s
`ensureEnvVars` only ever appends brand-new *blank* placeholders for a fixed, known list
of key names; it can neither update `_DB_CONNECTIONS`'s existing value in place nor
handle a dynamically-built key name like `{PREFIX}_DB_ANALYTICS_DSN`, only known at call
time — `setEnvLine` upserts any key, replacing in place if present, appending if not.

**Retrofitting a dialect** (`dialectEnabled` checks file existence — `mysql/mysql.go`,
`db/mongo.go`, etc. — same "detect state from what's on disk" precedent as `kpass.go`'s
own `detectAuthFiles`) writes whatever's missing, code before `.env` (so a crash mid-
retrofit self-heals into "dialect already enabled" on retry rather than getting stuck on
an overwrite refusal): the per-dialect helper package always; for mongo, a fresh
`db/mongo.go` (zero `{{ }}` references in that template at all — a plain copy); for a SQL
type, either a patched import block in an already-existing `db/sql.go` (reusing
`route.go`'s `insertImport` completely as-is — it was already generic, no `routeData`
coupling, so no new insertion code was needed there — `sqlDriverName`'s own `switch` needs
no change either, since it's unconditionally exhaustive for all three SQL dialects
regardless of which are actually compiled in) or a freshly-rendered `db/sql.go` if this is
the app's first SQL dialect ever.

`db/db.go` only needs touching when the dialect *family* (SQL vs. mongo) is new to the
app as a whole — a detail easy to miss since it's gated independently of `sql.go.tmpl`'s
own imports: `Connect()`/`Close()` each have their own `{{if or .HasMySQL .HasPostgres
.HasMSSQL}}` / `{{if .HasMongo}}` blocks. Renders fresh if the app had zero `-db`
originally; otherwise patches the existing file via two marker comments —
`// nexler:db-connect` (right before `Connect()`'s `default:` case) and `//
nexler:db-close` (right before `Close()`'s `return errors.Join(errs...)`) — the same
idempotent-marker-insertion shape `route.go` already established for handler/model/
aggregator files, chosen deliberately over full-file regeneration even though `db.go`'s
content is fully derived from a few booleans: these are files a developer could plausibly
hand-edit (connection pool tuning, custom TLS config, timeouts), and silent regeneration
on a retrofit would be a quiet, easy-to-miss way to lose that. An app scaffolded before
these markers existed gets a clear error naming the exact marker text to add back by
hand, same as the existing route-marker precedent.

#### The `mongo` package: chainable CRUD helpers

`db/mongo.go.tmpl` deliberately only ever exposes the raw `*mongo.Client`
(`db.Mongo(name)`) — connection lifecycle, nothing query-shaped. `mongo/mongo.go.tmpl`
(generated whenever mongo is selected via `-db` — reuses the existing `HasMongo` flag,
same `fs.SkipDir` skip technique as `auth/`/`db/`) is the layer above that: simplified
Get/GetOne/Set/Update/Insert/Delete/Aggregate helpers built on top of it.

Addressing is a plain method chain, assignable to a variable like any Go value:
`mongo.Conn(name)` → `.Database(name)` → `.Collection(name)`, matching MongoDB's real
connection→database→collection structure (not database-implicit). The six operations,
though, are **free generic functions taking that `Collection` handle as an argument**
(`mongo.Get[User](ctx, coll, filter)`), not methods on it (`coll.Get[User](ctx, filter)`)
— Go does not allow a method to introduce its own type parameter, only free functions
can, and generic decoding into the caller's own struct type was a deliberate, confirmed
requirement here (over returning raw `bson.M`). This is the one place the API shape had
to bend to a hard language constraint rather than pure ergonomics.

`Get`/`GetOne`/`Set`/`Update`/`Delete` take `filter any` — the caller's own struct, not a raw
`bson.M` (`filterToBSON`, unexported, does the translation): a single struct's non-zero
fields AND together; a `[]T` slice of structs ORs each element's AND-group (`$or`).
Field→key mapping uses each field's `bson` tag, falling back to the lowercased field
name; zero-valued fields are never part of the filter (documented limitation — can't
filter for an explicit zero/empty value this way). `Set` upserts (replaces-or-inserts)
the *one* document matching filter; `Delete` removes the *one* matching document — both
confirmed choices, not the only valid reading of "set"/"delete". `Aggregate` stays
pipeline-based (`driver.Pipeline`) — the escape hatch for anything the struct-filter
model can't express, playing the same role `Query` plays for the SQL packages below.

Scoped to exactly these six operations: no transactions, no query-builder beyond the
struct-filter translation. A caller needing more can still drop to `db.Mongo(name)`
directly for the full driver API.

**`InsertID[T](ctx, coll, doc) (bson.ObjectID, T, error)`** is a seventh, additive
function alongside `Insert` (never changing `Insert`'s own signature — a hard rule this
repo's own commit history is explicit about: "no removals or breaking signature
changes"), for the common real-world case a plain `Insert` can't serve:
`Insert`/`InsertOne` still work exactly as before, but the driver assigns `_id`
server-side and the caller's own `doc` value — passed by value — never sees it back.
`InsertID` locates `T`'s ID field by its `bson` tag (`bsonFieldName(f) == "_id"`,
recursing into anonymous embedded struct fields so a hand-defined "base" struct every
model embeds — a `Base` struct holding just an `ID bson.ObjectID` field tagged
`bson:"_id,omitempty"`, embedded as `Base` tagged `bson:",inline"` — is found the same
way a direct field would be); if it's still zero, generates a real `bson.NewObjectID()`
before inserting
and returns it. If `T` has no such field at all (e.g. `core/errorlog.go.tmpl`'s
`errorLogDoc`, which has none by design), `InsertID` just delegates to `Insert`
unchanged and returns a zero `ObjectID` — never an error, never a requirement. This
locator's own reflection walk is separate from `structToBSON`'s (below) — one finds and
sets an `_id` field for insertion, the other builds a filter — but both now recurse into
anonymous embedded struct fields, for the same reason: a shared "base" struct (e.g.
`store/common.Base`, below) every domain type embeds is the expected, common shape.

**`Update[T any](ctx, coll, filter, patch) error`** is an eighth, additive function
alongside `Set` (never changing `Set`'s own signature, same "no breaking signature
changes" rule as `InsertID` above) — the partial-patch counterpart `Set`'s full-document
`ReplaceOne`-with-upsert can't serve: `Update` reuses `structToBSON` (the same
non-zero-fields, embedded-struct-flattening reflection walk `filterToBSON` already runs
on the filter side) to build a `$set` document from `patch`'s non-zero fields only, then
calls `UpdateOne`, mirroring the SQL packages' own `Update` (non-zero-field patch, one
matching document) rather than exposing a raw `bson.M`/driver-options parameter — every
other function in this file builds its query/document from a caller struct, and a raw
update document would have been the first exception to that. `Update` refuses an empty
filter (would match every document) and a patch with no non-zero fields (nothing to
set), same guard rails as SQL's `Update`. Doesn't cover Mongo-native update operators
(`$inc`, `$push`, etc.) — a caller needing those still drops to `db.Mongo(name)`
directly. Apps scaffolded before `Update` existed pick it up via `nexler update`
(`ensureMongoUpdate` in `route.go`) — append-only, same precedent as `InsertID`'s own
`ensureInsertIDHelpers`: wholly new code that doesn't touch anything already in
`mongo.go`, so nothing to sanity-check before appending; a silent no-op if the app
wasn't scaffolded with mongo at all.

**`store/common/common.go.tmpl`** (generated whenever mongo is selected via `-db`, same
`HasMongo` gate as `mongo/` itself) provides exactly that shared base:
a `Base` struct holding just an `ID bson.ObjectID` field tagged `bson:"_id,omitempty"`,
meant to be embedded anonymously (tagged `bson:",inline"`) in every Mongo domain struct
so `_id` is picked up the same way every time without every model redeclaring it. Two
apps independently hand-built an identical package with the same shape before this
existed — the signal it belonged in the scaffold itself, not left to be re-solved per
app. An app scaffolded before it existed picks it up via `nexler update`
(`ensureStoreCommon` in `route.go`) — writes the file only if missing (never overwrites;
`Base` has no versioned content to bring up to date, just present-or-absent), a silent
no-op if the app wasn't scaffolded with mongo at all.

**`structToBSON` flattens anonymous embedded fields into the same top-level filter**,
not just `InsertID`'s locator — a filter struct literal like `T{Base: common.Base{ID:
id}}` used with `Get`/`GetOne`/`Set`/`Delete` builds `{"_id": id}`, matching how the
Mongo driver's own default (non-inline) BSON encoding already treats an embedded struct.
This was a real, initially-shipped bug (fixed later): the filter builder used to only
handle direct fields, so that same literal silently built `{"base": ...}` instead,
breaking every by-ID lookup for any type embedding a shared base struct — never caught
until a real app's `{id}` routes were the first to actually use `store/common.Base`
embedding. An app scaffolded before the fix picks it up via `nexler update`
(`ensureMongoEmbeddedFilterFix` in `route.go`) — an exact literal-text anchor
replacement of `structToBSON`'s loop body (zero per-app templating, so identical across
every app), same "narrow, anchor-based patch of a known-exact body" precedent
`ensureKgateResumeAll` already established for `kgate.go`'s `Register`; errors, naming
the file, if that loop doesn't match exactly (hand-rewritten) rather than overwriting it.

#### The `mysql`/`postgres`/`mssql` packages: struct-based SQL helpers

Same motivating idea as `mongo`, but SQL can't mirror its shape 1:1: there's no shared
filter-document model, no shared placeholder syntax across dialects (`?` / `$1` /
`@p1`), and no portable single-statement upsert (MySQL's native upsert keys off unique
constraints rather than an arbitrary condition) — confirmed decisions, not oversights.
So instead of one generic package: **three fully independent packages**
(`internal/scaffold/templates/{mysql,postgres,mssql}/*.go.tmpl`), each generated only
when that specific type is selected via `-db` (same per-directory `fs.SkipDir` gating
as `mongo/`, on `HasMySQL`/`HasPostgres`/`HasMSSQL`), each with its own copy of every
helper — independence over DRY, deliberately, so each package can be read/maintained in
total isolation from the others.

Identical shape in all three, one level shorter than `mongo`'s chain since a SQL
connection's DSN already picks the database (no separate `.Database()` hop needed):

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

Same struct-filter convention as `mongo` (non-zero fields AND; `[]T` ORs each element's
AND-group; zero-valued fields never filterable), field→column mapping via `db` tag
instead of `bson`. **`Insert` vs `Update` is intentionally asymmetric** — `Insert` writes
every mapped field's current value, zeros included (a complete new row); `Update`'s
second argument uses non-zero fields only (a partial `SET` patch, confirmed) — call this
out to anyone reading the generated code, it's the kind of thing that's a silent
surprise otherwise. `Update`/`Delete` both refuse an empty filter (would otherwise touch
every row) — a deliberate safety guard the struct-filter design needs, since an
accidentally zero-valued filter struct would otherwise silently mean "no condition."

`Query`/`Call` are the raw-SQL escape hatches (`Aggregate`'s role for `mongo`) — a
complete hand-written statement, or a stored-procedure invocation ("provision" only, a
single result set, not a general multi-result-set system). Per-dialect specifics, each
verified against real driver docs rather than assumed:

| | placeholder | `Call` |
|---|---|---|
| mysql | `?` | builds `CALL proc(?, ?, ...)` |
| postgres | `$1, $2, ...` | builds `CALL proc($1, ...)` — PG *functions* (vs *procedures*) aren't `CALL`-able; doc comment says to use `Query("SELECT * FROM my_func($1)", ...)` for those |
| mssql | `@p1, @p2, ...` | **no `EXEC`/`CALL` text at all** — go-mssqldb treats a query string that's just a procedure name as an RPC call with positionally-bound args; `Call` is `Query[T](ctx, conn, proc, args...)` verbatim |

Each package's `scanRows[T]` decodes `*sql.Rows` into `[]T` by matching `rows.Columns()`
names against `T`'s `db`-tagged fields via reflection (unmatched columns are discarded,
not an error — safe for `Query`/`Call` results that may not exactly match a mapped
struct). `Get`/`GetOne` build an explicit `SELECT col1, col2, ...` (from `T`'s own
mapped fields) rather than `SELECT *`, so the query is self-describing. No injection
risk: only *values* ever flow through parameterized placeholders; table/column/procedure
names come from Go struct tags and literal `.Table("...")`/`Call(ctx, conn, "proc", ...)`
arguments — developer-authored at compile time, never runtime data threaded
unparameterized into query text.

**`InsertID[T](ctx, t, doc) (int64, T, error)`** is an eighth, additive function
alongside `Insert` in each of the three packages (never changing `Insert`'s own
signature, same "no breaking signature changes" rule as `mongo.InsertID` above) — the
SQL-side answer to "every table should have a primary key," following the
server-assigned auto-increment integer convention nexler's own schemas already use
(`cmd/nexler/initdb.go`'s `core_config`/`core_error_log`/`core_kgate_channels`:
`id INT AUTO_INCREMENT` / `id SERIAL` / `id INT IDENTITY(1,1)`), not a client-generated
UUID (a confirmed choice — see "Design decisions" in this feature's implementation
history). `InsertID` locates `T`'s primary-key field by `db` tag or lowercased field
name resolving to column `"id"`, and requires it to hold a Go integer kind
(`Int`/`Int8`/.../`Int64`) — a `findIntPKField` helper, one independent copy per
package like every other helper here. When that field's current value is zero, it's
omitted from the `INSERT` column list entirely (`insertColumnsSkipping`, an
`insertColumns` variant) so the database assigns it, then read back via each dialect's
own native mechanism — mysql `LastInsertId()`, postgres `RETURNING <col>`, mssql
`OUTPUT INSERTED.<col>` (go-mssqldb doesn't support `LastInsertId()` reliably, so
`OUTPUT` plays `RETURNING`'s role there) — and set back onto the returned `T`. A
caller-supplied non-zero value is written as an ordinary column instead and echoed back
unchanged. If `T` has no integer `"id"`-mapped field at all, `InsertID` delegates to
`Insert` unchanged and returns `0` — never an error, same fallback precedent as the
mongo side. Composite keys and non-integer (e.g. string/UUID) primary keys aren't
supported by `InsertID` — a model needing one still uses the existing `Insert`.

Apps scaffolded before `InsertID` existed pick it up via `nexler update` (see
"Authentication" above for this same command's other retrofit,
`ensureAuthSubjectContext`) — `ensureInsertIDHelpers` (`route.go`) appends `InsertID`
and its helpers to whichever of `mongo.go`/`mysql.go`/`postgres.go`/`mssql.go` already
exist and don't yet define it (skipping any dialect the app wasn't scaffolded with).
Append-only, like `ensureResponseJSONRaw`'s `JSONRaw` insertion, not a full regen like
`ensureAuthSubjectContext`'s `middleware/auth.go` rewrite: `InsertID` is wholly new code
that never touches or replaces anything already in these files, so there's nothing to
sanity-check before appending.

`route.go`'s `readCoreDBType` (already used for the "core" connection — see above) also
threads the literal type name through as `routeData.CoreDBType`, so `store.go.tmpl`'s
informed TODO names the right package (`mysql.Conn("core")...`, not a generic
`db.SQL("core")`) for whichever type the app's core connection actually is.

#### The `core` package + `nexler init db`: app-wide system data

Independent of whatever business data an app's own `store/` packages manage, every
`-db`-enabled app also gets `core/config.go` (`internal/scaffold/templates/core/config.go.tmpl`,
gated on `hasDB` the same way `db/` is) — a stable, engine-independent function API
(`core.GetSetting(ctx, key)` / `core.SetSetting(ctx, key, value)`), the first of several
planned "core data" types (this was deliberately phased: Config first proves the whole
pattern, the rest are mechanical repetition of it). More are built the same way:
`core/errorlog.go.tmpl` (`core.LogError` — see "Request decoding and response envelope"
and `middleware.Recover` above), `core/kgate_channels.go.tmpl` (`core.AddKgateChannel`/
`RemoveKgateChannel`/`ListKgateChannels` — see "`nexler init kgate`" below; unlike Config/
ErrorLog this one is generated for every `-db` app regardless of whether `nexler init
kgate` is ever run, same zero-cost-when-unused precedent as ErrorLog always being
provisioned regardless of whether a panic is ever caught), and `core/users.go.tmpl`/
`services.go.tmpl` (`core.GetUser`/`CreateUser`/`SetUserStatus`,
`core.CreateService`/`VerifyServiceKey`/`RevokeService` — see "API-key (service-to-service)
auth" above; unlike Config/ErrorLog/kgate-channels, these two are gated on `-auth`
too, not just `-db` — see that section for the full picture). Session log remains
planned, not yet built. "Engine-independent function API" deliberately means a
stable Go function signature, **not** a literal Go `interface` type — the core DB engine
is already fixed per-app at `-core <type>` scaffold time, so there's only ever one
concrete implementation compiled in; runtime polymorphism would buy nothing.

For a Mongo core, `core/config.go.tmpl` just calls the `mongo` package directly
(`GetOne`/`Set`) — no server-side stored procedures exist in modern MongoDB. For a SQL
core, `GetSetting` is a plain parameterized `SELECT` (dialect-specific placeholder/quoting
text precomputed in Go by `configGetQuerySQL` in `scaffold.go`, since a single-table read
has no portability problem worth a stored procedure), but `SetSetting` calls a real
`core_config_upsert` stored procedure via `{{.CoreDB}}.Call` — unlike the generic
`mysql`/`postgres`/`mssql` packages (which can't assume anything about an arbitrary
caller's table, and so dropped `Set`/upsert entirely), nexler *owns* the `core_config`
schema, so a real dialect-native upsert (`ON DUPLICATE KEY UPDATE` / `ON CONFLICT DO
UPDATE` / `MERGE`) is safe to hand-write.

Provisioning (`CREATE TABLE IF NOT EXISTS core_config` + the `core_config_upsert`
procedure, or a unique Mongo index — and the same shape for `core_error_log`,
`core_kgate_channels`, `core_users`, and `core_services`) is **not** done by `create app`
or by the generated app's own startup — it's a separate, explicit `nexler init db [-dir
<app-dir>]` command (`cmd/nexler/initdb.go`), reading the target app's `.env` for
`{PREFIX}_DB_CORE_TYPE`/`_DSN`
the same way `route.go`'s `readCoreDBType` does. Deliberate: a physical database can be
shared by several scaffolded apps (auto-provisioning on every app's every boot would be
redundant), and a common production setup runs the app itself with a low-privilege DB
user that can't run DDL at all — schema setup belongs in its own explicit,
higher-privilege step. Every statement is idempotent (`IF NOT EXISTS` / `CREATE OR
REPLACE PROCEDURE` / `CREATE OR ALTER PROCEDURE` / a unique index Mongo no-ops on if it
already exists), so running it again — including against a database another app already
provisioned — is always safe. **This is the one place nexler itself, not just generated
apps, needs a real database connection and real third-party driver dependencies** — all
four (`go-sql-driver/mysql`, `jackc/pgx/v5`, `microsoft/go-mssqldb`,
`go.mongodb.org/mongo-driver/v2`) are unconditional entries in nexler's own `go.mod`,
same trade-off reasoning as `-db` in generated apps (simpler than trying to conditionally
compile the nexler binary itself per invocation).

#### `nexler init kpass`: kpass permission-check integration

`nexler init kpass [-dir <app-dir>]` (`cmd/nexler/initkpass.go` → `scaffold.NewKpass` in
`internal/scaffold/kpass.go`) adds a client for kpass — klivolks' own permission-check
service — to an *existing* generated app. Unlike `nexler init db`, this needs **no live
network connection at all**: it's pure local file scaffolding, same as `nexler create
<route>`, driven by a new embedded template tree (`kpass_templates/kpass.go.tmpl`,
`kpassTemplateFS` in `templates.go`, same pattern as `routeTemplateFS`).

It writes `kpass/kpass.go` (refusing to overwrite if it already exists — same collision
guard `NewRoute` uses for handler/service/store/model files, so a hand-edited copy is
never clobbered by re-running the command) and ensures `.env` declares `KPASS_URL`,
`KPASS_CLIENT_ID`, `KPASS_API_SECRET` (blank, appended only if not already present —
`ensureEnvVars`/`envHasKey` in `kpass.go`). These three are **deliberately not
`{APPNAME}_`-namespaced** like nexler's other env vars (`HOST`/`PORT`/`DB_*`) — kpass
credentials are a shared vendor convention (comparable to `STRIPE_API_KEY`), constant
across every app talking to the same kpass instance, not something that needs per-app
disambiguation.

Generated `kpass.Check(ctx, userID, resource, extra)` posts `{user_id, resource,
...extra}` to `{KPASS_URL}/permission/check/` (via the `apiclient` package — no new
third-party dependency) and returns kpass's full `Result{Status, Message, Access,
UserRole, UserType}` — deliberately **not** collapsed into a bool, so a caller can write
its own conditions on `UserRole`/`UserType` (e.g. restricting which data a given
role/level can fetch). `Allowed(result) bool` is the separate, simple "is this a plain
ALLOW" helper for the common case — kept as a distinct function from `Check` on purpose,
so code needing more than yes/no always has direct access to the raw `Result` instead of
being funneled through a boolean.

Also generated, unconditionally like `Check`/`Allowed`: `Authorise(ctx, code) (*AuthoriseResult,
error)`, a GET client for kpass's separate `GET {KPASS_URL}/api/v1/authorise` endpoint — resolves a
one-time authorization `code` (issued by whatever login/auth flow the frontend used — kpass's own,
or something else entirely) into the calling user's identity: `AuthoriseResult{UserId, UserType,
UserName, UserRole, OrgId}`, all `string`, decoded from a flat (unwrapped) JSON body with camelCase
keys — a deliberately different shape from `Check`'s `{status, message, access, ...}` envelope,
since this is a distinct kpass endpoint with its own response convention. Unlike `Check`, which
sends `KPASS_CLIENT_ID`/`KPASS_API_SECRET` as headers, `Authorise` sends `code`/`client_id`
(`KPASS_CLIENT_ID`)/`api_secret` (`KPASS_API_SECRET`) as query params — `api_secret` only when set,
matching the endpoint's own optional-parameter contract. Because this endpoint's response has no
in-body error signal the way `Check`'s `Status` field does, `Authorise` explicitly checks
`resp.StatusCode != http.StatusOK` before decoding — without it, an invalid/expired code returning
a non-2xx status would silently decode into a zero-valued `AuthoriseResult` instead of erroring.

Also generated, only when the target app has a real `-auth` mechanism: `UserIDFromRequest
(r *http.Request) (userID string, ok bool)`, extracting the request's authenticated
subject to pass into `Check`. Since `init kpass` runs independently of `create app`
(possibly long after), `AuthKind` isn't available as a stored flag — `detectAuthFiles`
detects it after the fact, from whether `auth/jwt.go`/`auth/session.go` exist on disk,
purely to decide *whether* to generate the function at all. Its body no longer
re-parses the bearer token/session cookie itself — it's a thin wrapper,
`return auth.Subject(r)`, over the same context value `middleware/auth.go.tmpl`'s
`RequireAuth` already attached (see "Authentication" above); `kpass` keeps its own
named `UserIDFromRequest` export purely for call-site convenience (`userID, ok :=
kpass.UserIDFromRequest(r)` reads naturally next to `kpass.Check`), not because it
does anything `auth.Subject` doesn't. For an app scaffolded with `-auth none`,
`UserIDFromRequest` isn't generated at all — there's no subject to extract — and the
caller supplies `Check`'s `userID` however that app already identifies callers.

Not built (same "primitives only" precedent as `-auth`'s missing `/login` route):
`kpass.Check`/`Allowed` aren't wired into `-protected`/`middleware.RequireAuth`
automatically — a handler/service calls them explicitly, typically as `userID, ok :=
kpass.UserIDFromRequest(r)` followed by `kpass.Check(ctx, userID, "myapp.users.create",
nil)`.

`NewKpass` also writes `services/kpass/kpass.go` (from a second embedded template,
`kpass_templates/kpass_service.go.tmpl` → `kpassServiceTmpl`) — a one-time,
hand-editable wrapper (`CheckAccess(ctx, userID, resource, extra) (bool, error)`, calling
`kpassclient.Check`/`Allowed` internally) that a handler/service is meant to call instead
of reaching into `kpass.Check` directly. `kpass/kpass.go` itself was already never
touched by `nexler update` (no retrofit function exists for it), so this isn't fixing a
correctness risk the way the same split does for kgate below — it's purely for
convention consistency between the two integrations, and so an app's own authorization
logic (caching, role-based overrides, logging) has one obvious place to live rather than
being duplicated at every `kpass.Check` call site. Same collision guard as `kpass/kpass.go`
(errors if it already exists). An app that ran `init kpass` before this file existed picks
it up via `nexler update` (`ensureKpassService` in `kpass.go`) — writes it only if
missing, a silent no-op otherwise or for an app that never ran `init kpass` at all.

#### `nexler init kgate`: kgate message-broker integration

`nexler init kgate [-dir <app-dir>]` (`cmd/nexler/initkgate.go` → `scaffold.NewKgate` in
`internal/scaffold/kgate.go`) adds a client for kgate — klivolks' message broker — to an
*existing* generated app, the same "pure local file scaffolding, no live network needed"
shape as `nexler init kpass` (a new embedded template tree, `kgate_templates/kgate.go.tmpl`,
`kgateTemplateFS` in `templates.go`). Unlike `kpass`, though, **it requires the target app
to already have a core database connection** (`-db` at `nexler create app` time) —
`NewKgate` checks this via the existing `readCoreDBType` (`route.go`, already used for
`store.go.tmpl`'s informed TODO) and fails fast, with a clear error, if absent.

That requirement exists because channel subscriptions are deliberately **not
static/env-driven**. The obvious design — a blocking `Subscribe(ctx, channel, handler)`
the caller manages itself, mirroring a typical reference client — was explicitly rejected:
a workflow (e.g. order creation) needs to start listening on a new channel *while the app
is already running*, not just at startup from a fixed config. Instead, `Subscribe(ctx,
channel) error` records channel in the **core kgate-channel registry**
(`core/kgate_channels.go.tmpl` — see "The `core` package" above; `core.AddKgateChannel`)
and adds it to a single shared WebSocket connection multiplexing every subscribed channel,
returning immediately — safe to call from inside a request handler or any in-flight
workflow. All subscribed channels share one connection (rather than the one-connection-
per-channel design this feature originally shipped with) because kgate's gateway allows
only one live connection per `X-Client-Id` — opening several concurrently under the same
client ID caused each new one to evict another, an endless reconnect churn (confirmed by a
real client hitting exactly this in production before the fix).
`Unsubscribe(ctx, channel) error` removes channel from the shared subscription set and the
registry; it does **not** tear down the shared connection itself — other channels may
still be using it, and kgate has no per-channel unsubscribe frame to send instead, so an
event for that channel between this call and the next reconnect is simply not dispatched
again. `ResumeAll(ctx) error` seeds the shared subscription set with every channel already
recorded. Unlike the
earlier "primitives only" precedent set by `kpass`'s missing `/login` route, `ResumeAll` **is**
wired automatically — but not via `main.go` (which stays untouched: it's scaffolded once by
`create app` and never edited again by any later command, `init kgate` included). Instead,
`Register(mux)` — already auto-inserted into `routes/public/public.go` by `NewKgate`, and
already called unconditionally from `main.go`'s existing one-time `routes.Register(mux)` — now
also kicks off `ResumeAll(context.Background())` in a background goroutine the moment it runs,
logging (never failing `Register`) if it errors. This makes resuming a fully zero-config,
"just works" property of every kgate-enabled app: a fresh process with channels already on
record resumes listening to them with no manual wiring at all; a process with none recorded yet
starts nothing and stays idle until `Subscribe` records a real one. A package-level `channels
map[string]struct{}` behind a `sync.Mutex` (`subMu`) tracks the shared subscription set, and a
second `sync.Mutex` (`connMu`) guards the single live `*websocket.Conn` (`active`, nil when
disconnected) and serializes every write to it — gorilla/websocket permits only one concurrent
writer per connection. A `started` bool (also behind `subMu`) makes `ensureLoopStarted` a
one-time gate: the single reconnect-loop goroutine (`subscribeLoop`/`subscribeOnce`) starts at
most once per process, on the first `Subscribe` or `ResumeAll` call, and re-sends a subscribe
frame for every channel in the shared set on every (re)connect — so a repeat `Subscribe` on an
already-listening channel, and a repeat `ResumeAll`, are both harmless no-ops.

An app that ran `init kgate` before channels were multiplexed over one shared connection picks
it up via `nexler update` (`ensureKgateSharedConnection` in `internal/scaffold/kgate.go`,
registered in `update.go`'s `updateChecks`) — a narrow, anchor-based patch of the exact original
`Subscribe`/`Unsubscribe`/`ResumeAll`/`startSubscription`/`subscribeLoop`/`subscribeOnce` block
(same "narrow, anchor-based patch of a known-exact body" precedent `ensureKgateResumeAll`
already established), leaving `handleEvent`, `HandleWebhook`, `Register`, and `Publish`
completely untouched — safe to run regardless of whether `handleEvent` has been hand-customized.
Same "has it been hand-rewritten?" guard as every other anchor-based kgate retrofit: a block
that doesn't match the known original errors out instead of being overwritten, naming the file
and the exact snippet to add by hand.

An app that ran `init kgate` before this behavior existed picks it up via `nexler update`
(`ensureKgateResumeAll` in `internal/scaffold/kgate.go`, registered in `update.go`'s
`updateChecks`) — a narrow, anchor-based patch of `Register`'s exact original body (plus adding
a `"log"` import if missing via `route.go`'s `insertImport`), deliberately *not* a full-file
regeneration: `handleEvent` in this same file is the documented, expected hand-edit point for
real business logic, so the retrofit must never risk touching it. Same "has it been
hand-rewritten?" guard as `ensureResponseJSONRaw`/`ensureJWTClaims` if `Register`'s body doesn't
match the known original — errors out naming the file and the exact snippet to add by hand,
rather than silently overwriting a customized `Register`.

`Register` also documents `POST /webhooks/kgate` with `openapi.Register` (right after the
`mux.HandleFunc` call, `ReqType: webhookEvent{}` — the file's own already-exported-field struct,
reused as-is rather than adding a new type — `ReqContentType: "application/json"`, no `RespType`
since the handler writes no JSON body on success, `Protected: false` since this route
authenticates via the HMAC `X-Signature` check inside `HandleWebhook`, not
`middleware.RequireAuth`, so `Protected: true` would be misleading) — previously the one
generated route invisible in `/openapi.json`/Swagger UI, unlike every route `nexler create
<route>` generates, which always pairs its `mux.HandleFunc` with `openapi.Register`. `Register`
also `Subscribe`s to a channel literally named `"test"` alongside `ResumeAll` (a sibling
statement, not nested in `ResumeAll`'s own error branch — a `ResumeAll` failure shouldn't skip
attempting this) — a built-in smoke test that the whole pipeline (core DB registry + WebSocket
connectivity) actually works end to end on a fresh app; the generated comment says it's safe to
leave in (`Subscribe` is idempotent for an already-subscribed channel) or remove once kgate's
wiring is confirmed. Both retrofit via `ensureKgateOpenAPIAndTestSubscribe` (`kgate.go`,
registered in `update.go`'s `updateChecks` *after* `ensureKgateResumeAll` — its anchor is
`kgateRegisterPatched`, the body `ensureKgateResumeAll`'s own patch produces, so it depends on
that one having already run), same anchor-based-patch-plus-import-insertion shape as
`ensureKgateResumeAll` itself.

Because a restart's `ResumeAll` can only recover channel *names* from the database, not
arbitrary handler closures, every channel — whether from a live subscription or the
webhook fallback below — is dispatched to a **single** generated `handleEvent(ctx,
channel, payload json.RawMessage) error` stub (`// TODO: process the event`, switch on
`channel` for per-channel behavior); this is a deliberate simplification, not an
oversight — a per-call handler parameter would be unrecoverable after a restart. The
WebSocket loop (`subscribeOnce`, dialing `KGATE_WS_SERVER` with `X-Client-Id`/`Origin`
headers via `github.com/gorilla/websocket` — Go's stdlib has no WebSocket client, so this
is a real third-party dependency in the *generated app's* `go.mod` only, same exception
already carved out for `-db`'s SQL/Mongo drivers, needing one `go mod tidy`; nexler's own
`go.mod` stays untouched since `init kgate` needs no live connection itself) acks an event
back only after `handleEvent` succeeds, and `subscribeLoop` retries with capped
exponential backoff (1s → 30s) on any connection error instead of giving up after one
failure. `Publish(ctx, channel, payload)` is unchanged from the obvious design: a plain
`apiclient.Post` to `{KGATE_HTTP_SERVER}/messenger/publish` with an `X-Client-Id` header.

The webhook fallback (`HandleWebhook`, standard `http.HandlerFunc` signature) is the one
piece of this feature that *is* wired automatically — `Register(mux)` is inserted into
`routes/public/public.go` by `NewKgate` itself, reusing `route.go`'s `wireAggregator`
(generalized from a route-specific `handlers/<relDirSlash>` importer into a plain
`wireAggregator(appDir, group, importPath, alias)` so a non-route package like `kgate` can
reuse the same marker-based insertion). This asymmetry is deliberate: a webhook payload
carries its own `channel` field, so the fallback route is channel-agnostic and safe to
wire up completely automatically, the moment `init kgate` runs — unlike `Subscribe`, which
is inherently business logic (*which* channel matters) nexler can't know in advance.
`HandleWebhook` verifies `X-Signature` (HMAC-SHA256 of the raw body, keyed by
`KGATE_WEBHOOK_SECRET`, constant-time-compared) before decoding and calling `handleEvent`
— note that registering this app's public `/webhooks/kgate` URL *with* kgate's server as
this client's fallback endpoint is a separate, manual, out-of-band step; no such
registration API was available to generate against, and the generated doc comment says so
explicitly rather than silently assuming it's handled.

Five env vars, all blank/appended-if-missing via `ensureEnvVars` (generalized from
`kpass.go` to take a comment-header string, since it was hardcoded to kpass's own comment
before): `KGATE_CLIENT_ID`, `KGATE_WS_SERVER` (the **full** WebSocket URL, unlike
`KGATE_HTTP_SERVER` below — `Subscribe` dials it directly, with no path appended),
`KGATE_HTTP_SERVER` (a base URL; `Publish` appends `/messenger/publish`), `KGATE_ORIGIN`
(no automatic derivation — only the app owner knows what kgate's server actually
allowlists for this client), `KGATE_WEBHOOK_SECRET`. Same **not** `{APPNAME}_`-namespaced
convention as `KPASS_*` — shared vendor credentials, not per-app.

`nexler init db` provisions `core_kgate_channels` alongside `core_config`/`core_error_log`
(`kgateChannelStatements` in `cmd/nexler/initdb.go`, mirroring `errorLogStatements`'s
per-dialect style — `core_kgate_channel_add`/`_remove` stored procedures for SQL backends,
a unique index on `channel` for Mongo) — needed before `Subscribe`/`Unsubscribe`/
`ResumeAll` will actually work against a real database.

##### Resilient delivery: structured logging, keepalive, permanent-vs-transient errors

`kgate.go.tmpl`'s WebSocket loop was revised again after the shared-connection fix above,
adding: a package-level `logger` (`log/slog`, JSON to stdout, tagged `component=kgate`) used
throughout instead of `log.Printf`, so connection-lifecycle events (dial, connect, subscribe,
receive, process, ack, disconnect) are structured and — deliberately — never include an
event's payload, only its channel and message_id; a bounded 15s dial timeout
(`context.WithTimeout` + `DialContext`, separate from gorilla's own `HandshakeTimeout`) so a
hung DNS lookup or half-dead proxy can never stall the reconnect loop indefinitely; WebSocket
ping/pong keepalive (5s ping period, 15s read deadline — measured against a live kgate
deployment showing sockets dying after ~10s of silence, well before a more conventional
20s/60s cadence would ever get a chance to send anything); `runSubscribeOnce`, a `recover()`
wrapper around `subscribeOnce` so a panic anywhere in connection setup or the read loop (not
just inside `handleEvent`, which `dispatchEvent` already guards) is logged and treated as an
ordinary disconnect rather than killing the goroutine `subscribeLoop` depends on;
`dispatchEvent`, which recovers a panic from `handleEvent` itself for the same reason;
`permanentError`/`permanentf`/`isPermanent`, marking an event-processing failure as one no
amount of redelivery can fix (e.g. a payload that will never successfully decode) — acked
anyway despite the error (so it doesn't resurface as a "processing failed" log line on every
future reconnect for the rest of the process's life) but still logged loudly, versus a
transient failure (a downstream dependency that might succeed on retry), which is left
unacked so kgate may redeliver it; and `unwrapPayload`, undoing kgate's JSON string-encoding
of every delivered payload (the wire format double-encodes: `payload` is itself a JSON-encoded
string, not the object/array directly) before `handleEvent` ever sees it. `HandleWebhook`
dispatches through `dispatchEvent` too, mapping a permanent error to a 400 response and any
other error to a 500 (previously a flat 500 for everything). `Publish`'s `publishBody.Payload`
is now always a JSON-encoded string on the wire (matching what subscribers actually receive,
undone on the way in by `unwrapPayload`), and it sends an `Origin` header alongside
`X-Client-Id` — both dial and publish requests need it, or kgate's gateway rejects the
request.

Four `nexler update` checks bring an app scaffolded before this revision up to date, in this
order (each anchors on the previous one's output, same "must run after" dependency chain as
`ensureKgateResumeAll` → `ensureKgateOpenAPIAndTestSubscribe`): `ensureKgatePublishEncoding`
(the `publishBody`/`Publish` change), `ensureKgateResilientDelivery` (the whole
logger/keepalive/dispatchEvent/permanentError/unwrapPayload block — anchored on
`ensureKgateSharedConnection`'s own output, `kgateSubscriptionPatched`), and
`ensureKgateWebhookDispatch` (`HandleWebhook`'s call site). All three are scoped well away from
`handleEvent` itself, same "never touch the documented hand-edit point" precedent as every
earlier kgate retrofit.

##### Extracting business logic into `services/kgate`

Two things about `kgate.go.tmpl` were still business logic sitting inside a file `nexler
update` patches via literal anchor-text matching: `handleEvent`'s real implementation, and
`Register`'s hardcoded startup `Subscribe(ctx, "test")` smoke-test call — every anchor-based
retrofit above has to either carve `handleEvent` out as a no-touch zone, or (for `Register`)
risk a hand-edited startup-subscription list going out of sync with a future patch. Fixed by
turning both into package-level function-variable hooks: `EventHandler` (defaults to
`defaultEventHandler`, a no-op) is what `handleEvent` now delegates to; `OnStartup` (defaults
to `defaultOnStartup`, the original `Subscribe(ctx, "test")` smoke test) is what `Register`'s
background goroutine now calls instead of embedding the `Subscribe` call directly. Neither
hook is ever touched again by any anchor-based patch — `kgate/kgate.go` becomes pure,
permanently-stable infrastructure, matching `kpass/kpass.go`'s already-safe status (see above).

A new one-time-written package, `services/kgate/kgate.go` (from a second embedded template,
`kgate_templates/kgate_service.go.tmpl` → `kgateServiceTmpl`), holds the actual business logic:
`Init(ctx)` (startup subscriptions — starts as the same `"test"` smoke-test Subscribe, meant to
be edited) and `HandleEvent(ctx, channel, payload) error` (the real per-channel event
processing). Its `init()` sets `kgateclient.EventHandler = HandleEvent` and
`kgateclient.OnStartup = Init` — dependency injection specifically to avoid an import cycle:
`services/kgate` imports the `kgate` client package (to call `Subscribe`/`Publish`), so `kgate`
itself must never import `services/kgate` back, which a direct "handleEvent calls into
services/kgate" design would have required. `NewKgate` writes this file (same collision guard
as `kgate/kgate.go` — errors if it already exists) and wires a blank side-effect import,
`import _ "{modulePath}/services/kgate"`, into `routes/public/public.go` via a new
`wireBlankImport` (`route.go`, `wireAggregator`'s sibling for a package whose only job is
running its own `init()` rather than exposing a `Register(mux)` — `wireAggregator` itself can't
be reused here since it always pairs an import with a `<alias>.Register(mux)` call, invalid Go
for a blank alias) — this is what actually makes `services/kgate`'s `init()` run, since nothing
else in the generated app imports it.

An app that ran `init kgate` before this split existed picks it up via `nexler update`
(`ensureKgateServiceExtraction`, registered last among the kgate checks — its anchors assume
every check above has already applied). `handleEvent` must match one of two known pristine
shapes **exactly** — the current stub (calls `unwrapPayload`) or the older pre-resilient-delivery
one (a bare `// TODO: process the event`), covering an app that jumps straight from very old to
current in one `nexler update` run — or the retrofit errors out naming the manual migration
steps, rather than guessing how to relocate hand-written business logic. This is a deliberate,
narrower safety bar than every other kgate retrofit's "does the known block match:" check, since
getting this one wrong would mean silently discarding a developer's real event-processing code.

#### `nexler init kube`: Kubernetes manifest

`nexler init kube [-dir <app-dir>] [-registry dockerhub|github] [-image <ref>] [-namespace
<ns>] [-replicas <n>]` (`cmd/nexler/initkube.go` → `scaffold.NewKube` in
`internal/scaffold/kube.go`) adds `k8s/deployment.yaml` to an *existing* generated app —
the same "pure local file scaffolding, no live network connection" shape as `nexler init
docker`, and the counterpart that actually runs what `init docker`/`init ci` build and
publish. One file, three `---`-separated YAML documents: a `Secret` (`<app>-env`,
`stringData` populated from every real `KEY=VALUE` line in `.env`, via a new
`readEnvVars` — `readEnvPort`'s parsing generalized from "just the `_PORT=` line" to
"every line, in file order"), a `Deployment` (`envFrom: - secretRef` referencing that
Secret, `tcpSocket` readiness/liveness probes on the app's `.env` port — TCP rather than
HTTP because no generated app exposes a health-check route today — and conservative
default `resources.requests`/`limits` as a starting point to tune), and a `ClusterIP`
`Service` on the same port. Refuses if the file already exists, same precedent as
`NewDocker`/`NewCI`.

Unlike `init ci`'s GitHub Actions workflows — which are fully static, resolving
`ghcr.io/<owner>/<repo>` or `<dockerhub-user>/<repo>` at *workflow run time* via GitHub's
own context variables (`${{ github.repository_owner }}`, `${{ secrets.DOCKERHUB_USERNAME
}}`), so nexler itself never computes an image string for them — `k8s/deployment.yaml` is
applied directly by `kubectl`, so it needs a concrete, static `image:` value baked in at
scaffold time. That resolution is split into an exported `scaffold.ResolveKubeImage(appDir,
image, registry, dockerHubUser)`, kept deliberately non-interactive (this repo's prompting
always lives in `cmd/nexler`, never `internal/scaffold`) so `internal/scaffold`'s
"pure, embed-only, no network/git access" claim (see "What this is" above) stays true here
too: `-image`, when set, always wins outright; `-registry github` derives
`ghcr.io/<owner>/<repo>:latest` purely by parsing `go.mod`'s own module path (the existing
`readModulePath`) — erroring, rather than guessing, if that path isn't
`github.com/<owner>/<repo>`-shaped (deliberately *not* shelling out to `git remote` to
discover this, preserving the "scaffolding never touches git" rule); `-registry dockerhub`
derives `<user>/<repo>:latest`, where `<user>` has no local source of truth at all (unlike a
GitHub owner, a Docker Hub username isn't recoverable from any file already on disk) — so
`cmd/nexler/initkube.go` prompts for it directly (`promptRequired`, same pattern
`initci.go` already established for `-registry` itself) and passes it through.
`appServiceName(appDir)` (factored out of `docker.go`, where it used to be inlined as
`NewDocker`'s own `service` variable — now shared, since `kube.go` needs the identical
value for the Docker Hub image name, every resource's name, and the pod-selector label) is
the same `sanitizeIdent(filepath.Base(appDir))` derivation `docker-compose.yml`'s service
name already used.

`-namespace` is omitted from every resource's `metadata` entirely when left blank (the
default) — `kubectl apply -n <ns>` or the current context decides, rather than a namespace
being silently hardcoded into a file that might get applied against any cluster/namespace.
`-replicas` defaults to `1`.

`k8s/deployment.yaml` is added to `.gitignore` (created if the app doesn't have one yet,
same `ensureGitignoreLine` helper `init docker` already uses for `docker-compose.yml`) —
a stronger case than `docker-compose.yml` ever was: `docker-compose.yml` only *references*
`.env` via `env_file:` at container-runtime, never containing a secret value itself, but
Kubernetes has no equivalent "read a local `.env` file at deploy time" mechanism — the only
way to get an app's real env vars into the cluster at all is to bake them into the Secret
document above, so this file is genuinely secret-bearing and must never be committed.

### `nexler add`: vendoring static assets from npm

`nexler add <package>[@version] [-dir <app-dir>] [-as <name>]` (`cmd/nexler/add.go`) vendors a
static asset package (Bootstrap, jQuery, htmx, ...) from npm into an existing generated app's
`templates/static/vendors/<name>/`, so it can be referenced from hand-written HTML without a CDN
dependency at runtime — the same "no CDN, no network dependency at runtime" property
`templates.go.tmpl`'s own package doc comment already claims for nexler's built-in CSS/JS. It
deliberately never wires a
`<link>`/`<script>` tag into any page itself — same "primitives only" precedent as `-auth`'s
missing `/login` route and `kpass`'s `Check`/`Allowed` not being wired into `middleware.RequireAuth`
— add the ones you need to whichever page(s) actually use the package.

Unlike every other `nexler create`/`nexler init` command, this is **not** scaffolding — there's no
embedded template involved at all, and it's the one place besides `nexler init db` where nexler
itself (not just the generated app) needs real network access and a real `os/exec` call (see "What
this is" above). It's entirely self-contained in `cmd/nexler/add.go`, deliberately outside
`internal/scaffold`, so that package's "pure, embed-only" scaffolding claim stays literally true.

`runAdd` shells out to `npm install <package> --prefix <scratch-tmp-dir> --no-save --no-audit
--no-fund` (streaming npm's own stdout/stderr live, since this is the one nexler operation that
talks to the network and the user should see registry/progress/error output as it happens) into a
throwaway `os.MkdirTemp` directory — never the app's own directory, so a generated app never gets a
`package.json` or `node_modules` of its own. `parsePackageSpec` accepts exactly the four common npm
spec forms (`name`, `name@version`, `@scope/name`, `@scope/name@version`) and rejects anything else
(git/tarball/URL specs, `npm:` aliases, a bare `user/repo`) up front with a clear error — those
don't have a bare name npm's own `node_modules` layout would agree with (npm derives the installed
directory name from the resolved package's own `package.json`, which can differ arbitrarily from a
git/URL/alias spec), so best-effort parsing would only fail confusingly later instead.

**File selection** (`selectSource`): if the installed package has a `dist/` folder, its contents are
copied flattened and completely unfiltered — dist output is a package's own build artifact, meant
to ship as-is. Otherwise the whole package directory is copied instead, minus obvious junk
(`isJunkEntry`): tooling/metadata directories (`node_modules`, `.git`, `.github`, `test(s)`,
`__tests__`, `docs`, `example(s)`) and files (`package.json`, `package-lock.json`, `.npmignore`,
`.gitignore`, `.gitattributes`, `.editorconfig`, `.babelrc`, `.npmrc`, `tsconfig.json`, and anything
case-insensitively prefixed `README`/`CHANGELOG`/`LICENSE`/`CONTRIBUTING`/`HISTORY`/`AUTHORS`/
`NOTICE`) — none of which is a static asset, unlike `dist/`'s output, which is never filtered at
all since it's already exactly what the package intends to ship for browser use. `copyTree` walks
the source with `filepath.WalkDir` (a real OS directory, not `scaffold.go`'s `embed.FS`-based
`fs.WalkDir`) and copies bytes verbatim — deliberately **not** reusing `scaffold.go`'s
`processFile`/`render`, which run every non-`.tmpl` file through an LF-normalizing `text/template`
pass meant for nexler's own hand-authored templates; that would be actively wrong here; a minified
vendored bundle can easily contain a literal `{{` that isn't a template action. Symlinks inside
`node_modules` are skipped, not followed — a stated limitation, not a bug.

`<name>` (the destination folder under `vendors/`) defaults to the bare package name with its scope
stripped and `/` replaced by `-` (`defaultVendorName`, e.g. `@popperjs/core` → `popperjs-core`),
overridable via `-as`; either way it's validated against `vendorNameRe` (`^[A-Za-z0-9][A-Za-z0-9._-
]*$`, the same allow-list-regex style as `route.go`'s `pathParamNameRe`) before being joined into a
filesystem path, so it can't smuggle in a `/`, `\`, or `..`. **Re-running `nexler add` for the same
`<name>` always refreshes it** — `os.RemoveAll(destDir)` then recreate, no `-force` flag — since a
vendored third-party directory isn't something a developer hand-edits in place, unlike e.g.
`kpass/kpass.go`, which deliberately refuses to overwrite. One accepted trade-off of always-refresh:
`destDir` is cleared *before* copying starts, so an error partway through `copyTree` (disk full, a
permissions issue) can leave a half-copied `vendors/<name>/` with the previous good version already
gone — a copy-to-temp-then-rename would avoid this, but adds real complexity for a low-probability
failure mode on what's meant to be a simple, easily-rerunnable command.

Because `templates/static` is embedded into the **generated app's own binary** at **its own** build
time (`//go:embed static` in `templates/templates.go`, rendered from
`internal/scaffold/templates/templates/templates.go.tmpl` — see `RegisterStatic` under "HTML
responses" below), a newly vendored package isn't served until the app is rebuilt; `nexler add`'s
own success message says so explicitly rather than leaving it to be discovered.

### HTML responses (`-response html`)

A route scaffolded (or extended, via `-methods`) with `-response html` gets
`response.HTML(w, r, module, name, title, data)` instead of `response.JSON` in each method's handler
— `module` is always the route's `RelDirSlash` (e.g. `purchase/verify`) and `name` is precomputed
per method as `routeMethod.HTMLContentName` in `route.go` (`<pkg>-content`, or
`<pkg>-<verb>-content` when a single `nexler create` invocation generates more than one method, to
avoid two methods overwriting each other's content file).

Unlike every other template in this repo, `response.HTML` does *not* read from the embedded
`templateFS` — it reads a content file (`<name>.html`) and `layout.html` straight off disk, on
every request, resolving each independently through `resolveHTMLFile(module, filename)`: a
module's own `templates/html/<module>/<filename>` if present, else the shared
`templates/html/shared/<filename>` fallback. This is how the layout in particular is meant to be
shared across every module rather than duplicated. Both files (whichever copy — module or shared —
ends up used) are written once, at CLI scaffold time, not lazily on first request; `response.HTML`
only ever reads them, reporting a clear error naming both paths it checked if neither exists (e.g.
hand-deleted) instead of silently recreating a placeholder.

For a route, `-layout` (default `shared`; only asked/meaningful when `-response html`) picks which
layout.html gets used: `shared` (default) relies on the app-wide `templates/html/shared/
layout.html`, written once by `writeSharedHTMLLayout`; `module` gives this one route its own
`templates/html/<module>/layout.html` copy instead, written by `writeHTMLTemplates` itself — a
module's own copy always wins over the shared one at request time since `resolveHTMLFile` checks
it first. `route.go`'s `writeHTMLTemplates` writes the content file(s) into the route's own module
folder regardless, plus whichever layout `-layout` calls for — called from both `NewRoute` and
`addToExistingRoute`, so adding a method later (or switching `-layout` on a later `nexler
create` for the same route) never clobbers a hand-edited template, only ever adding what's
missing. For the app's own homepage under `nexler create app -ui`, `scaffold.go`'s
`writeAppUIHomepage` writes `home.html` into module `"home"` the same way, and always uses the
shared layout (no `-layout` choice at the app level). Both ultimately reuse the same
`defaultHTMLContent`/`defaultHTMLLayout`
placeholder constants and the `writeIfMissing` helper (all in `route.go`, same `scaffold`
package) — existing files are never clobbered, only missing ones created; `writeSharedHTMLLayout`
is the one place that actually writes to `templates/html/shared/layout.html`. The default layout
references `/static/css/styles.css` and `/static/js/site.js`, which — unlike the per-module/shared
html/ files — *are* embedded and served by the existing `templates.RegisterStatic`; they're
scaffolded once into every new app under `internal/scaffold/templates/templates/static/{css,js}/`.

Content is rendered first (via `html/template`, so `{{.Title}}`/`{{.Data}}` are auto-escaped), then
its output is embedded as `template.HTML` into the layout's `{{.Content}}` — there's no
`{{define "content"}}` block convention to keep the two files in sync, which matters because the
content file may just be a placeholder that doesn't know about its layout's structure.

The page template data also carries `{{.Subject}}` (the caller's authenticated subject, via
`auth.Subject(r)` — always present as a field, but always `""` for an `-auth none` app, since
there's no `auth.Subject` to call at all in that case) and `{{.Path}}` (`r.URL.Path`, always
present). `HTML`'s own rendering is factored into an unexported `composeHTML(r, module, name,
title, data) ([]byte, error)` — returns bytes rather than writing directly, so it can be shared by
`HTML` (always writes 200) and two more exported functions, `HTMLError(w, r, module, status, title,
data)` and `Unauthorised(w, r, module, data)` (a fixed 403), which render `module`'s
`error-content.html`/`unauthorised-content.html` the same way, for an admin page that wants a
styled error instead of `Error`'s plain JSON envelope — falling back to that same JSON envelope if
the page itself can't be found/rendered, rather than risking a recursive failure. A `>= 500` through
either goes through `core.LogError` the same way `Error` already does (gated on `HasDB`, same as
`Error`'s own gate).

`composeHTML` also composes three more **optional** partials — `header.html`/`sidebar.html`/
`footer.html`, resolved through the same `resolveHTMLFile` hierarchy walk and rendered with the same
page data, injected into `layout.html` as `{{.Header}}`/`{{.Sidebar}}`/`{{.Footer}}` via a new
`renderOptionalPartial` — the one place `resolveHTMLFile`'s "not found" case is *not* an error:
missing anywhere in the hierarchy (not just this module, all the way up through `shared/`) simply
renders as an empty string, so a module with no header/sidebar/footer of its own is unaffected.

An app scaffolded before any of this existed (the current file only had `HTML` itself, inlining what
is now `composeHTML`'s body, with no `Subject`/`Path`/partials/`HTMLError`/`Unauthorised`) picks it
up via `nexler update` (`ensureResponseHTMLUpgrade` in `route.go`). Same "response.go is realistically
hand-extended, never a full rewrite" reasoning `ensureResponseJSONRaw` already established for this
file: `HTML`'s old body has zero per-app templating, so it's byte-identical across every
pre-upgrade app, making it a safe, exact anchor to replace (erroring, naming the file, if it's been
hand-rewritten) — the retrofit re-renders the *current* `response.go.tmpl` to a string (with this
app's own `HasDB`/`-auth` inferred from disk, the same `readCoreDBType`/`detectAuthFiles` precedent
`ensureServiceAuth` already set, never written to disk directly) purely to extract the new `HTML`
body and the new-functions block correctly conditioned for this app, then splices both into the
existing file — the new-functions block inserted right before `responseMarker`, coexisting safely
with `ensureResponseJSONRaw`'s own `JSONRaw` insertion at the same anchor.

`nexler create app <name> -ui` wires the app's own homepage (`GET /`) through this same mechanism —
`handlers/home/home.go.tmpl` branches on `.UI` to call `response.HTML(w, r, "home", "home", ...)`
instead of the default `templates.ServePage("home.html")` (the embedded, compile-time homepage).
Under `-ui`, `NewApp`'s `fs.WalkDir` callback also skips generating the now-unused embedded
`templates/html/home.html` and `templates/static/css/home.css` entirely (see the `cfg.UI` switch
in `NewApp`), so a `-ui` app never ships a dead, never-served homepage alongside the real one.

`openapi.go.tmpl` documents this correctly via `Operation.RespContentType` (set to `"text/html"` by
`handler.go.tmpl`/`register_methods.tmpl` when `$.HTMLResponse` is true): the `"200"` response
becomes a plain `{"type": "string"}` body under `text/html` instead of the usual JSON
data-envelope $ref.

Note: because `.tmpl` files are themselves rendered through `text/template` at CLI build/scaffold
time (see below), `response.go.tmpl`'s own `{{.Title}}`/`{{.Content}}`/`{{.Data}}` placeholders —
meant to be evaluated later, at the *generated app's* runtime — are written as
`` {{"{{.Title}}"}} `` etc., the same "print a literal `{{`" idiom used to escape any other literal
`{{` that must survive into generated output.

### Idempotent route wiring via marker comments

Generated files carry fixed marker comments that later CLI invocations anchor on to insert new
code without reparsing Go:

- `// nexler:routes` in `routes/public/public.go` / `routes/protected/protected.go` — where new
  route imports/`Register(mux)` calls get inserted (`wireAggregator` in `route.go`).
- `// nexler:handlers` in a route's `handlers/.../<file>.go` — where new per-method handler
  functions get inserted when re-running `nexler create <route>` with additional `-methods` on an
  already-scaffolded route. A package's handler code can span more than one file (see below), so
  this marker can appear in more than one file per package.
- `// nexler:register` in a route's `handlers/.../<file>.go` — where new `mux.HandleFunc`/
  `openapi.Register` calls get inserted. Since Go permits only one `func Register` per package,
  by default exactly one file per package ever contains this marker — that file is the package's
  **primary** file, and every method's registration wiring lives there regardless of which file
  its handler function itself is defined in. `-own-register` (below) is the one way a package
  legitimately ends up with more than one register-marked file, each its own independent primary
  with its own distinctly-named `Register<Name>` function.
- `// nexler:models` in a route's `models/.../<file>.go` — where new Request/Response struct
  pairs get inserted for the same case; also potentially more than one file per package.

If a route's handler package already exists (a file containing `// nexler:register` is found
under `handlers/<relDir>`), `NewRoute` dispatches to `addToExistingRoute` instead of erroring:
it adds only the newly-requested HTTP methods (skipping any already registered, detected by an
exact `"<VERB> <route>"` string marker inside the **primary** file specifically — checking any
other file here would risk letting a duplicate route/verb slip past and panic `mux.HandleFunc` on
a duplicate pattern at runtime) and leaves `services/`/`store/` untouched, since those are generic
per-route, not per-method.

**`-file` can now create a genuine second file.** `fileName` (from `-file`, defaulting to the
package name) names the specific handler/model file this invocation targets. If
`handlers/<relDir>/<fileName>.go` already exists (and carries `// nexler:handlers`), the new
method(s) are appended to it, exactly as before this existed. If it doesn't exist yet,
`addToExistingRoute` renders a brand-new handler+model file pair for just the new method(s) —
the handler file via a dedicated `route_templates/handler_file.tmpl` (package decl + trimmed
imports — no `middleware`/`openapi`, since those are only needed by `Register` — + the method
bodies + a trailing `// nexler:handlers` marker, so a later invocation reusing the same `-file`
value can extend it too) rather than `handler.go.tmpl`, since a second file must never define its
own `Register`. Either way, the new method(s)' `mux.HandleFunc`/`openapi.Register` wiring is
always inserted into the **primary** file via `// nexler:register`, never into a secondary file.
`detectIdentifierCollisions` (below) and the file-pair-consistency check both run before any file
is written, so a `-file` value that would collide with another file already in the package (same
verb, no `-name` override) errors out instead of silently duplicating.

Which aggregator file a package's `Register(mux)` call lives in (`routes/public/public.go` vs
`routes/protected/protected.go`) is decided once, at the package's first creation, and never
changes afterward — `addToExistingRoute` skips aggregator wiring entirely, since the package is
already imported and registered there. `-protected` on a later `nexler create` for an
already-existing route only governs the newly-added method(s): each one independently wraps
itself in `middleware.RequireAuth` (or not) via its own `RegisterExpr`, regardless of what the
rest of the package's methods already do — so one package can end up with a mix of protected and
public methods, and of methods spread across more than one file. `detectExistingProtection` no
longer rejects this; it only powers an informational `RouteResult.Note` (printed by the CLI, not
an error) when this invocation's `-protected` differs from the package's original aggregator
classification.

If any marker is missing (e.g. a route scaffolded before this feature existed, or a hand-edited
file), the corresponding insert fails with an error naming the exact marker text to add back by
hand — it does not silently fall back to guessing a location.

#### A second independent resource in an existing package (`-own-register`)

`-file` naming a not-yet-existing file normally still folds its wiring into the package's one
existing primary (above) — but `-own-register` (bool, default `false`) makes that same `-file`
value instead create a genuinely **independent** second (third, ...) resource: its own
`Register<Name>` function (rendered from the very same `handler.go.tmpl` a package's first
resource uses — the template's one hardcoded `Register` identifier is now
`{{ .RegisterFuncName }}`, defaulting to `"Register"` everywhere else so every other render stays
byte-identical), its own `middleware`/`openapi` imports, its own `// nexler:handlers`/
`// nexler:register` markers, and its own model/service/store files — living alongside, never
touching, the package's existing resource. `wireAggregatorAdditionalCall` (`route.go`, a tolerant
sibling of `wireAggregator` that doesn't error when the package's import already exists) adds one
more `<alias>.Register<Name>(mux)` call into the aggregator, under the same import. This mirrors
a pattern already hand-built in a live app before this feature existed: two resources
(`email`/`sms`) sharing one `handlers/admin/providers` package, the second one hand-written with
its own `RegisterSms` because no scaffold path for it existed yet.

Requires both `-file` (a name not already used in the package) and `-name` (to disambiguate
`Register<Name>` — and every other identifier — from the package's existing resource); only
meaningful when the target package already exists. `addToExistingRoute`'s primary-file resolution
checks `-file`'s own target file **directly** for `// nexler:register` first, before ever falling
back to a directory-wide scan — this is what makes a later `nexler create <route>` adding a method
to, say, `sms` (without `-own-register`) correctly land in `sms.go`'s own `RegisterSms`, never in
`email.go`'s `Register`, regardless of which file a scan would otherwise find first. A directory
that already has two or more independent resources, given a *new* `-file` value with **no**
`-own-register`, is a hard, explicit error listing every existing resource rather than guessing
which one it should fold into.

#### Cross-invocation identifier collisions (`-name`)

Every generated identifier for a method — its handler function name, Request/Response type
names, and OperationID — is built purely from `-module`/`-submodule` + the HTTP verb
(`routeMethod.HandlerName`/`ReqTypeName`/`RespTypeName`/`OperationID`, all computed once in
`NewRoute`'s per-verb loop and referenced directly by the `.tmpl` files as the single source of
truth, rather than each template re-deriving `VerbTitle`+`Name` on its own). The URL route itself
never feeds into any identifier — only into the `"<VERB> <route>"` marker `addToExistingRoute`
already uses to detect "this exact method is already registered" (see above). That means a second
`nexler create <route>` invocation targeting a **different** route but the same
`-module`/`-submodule`/verb as an already-scaffolded one would, without a check, sail past the
already-registered marker (it's a different route string) and generate identifiers identical to the
first route's — `insertFragment` (or a brand-new secondary file) would then produce a second
`func HandleXGet(...)`/`type GetXRequest struct` in the same package: Go-illegal at best (duplicate
declaration), a silent `/openapi.json`-breaking duplicate `OperationID` at worst.

`addToExistingRoute` guards against this with `detectIdentifierCollisions`, run *before* any file
is written or inserted into (matching the same "error instead of silently guessing/corrupting"
precedent as the missing-marker errors above): for every method about to be added, it checks
whether that method's `HandlerName`/`ReqTypeName`/`RespTypeName`/`OperationID` already appears
anywhere in the package — the concatenated content of **every** `.go` file under
`handlers/<relDir>` and `models/<relDir>` (`concatGoFiles`), not just the one file this invocation
happens to be reading or writing, since a package can now span more than one file — and if so,
aborts the whole call with an error naming every colliding identifier — no partial writes. The fix
is `-name` (`RouteConfig.IdentName`), a per-route flag that overrides just the identifier base (not
the file/package location, which stays `-module`/`-submodule`-derived, same as `-file` already
does for the on-disk file name) — e.g. adding `/purchase/new` to a package that already has
`/purchase/verify` (both POST) needs `-name New` so the new route gets
`HandleNewPost`/`PostNewRequest`/OperationID `"postNew"` instead of colliding with the existing
`HandlePurchasePost`. Not interactively prompted (same as `-file` isn't proactively surfaced
beyond its own optional prompt) — the collision error itself is what tells a developer to pass it,
since the flag is only ever needed in this one situation.

`detectIdentifierCollisions` also guards one more identifier: `-own-register`'s derived
`Register<Name>` function name (checked whenever a non-empty name is passed in, which only
`addOwnRegisterResource` ever does) — since `-own-register` already makes `-name` effectively
mandatory (see above), this is mostly defense-in-depth against two separate `-own-register` calls
on the same package accidentally reusing the same `-name`.

`-name` also protects `routeMethod.HTMLContentName` (the `templates/html/<relDir>/<name>.html`
content file a `-response html` method writes to): it's keyed off the same identifier base
(`strings.ToLower(name)`, not `pkgName`) rather than the file name, so two routes that would
otherwise collide on their HTML content file are exactly the two routes `-name` is already
mandatory for — reusing that one mechanism means the content file can never silently collide in
any case nexler currently allows to proceed. Whenever `-name` isn't used, this is byte-identical
to the pre-existing `pkgName`-based name (`sanitizeIdent` already lowercases), so a single-route
package's content file name is unchanged.

### Optional service/store layers (`-service`/`-store none`, and adding them later)

A route doesn't have to generate all four layers. `-service`/`-store` (`resolveLayerRef` in
`route.go`) already meant "reuse an already-scaffolded package instead of generating a new one";
the literal value `"none"` is a third state — skip that layer entirely, so a route can stop at
handler + model. `routeData.HasService`/`HasStore` (`true` when ref'd *or* generated, `false` only
when explicitly skipped) drive a three-way branch in `handler.go.tmpl`/`handler_methods.tmpl`'s
service-call TODO comment and `service.go.tmpl`'s store-reference TODO comment, so the generated
comments never point at a layer that doesn't exist. The CLI simplifies this to a single gate —
`promptBool("Generate service and store layers for this route?", true)` — asked only when neither
`-service` nor `-store` was passed explicitly; answering no sets both to `"none"`, removing the
"have to mention store and service" friction of the two unconditional reuse prompts that existed
before this feature. Default is `true`/"generate", so nothing changes for anyone who doesn't care.

**Adding a missing layer later** to an already-existing (handler-only) route reuses
`addToExistingRoute` — re-running `nexler create <route>` with `-service`/`-store` passed
explicitly (even as an empty string, meaning "generate a fresh one now") retrofits the missing
layer(s), independently of whether any new HTTP method is also being added; its previous hard
requirement of at least one new method is relaxed to "at least one new method *or* a layer to add".
This is deliberately gated on `RouteConfig.ServiceRequested`/`StoreRequested` (`bool`, set in
`cmd/nexler/main.go` from `flag.FlagSet.Visit` — was the flag explicitly touched on *this*
invocation) rather than on the resolved string value: a blank `-service` means "generate fresh"
at first creation, but a scripted `nexler create <route> -methods POST` with no `-service`/`-store`
flags at all must never silently start generating a service/store an existing handler-only route
never had — only an explicit flag on that specific invocation can trigger the retrofit.
`addToExistingRoute` also re-detects `HasService`/`HasStore` from disk (`dirHasGoFile` on
`services/store/<relDir>`, not from this invocation's flag resolution) before rendering anything,
so a plain follow-up call's inserted methods get accurate TODO wording regardless of what
`-service`/`-store` happened to resolve to this time — and if a retrofit *does* happen in the same
call, `HasService`/`HasStore` are flipped to `true` before the method-insertion fragment renders,
so a method added in that same invocation immediately gets the "service exists" wording rather than
the stale "no service" one. The actual file write is shared with first-creation via
`writeRouteLayerFile` (extracted from `NewRoute`'s own per-layer loop). Reported back via
`RouteResult.LayersAdded` (e.g. `["service", "store"]`), printed by the CLI as "Added missing
layer(s) to the existing route: ...".

#### Standalone service/store (`nexler create store|service <name>`)

A service/store doesn't have to belong to a route at all. `nexler create store <name>` and
`nexler create service <name>` (`internal/scaffold/layer.go`'s `NewLayer`, dispatched from
`cmd/nexler/main.go`'s `runCreateLayer` — `store`/`service` are new top-level resource keywords
alongside `app` in `runCreate`'s switch, same "leading `/` means route, else a keyword" dispatch
`app` already established) scaffold a plain, unwired package: no handler, no model, no route, no
aggregator wiring — `NewLayer` never calls `wireAggregator`. Nothing imports it and nexler doesn't
track any reference to it going forward; a handler, another service, or hand-written code picks it
up later by importing it directly, the same as any other Go package — nexler's job ends at
generating the file. `<name>` addresses the package exactly the way `-service`/`-store`'s own reuse
references already do: `module[/submodule]`, e.g. `purchase` or `purchase/verify` (parsed the same
`sanitizeIdent`-per-segment way `resolveLayerRef` parses a reuse reference — no separate
`-module`/`-submodule` flags needed). `-file` overrides the generated file's base name; left blank,
it defaults to `<pkgName><kind>` — e.g. `nexler create service apps` (no `-file`) writes
`services/apps/appsservice.go`, `nexler create store apps` writes `store/apps/appsstore.go` — rather
than the plain `<pkgName>.go` a route's own service/store gets. Deliberately scoped to standalone
creation only: a route's handler/service/store/model already live in four differently-named
directories (`handlers/x`, `services/x`, `store/x`, `models/x`) and are never confused for one
another, but a standalone layer's file is more likely to be the only one directly under its
directory, worth self-describing by name alone — so `NewRoute` keeps generating plain
`<pkgName>.go` for its own service/store, unchanged.

Both reuse `writeRouteLayerFile` and `service.go.tmpl`/`store.go.tmpl` — the exact same templates
`nexler create <route>` itself renders — via a `routeData` populated directly by `NewLayer` rather
than by `NewRoute`. Since there's no route, `routeData.Route` stays empty; `RouteLabel` (new field,
`"this package"` when `Route` is empty, else `Route` itself) is what those two templates' TODO
comments actually print, so "TODO: persistence for /checkout" (route-tied) and "TODO: persistence
for this package" (standalone) share one template with no route-specific branching needed beyond
that one field. `routeData.Standalone` (`true` only via `NewLayer`) drives one further branch:
`service.go.tmpl`'s "no store linked" TODO points at `nexler create store <module>[/<submodule>]`
for a standalone service, vs. "re-run `nexler create {{ .Route }} ... -store <name>`" for a
route-tied one, since the latter command doesn't exist/make sense outside a route's context.
`nexler create service <name> -store <ref>` (optional) links the new service's TODO comment to an
already-scaffolded store the same way `-store` on `nexler create <route>` does — purely
informational, resolved via the same `resolveLayerRef`; `nexler create store` takes no equivalent
flag, since `store.go.tmpl` never references a service. A standalone service generated without
`-store` gets `HasStore: false` unconditionally (unlike a route's own service, which defaults to
assuming a same-path store was generated alongside it) — there's no implicit paired store for a
standalone service the way there is for a route's first-creation service.

### Per-route code generation (`route.go`)

`RouteConfig` → `routeData`/`routeMethod` precompute all identifiers in Go (handler names like
`HandleVerifyPost`, `RegisterExpr` — either bare or wrapped in
`middleware.RequireAuth(...)` when `-protected` — `OperationID`, content types, doc-comment
phrasing) so the `.tmpl` files themselves stay mostly free of conditional logic; they just range
over `.Methods`. `routeMethod.Protected` (feeding both `RegisterExpr` and `openapi.Register`'s
`Protected` field) is tracked per method, not just once for the whole route — a single `nexler
create` invocation still applies one `-protected` value to every method it creates/adds in that
call, but this is what lets a later invocation add differently-protected methods to an
already-existing package (see above). GET always gets a query-parameter-only request struct
regardless of `-body`;
other methods follow `-body json|form|multipart`. `-response json|html` (route-level, like
`-protected`, not per-method) picks `routeData.HTMLResponse`, the one conditional the handler
templates do branch on — see "HTML responses" above. OPTIONS is wired automatically for every
route via `middleware.HandleOptions` (always 200 OK) and cannot be requested explicitly —
`parseMethods` rejects it.

Route `-module`/`-submodule` values are sanitized to letters/digits only (`sanitizeIdent`) for use
as a Go package name; the package's import alias is the concatenation of the sanitized parts
(e.g. `-module purchase -submodule verify` → package `verify`, alias `purchaseverify`, files under
`purchase/verify/`).

### Dynamic path parameters (`{name}` segments in a route)

A route's URL can carry named wildcard segments, e.g. `/admin/user/{id}/profile` — Go 1.22+'s
`net/http.ServeMux` already routes `"METHOD /path"` patterns containing `{name}` (and a trailing
`{name...}` catch-all) natively, so `Register`'s `mux.HandleFunc` call (`handler.go.tmpl`) needed no
change at all: `cfg.Route` is passed through to it verbatim, same as always. What's generated around
that routing is what's new: `parsePathParams` (`route.go`) extracts every `{name}` segment from
`cfg.Route` at scaffold time — validating each as a legal Go identifier, rejecting duplicates, and
requiring a `{name...}` catch-all to be the last segment (the same restriction `net/http` itself
enforces) — into `routeData.PathParams`/`HasPathParams`/`PathParamArgs` (a precomputed Go source
literal like `"id", "code"`, ready to splice into a variadic call) /`PathParamsCSV` (for doc
comments). A literal `?` or `#` anywhere in the route is rejected outright with an error pointing at
`{name}` instead — `ServeMux` patterns don't parse query strings/fragments out of a route at all, so
what looks like one (e.g. a leftover `/admin/user/?id=u1` from before this feature existed) would
just become part of the literal path, silently never matching a real request.

Every generated handler method for such a route calls a new `request.DecodePath(r, &req,
<names...>)` (`request.go.tmpl`) right after its primary decode call — `handler.go.tmpl` and
`handler_methods.tmpl` both emit this call, guarded by `{{if $.HasPathParams}}`, for every HTTP
method, not just GET, since path values exist independently of whatever a method's body/query
decoding already populated. `DecodePath` reads each name via `r.PathValue(name)` and populates dst
through the same `populate` helper `DecodeQuery`/`DecodeForm` already use — so a path parameter is
matched against the Request struct by the exact same `json` tag convention as every other decode
source; a route with `{id}` and a GET handler decoding both query and path values into one struct
just needs one `ID string \`json:"id"\`` field to receive either. Because `model.go.tmpl`/
`model_methods.tmpl` never auto-generate fields (see their `// TODO: add fields.` stub, unchanged),
the Request struct's doc comment gets an extra line naming the route's path parameter(s) when any
exist, pointing at `request.DecodePath` — the same "informed TODO" precedent `store.go.tmpl`'s core-
DB comment already sets, rather than silently doing nothing until a developer discovers `PathValue`
on their own.

`openapi.go.tmpl`'s `Spec` documents `{name}` segments as `in: "path", required: true` parameters
for every operation on that path (`pathParamNames`, parsed straight from `op.Path` — independent of
`Operation.ReqType`, so it works for POST/PUT/PATCH/DELETE routes too, not just GET). For a GET
operation, whose `ReqType` fields are otherwise documented as `in: "query"` via `queryParams`,
`queryParams` now takes a skip-set of the path parameter names on that operation and omits them —
without it, a GET route's `{id}` field would be listed twice (once as a path parameter, once as a
query parameter) since one struct field is matched against both sources by the same json tag.