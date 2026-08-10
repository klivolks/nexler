// This file implements `nexler init kpass [-dir <app-dir>]` — see
// internal/scaffold/kpass.go for the actual scaffolding logic. Unlike
// `nexler init db`, this needs no live network connection: it's pure
// local file scaffolding against an existing app directory.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

func runInitKpass(args []string) {
	fs := flag.NewFlagSet("init kpass", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	fs.Parse(args)

	if err := scaffold.NewKpass(scaffold.KpassConfig{AppDir: *dir}); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Added kpass/kpass.go and KPASS_URL/KPASS_CLIENT_ID/KPASS_API_SECRET to .env.")
	fmt.Println("Fill in .env's KPASS_* values, then call kpass.Check(ctx, userID, resource, nil) from your handlers/services.")
}
