package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/enroll"
)

// The update provider's server copy comes through the agent's own client on
// the payload route, under the `update` capability.
func TestUpdatePayloadAsksThePayloadRouteForUpdate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/payload/update/7" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "the agent binary")
	}))
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	client, err := enroll.NewClient(srv.URL, ca)
	if err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	if err := updatePayload(client)(context.Background(), 7, &got); err != nil {
		t.Fatal(err)
	}
	if got.String() != "the agent binary" {
		t.Errorf("payload bytes %q", got.String())
	}

	// A refusal is an error that names the capability, never a body.
	var refused bytes.Buffer
	err = updatePayload(client)(context.Background(), 8, &refused)
	if err == nil || !strings.Contains(err.Error(), "update payload: HTTP 404") {
		t.Errorf("a 404 must be an error naming the update payload, got %v", err)
	}
}
