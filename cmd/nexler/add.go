// This file implements `nexler add <package>[@version] [-dir <app-dir>]
// [-as <name>]` — vendoring a static asset package from npm into an
// existing generated app's templates/static/vendors/<name>/, without ever
// giving the app itself a package.json or node_modules.
//
// Like `nexler init db`, this is self-contained in cmd/nexler and
// deliberately does NOT go through internal/scaffold: it's real, impure
// work (a live npm install, real network access, a real os/exec call), not
// template rendering, so internal/scaffold's package doc comment ("no
// network access, no shelling out to git or any other external process")
// stays literally true. This is the one other place — alongside
// `nexler init db`'s live database connection — where nexler itself
// (not just the generated app) touches the network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// npmInstallTimeout bounds how long `npm install` is allowed to run before
// nexler gives up and reports a timeout — a hang-safety-net, not a UX
// feature; vendoring a static asset package is expected to be quick.
const npmInstallTimeout = 10 * time.Minute

func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	as := fs.String("as", "", "destination folder name under templates/static/vendors/ (defaults to the package name, scope stripped and \"/\" replaced with \"-\")")
	fs.Parse(reorderFlagsFirst(fs, args))

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	spec := ""
	if fs.NArg() >= 1 {
		spec = fs.Arg(0)
	} else {
		spec = promptRequired("Package spec (e.g. bootstrap, bootstrap@5.3.3, @popperjs/core)")
	}

	dirVal := *dir
	if !set["dir"] {
		dirVal = prompt("App directory", dirVal)
	}
	if _, err := os.Stat(filepath.Join(dirVal, "go.mod")); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %s/go.mod not found (run this from inside a nexler app directory, or pass -dir)\n", dirVal)
		os.Exit(1)
	}

	bareName, err := parsePackageSpec(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	asVal := *as
	if !set["as"] {
		asVal = prompt("Vendor folder name", defaultVendorName(bareName))
	}
	if asVal == "" {
		asVal = defaultVendorName(bareName)
	}
	if err := validateVendorName(asVal); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	npmPath, err := npmLookPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	scratchDir, err := os.MkdirTemp("", "nexler-add-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: creating scratch directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(scratchDir)

	ctx, cancel := context.WithTimeout(context.Background(), npmInstallTimeout)
	defer cancel()

	fmt.Printf("Running npm install %s ...\n", spec)
	if err := runNpmInstall(ctx, npmPath, spec, scratchDir); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: npm install %s failed: %v\n", spec, err)
		os.Exit(1)
	}

	pkgDir, err := resolvePackageDir(scratchDir, bareName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	srcDir, filter := selectSource(pkgDir)

	destDir := filepath.Join(dirVal, "templates", "static", "vendors", asVal)
	if err := os.RemoveAll(destDir); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: clearing %s: %v\n", destDir, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: creating %s: %v\n", destDir, err)
		os.Exit(1)
	}

	count, copyErr := copyTree(srcDir, destDir, filter)
	if copyErr == nil && count == 0 {
		copyErr = fmt.Errorf("no files found to copy from %s", srcDir)
	}
	if copyErr != nil {
		os.RemoveAll(destDir)
		fmt.Fprintf(os.Stderr, "nexler: %v\n", copyErr)
		os.Exit(1)
	}

	fmt.Printf("Added %s (npm %s) to templates/static/vendors/%s/ (%d file(s)).\n", bareName, spec, asVal, count)
	fmt.Println("Next steps:")
	fmt.Printf("  Reference it from your own HTML, e.g. <link rel=\"stylesheet\" href=\"/static/vendors/%s/...\">\n", asVal)
	fmt.Println("  Rebuild the app (go build / go run main.go) — templates/static is embedded at the app's own build time, so new files aren't served until then.")
}

// packageSpecRe matches the four npm package spec forms nexler add
// supports: "name", "name@version", "@scope/name", "@scope/name@version".
// Anything else (git/tarball/URL specs, npm: aliases, a bare "user/repo")
// is rejected outright — those don't have a bare name npm's own
// node_modules layout would agree with (npm derives the installed
// directory name from the resolved package's own package.json, which can
// differ arbitrarily from a git/URL/alias spec), so best-effort parsing
// would just fail confusingly later instead of failing clearly now.
var packageSpecRe = regexp.MustCompile(`^(@[^/@]+/[^/@]+|[^/@]+)(@.+)?$`)

// parsePackageSpec splits spec into its bare package name — scope
// preserved (e.g. "@popperjs/core"), version stripped.
func parsePackageSpec(spec string) (bareName string, err error) {
	if !packageSpecRe.MatchString(spec) {
		return "", fmt.Errorf("unsupported package spec %q — expected one of: name, name@version, @scope/name, @scope/name@version", spec)
	}
	m := packageSpecRe.FindStringSubmatch(spec)
	return m[1], nil
}

// defaultVendorName derives the default -as value from a bare package
// name: strip a leading "@", replace "/" with "-", e.g.
// "@popperjs/core" -> "popperjs-core".
func defaultVendorName(bareName string) string {
	return strings.ReplaceAll(strings.TrimPrefix(bareName, "@"), "/", "-")
}

// vendorNameRe allow-lists a single safe path segment for -as (or its
// computed default) — same allow-list-regex style as route.go's
// pathParamNameRe. Must start with a letter/digit; the rest may add '.',
// '_', '-'. This structurally rejects "/", "\", and a leading "." (so
// ".." can't match either, since its first character is never in the
// required leading class) — no separate explicit checks needed.
var vendorNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateVendorName(name string) error {
	if !vendorNameRe.MatchString(name) {
		return fmt.Errorf("invalid -as %q: must start with a letter or digit and contain only letters, digits, '.', '_', '-'", name)
	}
	return nil
}

// npmLookPath resolves the npm executable on PATH, with a clear,
// actionable error if it isn't found.
func npmLookPath() (string, error) {
	path, err := exec.LookPath("npm")
	if err != nil {
		return "", errors.New("npm not found on PATH — install Node.js (https://nodejs.org) first")
	}
	return path, nil
}

// runNpmInstall shells out to `npm install <spec> --prefix <scratchDir>
// --no-save --no-audit --no-fund`, streaming npm's own stdout/stderr live
// since this is the one nexler operation that talks to the network and
// the user should see registry/progress/error output as it happens, not
// just on failure.
func runNpmInstall(ctx context.Context, npmPath, spec, scratchDir string) error {
	cmd := exec.CommandContext(ctx, npmPath, "install", spec, "--prefix", scratchDir, "--no-save", "--no-audit", "--no-fund")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// resolvePackageDir locates scratchDir/node_modules/<bareName> — bareName's
// "@scope/name" form maps directly onto npm's own node_modules/@scope/name
// nesting via filepath.FromSlash, so no special-casing is needed beyond
// the join itself.
func resolvePackageDir(scratchDir, bareName string) (string, error) {
	pkgDir := filepath.Join(scratchDir, "node_modules", filepath.FromSlash(bareName))
	info, err := os.Stat(pkgDir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("npm install succeeded but %s was not found", pkgDir)
	}
	return pkgDir, nil
}

// copyFilter reports whether rel (slash-form path relative to the copy
// root) should be included in copyTree's output. nil means "copy
// everything".
type copyFilter func(rel string, d fs.DirEntry) bool

// selectSource picks what to copy from an installed package: pkgDir/dist's
// contents (flattened, unfiltered — dist output ships as-is) if dist/
// exists, else pkgDir's own contents filtered through isJunkEntry.
func selectSource(pkgDir string) (srcDir string, filter copyFilter) {
	distDir := filepath.Join(pkgDir, "dist")
	if info, err := os.Stat(distDir); err == nil && info.IsDir() {
		return distDir, nil
	}
	return pkgDir, func(rel string, d fs.DirEntry) bool {
		return !isJunkEntry(d.Name(), d.IsDir())
	}
}

// junkDirs/junkFiles/junkFilePrefixes are excluded when copying a whole
// package (no dist/ folder) — everything not meant to ship as a static
// asset: tooling metadata, docs, tests, and package bookkeeping files
// dist/ output never needs filtering for in the first place.
var junkDirs = map[string]bool{
	"node_modules": true, ".git": true, ".github": true,
	"test": true, "tests": true, "__tests__": true,
	"docs": true, "example": true, "examples": true,
}

var junkFiles = map[string]bool{
	"package.json": true, "package-lock.json": true, ".npmignore": true,
	".gitignore": true, ".gitattributes": true, ".editorconfig": true,
	".babelrc": true, ".npmrc": true, "tsconfig.json": true,
}

// junkFilePrefixes is matched case-insensitively against a file's base
// name, covering README/README.md/README.txt, LICENSE/LICENSE.md, etc.
var junkFilePrefixes = []string{
	"README", "CHANGELOG", "LICENSE", "CONTRIBUTING", "HISTORY", "AUTHORS", "NOTICE",
}

func isJunkEntry(name string, isDir bool) bool {
	if isDir {
		return junkDirs[name]
	}
	if junkFiles[name] {
		return true
	}
	upper := strings.ToUpper(name)
	for _, prefix := range junkFilePrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// copyTree walks src (a real OS directory via filepath.WalkDir — NOT
// fs.WalkDir over an embed.FS, since the source here is a runtime temp
// directory, not nexler's own compiled-in templateFS) and copies every
// entry filter allows (nil filter means copy everything) into dst,
// creating directories as needed.
//
// Deliberately does NOT reuse scaffold.go's processFile/render: those run
// every non-.tmpl file through an LF-normalizing text/template pass meant
// for nexler's own hand-authored Go/text templates — wrong for arbitrary
// vendored JS/CSS/font/map files, which must be copied byte-for-byte (a
// minified bundle can easily contain a literal "{{" that isn't a template
// action).
//
// Symlinks are skipped, not followed — a known, deliberate limitation;
// following one could copy content from outside src entirely, or copy a
// broken/dangling link. Files are streamed via io.Copy rather than
// ReadFile/WriteFile, since vendored assets (fonts, source maps, images)
// can be large. Returns the count of files actually copied.
func copyTree(src, dst string, filter copyFilter) (filesCopied int, err error) {
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if filter != nil && !filter(filepath.ToSlash(rel), d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		dstFile, err := os.Create(target)
		if err != nil {
			return err
		}
		defer dstFile.Close()
		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return err
		}
		filesCopied++
		return nil
	})
	return filesCopied, err
}
