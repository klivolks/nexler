// This file implements per-app opt-outs from pieces `nexler update` would
// otherwise (re)create. An app records them in nexler.json at its root —
// committed with the app, so every later `nexler update` honours them —
// via `nexler update -opt-out <name>[,<name>]` (and `-opt-in` to undo).
// Opting out never deletes anything already on disk; it only stops nexler
// from adding it back.
package scaffold

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AppConfigFile is the per-app nexler settings file, at the app root.
const AppConfigFile = "nexler.json"

// OptOuts lists every name -opt-out accepts, with what it covers.
var OptOuts = map[string]string{
	"admin-users":    "handlers/admin/users (the core_users admin API), its routes/protected wiring, and core/users.go",
	"admin-services": "handlers/admin/services (the core_services admin API) and its routes/protected wiring; core/services.go stays, service-key auth needs it",
}

const (
	optOutAdminUsers    = "admin-users"
	optOutAdminServices = "admin-services"
)

// readAppConfig returns nexler.json as raw top-level fields (so keys this
// version doesn't know survive a rewrite) plus its optOut list. A missing
// file is an empty config.
func readAppConfig(appDir string) (map[string]json.RawMessage, []string, error) {
	p := filepath.Join(appDir, AppConfigFile)
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON: %w", p, err)
	}
	var optOut []string
	if v, ok := fields["optOut"]; ok {
		if err := json.Unmarshal(v, &optOut); err != nil {
			return nil, nil, fmt.Errorf("%s: optOut must be a list of names: %w", p, err)
		}
	}
	return fields, optOut, nil
}

// OptedOut returns the opt-outs appDir's nexler.json records.
func OptedOut(appDir string) ([]string, error) {
	_, optOut, err := readAppConfig(appDir)
	return optOut, err
}

// optedOut reports whether appDir opted out of name.
func optedOut(appDir, name string) (bool, error) {
	optOut, err := OptedOut(appDir)
	if err != nil {
		return false, err
	}
	for _, n := range optOut {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

// SetOptOut adds names to (out true) or removes them from (out false)
// appDir's nexler.json optOut list, creating the file when needed. Unknown
// names are rejected before anything is written.
func SetOptOut(appDir string, names []string, out bool) (bool, error) {
	for _, n := range names {
		if _, ok := OptOuts[n]; !ok {
			return false, fmt.Errorf("unknown opt-out %q (known: %s)", n, strings.Join(optOutNames(), ", "))
		}
	}
	fields, current, err := readAppConfig(appDir)
	if err != nil {
		return false, err
	}
	set := map[string]bool{}
	for _, n := range current {
		set[n] = true
	}
	changed := false
	for _, n := range names {
		if set[n] != out {
			set[n] = out
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	var list []string
	for n, on := range set {
		if on {
			list = append(list, n)
		}
	}
	sort.Strings(list)
	if len(list) == 0 {
		delete(fields, "optOut")
	} else {
		encoded, err := json.Marshal(list)
		if err != nil {
			return false, err
		}
		fields["optOut"] = encoded
	}
	raw, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(filepath.Join(appDir, AppConfigFile), append(raw, '\n'), 0o644)
}

func optOutNames() []string {
	names := make([]string, 0, len(OptOuts))
	for n := range OptOuts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
