// This file implements the `nexler update` retrofits that let core_tasks
// address every task over HTTP: openapi.Operation's Task/Callable fields and
// Operations() accessor, core.SyncTasks recording service/method/path/
// base_path/callable, and a Task field on every already-generated
// openapi.Register block. A workflow engine reads core_tasks to find which
// service serves a task and how to call it.
package scaffold

import (
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// openAPITaskFields is the Operation struct addition ensureOpenAPITaskFields
// inserts — the same text openapi.go.tmpl carries.
const openAPITaskFields = `	// Task is the middleware.RegisterTask action name this operation
	// serves (e.g. "icorp.crm.accounts.create"). core.SyncTasks uses it to
	// record the task's HTTP method and path in core_tasks, so another
	// service (e.g. a workflow engine) can call the task by name.
	Task string
	// Callable marks the task as one another service may run on a
	// caller's behalf (a workflow step). core.SyncTasks copies it into
	// core_tasks; nothing at request time reads it.
	Callable bool
`

const openAPIOperationsFunc = `
// Operations returns a copy of every Operation registered so far.
func Operations() []Operation {
	mu.Lock()
	defer mu.Unlock()
	return append([]Operation(nil), ops...)
}
`

// ensureOpenAPITaskFields adds Task/Callable to openapi.Operation and the
// Operations() accessor, by insertion rather than ensureOpenAPIUpToDate's
// full regeneration: some apps extend openapi.go by hand (e.g. a second
// public-API spec), and a full rewrite would discard that.
func ensureOpenAPITaskFields(appDir string) (bool, error) {
	p := filepath.Join(appDir, "openapi", "openapi.go")
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := string(raw)
	changed := false

	if !regexp.MustCompile(`(?m)^\s*Callable\s+bool`).MatchString(content) {
		start := strings.Index(content, "type Operation struct {")
		if start == -1 {
			return false, fmt.Errorf("%s has no \"type Operation struct {\" — has it been hand-rewritten? Add the Task string and Callable bool fields to Operation by hand", p)
		}
		end := strings.Index(content[start:], "\n}\n")
		if end == -1 {
			return false, fmt.Errorf("%s: can't find the end of the Operation struct — add the Task string and Callable bool fields by hand", p)
		}
		insertAt := start + end + 1
		content = content[:insertAt] + openAPITaskFields + content[insertAt:]
		changed = true
	}
	if !strings.Contains(content, "func Operations()") {
		if !strings.Contains(content, "ops = append(ops, op)") {
			return false, fmt.Errorf("%s doesn't keep registered operations in \"ops\" — add an Operations() []Operation accessor by hand", p)
		}
		content = strings.TrimRight(content, "\n") + "\n" + openAPIOperationsFunc
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, writeGoFile(p, content)
}

// Anchors from core/tasks.go.tmpl before route metadata existed. A file that
// still has both was never hand-edited in those places and is regenerated.
const (
	oldTaskDocTail = "	RespSchema  map[string]any `bson:\"resp_schema,omitempty\"`\n	SyncedAt    time.Time      `bson:\"synced_at\"`\n}\n"
	oldTaskDocInit = "		doc := taskDoc{\n			Name:        t.Name,\n			RequireAuth: t.RequireAuth,\n			PathParams:  t.PathParams,\n			SyncedAt:    now,\n		}\n"
)

// ensureCoreTaskMetadata regenerates core/tasks.go from the current template
// on a Mongo-core app whose copy predates the service/method/path/base_path/
// callable fields. A SQL core is left alone: its core_task_upsert stored
// procedure has a fixed parameter list.
func ensureCoreTaskMetadata(appDir string) (bool, error) {
	coreDBType, hasCoreDB := readCoreDBType(appDir)
	if !hasCoreDB || coreDBType != "mongo" {
		return false, nil
	}
	p := filepath.Join(appDir, "core", "tasks.go")
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := string(raw)
	if strings.Contains(content, "pruneStaleTasks(") {
		return false, nil
	}
	if !strings.Contains(content, oldTaskDocTail) || !strings.Contains(content, oldTaskDocInit) {
		return false, fmt.Errorf("%s doesn't match the generated taskDoc/SyncTasks shape — has it been hand-edited? Add Service/Method/Path/BasePath/Callable to taskDoc and fill them in SyncTasks by hand (see nexler's core/tasks.go.tmpl)", p)
	}
	configRaw, err := os.ReadFile(filepath.Join(appDir, "config", "config.go"))
	if err != nil {
		return false, err
	}
	if !strings.Contains(string(configRaw), "APIBasePath") {
		return false, fmt.Errorf("config/config.go has no APIBasePath field yet — run the \"config: API base path\" check first, then nexler update again")
	}
	modulePath, err := readModulePath(appDir)
	if err != nil {
		return false, err
	}
	data := taskRegistryCoreData(modulePath, coreDBType, "Mongo")
	tmplPath := templatesRoot + "/core/tasks.go.tmpl"
	tmpl, err := templateFS.ReadFile(tmplPath)
	if err != nil {
		return false, fmt.Errorf("reading embedded template %s: %w", tmplPath, err)
	}
	rendered, err := render(tmplPath, tmpl, data)
	if err != nil {
		return false, fmt.Errorf("rendering %s: %w", tmplPath, err)
	}
	return true, os.WriteFile(p, rendered, 0o644)
}

var (
	// registerTaskCallRe matches a generated route's mux wiring:
	// mux.HandleFunc("POST /accounts", ...RegisterTask("icorp.crm.accounts.create", ...
	registerTaskCallRe = regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)",[^\n]*?RegisterTask\("([^"]+)"`)
	operationStartRe   = regexp.MustCompile(`openapi\.Register\(openapi\.Operation\{`)
	taskFieldRe        = regexp.MustCompile(`(?m)^\s*Task:\s`)
)

// ensureOperationTaskFields inserts Task: "<name>" into every generated
// openapi.Register block in handlers/ that follows a RegisterTask wiring
// for the same method and path and has no Task yet. A block it can't pair
// up is left alone; core.SyncTasks then falls back to matching the task by
// its request and response types.
func ensureOperationTaskFields(appDir string) (bool, error) {
	root := filepath.Join(appDir, "handlers")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return false, nil
	}
	changed := false
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		content, n := addOperationTaskFields(string(raw))
		if n == 0 {
			return nil
		}
		if err := writeGoFile(p, content); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// addOperationTaskFields returns content with Task fields added and how many
// were added.
func addOperationTaskFields(content string) (string, int) {
	var b strings.Builder
	added, pos := 0, 0
	for _, m := range registerTaskCallRe.FindAllStringSubmatchIndex(content, -1) {
		if m[0] < pos {
			continue
		}
		verb, route, name := content[m[2]:m[3]], content[m[4]:m[5]], content[m[6]:m[7]]
		rest := content[m[1]:]
		loc := operationStartRe.FindStringIndex(rest)
		if loc == nil {
			continue
		}
		// The operation must come before the next route's wiring.
		if next := strings.Index(rest, "mux.HandleFunc("); next != -1 && next < loc[0] {
			continue
		}
		blockStart := m[1] + loc[1]
		blockLen := strings.Index(content[blockStart:], "\n\t})")
		if blockLen == -1 {
			continue
		}
		block := content[blockStart : blockStart+blockLen]
		if taskFieldRe.MatchString(block) {
			continue
		}
		methodRe := regexp.MustCompile(`(?m)^\s*Method:\s*"` + regexp.QuoteMeta(verb) + `",`)
		pathRe := regexp.MustCompile(`(?m)^\s*Path:\s*"` + regexp.QuoteMeta(route) + `",[^\n]*\n`)
		pl := pathRe.FindStringIndex(block)
		if !methodRe.MatchString(block) || pl == nil {
			continue
		}
		insertAt := blockStart + pl[1]
		b.WriteString(content[pos:insertAt])
		fmt.Fprintf(&b, "\t\tTask: %q,\n", name)
		pos = insertAt
		added++
	}
	if added == 0 {
		return content, 0
	}
	b.WriteString(content[pos:])
	return b.String(), added
}

// writeGoFile gofmt-formats content when it parses (so an inserted field
// lines up with its neighbours) and writes it either way.
func writeGoFile(p, content string) error {
	if formatted, err := format.Source([]byte(content)); err == nil {
		content = string(formatted)
	}
	return os.WriteFile(p, []byte(content), 0o644)
}
