package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var checkinSyncPath = regexp.MustCompile(`^(info|changes|deliveries|deliveries/report|deliveries/actions|sources/[a-fA-F0-9-]{36}|media/[a-fA-F0-9-]{36}|receipts|receipts/[a-fA-F0-9-]{36}/ack|conflicts|conflicts/[a-fA-F0-9-]{36}/resolve)$`)
var checkinTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 15 * time.Second
	return t
}()

type syncTransferWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *syncTransferWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *syncTransferWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *syncTransferWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (w *Worker) proxyCheckinSync(out http.ResponseWriter, r *http.Request) {
	a, err := w.syncAuth(r.Context(), r)
	if err != nil {
		syncFail(out, 401, "配对无效或插件已停用。")
		return
	}
	w.transferCheckin(out, r, a)
}

func (w *Worker) transferCheckin(out http.ResponseWriter, r *http.Request, a syncAuth) {
	if w.CheckinURL == "" || w.CheckinSecret == "" {
		syncFail(out, 503, "打卡服务未启用。")
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/task-sync/v1/checkin/")
	if !checkinSyncPath.MatchString(suffix) {
		syncFail(out, 404, "接口未找到。")
		return
	}
	write := suffix == "receipts" || suffix == "conflicts" || strings.HasSuffix(suffix, "/ack") || strings.HasSuffix(suffix, "/resolve") || suffix == "deliveries/report" || suffix == "deliveries/actions"
	if (write && r.Method != "POST") || (!write && r.Method != "GET") {
		syncFail(out, 405, "请求方式无效。")
		return
	}
	target, err := url.Parse(w.CheckinURL)
	if err != nil {
		syncFail(out, 503, "同步服务暂不可用。")
		return
	}
	timeout := 15 * time.Second
	if strings.HasPrefix(suffix, "media/") {
		timeout = 300 * time.Second
		_ = http.NewResponseController(out).SetWriteDeadline(time.Now().Add(timeout))
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if write {
		r.Body = http.MaxBytesReader(out, r.Body, 1024*1024)
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	id := hex.EncodeToString(nonce)
	out.Header().Set("X-Request-ID", id)
	writer := &syncTransferWriter{ResponseWriter: out}
	started := time.Now()
	complete := false
	defer func() {
		result := recover()
		if result != nil {
			complete = false
		}
		entry, _ := json.Marshal(map[string]any{"event": "checkin_sync_transfer", "request_id": id, "path": suffix, "status": writer.status, "bytes": writer.bytes, "complete": complete, "milliseconds": time.Since(started).Milliseconds()})
		log.Print(string(entry))
		if result != nil {
			panic(result)
		}
	}()
	proxy := &httputil.ReverseProxy{
		Transport: checkinTransport,
		ErrorLog:  log.New(io.Discard, "", 0),
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.URL.Path = strings.TrimRight(target.Path, "/") + "/internal/v1/sync/" + suffix
			p.Out.URL.RawPath = ""
			p.Out.Host = target.Host
			p.Out.Header = make(http.Header)
			p.Out.Header.Set("Authorization", "Bearer "+w.CheckinSecret)
			p.Out.Header.Set("Content-Type", "application/json")
			p.Out.Header.Set("X-Sync-Project", a.Project)
			p.Out.Header.Set("X-Sync-Connection", a.Connection)
			p.Out.Header.Set("X-Sync-Device", a.Device)
			p.Out.Header.Set("X-Request-ID", id)
		},
		ModifyResponse: func(reply *http.Response) error {
			reply.Header.Set("Cache-Control", "private, no-store")
			reply.Header.Set("X-Accel-Buffering", "no")
			return nil
		},
		ErrorHandler: func(out http.ResponseWriter, _ *http.Request, _ error) {
			complete = false
			syncJSON(out, 503, map[string]any{"code": "transfer_unavailable", "error": "同步服务暂不可用，请稍后重试。", "retryable": true, "request_id": id})
		},
	}
	complete = true
	proxy.ServeHTTP(writer, r.WithContext(ctx))
}
