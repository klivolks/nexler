// This file implements `nexler init ci [-dir <app-dir>] [-registry
// dockerhub|github]` — see internal/scaffold/ci.go for the actual
// scaffolding logic. Unlike `nexler init kpass`/`nexler init kgate`, this
// needs one interactive choice (which registry to publish to), so it's
// the first `init` subcommand to use the flag-vs-prompt pattern
// `runCreateApp`/`runCreateRoute` already established.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

func runInitCI(args []string) {
	fs := flag.NewFlagSet("init ci", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	registry := fs.String("registry", "", "where release images are published: dockerhub or github (GitHub Container Registry)")
	fs.Parse(args)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	registryVal := *registry
	if !set["registry"] {
		registryVal = promptChoice("Where should release images be published?", []string{"dockerhub", "github"}, "github")
	}

	if err := scaffold.NewCI(scaffold.CIConfig{AppDir: *dir, Registry: registryVal}); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Added .github/workflows/release.yml — builds and pushes a Docker image whenever a GitHub Release is published.")
	switch registryVal {
	case "github":
		fmt.Println("Publishes to ghcr.io using the automatic GITHUB_TOKEN — no repo secrets to configure.")
	case "dockerhub":
		fmt.Println("Publishes to Docker Hub — add DOCKERHUB_USERNAME and DOCKERHUB_TOKEN under repo Settings > Secrets and variables > Actions.")
	}
	if !scaffold.DockerfileExists(*dir) {
		fmt.Println("Note: no Dockerfile found yet — run \"nexler init docker\" (or add one by hand) before this workflow can build anything.")
	}
}
