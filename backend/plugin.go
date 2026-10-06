package main

import (
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/buildinfo"
)

const pluginID = "com.selfcommand.tasknotes-webhook"
const pluginVersion = buildinfo.Version

type integrationPlugin struct {
	db  *plugin.DB
	cfg *plugin.Config
}

func (p *integrationPlugin) Init(ctx *plugin.Context) error {
	p.db = ctx.DB()
	p.cfg = ctx.Config()
	ctx.Route("POST", "/admin/worker-credential", p.rotateWorkerCredential)
	ctx.Route("GET", "/worker/control", p.workerControl)
	ctx.Route("GET", "/health", p.health)
	ctx.Route("GET", "/projects/:projectId/status", p.status)
	ctx.Route("GET", "/projects/:projectId/connections", p.connections)
	ctx.Route("POST", "/projects/:projectId/connections", p.createConnection)
	ctx.Route("PATCH", "/projects/:projectId/connections/:id", p.updateConnection)
	ctx.Route("POST", "/projects/:projectId/connections/:id/rotate-secret", p.rotateConnection)
	ctx.Route("GET", "/projects/:projectId/connections/:id/deliveries", p.deliveries)
	ctx.Route("GET", "/projects/:projectId/connections/:id/sources", p.sources)
	ctx.Route("POST", "/projects/:projectId/connections/:id/reprocess/:deliveryId", p.reprocess)
	ctx.Route("POST", "/projects/:projectId/connections/:id/link", p.link)
	ctx.Route("POST", "/receive/:id", p.receive)
	return nil
}
func (p *integrationPlugin) Shutdown() {}
func (p *integrationPlugin) health(req *plugin.Request, res *plugin.Response) {
	result, err := p.db.Query("SELECT version FROM plugin_metadata WHERE id = 1")
	if err != nil || len(result.Rows) != 1 {
		res.Error(503, "plugin migration unavailable")
		return
	}
	res.JSON(200, map[string]any{"id": pluginID, "version": pluginVersion, "source_sha": buildinfo.SourceSHA, "schema_version": result.Rows[0][0], "phase": "tasknotes-integration"})
}
func (p *integrationPlugin) status(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT COUNT(*) FROM connections WHERE project_id=$1", req.PathParam("projectId"))
	if err != nil || len(rows.Rows) != 1 {
		res.Error(503, "configuration unavailable")
		return
	}
	res.JSON(200, map[string]any{"project_id": req.PathParam("projectId"), "connections": rows.Rows[0][0], "phase": "tasknotes-integration"})
}
