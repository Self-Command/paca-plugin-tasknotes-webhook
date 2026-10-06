package main

import plugin "github.com/Paca-AI/plugin-sdk-go"

const pluginID = "com.selfcommand.tasknotes-webhook"
const pluginVersion = "0.1.0"

type integrationPlugin struct{ db *plugin.DB }

func (p *integrationPlugin) Init(ctx *plugin.Context) error {
	p.db = ctx.DB()
	ctx.Route("GET", "/health", p.health)
	ctx.Route("GET", "/projects/:projectId/status", p.status)
	return nil
}
func (p *integrationPlugin) Shutdown() {}
func (p *integrationPlugin) health(req *plugin.Request, res *plugin.Response) {
	result, err := p.db.Query("SELECT version FROM plugin_metadata WHERE id = 1")
	if err != nil || len(result.Rows) != 1 {
		res.Error(503, "plugin migration unavailable")
		return
	}
	res.JSON(200, map[string]any{"id": pluginID, "version": pluginVersion, "schema_version": result.Rows[0][0], "phase": "host-baseline"})
}
func (p *integrationPlugin) status(req *plugin.Request, res *plugin.Response) {
	res.JSON(200, map[string]any{"project_id": req.PathParam("projectId"), "configured": false, "worker_online": false, "phase": "host-baseline"})
}
