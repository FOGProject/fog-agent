package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// Forum 18319: an identity conflict turned the directory feature off for the
// first enroll request, so the server stored that key's request without the
// feature. Every later request declares it, and the server answers 409
// enrollment_upgrade_requires_new_key for that key forever. The agent must
// make a new key once and enroll again with it.
func TestEnrollLoopMakesANewKeyWhenTheServerRequiresOne(t *testing.T) {
	stubIdentity(t, machine("laptop"))
	var mu sync.Mutex
	var csrs []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req enroll.Request
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		csrs = append(csrs, req.CSRPEM)
		n := len(csrs)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The server refuses every request with the first key only.
		if n == 1 || req.CSRPEM == csrs[0] {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"status":"error","reason":"enrollment_upgrade_requires_new_key"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"pending","reason":"known-host-no-agent"}`)
	}))
	t.Cleanup(srv.Close)

	st, err := enroll.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = srv.URL
	if err := st.SaveCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})); err != nil {
		t.Fatal(err)
	}
	req, err := enrollRequest(st, "", &sayer{})
	if err != nil {
		t.Fatal(err)
	}
	req.Features = []string{enroll.DirectoryJoinTargetFeature}
	client, err := st.Client()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := enrollLoop(context.Background(), st, client, req, true, &sayer{})
	if err != nil {
		t.Fatalf("enrollLoop: %v", err)
	}
	if resp == nil || resp.Status != enroll.StatusPending {
		t.Fatalf("want pending after a new key, got %+v", resp)
	}
	if len(csrs) != 2 || csrs[0] == csrs[1] {
		t.Fatalf("want two requests with different keys, got %d", len(csrs))
	}
}

// A server that refuses the new key too must not make the agent rotate keys
// on every request.
func TestEnrollLoopMakesANewKeyOnlyOnce(t *testing.T) {
	stubIdentity(t, machine("laptop"))
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req enroll.Request
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		seen[req.CSRPEM] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"status":"error","reason":"enrollment_upgrade_requires_new_key"}`)
	}))
	t.Cleanup(srv.Close)

	st, err := enroll.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Config.ServerURL = srv.URL
	if err := st.SaveCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})); err != nil {
		t.Fatal(err)
	}
	req, err := enrollRequest(st, "", &sayer{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := st.Client()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enrollLoop(context.Background(), st, client, req, true, &sayer{}); err == nil {
		t.Fatal("want the second refusal returned as an error")
	}
	if len(seen) != 2 {
		t.Fatalf("want exactly two keys tried, got %d", len(seen))
	}
}
