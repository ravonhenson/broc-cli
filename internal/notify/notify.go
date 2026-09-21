// Package notify surfaces run results to the outside world, so that a
// failed drill (or a drill that never ran at all) can't quietly pass as a
// success just because nobody was watching the terminal.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ravonhenson/broc-cli/internal/config"
	"github.com/ravonhenson/broc-cli/internal/verify"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Send dispatches the configured notifications for a completed run.
// runErr is the operational error from Run, if any (e.g. backend
// unreachable) - distinct from result.OK(), which reflects data-integrity
// findings within a run that otherwise completed.
func Send(ctx context.Context, cfg config.NotifyConfig, result verify.RunResult, runErr error) error {
	var errs []error

	if cfg.Healthcheck != nil && cfg.Healthcheck.PingURL != "" {
		if err := pingHealthcheck(ctx, cfg.Healthcheck.PingURL, result, runErr); err != nil {
			errs = append(errs, fmt.Errorf("healthcheck ping: %w", err))
		}
	}

	if cfg.Webhook != nil && cfg.Webhook.URL != "" {
		ok := runErr == nil && result.OK()
		if !ok || cfg.Webhook.OnSuccess {
			if err := postWebhook(ctx, cfg.Webhook.URL, result, runErr); err != nil {
				errs = append(errs, fmt.Errorf("webhook: %w", err))
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("notify: %v", errs)
}

// StartPing pings the configured healthcheck's /start endpoint, if any, so
// a drill that never finishes (crash, hang, host loss) is itself detected
// by the healthcheck's own timeout/grace-period alerting.
func StartPing(ctx context.Context, cfg config.NotifyConfig) {
	if cfg.Healthcheck == nil || cfg.Healthcheck.PingURL == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Healthcheck.PingURL+"/start", nil)
	if err != nil {
		return
	}
	resp, err := httpClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func pingHealthcheck(ctx context.Context, baseURL string, result verify.RunResult, runErr error) error {
	url := baseURL
	if runErr != nil || !result.OK() {
		url = baseURL + "/fail"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(summaryText(result, runErr)))
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

func summaryText(result verify.RunResult, runErr error) []byte {
	if runErr != nil {
		return []byte("broc run failed: " + runErr.Error())
	}
	return []byte(fmt.Sprintf("broc: %d checked, %d new baselines, %d mismatches, %d errors",
		len(result.Files), result.NewBaselines, result.Mismatches, result.Errors))
}

type webhookPayload struct {
	Repo  string           `json:"repo"`
	OK    bool             `json:"ok"`
	Error string           `json:"error,omitempty"`
	Run   verify.RunResult `json:"run"`
}

func postWebhook(ctx context.Context, url string, result verify.RunResult, runErr error) error {
	payload := webhookPayload{
		Repo: result.RepoName,
		OK:   runErr == nil && result.OK(),
		Run:  result,
	}
	if runErr != nil {
		payload.Error = runErr.Error()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}
