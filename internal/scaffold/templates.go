package scaffold

import "embed"

// templateFS embeds every file under templates/ directly into the nexler
// binary at build time. This is the mechanism that makes scaffolding
// dependency-free: there is nothing to fetch at runtime, from git or
// anywhere else — the templates are already part of the executable.
//
//go:embed templates
var templateFS embed.FS

// templatesRoot is the root directory name inside templateFS.
const templatesRoot = "templates"

// routeTemplateFS embeds the per-route handler/service/store stubs used by
// `nexler create <route>`, same dependency-free principle as templateFS.
//
//go:embed route_templates
var routeTemplateFS embed.FS

const (
	routeHandlerTmpl     = "route_templates/handler.go.tmpl"
	routeHandlerFileTmpl = "route_templates/handler_file.tmpl"
	routeServiceTmpl     = "route_templates/service.go.tmpl"
	routeStoreTmpl       = "route_templates/store.go.tmpl"
	routeModelTmpl       = "route_templates/model.go.tmpl"

	routeHandlerMethodsFragment  = "route_templates/handler_methods.tmpl"
	routeRegisterMethodsFragment = "route_templates/register_methods.tmpl"
	routeModelMethodsFragment    = "route_templates/model_methods.tmpl"
)

// kpassTemplateFS embeds the kpass integration template used by
// `nexler init kpass`, same dependency-free principle as templateFS.
//
//go:embed kpass_templates
var kpassTemplateFS embed.FS

const kpassTmpl = "kpass_templates/kpass.go.tmpl"

// kgateTemplateFS embeds the kgate integration template used by
// `nexler init kgate`, same dependency-free principle as templateFS.
//
//go:embed kgate_templates
var kgateTemplateFS embed.FS

const kgateTmpl = "kgate_templates/kgate.go.tmpl"

// ciTemplateFS embeds the release-workflow templates used by `nexler init
// ci`, same dependency-free principle as templateFS. Both are fully
// static (no {{ }} placeholders) — GitHub Actions' own contexts
// (github.repository_owner, github.event.repository.name, secrets.*)
// resolve at workflow-run time, not scaffold time, so the same file works
// unmodified for every app.
//
//go:embed ci_templates
var ciTemplateFS embed.FS

const (
	ciTmplGHCR      = "ci_templates/release-ghcr.yml"
	ciTmplDockerHub = "ci_templates/release-dockerhub.yml"
)
