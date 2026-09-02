// This file implements `nexler init tenant [-dir <app-dir>]` — see
// internal/scaffold/tenant.go for the actual scaffolding logic. Like
// `nexler init kgate`, this needs no live network connection: it's pure
// local file scaffolding against an existing app directory that already
// has a core database connection (-db at `nexler create app` time) and a
// JWT-capable -auth choice.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klivolks/nexler/internal/scaffold"
)

func runInitTenant(args []string) {
	fs := flag.NewFlagSet("init tenant", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain go.mod); defaults to the current directory")
	fs.Parse(args)

	if err := scaffold.NewTenant(scaffold.TenantConfig{AppDir: *dir}); err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Added tenant/tenant.go and handlers/admin/tenants/tenants.go, wired the latter into routes/protected/protected.go.")
	fmt.Println("GET /admin/tenants lists every tenant_orgs record; DELETE /admin/tenants/{id} permanently deletes one (no undo).")
	fmt.Println("Run (or re-run) \"nexler init db\" to provision tenant_orgs on your core database (SQL cores only — Mongo needs no provisioning).")
	fmt.Println("Both routes are guarded only by middleware.RequireAuth (authenticated, not authorized) until \"nexler init kpass\" wires a real permission check.")
}
