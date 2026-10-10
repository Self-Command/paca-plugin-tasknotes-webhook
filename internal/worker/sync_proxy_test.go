package worker

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckinTransferPreservesCompleteMediaAndScope(t *testing.T) {
	photo := bytes.Repeat([]byte{1, 2, 3, 4}, 2621440)
	upstream := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/sync/media/00000000-0000-4000-8000-000000000001" || r.Header.Get("Authorization") != "Bearer internal" || r.Header.Get("X-Sync-Device") != "device" {
			t.Error("unexpected delegation")
		}
		out.Header().Set("Content-Type", "image/jpeg")
		out.Header().Set("Content-Length", fmt.Sprint(len(photo)))
		_, _ = out.Write(photo)
	}))
	defer upstream.Close()
	w := Worker{CheckinURL: upstream.URL, CheckinSecret: "internal"}
	proxy := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		w.transferCheckin(out, r, syncAuth{Project: "project", Connection: "vault", Device: "device"})
	}))
	defer proxy.Close()
	r, err := http.Get(proxy.URL + "/task-sync/v1/checkin/media/00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil || !bytes.Equal(data, photo) || r.ContentLength != int64(len(photo)) || r.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("incomplete or unverified media: %d, %v", len(data), err)
	}
}

func TestCheckinTransferRejectsWrongMethod(t *testing.T) {
	w := Worker{CheckinURL: "http://127.0.0.1", CheckinSecret: "internal"}
	out := httptest.NewRecorder()
	w.transferCheckin(out, httptest.NewRequest("POST", "/task-sync/v1/checkin/media/00000000-0000-4000-8000-000000000001", nil), syncAuth{})
	if out.Code != 405 {
		t.Fatal(out.Code)
	}
}

func TestCheckinTransferAbortsTruncatedUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		out.Header().Set("Content-Length", "1048576")
		_, _ = out.Write([]byte("partial"))
	}))
	defer upstream.Close()
	w := Worker{CheckinURL: upstream.URL, CheckinSecret: "internal"}
	proxy := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		w.transferCheckin(out, r, syncAuth{})
	}))
	defer proxy.Close()
	r, err := http.Get(proxy.URL + "/task-sync/v1/checkin/media/00000000-0000-4000-8000-000000000001")
	if err == nil {
		defer r.Body.Close()
		_, err = io.ReadAll(r.Body)
	}
	if err == nil {
		t.Fatal("truncated upstream was accepted")
	}
}
