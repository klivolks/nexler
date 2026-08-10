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

	return nil
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
