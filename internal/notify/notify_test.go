package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ravonhenson/drillbit/internal/config"
	"github.com/ravonhenson/drillbit/internal/verify"
)

// recordingServer captures every request it receives so tests can assert on
// method/path/body without racing the handler goroutine.
type recordingServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
}

type capturedRequest struct {
	Method string
	Path   string
	Body   []byte
}

func newRecordingServer(t *testing.T, status int) *recordingServer {
	t.Helper()
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		rs.mu.Lock()
		rs.requests = append(rs.requests, capturedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		rs.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *recordingServer) all() []capturedRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]capturedRequest, len(rs.requests))
	copy(out, rs.requests)
	return out
}

func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSendWebhookNotCalledOnSuccessByDefault(t *testing.T) {
	srv := failingServer(t)
	cfg := config.NotifyConfig{Webhook: &config.WebhookConfig{URL: srv.URL}} // OnSuccess defaults false
	if err := Send(context.Background(), cfg, verify.RunResult{}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSendWebhookCalledOnSuccessWhenOptedIn(t *testing.T) {
	srv := newRecordingServer(t, 200)
	cfg := config.NotifyConfig{Webhook: &config.WebhookConfig{URL: srv.URL, OnSuccess: true}}
	if err := Send(context.Background(), cfg, verify.RunResult{RepoName: "r1"}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := srv.all()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	var payload map[string]any
	if err := json.Unmarshal(reqs[0].Body, &payload); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if payload["repo"] != "r1" || payload["ok"] != true {
		t.Errorf("payload = %+v, want repo=r1 ok=true", payload)
	}
	if _, hasRun := payload["run"]; !hasRun {
		t.Errorf("payload missing run key: %+v", payload)
	}
}

func TestSendWebhookAlwaysCalledOnFailureRegardlessOfOnSuccess(t *testing.T) {
	srv := newRecordingServer(t, 200)
	cfg := config.NotifyConfig{Webhook: &config.WebhookConfig{URL: srv.URL, OnSuccess: false}}
	result := verify.RunResult{RepoName: "r1", Mismatches: 1}
	if err := Send(context.Background(), cfg, result, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := srv.all()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	var payload map[string]any
	_ = json.Unmarshal(reqs[0].Body, &payload)
	if payload["ok"] != false {
		t.Errorf("payload ok = %v, want false for a run with mismatches", payload["ok"])
	}
}

func TestSendHealthcheckSuccessPingsBaseURL(t *testing.T) {
	srv := newRecordingServer(t, 200)
	cfg := config.NotifyConfig{Healthcheck: &config.HealthcheckConfig{PingURL: srv.URL}}
	if err := Send(context.Background(), cfg, verify.RunResult{}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := srv.all()
	if len(reqs) != 1 || reqs[0].Path != "/" {
		t.Fatalf("requests = %+v, want exactly one hit to the base path", reqs)
	}
}

func TestSendHealthcheckFailurePingsFailSuffix(t *testing.T) {
	srv := newRecordingServer(t, 200)
	cfg := config.NotifyConfig{Healthcheck: &config.HealthcheckConfig{PingURL: srv.URL}}
	if err := Send(context.Background(), cfg, verify.RunResult{Errors: 1}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := srv.all()
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/fail") {
		t.Fatalf("requests = %+v, want a hit to the /fail suffix", reqs)
	}
}

func TestSendHealthcheckFailurePingsFailSuffixOnRunErr(t *testing.T) {
	srv := newRecordingServer(t, 200)
	cfg := config.NotifyConfig{Healthcheck: &config.HealthcheckConfig{PingURL: srv.URL}}
	if err := Send(context.Background(), cfg, verify.RunResult{}, errors.New("boom")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	reqs := srv.all()
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/fail") {
		t.Fatalf("requests = %+v, want a hit to the /fail suffix on operational error too", reqs)
	}
}

func TestStartPingHitsStartSuffix(t *testing.T) {
	srv := newRecordingServer(t, 200)
	cfg := config.NotifyConfig{Healthcheck: &config.HealthcheckConfig{PingURL: srv.URL}}
	StartPing(context.Background(), cfg)
	reqs := srv.all()
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/start") {
		t.Fatalf("requests = %+v, want a hit to the /start suffix", reqs)
	}
}

func TestStartPingNoopWhenUnconfigured(t *testing.T) {
	// Must not panic or hang when no healthcheck is configured.
	StartPing(context.Background(), config.NotifyConfig{})
}

func TestSendReturnsErrorOnWebhookFailureStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	cfg := config.NotifyConfig{Webhook: &config.WebhookConfig{URL: srv.URL, OnSuccess: true}}
	err := Send(context.Background(), cfg, verify.RunResult{}, nil)
	if err == nil {
		t.Fatal("expected Send to return an error when the webhook responds with a failure status")
	}
}

func TestSendAggregatesErrorsFromBothTargets(t *testing.T) {
	cfg := config.NotifyConfig{
		Webhook:     &config.WebhookConfig{URL: "http://127.0.0.1:0", OnSuccess: true}, // unreachable
		Healthcheck: &config.HealthcheckConfig{PingURL: "http://127.0.0.1:0"},          // unreachable
	}
	err := Send(context.Background(), cfg, verify.RunResult{}, nil)
	if err == nil {
		t.Fatal("expected an aggregated error when both notify targets are unreachable")
	}
}

func TestSendNoopWhenNothingConfigured(t *testing.T) {
	if err := Send(context.Background(), config.NotifyConfig{}, verify.RunResult{}, nil); err != nil {
		t.Fatalf("Send with no configured targets should be a no-op, got: %v", err)
	}
}
