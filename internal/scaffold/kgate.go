// This file implements `nexler init kgate [-dir <app-dir>]` — adding a
// kgate (klivolks' message broker) client package to an *existing*
// generated app, plus the KGATE_* env vars it reads, and auto-wiring its
// webhook fallback route.
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
