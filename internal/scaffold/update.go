// This file implements `nexler update [-dir <app-dir>]` — a discoverable
// command for bringing an existing generated app's nexler-owned files
// (ones users generally don't hand-edit) up to date with whatever the
// current nexler binary knows how to generate, without needing to
// coincidentally trigger it as a side effect of `nexler create <route>`.
//
// This is a thin registry around retrofit functions that already exist
// for other reasons — ensureOpenAPIUpToDate (route.go) and
// ensureResponseJSONRaw (route.go) were both originally built to run
// silently inside NewRoute; Update just runs the same functions directly,
// unconditionally, and reports what happened. Deliberately never touches
// anything a developer is expected to hand-edit — handlers/services/
// store/models, main.go, .env, templates/html/* — only ever the same
// narrow set of files those two functions already know how to safely
// upgrade (full regeneration for pure generated infra like openapi.go;
// append-only insertion for files that are realistically hand-extended,
// like response.go).
package scaffold

// UpdateResult reports which of Update's checks changed something vs.
// were already current.
type UpdateResult struct {
	Applied []string
	Current []string
}

// updateCheck is one named retrofit — apply reports whether it changed
// anything. Extend updateChecks whenever nexler adds another such
// retrofit function, the same way openAPIUpToDateMarkers documents
// extending its own narrower list.
type updateCheck struct {
	name  string
	apply func(appDir string) (bool, error)
}

var updateChecks = []updateCheck{
	{"openapi/openapi.go", ensureOpenAPIUpToDate},
	{"response/response.go (JSONRaw)", ensureResponseJSONRaw},
}

// Update runs every registered check against appDir in order, stopping at
// the first error (matching NewRoute's own fail-fast style rather than
// best-effort-continue). Unlike NewRoute's narrow gating — which only
// touches response.go when the route being created actually uses
// -response raw — Update runs every check unconditionally: its whole job
// is making every nexler-owned primitive current, the same "every app
// gets it, no flag gating" precedent JSONRaw's own generation-time
// behavior already set.
func Update(appDir string) (UpdateResult, error) {
	if appDir == "" {
		appDir = "."
	}

	if _, err := readModulePath(appDir); err != nil {
		return UpdateResult{}, err
	}

	var result UpdateResult
	for _, check := range updateChecks {
		changed, err := check.apply(appDir)
		if err != nil {
			return result, err
		}
		if changed {
			result.Applied = append(result.Applied, check.name)
		} else {
			result.Current = append(result.Current, check.name)
		}
	}
	return result, nil
}
