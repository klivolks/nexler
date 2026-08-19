// This file implements `nexler update [-dir <app-dir>]` — see
// internal/scaffold/update.go for the actual retrofit logic. Like `nexler
// init kpass`/`nexler init kgate`, this needs no live network connection:
// it's pure local file scaffolding against an existing app directory.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	mergeServiceAuth := fs.Bool("merge-service-auth", false, "explicit opt-in: convert an already-scaffolded -auth jwt|both -db app from a separate middleware.RequireServiceAuth to service-key auth folded into RequireAuth (see 'create app -merge-service-auth'); not run automatically, since merged-vs-separate is a per-app design choice, not a one-true-shape fix")
	fs.Parse(args)

	result, err := scaffold.Update(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	for _, name := range result.Applied {
		fmt.Printf("%s: updated\n", name)
	}
	for _, name := range result.Current {
		fmt.Printf("%s: already up to date\n", name)
	}

	if *mergeServiceAuth {
		changed, err := scaffold.MergeServiceAuth(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
			os.Exit(1)
		}
		if changed {
			fmt.Println("auth: merged service-key auth into RequireAuth: updated")
		} else {
			fmt.Println("auth: merged service-key auth into RequireAuth: already up to date (or not eligible — needs -auth jwt|both and -db)")
		}
	}
}
