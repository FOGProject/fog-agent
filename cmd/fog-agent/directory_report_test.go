package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
	"github.com/FOGProject/fog-agent/internal/provider/directoryjoin"
)

// An operator-authorized operation has task 0. The server compares task_id
// exactly, so the result must carry "task_id": 0, not leave it out.
// (Lab 2026-11-03: a missing task_id gave HTTP 409 rejoin_result_mismatch.)
func TestRejoinReportSendsTaskZero(t *testing.T) {
	var item map[string]json.RawMessage
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Item map[string]json.RawMessage `json:"item"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode result request: %v", err)
		}
		item = request.Item
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()
	client := &enroll.Client{ServerURL: server.URL, HTTP: server.Client()}

	st, err := enroll.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Config.HostID = 71
	a := &directoryjoin.Attempt{Plan: directoryjoin.RejoinPlan{
		TaskID: 0, AttemptID: strings.Repeat("a", 32), ConfigRevision: strings.Repeat("b", 32),
	}}
	if err := rejoinReport(context.Background(), st, client, a, "awaiting_reboot", directoryjoin.RejoinObservation{}, false); err != nil {
		t.Fatalf("report: %v", err)
	}
	if got, ok := item["task_id"]; !ok || string(got) != "0" {
		t.Fatalf("task_id = %q (present %t), want 0: %v", got, ok, item)
	}
}
