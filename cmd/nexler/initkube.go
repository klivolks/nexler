// This file implements `nexler init kube [-dir <app-dir>] [-registry
// dockerhub|github] [-image <ref>] [-namespace <ns>] [-replicas <n>]` —
// see internal/scaffold/kube.go for the actual scaffolding logic. Like
// `nexler init docker`, this needs no live network connection: it's pure
// local file scaffolding against an existing app directory.
//
// Image resolution needs one interactive choice in the dockerhub case (a
// Docker Hub username has no local source of truth), so — like `nexler
// init ci`'s -registry — that prompting happens here, never inside
// internal/scaffold: scaffold.ResolveKubeImage is called with whatever
// this layer already resolved, never left to prompt on its own.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

func runInitKube(args []string) {
	fs := flag.NewFlagSet("init kube", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	registry := fs.String("registry", "", "where the container image is published: dockerhub or github (GitHub Container Registry) — used to derive the image: reference; ignored if -image is set")
	image := fs.String("image", "", "full image reference to deploy, e.g. ghcr.io/owner/app:latest — overrides -registry entirely")
	namespace := fs.String("namespace", "", "Kubernetes namespace to set on every generated resource (optional — left blank, kubectl's -n flag/current context decides instead)")
	replicas := fs.Int("replicas", 1, "initial replica count for the Deployment")
	fs.Parse(args)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	imageVal := *image
	if imageVal == "" {
		registryVal := *registry
		if !set["registry"] {
			registryVal = promptChoice("Where is the container image published?", []string{"github", "dockerhub"}, "github")
		}
		dockerHubUser := ""
		if registryVal == "dockerhub" {
			dockerHubUser = promptRequired("Docker Hub username (used to build the image: reference)")
		}
		resolved, err := scaffold.ResolveKubeImage(*dir, "", registryVal, dockerHubUser)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
			os.Exit(1)
		}
		imageVal = resolved
	}

	if err := scaffold.NewKube(scaffold.KubeConfig{
		AppDir:    *dir,
		Image:     imageVal,
		Namespace: *namespace,
		Replicas:  *replicas,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Added k8s/deployment.yaml (Secret + Deployment + Service).")
	fmt.Printf("Image: %s\n", imageVal)
	fmt.Println("k8s/deployment.yaml was added to .gitignore — the Secret document embeds real values from .env, so this file must never be committed.")
	fmt.Println("Next steps:")
	fmt.Println("  kubectl apply -f k8s/deployment.yaml")
}
