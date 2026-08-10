// This file implements `nexler init ci [-dir <app-dir>] [-registry
// dockerhub|github]` — adding a GitHub Actions release workflow
// (.github/workflows/release.yml) to an *existing* generated app, that
// builds and pushes a Docker image whenever a GitHub Release is
// published. Like `nexler init kpass`/`nexler init kgate`, this needs no
// live network connection: it's pure local file scaffolding.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CIConfig holds the parameters for `nexler init ci`.
type CIConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
	// Registry picks where release images are published: "github" (GitHub
	// Container Registry, ghcr.io, authenticated via the automatic
	// GITHUB_TOKEN) or "dockerhub" (authenticated via DOCKERHUB_USERNAME/
	// DOCKERHUB_TOKEN secrets you configure in the repo).
	Registry string
}

// NewCI writes .github/workflows/release.yml into cfg.AppDir, picking the
// embedded template matching cfg.Registry. Refuses if the file already
// exists. Both embedded templates are fully static — no per-app
// substitution needed, see ci_templates' package doc comment in
// templates.go — so this is a plain byte copy (CRLF-normalized, same as
// every other embedded template) rather than a text/template render.
func NewCI(cfg CIConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	var tmplPath string
	switch cfg.Registry {
	case "github":
		tmplPath = ciTmplGHCR
	case "dockerhub":
		tmplPath = ciTmplDockerHub
	default:
		return fmt.Errorf("unsupported -registry %q (supported: github, dockerhub)", cfg.Registry)
	}

	destDir := filepath.Join(appDir, ".github", "workflows")
	destFile := filepath.Join(destDir, "release.yml")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	raw, err := ciTemplateFS.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", destDir, err)
	}
	if err := os.WriteFile(destFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", destFile, err)
	}

	return nil
}

// DockerfileExists reports whether appDir has a Dockerfile at its root —
// used by the CLI layer to print a soft warning (not a hard error, unlike
// kgate's core-DB precondition) when `nexler init ci` runs before `nexler
// init docker`, since the generated workflow assumes ./Dockerfile builds
// successfully.
func DockerfileExists(appDir string) bool {
	if appDir == "" {
		appDir = "."
	}
	_, err := os.Stat(filepath.Join(appDir, "Dockerfile"))
	return err == nil
}
