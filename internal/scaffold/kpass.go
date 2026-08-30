// This file implements `nexler init kpass [-dir <app-dir>]` — adding a
// kpass (klivolks' permission-check service) client package to an
// *existing* generated app, plus the KPASS_* env vars it reads.
//
// Unlike `nexler init db`, this needs no live network connection at all:
// it's pure local file scaffolding, same as `nexler create <route>`.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KpassConfig holds the parameters for `nexler init kpass`.
type KpassConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
}

// kpassData is what's available to kpass_templates/kpass.go.tmpl placeholders.
type kpassData struct {
	ModulePath string
	HasJWT     bool
	HasSession bool
}

// kpassEnvVars are appended to the target app's .env (blank, for the user
// to fill in) by NewKpass, in this fixed order.
var kpassEnvVars = []string{"KPASS_URL", "KPASS_CLIENT_ID", "KPASS_API_SECRET"}

// NewKpass scaffolds kpass/kpass.go inside cfg.AppDir and ensures its .env
// has KPASS_URL/KPASS_CLIENT_ID/KPASS_API_SECRET declared.
func NewKpass(cfg KpassConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return err
	}

	destFile := filepath.Join(appDir, "kpass", "kpass.go")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	hasJWT, hasSession := detectAuthFiles(appDir)
	data := kpassData{ModulePath: modulePath, HasJWT: hasJWT, HasSession: hasSession}

	raw, err := kpassTemplateFS.ReadFile(kpassTmpl)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", kpassTmpl, err)
	}
	content, err := processFile(kpassTmpl, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", kpassTmpl, err)
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(destFile), err)
	}
	if err := os.WriteFile(destFile, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", destFile, err)
	}

	if err := ensureEnvVars(appDir, kpassEnvVars, "kpass — see kpass/kpass.go (Check/Allowed/UserIDFromRequest)"); err != nil {
		return fmt.Errorf("generated %s, but could not update .env: %w", destFile, err)
	}

	if err := writeKpassService(appDir, modulePath); err != nil {
		return fmt.Errorf("generated %s, but could not generate services/kpass/kpass.go: %w", destFile, err)
	}

	if err := writeKpassPermissionHook(appDir, modulePath); err != nil {
		return fmt.Errorf("generated %s, but could not wire middleware.PermissionCheck: %w", destFile, err)
	}

	return nil
}

// writeKpassPermissionHook writes middleware/permission_kpass.go, wiring
// middleware.PermissionCheck to services/kpass.CheckAccess — only when
// this app already has the Task Registry (middleware/task.go); a silent
// no-op otherwise (an app that predates the Task Registry has no
// PermissionCheck var to set). Errors if the file already exists, same
// collision guard as kpass/kpass.go and services/kpass/kpass.go above.
func writeKpassPermissionHook(appDir, modulePath string) error {
	if _, err := os.Stat(filepath.Join(appDir, "middleware", "task.go")); err != nil {
		return nil
	}

	destFile := filepath.Join(appDir, "middleware", "permission_kpass.go")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	data := kpassData{ModulePath: modulePath}
	raw, err := kpassTemplateFS.ReadFile(kpassPermissionTmpl)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", kpassPermissionTmpl, err)
	}
	content, err := processFile(kpassPermissionTmpl, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", kpassPermissionTmpl, err)
	}
	return os.WriteFile(destFile, content, 0o644)
}

// ensureKpassPermissionHook brings an app that ran `nexler init kpass`
// before the Task Registry existed, or ran `nexler update` (adding
// middleware/task.go) before `nexler init kpass`, up to date: writes
// middleware/permission_kpass.go if this app has both kpass/kpass.go and
// middleware/task.go but not yet the hook file. A silent no-op for an app
// missing either prerequisite.
func ensureKpassPermissionHook(appDir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(appDir, "kpass", "kpass.go")); err != nil {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(appDir, "middleware", "task.go")); err != nil {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(appDir, "middleware", "permission_kpass.go")); err == nil {
		return false, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	if err := writeKpassPermissionHook(appDir, modulePath); err != nil {
		return false, err
	}
	return true, nil
}

// writeKpassService renders services/kpass/kpass.go — a one-time,
// hand-editable wrapper around the kpass client (kpass/kpass.go) for the
// app's own authorization calls, so callers reach for CheckAccess instead
// of calling kpassclient.Check directly. Unlike kpass/kpass.go itself,
// this file is never touched again by `nexler update` once it exists.
// Errors if it already exists, same collision guard as kpass/kpass.go
// above.
func writeKpassService(appDir, modulePath string) error {
	destFile := filepath.Join(appDir, "services", "kpass", "kpass.go")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	data := kpassData{ModulePath: modulePath}

	raw, err := kpassTemplateFS.ReadFile(kpassServiceTmpl)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", kpassServiceTmpl, err)
	}
	content, err := processFile(kpassServiceTmpl, raw, data)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", kpassServiceTmpl, err)
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(destFile), err)
	}
	return os.WriteFile(destFile, content, 0o644)
}

// ensureKpassService brings an app that ran `nexler init kpass` before
// services/kpass existed up to date — writes services/kpass/kpass.go only
// if kpass/kpass.go exists (this app is eligible) and services/kpass/
// kpass.go doesn't already exist. Purely additive: never overwrites an
// existing services/kpass/kpass.go (which may already have been hand-
// edited), same "write only if missing" precedent as ensureStoreCommon.
// A silent no-op for an app that never ran `init kpass` at all.
func ensureKpassService(appDir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(appDir, "kpass", "kpass.go")); err != nil {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(appDir, "services", "kpass", "kpass.go")); err == nil {
		return false, nil
	}

	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	if err := writeKpassService(appDir, modulePath); err != nil {
		return false, err
	}
	return true, nil
}

// detectAuthFiles reports whether appDir's auth/jwt.go and/or
// auth/session.go exist — the after-the-fact signal for which -auth
// mechanism(s) the app was scaffolded with, since `init kpass` runs
// independently of `create app` and AuthKind isn't stored anywhere.
func detectAuthFiles(appDir string) (hasJWT, hasSession bool) {
	_, jwtErr := os.Stat(filepath.Join(appDir, "auth", "jwt.go"))
	_, sessErr := os.Stat(filepath.Join(appDir, "auth", "session.go"))
	return jwtErr == nil, sessErr == nil
}

// ensureEnvVars appends any of names not already present in appDir/.env
// (as a "NAME=" line prefix, checked the same substring-scan way
// readCoreDBType/readCoreConnection already do) as blank "NAME=" lines,
// preceded by a "# comment" line (comment should not include the leading
// "# "). Safe to call repeatedly — already-present names are left
// untouched.
func ensureEnvVars(appDir string, names []string, comment string) error {
	envPath := filepath.Join(appDir, ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", envPath, err)
	}
	existing := strings.ReplaceAll(string(raw), "\r\n", "\n")

	var toAdd []string
	for _, name := range names {
		if !envHasKey(existing, name) {
			toAdd = append(toAdd, name)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString(existing)
	if len(existing) > 0 && !strings.HasSuffix(existing, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n# ")
	b.WriteString(comment)
	b.WriteString("\n")

	for _, name := range toAdd {
		b.WriteString(name)
		b.WriteString("=\n")
	}

	return os.WriteFile(envPath, []byte(b.String()), 0o644)
}

// envHasKey reports whether env (a .env file's contents) already declares
// key, i.e. contains a line starting with "key=".
func envHasKey(env, key string) bool {
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
			return true
		}
	}
	return false
}
