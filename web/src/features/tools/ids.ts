// Model presets list tool servers as "server:<id>" (and builtins as
// "builtin:<name>"); GET /api/v1/tools/ and integrations use the bare server
// id. The composer keeps bare ids so one server is one checkbox.
export const toolKey = (id: string) => id.replace(/^server:/, '')
