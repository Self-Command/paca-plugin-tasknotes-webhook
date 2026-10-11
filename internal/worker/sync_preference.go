package worker

import "net/http"

func (w *Worker) syncPreference(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效。")
		return
	}
	var body struct {
		Prefer string `json:"prefer"`
	}
	if !syncRead(out, r, &body) {
		return
	}
	if body.Prefer != "latest" && body.Prefer != "server" && body.Prefer != "local" {
		syncFail(out, 400, "同步优先规则无效。")
		return
	}
	if _, err = w.Pool.Exec(r.Context(), "UPDATE connections SET sync_priority=$2 WHERE id=$1 AND sync_priority IS DISTINCT FROM $2", a.Connection, body.Prefer); err != nil {
		syncFail(out, 503, "同步优先规则暂未保存。")
		return
	}
	syncJSON(out, 200, map[string]any{"prefer": body.Prefer})
}
