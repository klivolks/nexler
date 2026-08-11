// This file implements `nexler init kgate [-dir <app-dir>]` — see
// internal/scaffold/kgate.go for the actual scaffolding logic. Like
// `nexler init kpass`, this needs no live network connection: it's pure
// local file scaffolding against an existing app directory. Unlike
// kpass, it does require that app to already have a core database
// connection (-db at `nexler create app` time) — see kgate.go's doc
// comment.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

func runInitKgate(args []string) {
	fs := flag.NewFlagSet("init kgate", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	fs.Parse(args)

	if err := scaffold.NewKgate(scaffold.KgateConfig{AppDir: *dir}); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Added kgate/kgate.go, wired its /webhooks/kgate fallback route into routes/public/public.go, and added")
	fmt.Println("KGATE_CLIENT_ID/KGATE_WS_SERVER/KGATE_HTTP_SERVER/KGATE_ORIGIN/KGATE_WEBHOOK_SECRET to .env.")
	fmt.Println("Next steps:")
	fmt.Println("  1. Fill in .env's KGATE_* values.")
	fmt.Println("  2. Run \"go mod tidy\" — this pulls in github.com/gorilla/websocket, which needs network access once.")
	fmt.Println("  3. Run (or re-run) \"nexler init db\" to provision core_kgate_channels on your core database.")
	fmt.Println("  4. Call kgate.Subscribe(ctx, channel) / kgate.Publish(ctx, channel, payload) from your own code, and edit kgate.handleEvent.")
	fmt.Println("     (kgate.Register already resumes any previously recorded channel automatically on startup — no manual wiring needed.)")
}
