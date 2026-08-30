// This file implements ensureTaskRegistry — the `nexler update` retrofit
// for the Task Registry (task/task.go, middleware/task.go, and, on a
// core-database app, core/tasks.go/core/audit.go plus main.go's startup
// core.SyncTasks call). See those templates' own doc comments for what
// they do; this file only brings an app scaffolded before they existed
// up to date with them.
//
// Deliberately does not rewrite RegisterExpr in already-generated route
// files — that's static Go source baked in at each route's own creation
// time. Only a newly-added method (via a future `nexler create <route>`
// call) on an existing app gets wrapped in the new
// middleware.RegisterTask; existing routes keep whatever they were
// scaffolded with (middleware.RequireAuth or a bare handler reference).
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ensureTaskRegistry brings appDir's Task Registry infrastructure up to
// date: writes task/task.go and middleware/task.go unconditionally if
// missing (every app gets these, same as apiclient/), writes
// core/tasks.go and core/audit.go when this app has a core database
// connection and they're missing, and patches main.go's startup sequence
// to call core.SyncTasks — only meaningful (and only attempted) on a
// core-database app, since main.go's whole db.Connect()/db.Close()
// sequence doesn't exist otherwise.
func ensureTaskRegistry(appDir string) (bool, error) {
	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}

	hasJWT, hasSession := detectAuthFiles(appDir)
	authKind := "none"
	switch {
	case hasJWT && hasSession:
		authKind = "both"
	case hasJWT:
		authKind = "jwt"
	case hasSession:
		authKind = "session"
	}
	coreDBType, hasCoreDB := readCoreDBType(appDir)
	coreDBAccessor := "SQL"
	if coreDBType == "mongo" {
		coreDBAccessor = "Mongo"
	}

	changed := false

	taskPath := filepath.Join(appDir, "task", "task.go")
	if _, err := os.Stat(taskPath); os.IsNotExist(err) {
		if err := writeTemplateFile(templatesRoot+"/task/task.go.tmpl", taskPath, struct{}{}); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	middlewareTaskPath := filepath.Join(appDir, "middleware", "task.go")
	if _, err := os.Stat(middlewareTaskPath); os.IsNotExist(err) {
		mwData := struct {
			ModulePath string
			AuthKind   string
			HasCoreDB  bool
		}{ModulePath: modulePath, AuthKind: authKind, HasCoreDB: hasCoreDB}
		if err := writeTemplateFile(templatesRoot+"/middleware/task.go.tmpl", middlewareTaskPath, mwData); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	if !hasCoreDB {
		return changed, nil
	}

	coreData := struct {
		ModulePath     string
		CoreDB         string
		CoreDBAccessor string
	}{ModulePath: modulePath, CoreDB: coreDBType, CoreDBAccessor: coreDBAccessor}

	tasksPath := filepath.Join(appDir, "core", "tasks.go")
	if _, err := os.Stat(tasksPath); os.IsNotExist(err) {
		if err := writeTemplateFile(templatesRoot+"/core/tasks.go.tmpl", tasksPath, coreData); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	auditPath := filepath.Join(appDir, "core", "audit.go")
	if _, err := os.Stat(auditPath); os.IsNotExist(err) {
		if err := writeTemplateFile(templatesRoot+"/core/audit.go.tmpl", auditPath, coreData); err != nil {
			return changed, err
		}
		changed = true
	} else if err != nil {
		return changed, err
	}

	mainPath := filepath.Join(appDir, "main.go")
	mainRaw, err := os.ReadFile(mainPath)
	if err != nil {
		return changed, fmt.Errorf("%s does not exist — is %s a nexler app directory? %w", mainPath, appDir, err)
	}
	mainContent := string(mainRaw)
	if !strings.Contains(mainContent, "core.SyncTasks") {
		const anchor = "\tdefer db.Close()\n"
		idx := strings.Index(mainContent, anchor)
		if idx == -1 {
			return changed, fmt.Errorf("%s doesn't contain the expected \"defer db.Close()\" line — has it been hand-rewritten? Reconcile it by hand before re-running nexler update", mainPath)
		}
		insertAt := idx + len(anchor)
		syncBlock := "\n" +
			"\t// nexler:tasks-sync (do not remove this marker) — `nexler update`\n" +
			"\t// anchors on this exact call to retrofit an app scaffolded before the\n" +
			"\t// Task Registry existed. Best-effort: a failure here is logged, never\n" +
			"\t// fatal — the server still starts even if core_tasks couldn't be\n" +
			"\t// synced.\n" +
			"\tif err := core.SyncTasks(context.Background()); err != nil {\n" +
			"\t\tlog.Printf(\"syncing task registry: %v\", err)\n" +
			"\t}\n"
		mainContent = mainContent[:insertAt] + syncBlock + mainContent[insertAt:]

		if !strings.Contains(mainContent, `"`+modulePath+`/core"`) {
			mainContent, err = insertImport(mainContent, "", modulePath+"/core")
			if err != nil {
				return changed, err
			}
		}
		if err := os.WriteFile(mainPath, []byte(mainContent), 0o644); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}
