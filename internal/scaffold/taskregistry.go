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
	"path"
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

	coreData := taskRegistryCoreData(modulePath, coreDBType, coreDBAccessor)

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
		idx := strings.Index(mainContent, routesRegisterLine)
		if idx == -1 {
			return changed, fmt.Errorf("%s doesn't contain the expected \"routes.Register(mux)\" line — has it been hand-rewritten? Reconcile it by hand before re-running nexler update", mainPath)
		}
		insertAt := idx + len(routesRegisterLine)
		mainContent = mainContent[:insertAt] + "\n" + taskSyncBlock + mainContent[insertAt:]

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

// coreTaskData is what core/tasks.go.tmpl and core/audit.go.tmpl need when
// rendered outside NewApp (whose own templateData carries the same fields).
type coreTaskData struct {
	ModulePath     string
	AppName        string
	CoreDB         string
	CoreDBAccessor string
}

// taskRegistryCoreData builds coreTaskData for an existing app. AppName is
// the module path's last element, the same value NewApp uses for an app
// created with -module set to its folder name.
func taskRegistryCoreData(modulePath, coreDBType, coreDBAccessor string) coreTaskData {
	return coreTaskData{
		ModulePath:     modulePath,
		AppName:        path.Base(modulePath),
		CoreDB:         coreDBType,
		CoreDBAccessor: coreDBAccessor,
	}
}

// routesRegisterLine is main.go's route wiring; the task sync goes right
// after it, once every route has added its task to the registry.
const routesRegisterLine = "\troutes.Register(mux)\n"

// taskSyncBlock is main.go's core.SyncTasks call, as main.go.tmpl writes it.
const taskSyncBlock = "\t// nexler:tasks-sync (do not remove this marker) — `nexler update`\n" +
	"\t// anchors on this exact call to retrofit an app scaffolded before the\n" +
	"\t// Task Registry existed. It must run after routes.Register: routes add\n" +
	"\t// their tasks to the registry there, so an earlier call syncs nothing.\n" +
	"\t// Best-effort: a failure here is logged, never fatal — the server still\n" +
	"\t// starts even if core_tasks couldn't be synced.\n" +
	"\tif err := core.SyncTasks(context.Background()); err != nil {\n" +
	"\t\tlog.Printf(\"syncing task registry: %v\", err)\n" +
	"\t}\n"

// ensureTaskSyncAfterRoutes moves main.go's core.SyncTasks call after
// routes.Register. Apps generated before this fix synced before any route
// had registered its task, so core_tasks never received their tasks.
func ensureTaskSyncAfterRoutes(appDir string) (bool, error) {
	mainPath := filepath.Join(appDir, "main.go")
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := string(raw)
	syncAt := strings.Index(content, "core.SyncTasks(")
	routesAt := strings.Index(content, routesRegisterLine)
	if syncAt == -1 || routesAt == -1 || syncAt > routesAt {
		return false, nil
	}
	start := strings.Index(content, "\t// nexler:tasks-sync")
	if start == -1 || start > syncAt {
		return false, fmt.Errorf("%s calls core.SyncTasks before routes.Register, without the nexler:tasks-sync comment — move the call below routes.Register(mux) by hand", mainPath)
	}
	end := strings.Index(content[syncAt:], "\n\t}\n")
	if end == -1 {
		return false, fmt.Errorf("%s: can't find the end of the core.SyncTasks block — move it below routes.Register(mux) by hand", mainPath)
	}
	end = syncAt + end + len("\n\t}\n")
	block := content[start:end]
	rest := content[:start] + strings.TrimPrefix(content[end:], "\n")
	idx := strings.Index(rest, routesRegisterLine)
	if idx == -1 {
		return false, fmt.Errorf("%s: lost routes.Register(mux) while moving core.SyncTasks — reconcile by hand", mainPath)
	}
	insertAt := idx + len(routesRegisterLine)
	rest = rest[:insertAt] + "\n" + block + rest[insertAt:]
	return true, os.WriteFile(mainPath, []byte(rest), 0o644)
}
