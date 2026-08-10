// This file implements `nexler init docker [-dir <app-dir>]` — see
// internal/scaffold/docker.go for the actual scaffolding logic. Like
// `nexler init kpass`/`nexler init kgate`, this needs no live network
// connection: it's pure local file scaffolding against an existing app
// directory.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

func runInitDocker(args []string) {
	fs := flag.NewFlagSet("init docker", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	fs.Parse(args)

	if err := scaffold.NewDocker(scaffold.DockerConfig{AppDir: *dir}); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Added Dockerfile and docker-compose.yml.")
	fmt.Println("docker-compose.yml was added to .gitignore (Dockerfile was not — it's meant to be committed, since CI builds from it).")
	fmt.Println("Next steps:")
	fmt.Println("  docker compose up --build")
}
