// This file implements `nexler init docker [-dir <app-dir>]` — adding a
// multi-stage Dockerfile (build + serve) and a docker-compose.yml to an
// *existing* generated app, for containerized deployment. Like `nexler
// init kpass`/`nexler init kgate`, this needs no live network connection:
// it's pure local file scaffolding, and has no preconditions on -db/-auth
// — unlike kgate, nothing here depends on the app's other choices.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DockerConfig holds the parameters for `nexler init docker`.
type DockerConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
}

// dockerfileTemplate is rendered with two substitutions (goVersion, port)
// via strings.NewReplacer — no text/template pass needed for two plain
// values, unlike the embedded .tmpl trees.
//
// "COPY go.mod go.sum* ./" (the trailing "*") is deliberate: an app with
// zero third-party dependencies (e.g. -auth none and no -db) has no
// go.sum at all, and a bare "COPY go.mod go.sum ./" would fail the build
// for exactly that common case — the glob tolerates a missing file.
//
// templates/ is copied from the build context directly (not the build
// stage) in the final image: it always exists in every generated app
// (templates/static + templates/templates.go are unconditional), even
// when templates/html/ specifically doesn't — response.HTML reads
// templates/html/... straight off disk at request time (it's not
// embedded like templates/static is), so it needs to survive into the
// image whenever it's present.
//
// .env is deliberately never copied into the image — real environment
// variables (via docker-compose's env_file:) are what a container should
// run on; baking .env into an image layer would ship secrets inside it.
// config.go's loadDotEnv only fills gaps real env vars don't already
// cover, so running without a baked-in .env is a pure win, not a
// behavior change.
const dockerfileTemplate = `# syntax=docker/dockerfile:1
FROM golang:{{GOVERSION}}-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/app .

FROM alpine:latest
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/app ./app
COPY templates ./templates
EXPOSE {{PORT}}
ENTRYPOINT ["./app"]
`

// composeTemplate is rendered with two substitutions (service, port).
const composeTemplate = `services:
  {{SERVICE}}:
    build: .
    env_file:
      - .env
    ports:
      - "{{PORT}}:{{PORT}}"
    restart: unless-stopped
`

// NewDocker writes a multi-stage Dockerfile and docker-compose.yml into
// cfg.AppDir, and ensures docker-compose.yml (but not Dockerfile) is
// gitignored. Refuses if either target file already exists, checking both
// before writing either so a rerun never partially clobbers one and not
// the other.
func NewDocker(cfg DockerConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}

	dockerfilePath := filepath.Join(appDir, "Dockerfile")
	composePath := filepath.Join(appDir, "docker-compose.yml")
	for _, p := range []string{dockerfilePath, composePath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", p)
		}
	}

	goVersion, err := readGoVersion(appDir)
	if err != nil {
		return err
	}
	port := readEnvPort(appDir)
	service := ""
	if abs, err := filepath.Abs(appDir); err == nil {
		service = sanitizeIdent(filepath.Base(abs))
	}
	if service == "" {
		service = "app"
	}

	dockerfile := strings.NewReplacer("{{GOVERSION}}", goVersion, "{{PORT}}", port).Replace(dockerfileTemplate)
	compose := strings.NewReplacer("{{SERVICE}}", service, "{{PORT}}", port).Replace(composeTemplate)

	if err := os.WriteFile(dockerfilePath, []byte(dockerfile), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dockerfilePath, err)
	}
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", composePath, err)
	}

	if err := ensureGitignoreLine(appDir, "docker-compose.yml"); err != nil {
		return fmt.Errorf("generated %s and %s, but could not update .gitignore: %w", dockerfilePath, composePath, err)
	}

	return nil
}

// goVersionRe matches go.mod's "go X.Y" (or "go X.Y.Z") directive line.
var goVersionRe = regexp.MustCompile(`^go\s+(\d+\.\d+(?:\.\d+)?)`)

// readGoVersion extracts the "go X.Y" version directive from appDir/go.mod
// — the Dockerfile's build stage uses this as its golang:<version>-alpine
// base image tag, so it always matches the app's actual go.mod (even if
// hand-bumped since scaffolding) rather than nexler's own template default.
func readGoVersion(appDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(appDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("reading go.mod (run this from inside a nexler app directory, or pass -dir): %w", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if m := goVersionRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no \"go X.Y\" version directive found in go.mod")
}

// readEnvPort looks for a "..._PORT=<value>" line in appDir's .env (the
// env-var prefix varies per app, so this matches on the fixed suffix
// rather than needing to know it, same trick readCoreDBType uses for
// "_DB_CORE_TYPE="), defaulting to "8080" — the scaffolded default —
// when missing, blank, or .env doesn't exist.
func readEnvPort(appDir string) string {
	raw, err := os.ReadFile(filepath.Join(appDir, ".env"))
	if err != nil {
		return "8080"
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "_PORT=")
		if idx == -1 {
			continue
		}
		value := strings.TrimSpace(line[idx+len("_PORT="):])
		if value == "" {
			return "8080"
		}
		return value
	}
	return "8080"
}

// ensureGitignoreLine appends line to appDir/.gitignore (creating the file
// if it doesn't exist yet — no nexler-generated app has one today) unless
// it's already present, checked as an exact trimmed-line match. Safe to
// call repeatedly.
func ensureGitignoreLine(appDir, line string) error {
	path := filepath.Join(appDir, ".gitignore")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	existing := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, l := range strings.Split(existing, "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}

	var b strings.Builder
	b.WriteString(existing)
	if len(existing) > 0 && !strings.HasSuffix(existing, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(line)
	b.WriteString("\n")

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
