package scaleway

// Regression tests for Client.Delete (instance) edge-case error absorption.
//
// The Scaleway Instance API returns a 404 ResponseError when the target
// server is already gone (e.g. terminated out-of-band or by a previous
// Karpenter reconcile loop that did not persist the result). Client.Delete
// must absorb that error and return nil (idempotent success) so Karpenter
// does not treat it as a fatal failure.
//
// These tests use a real scw.Client pointed at an httptest.Server to exercise
// the full SDK error-parsing stack, rather than mocking at the InstanceAPI
// interface level.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	instance "github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// newInstanceClientForTest creates a Client backed by a scw.Client whose API
// URL is pointed at the provided httptest.Server. Fake auth values must pass
// client-side format checks but are never sent anywhere real.
func newInstanceClientForTest(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	scwClient, err := scw.NewClient(
		scw.WithAuth("SCW0000000000000FAKE", "00000000-0000-4000-8000-000000000000"),
		scw.WithDefaultProjectID("00000000-0000-4000-8000-000000000000"),
		scw.WithAPIURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("scw.NewClient: %v", err)
	}
	return &Client{
		api: instance.NewAPI(scwClient),
	}
}

// TestDelete_404IsNil is the regression test for this edge case:
// Client.Delete must return nil when the Scaleway Instance API responds with
// HTTP 404 — the instance is already gone, so this is idempotent success.
func TestDelete_404IsNil(t *testing.T) {
	// Build the JSON body that the Scaleway Instance API would return for a
	// POST /instance/v1/zones/<zone>/servers/<id>/action request when the
	// server no longer exists. The SDK's hasResponseError path deserialises
	// this into a *scw.ResponseError with StatusCode == 404.
	body, err := json.Marshal(map[string]interface{}{
		"message": "instance not found",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound) // 404
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := newInstanceClientForTest(t, srv)
	gotErr := client.Delete(context.Background(), scw.ZoneFrPar1, "inst-gone")
	if gotErr != nil {
		t.Fatalf("Delete returned non-nil error for HTTP 404: %v", gotErr)
	}
}
