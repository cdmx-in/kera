package audit

// actions names the event of each admin route that changes something, by its route pattern exactly as it is
// registered. A route missing here is still recorded, as admin.request with its method and route in meta, so
// this list makes the log readable, not complete. The web app turns each action into a sentence
// (web/src/features/admin/audit.ts); TestActionsHaveSentences keeps the two in step.
//
// Actions recorded outside admin routes (sign-in, API keys, sharing, models, knowledge, the command line) are
// named where they are recorded: auth.sign_in, auth.sign_in_failed, auth.sign_out, auth.password_change,
// auth.login_link, apikey.create, apikey.revoke, user.create, user.role_change, user.rename, user.disable,
// user.enable, user.delete, model.create, model.update, model.toggle, model.delete, knowledge.delete,
// knowledge.file_remove, chat.share, chat.unshare, memory.clear, config.apply, config.export, audit.export,
// guardrails.update.
var actions = map[string]string{
	// users, groups, access
	"POST /api/v1/auths/add":                   "user.create",
	"POST /api/v1/users/{id}/update":           "user.update",
	"POST /api/v1/users/{id}/password":         "user.password_set",
	"DELETE /api/v1/users/{id}":                "user.delete",
	"POST /api/v1/users/default/permissions":   "permissions.update",
	"POST /api/v1/groups/create":               "group.create",
	"POST /api/v1/groups/id/{id}/update":       "group.update",
	"POST /api/v1/groups/id/{id}/users/add":    "group.member_add",
	"POST /api/v1/groups/id/{id}/users/remove": "group.member_remove",
	"DELETE /api/v1/groups/id/{id}/delete":     "group.delete",
	"PUT /api/v1/grants/{type}/{id...}":        "access.update",

	// settings and servers
	"POST /api/v1/configs/settings":                   "settings.update",
	"POST /api/v1/configs/tool_servers":               "tool_server.update",
	"POST /api/v1/configs/tool_servers/test":          "tool_server.test",
	"DELETE /api/v1/configs/tool_servers/{id...}":     "tool_server.delete",
	"POST /api/v1/configs/hook_servers":               "hook_server.update",
	"POST /api/v1/configs/hook_servers/test":          "hook_server.test",
	"DELETE /api/v1/configs/hook_servers/{id...}":     "hook_server.delete",
	"POST /api/v1/configs/oauth_providers":            "oauth_provider.update",
	"POST /api/v1/configs/oauth_providers/test":       "oauth_provider.test",
	"DELETE /api/v1/configs/oauth_providers/{id...}":  "oauth_provider.delete",
	"POST /api/v1/configs/connections":                "connection.update",
	"POST /api/v1/configs/connections/{id}/verify":    "connection.verify",
	"POST /api/v1/configs/connections/test":           "connection.test",
	"DELETE /api/v1/configs/connections/{id}":         "connection.delete",
	"POST /api/v1/admin/connectors/{name}/run":        "integration.run",
	"POST /api/v1/skills/import":                      "skill.import",
	"POST /api/v1/skills/id/{id}/update":              "skill.update",
	"DELETE /api/v1/skills/id/{id}/delete":            "skill.delete",
	"POST /api/v1/admin/brand/assets":                 "brand.asset_upload",
	"DELETE /api/v1/admin/brand/assets/{file}":        "brand.asset_delete",
	"POST /api/v1/admin/secrets":                      "secret.create",
	"PUT /api/v1/admin/secrets/{name}":                "secret.update",
	"DELETE /api/v1/admin/secrets/{name}":             "secret.delete",
	"POST /api/v1/admin/pipeline-runners":             "runner.create",
	"POST /api/v1/admin/pipeline-runners/{id}/token":  "runner.token_rotate",
	"DELETE /api/v1/admin/pipeline-runners/{id}":      "runner.delete",
	"POST /api/v1/admin/pipelines":                    "pipeline.create",
	"PUT /api/v1/admin/pipelines/{name}":              "pipeline.update",
	"DELETE /api/v1/admin/pipelines/{name}":           "pipeline.delete",
	"POST /api/v1/admin/pipelines/{name}/token":       "pipeline.token_rotate",
	"POST /api/v1/admin/pipelines/{name}/once":        "pipeline.override",
	"PUT /api/v1/admin/pipelines/{name}/schedule":     "pipeline.schedule_change",
	"PUT /api/v1/admin/pipelines/{name}/stages":       "pipeline.stages_update",
	"PUT /api/v1/admin/pipelines/{name}/script":       "pipeline.script_save",
	"PUT /api/v1/admin/pipelines/{name}/env":          "pipeline.env_set",
	"DELETE /api/v1/admin/pipelines/{name}/env/{var}": "pipeline.env_delete",
	"POST /api/v1/admin/pipelines/{name}/check":       "pipeline.check",
	"POST /api/v1/admin/pipelines/{name}/run":         "pipeline.run",

	"POST /api/v1/admin/pipelines/{name}/runs/{run}/cancel":                    "pipeline.run_cancel",
	"POST /api/v1/admin/pipelines/{name}/runs/{run}/close":                     "pipeline.run_close",
	"POST /api/v1/admin/pipelines/{name}/sessions":                             "pipeline.notebook_start",
	"DELETE /api/v1/admin/pipelines/{name}/sessions/{session}":                 "pipeline.notebook_close",
	"POST /api/v1/admin/pipelines/{name}/sessions/{session}/cells":             "pipeline.notebook_run",
	"POST /api/v1/admin/pipelines/{name}/sessions/{session}/cells/{cell}/stop": "pipeline.notebook_stop",
}
