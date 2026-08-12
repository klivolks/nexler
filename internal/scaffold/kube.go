// This file implements `nexler init kube [-dir <app-dir>] [-registry
// dockerhub|github] [-image <ref>] [-namespace <ns>] [-replicas <n>]` —
// adding a Kubernetes manifest (k8s/deployment.yaml: Secret + Deployment +
// Service) to an *existing* generated app. Like `nexler init docker`, this
// needs no live network connection: it's pure local file scaffolding.
//
// Unlike `nexler init ci`'s GitHub Actions workflows (which resolve an
// image reference at *workflow run time* via GitHub's own context
// variables), this file is applied directly by kubectl — it needs a
// concrete, static image: string baked in at scaffold time. Resolving that
// is split out into the exported ResolveKubeImage so this package never
// needs to prompt interactively (prompting lives in cmd/nexler, same as
// every other init command): the github case is derived purely from
// go.mod's module path (no git/network access, preserving this repo's
// "scaffolding never shells out to git" constraint — see CLAUDE.md); the
// dockerhub case has no local source of truth for a username at all, so
// the caller (cmd/nexler/initkube.go) must prompt for one and pass it
// through.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// KubeConfig holds the parameters for `nexler init kube`.
type KubeConfig struct {
	// AppDir is the path to the generated app's root (the directory
	// containing its go.mod). Defaults to "." by the caller if unset.
	AppDir string
	// Image is the fully-resolved container image reference to deploy,
	// e.g. "ghcr.io/owner/app:latest" — see ResolveKubeImage. Required.
	Image string
	// Namespace, when set, is written as metadata.namespace on every
	// generated resource. Left blank (the default) it's omitted entirely
	// from all three resources, so kubectl's -n flag / current context
	// decides — never hardcoded unless explicitly asked for.
	Namespace string
	// Replicas is the Deployment's initial replica count. <= 0 defaults
	// to 1.
	Replicas int
}

// githubModuleRe matches a go.mod module path rooted at github.com, e.g.
// "github.com/owner/repo" or "github.com/owner/repo/v2" (a major-version
// suffix module path) — only the first two path segments after
// "github.com/" are the actual owner/repo.
var githubModuleRe = regexp.MustCompile(`^github\.com/([^/]+)/([^/]+)`)

// ResolveKubeImage computes the image: reference for `nexler init kube`.
// image, if non-empty, is returned verbatim (the CLI's own -image
// override, which always wins and skips every other case below).
// Otherwise, registry must be "github" or "dockerhub":
//
//   - "github" derives ghcr.io/<owner>/<repo>:latest from appDir's go.mod
//     module path — erroring if that path isn't github.com/<owner>/<repo>
//     shaped, since there's nothing else local to guess an owner from.
//   - "dockerhub" derives <dockerHubUser>/<repo>:latest, where <repo> is
//     the same appServiceName used elsewhere for this app's identity.
//     dockerHubUser has no local source of truth (unlike a GitHub owner,
//     a Docker Hub username can't be read from go.mod or any other file
//     already on disk) — the caller must have already prompted for it.
func ResolveKubeImage(appDir, image, registry, dockerHubUser string) (string, error) {
	if image != "" {
		return image, nil
	}
	switch registry {
	case "github":
		modulePath, err := readModulePath(appDir)
		if err != nil {
			return "", err
		}
		m := githubModuleRe.FindStringSubmatch(modulePath)
		if m == nil {
			return "", fmt.Errorf("go.mod's module path %q isn't github.com/<owner>/<repo>-shaped, so an image can't be derived automatically — pass -image explicitly instead, e.g. -image ghcr.io/you/app:latest", modulePath)
		}
		return fmt.Sprintf("ghcr.io/%s/%s:latest", m[1], m[2]), nil
	case "dockerhub":
		if dockerHubUser == "" {
			return "", fmt.Errorf("-registry dockerhub needs a Docker Hub username to derive an image reference — pass -image explicitly instead")
		}
		return fmt.Sprintf("%s/%s:latest", dockerHubUser, appServiceName(appDir)), nil
	default:
		return "", fmt.Errorf("unsupported -registry %q (supported: github, dockerhub) — or pass -image directly", registry)
	}
}

// NewKube writes k8s/deployment.yaml into cfg.AppDir — a single file
// holding three "---"-separated YAML documents: a Secret populated from
// every real value in .env, a Deployment referencing that Secret via
// envFrom, and a ClusterIP Service — then ensures the file is gitignored,
// since (unlike docker-compose.yml, which only references .env by path at
// container runtime) the Secret document embeds .env's actual values:
// Kubernetes has no "read a local .env file at deploy time" mechanism of
// its own, so baking them in here is the only way to get them into the
// cluster. Refuses if the file already exists, same precedent as
// NewDocker/NewCI.
func NewKube(cfg KubeConfig) error {
	appDir := cfg.AppDir
	if appDir == "" {
		appDir = "."
	}
	if cfg.Image == "" {
		return fmt.Errorf("KubeConfig.Image is required")
	}

	destDir := filepath.Join(appDir, "k8s")
	destFile := filepath.Join(destDir, "deployment.yaml")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("%s already exists — edit it directly, or remove it first to regenerate", destFile)
	}

	name := appServiceName(appDir)
	port := readEnvPort(appDir)
	replicas := cfg.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	envVars, err := readEnvVars(appDir)
	if err != nil {
		return err
	}

	manifest := renderKubeManifest(name, cfg.Image, cfg.Namespace, port, replicas, envVars)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", destDir, err)
	}
	if err := os.WriteFile(destFile, []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", destFile, err)
	}

	if err := ensureGitignoreLine(appDir, "k8s/deployment.yaml"); err != nil {
		return fmt.Errorf("generated %s, but could not update .gitignore: %w", destFile, err)
	}

	return nil
}

// envVar is one KEY=VALUE line from .env, in file order.
type envVar struct {
	Key   string
	Value string
}

// readEnvVars parses every "KEY=VALUE" line out of appDir/.env, in file
// order (a plain map would lose that order, making regenerated output
// non-deterministic across reruns) — skipping blank lines and
// "#"-prefixed comments, same shape as config.go.tmpl's own generated
// loadDotEnv. Returns an empty slice, not an error, when .env doesn't
// exist — a kube manifest with an empty Secret is still valid, just not
// very useful; the missing file itself isn't this function's problem to
// report.
func readEnvVars(appDir string) ([]envVar, error) {
	raw, err := os.ReadFile(filepath.Join(appDir, ".env"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", filepath.Join(appDir, ".env"), err)
	}
	var vars []envVar
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		vars = append(vars, envVar{Key: key, Value: strings.TrimSpace(value)})
	}
	return vars, nil
}

// yamlQuote double-quotes s for use as a YAML scalar value, escaping the
// two characters that matter inside a double-quoted YAML string
// (backslash and the quote itself) — needed because .env values commonly
// contain ":"/"//" (DSNs) that unquoted YAML would misparse as its own
// syntax.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// namespaceLine renders an indented "namespace: <ns>" metadata line, or
// "" when ns is blank — spliced directly under a "name:" line by
// renderKubeManifest's callers.
func namespaceLine(indent, ns string) string {
	if ns == "" {
		return ""
	}
	return indent + "namespace: " + ns + "\n"
}

// renderKubeManifest builds the full k8s/deployment.yaml content: a
// Secret (from envVars), a Deployment, and a ClusterIP Service, as three
// "---"-separated YAML documents. Plain string assembly (like
// dockerfileTemplate/composeTemplate above) rather than text/template —
// the only variable-length part is the Secret's stringData block, which a
// simple loop handles more clearly than a template range would here.
func renderKubeManifest(name, image, namespace, port string, replicas int, envVars []envVar) string {
	var b strings.Builder

	b.WriteString("# Generated by `nexler init kube`. The Secret below contains real\n")
	b.WriteString("# values from .env — do not commit this file or paste it anywhere\n")
	b.WriteString("# secrets shouldn't go (see .gitignore). Apply with:\n")
	b.WriteString("#   kubectl apply -f k8s/deployment.yaml\n")

	// --- Secret ---
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "-env\n")
	b.WriteString(namespaceLine("  ", namespace))
	b.WriteString("type: Opaque\n")
	if len(envVars) == 0 {
		b.WriteString("stringData: {}\n")
	} else {
		b.WriteString("stringData:\n")
		for _, v := range envVars {
			b.WriteString("  " + v.Key + ": " + yamlQuote(v.Value) + "\n")
		}
	}

	// --- Deployment ---
	b.WriteString("---\n")
	b.WriteString("apiVersion: apps/v1\n")
	b.WriteString("kind: Deployment\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString(namespaceLine("  ", namespace))
	b.WriteString("  labels:\n")
	b.WriteString("    app: " + name + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  replicas: " + strconv.Itoa(replicas) + "\n")
	b.WriteString("  selector:\n")
	b.WriteString("    matchLabels:\n")
	b.WriteString("      app: " + name + "\n")
	b.WriteString("  template:\n")
	b.WriteString("    metadata:\n")
	b.WriteString("      labels:\n")
	b.WriteString("        app: " + name + "\n")
	b.WriteString("    spec:\n")
	b.WriteString("      containers:\n")
	b.WriteString("        - name: " + name + "\n")
	b.WriteString("          image: " + image + "\n")
	b.WriteString("          ports:\n")
	b.WriteString("            - containerPort: " + port + "\n")
	b.WriteString("          envFrom:\n")
	b.WriteString("            - secretRef:\n")
	b.WriteString("                name: " + name + "-env\n")
	b.WriteString("          readinessProbe:\n")
	b.WriteString("            tcpSocket:\n")
	b.WriteString("              port: " + port + "\n")
	b.WriteString("            initialDelaySeconds: 5\n")
	b.WriteString("            periodSeconds: 10\n")
	b.WriteString("          livenessProbe:\n")
	b.WriteString("            tcpSocket:\n")
	b.WriteString("              port: " + port + "\n")
	b.WriteString("            initialDelaySeconds: 10\n")
	b.WriteString("            periodSeconds: 20\n")
	b.WriteString("          # Starting point only — tune to your workload.\n")
	b.WriteString("          resources:\n")
	b.WriteString("            requests:\n")
	b.WriteString("              cpu: 100m\n")
	b.WriteString("              memory: 64Mi\n")
	b.WriteString("            limits:\n")
	b.WriteString("              cpu: 500m\n")
	b.WriteString("              memory: 256Mi\n")

	// --- Service ---
	b.WriteString("---\n")
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Service\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString(namespaceLine("  ", namespace))
	b.WriteString("spec:\n")
	b.WriteString("  selector:\n")
	b.WriteString("    app: " + name + "\n")
	b.WriteString("  ports:\n")
	b.WriteString("    - port: " + port + "\n")
	b.WriteString("      targetPort: " + port + "\n")
	b.WriteString("  type: ClusterIP\n")

	return b.String()
}
