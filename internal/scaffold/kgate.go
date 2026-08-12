// This file implements `nexler init kgate [-dir <app-dir>]` — adding a
// kgate (klivolks' message broker) client package to an *existing*
// generated app, plus the KGATE_* env vars it reads, and auto-wiring its
// webhook fallback route. Register(mux) — auto-wired by NewKgate below —
// also resumes every channel already recorded in the registry, in the
// background, the moment it runs (see kgate_templates/kgate.go.tmpl's
// Register), so a freshly generated app needs no further manual wiring
// for subscriptions to survive a restart. ensureKgateResumeAll (below)
// is the `nexler update` retrofit that brings an app scaffolded before
// Register did this up to date.
//
// Unlike `nexler init db`, this needs no live network connection at all:
// it's pure local file scaffolding, same as `nexler create <route>`.
// Unlike `nexler init kpass`, it does require the target app to already
// have a core database connection (-db at `nexler create app` time) —
// Subscribe/Unsubscribe/ResumeAll are backed by the core kgate-channel
// registry (core/kgate_channels.go, generated for every -db app — see
// scaffold.go's NewApp), which needs somewhere to live.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KgateConfig holds the parameters for `nexler init kgate`.
type KgateConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
}

// kgateData is what's available to kgate_templates/kgate.go.tmpl placeholders.
type kgateData struct {
	ModulePath string
}

// kgateEnvVars are appended to the target app's .env (blank, for the user
// to fill in) by NewKgate, in this fixed order.
var kgateEnvVars = []string{"KGATE_CLIENT_ID", "KGATE_WS_SERVER", "KGATE_HTTP_SERVER", "KGATE_ORIGIN", "KGATE_WEBHOOK_SECRET"}

// NewKgate scaffolds kgate/kgate.go inside cfg.AppDir, ensures its .env
// has the KGATE_* vars declared, and auto-wires kgate.Register (its
// /webhooks/kgate fallback route) into routes/public/public.go.
func NewKgate(cfg KgateConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return err
	}

	if _, ok := readCoreDBType(appDir); !ok {
		return fmt.Errorf("%s has no core database connection (no _DB_CORE_TYPE in .env) — kgate's channel registry needs one: re-scaffold with -db, or run \"nexler create app\" with -db in the first place", filepath.Join(appDir, ".env"))
	}

	destFile := filepath.Join(appDir, "kgate", "kgate.go")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	data := kgateData{ModulePath: modulePath}

	raw, err := kgateTemplateFS.ReadFile(kgateTmpl)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", kgateTmpl, err)
	}
	content, err := processFile(kgateTmpl, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", kgateTmpl, err)
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(destFile), err)
	}
	if err := os.WriteFile(destFile, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", destFile, err)
	}

	if err := ensureEnvVars(appDir, kgateEnvVars, "kgate — see kgate/kgate.go (Subscribe/Unsubscribe/ResumeAll/Publish)"); err != nil {
		return fmt.Errorf("generated %s, but could not update .env: %w", destFile, err)
	}

	if err := wireAggregator(appDir, "public", modulePath+"/kgate", "kgate"); err != nil {
		return fmt.Errorf("generated %s, but could not wire routes/public/public.go automatically: %w\nAdd manually in that file:\n  import kgate %q\n  kgate.Register(mux)",
			destFile, err, modulePath+"/kgate")
	}

	return nil
}

// kgateRegisterOriginal is kgate.go.tmpl's Register function exactly as
// it read before ResumeAll was wired into it automatically — the literal
// anchor ensureKgateResumeAll matches against for its surgical patch.
// Deliberately scoped to the function body only, not its doc comment
// above: a hand-tweaked comment shouldn't cause the patch to fail (it's
// left stale on a retrofitted file, cosmetic-only).
const kgateRegisterOriginal = `func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/kgate", HandleWebhook)
}`

// kgateRegisterPatched is what ensureKgateResumeAll replaces
// kgateRegisterOriginal with — same as the current kgate.go.tmpl.
const kgateRegisterPatched = `func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/kgate", HandleWebhook)
	go func() {
		if err := ResumeAll(context.Background()); err != nil {
			log.Printf("kgate: resuming channels: %v", err)
		}
	}()
}`

// kgateRegisterPatchedV2 is what ensureKgateOpenAPIAndTestSubscribe
// replaces kgateRegisterPatched with — same as the current
// kgate.go.tmpl. Adds the openapi.Register call documenting POST
// /webhooks/kgate (previously invisible in /openapi.json/Swagger UI,
// unlike every route `nexler create <route>` generates) and a startup
// Subscribe(ctx, "test") alongside ResumeAll — a built-in smoke test that
// the whole pipeline (core DB registry + WebSocket connectivity) works
// end to end on a fresh app.
const kgateRegisterPatchedV2 = `func Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/kgate", HandleWebhook)
	// Protected is left false (the default): this route authenticates via
	// the HMAC X-Signature check in HandleWebhook above, not
	// middleware.RequireAuth, so Protected: true here would be misleading.
	openapi.Register(openapi.Operation{
		Method:         "POST",
		Path:           "/webhooks/kgate",
		OperationID:    "kgateWebhook",
		Summary:        "POST /webhooks/kgate",
		Tags:           []string{"kgate"},
		ReqType:        webhookEvent{},
		ReqContentType: "application/json",
	})
	go func() {
		if err := ResumeAll(context.Background()); err != nil {
			log.Printf("kgate: resuming channels: %v", err)
		}
		// Subscribes to a channel named "test" as a built-in smoke test —
		// verifies the whole pipeline (core DB registry + WebSocket
		// connectivity) works end to end on a fresh app. Subscribe is
		// idempotent for an already-subscribed channel, so this is safe to
		// leave in; remove it once you've confirmed kgate is wired
		// correctly, or if a permanent "test" channel isn't wanted.
		if err := Subscribe(context.Background(), "test"); err != nil {
			log.Printf("kgate: subscribing to test channel: %v", err)
		}
	}()
}`

// ensureKgateOpenAPIAndTestSubscribe brings an app scaffolded before
// Register documented its webhook route with openapi.Register and
// startup-subscribed to a "test" channel up to date. Anchors on
// kgateRegisterPatched (the body ensureKgateResumeAll's own patch
// produces) rather than kgateRegisterOriginal — this must run after
// ensureKgateResumeAll in updateChecks so that anchor is guaranteed to
// already be in place by the time this one runs. A missing kgate/kgate.go
// is a silent no-op, same precedent as ensureKgateResumeAll. Same
// "sanity-check the anchor, error out instead of guessing" precedent too:
// a hand-rewritten Register fails loud, naming the exact snippet to add
// by hand, rather than being silently overwritten.
func ensureKgateOpenAPIAndTestSubscribe(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, `Subscribe(context.Background(), "test")`) {
		return false, nil
	}
	if !strings.Contains(content, kgateRegisterPatched) {
		return false, fmt.Errorf("%s: Register doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateRegisterPatchedV2)
	}
	content = strings.Replace(content, kgateRegisterPatched, kgateRegisterPatchedV2, 1)

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	openapiImport := modulePath + "/openapi"
	if !strings.Contains(content, `"`+openapiImport+`"`) {
		content, err = insertImport(content, "", openapiImport)
		if err != nil {
			return false, fmt.Errorf("%s: adding %q import: %w", path, openapiImport, err)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ensureKgateResumeAll brings an app scaffolded before Register started
// auto-resuming recorded channels up to date. A missing kgate/kgate.go
// (the app never ran `init kgate`) is a silent no-op, same precedent as
// every other `ensure*` retrofit skipping a feature the app never had.
//
// Deliberately a narrow, surgical patch — never a full-file regeneration
// like ensureAuthSubjectContext/ensureJWTClaims use for their own
// pure-generated-infra targets: handleEvent in this file is the
// documented, expected hand-edit point (real event-processing business
// logic lives there), so nothing here may risk touching it. A sanity
// check (kgateRegisterOriginal must match verbatim) guards against
// silently overwriting a Register that's been hand-rewritten beyond
// recognition — that case errors out instead, naming the file and the
// exact snippet to add by hand.
func ensureKgateResumeAll(appDir string) (bool, error) {
	path := filepath.Join(appDir, "kgate", "kgate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Contains(content, "ResumeAll(context.Background())") {
		return false, nil
	}
	if !strings.Contains(content, kgateRegisterOriginal) {
		return false, fmt.Errorf("%s: Register doesn't match the known original (has it been hand-rewritten?) — add the following manually:\n%s", path, kgateRegisterPatched)
	}
	content = strings.Replace(content, kgateRegisterOriginal, kgateRegisterPatched, 1)

	if !strings.Contains(content, `"log"`) {
		content, err = insertImport(content, "", "log")
		if err != nil {
			return false, fmt.Errorf("%s: adding \"log\" import: %w", path, err)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
