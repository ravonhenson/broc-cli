package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ravonhenson/drillbit/internal/config"
	"github.com/ravonhenson/drillbit/internal/state"
)

func TestE2E_InitCreatesConfig(t *testing.T) {
	dir := t.TempDir()
	res := runDrillbit(t, dir, "init", "--name", "myrepo", "--repository", "/tmp/somewhere")
	if res.ExitCode != 0 {
		t.Fatalf("init exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "drillbit.yaml"))
	if err != nil {
		t.Fatalf("reading generated config: %v", err)
	}
	if !strings.Contains(string(data), "name: myrepo") {
		t.Errorf("config = %s, want it to contain the repo name", data)
	}
}

func TestE2E_InitRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	first := runDrillbit(t, dir, "init", "--name", "a")
	if first.ExitCode != 0 {
		t.Fatalf("first init failed: %s", first.Stderr)
	}
	second := runDrillbit(t, dir, "init", "--name", "b")
	if second.ExitCode == 0 {
		t.Fatal("expected the second init to fail without --force")
	}
	forced := runDrillbit(t, dir, "init", "--name", "b", "--force")
	if forced.ExitCode != 0 {
		t.Fatalf("forced init failed: %s", forced.Stderr)
	}
}

func TestE2E_RunBaselinesThenSettlesToNoNewWork(t *testing.T) {
	dir := t.TempDir()
	repoDir, srcDir := resticRepo(t, dir)
	writeFile(t, filepath.Join(srcDir, "a.txt"), []byte("hello"))
	writeFile(t, filepath.Join(srcDir, "b.txt"), []byte("world"))
	resticBackup(t, repoDir, srcDir)

	cfgPath := writeConfig(t, dir, nil)

	first := runDrillbit(t, dir, "run", "--config", cfgPath)
	if first.ExitCode != 0 {
		t.Fatalf("first run exit=%d\nstdout=%s\nstderr=%s", first.ExitCode, first.Stdout, first.Stderr)
	}
	if !strings.Contains(first.Stdout, "new baselines") {
		t.Errorf("first run stdout = %s, want it to mention new baselines", first.Stdout)
	}

	second := runDrillbit(t, dir, "run", "--config", cfgPath)
	if second.ExitCode != 0 {
		t.Fatalf("second run exit=%d\nstdout=%s", second.ExitCode, second.Stdout)
	}
	if !strings.Contains(second.Stdout, "0 checked") {
		t.Errorf("second run stdout = %s, want 0 checked (nothing due yet)", second.Stdout)
	}
}

func TestE2E_StatusReportsCoverage(t *testing.T) {
	dir := t.TempDir()
	repoDir, srcDir := resticRepo(t, dir)
	writeFile(t, filepath.Join(srcDir, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, srcDir)
	cfgPath := writeConfig(t, dir, nil)

	if res := runDrillbit(t, dir, "run", "--config", cfgPath); res.ExitCode != 0 {
		t.Fatalf("run failed: %s", res.Stderr)
	}

	status := runDrillbit(t, dir, "status", "--config", cfgPath)
	if status.ExitCode != 0 {
		t.Fatalf("status exit=%d stderr=%s", status.ExitCode, status.Stderr)
	}
	if !strings.Contains(status.Stdout, "1 (snapshot,file) pairs ever verified") {
		t.Errorf("status stdout = %s, want coverage of 1 pair", status.Stdout)
	}
}

func TestE2E_JSONOutputIsWellFormed(t *testing.T) {
	dir := t.TempDir()
	repoDir, srcDir := resticRepo(t, dir)
	writeFile(t, filepath.Join(srcDir, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, srcDir)
	cfgPath := writeConfig(t, dir, nil)

	res := runDrillbit(t, dir, "run", "--config", cfgPath, "--json")
	if res.ExitCode != 0 {
		t.Fatalf("run exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	var payload struct {
		Repo string `json:"repo"`
		OK   bool   `json:"ok"`
		Run  struct {
			Files []struct {
				Path   string `json:"path"`
				Status string `json:"status"`
			} `json:"files"`
			NewBaselines int `json:"new_baselines"`
		} `json:"run"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &payload); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, res.Stdout)
	}
	if payload.Repo != "e2e" || !payload.OK {
		t.Errorf("payload = %+v, want repo=e2e ok=true", payload)
	}
	if payload.Run.NewBaselines != 1 || len(payload.Run.Files) != 1 {
		t.Errorf("payload.Run = %+v, want exactly one newly baselined file", payload.Run)
	}
}

func TestE2E_ExitCodeTwoOnOperationalError(t *testing.T) {
	dir := t.TempDir()
	// Point at a repository directory that was never `restic init`-ed.
	cfgPath := writeConfig(t, dir, func(c *config.Config) {
		c.Restic.Repository = filepath.Join(dir, "never-initialized")
	})
	res := runDrillbit(t, dir, "run", "--config", cfgPath)
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d, want 2 (operational error)\nstdout=%s\nstderr=%s", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestE2E_MissingConfigFileIsOperationalError(t *testing.T) {
	dir := t.TempDir()
	res := runDrillbit(t, dir, "run", "--config", filepath.Join(dir, "does-not-exist.yaml"))
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d, want 2 for a missing config file\nstderr=%s", res.ExitCode, res.Stderr)
	}
}

// TestE2E_ExitCodeOneOnMismatch drives the real CLI, real restic restore,
// and real hashing end-to-end, but deterministically forces a mismatch by
// tampering with the recorded baseline directly (rather than corrupting
// restic's on-disk pack format, which - per the backend integration tests
// - restic's own ciphertext verification would catch as an error before
// drillbit's hash comparison ever runs). This isolates and proves out
// drillbit's own mismatch-reporting path end-to-end.
func TestE2E_ExitCodeOneOnMismatch(t *testing.T) {
	dir := t.TempDir()
	repoDir, srcDir := resticRepo(t, dir)
	writeFile(t, filepath.Join(srcDir, "important.txt"), []byte("the real content"))
	resticBackup(t, repoDir, srcDir)

	cfgPath := writeConfig(t, dir, nil)

	first := runDrillbit(t, dir, "run", "--config", cfgPath)
	if first.ExitCode != 0 {
		t.Fatalf("baseline run exit=%d stderr=%s", first.ExitCode, first.Stderr)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	st, err := state.Open(cfg.State.Path)
	if err != nil {
		t.Fatalf("opening state db: %v", err)
	}
	records, err := st.All()
	if err != nil || len(records) != 1 {
		t.Fatalf("expected exactly one ledger record, got %v err=%v", records, err)
	}
	rec := records[0]
	rec.SHA256 = "0000000000000000000000000000000000000000000000000000000000000" // tampered baseline
	// Backdate past the default 30-day reverify window so the sampler
	// picks this pair up again on the very next run (reverify_after_days:
	// 0 in config doesn't mean "always" - YAML can't distinguish an
	// explicit 0 from an omitted field, so ApplyDefaults treats <=0 as
	// unset and falls back to 30; see config.ApplyDefaults).
	rec.LastVerifiedAt = time.Now().Add(-60 * 24 * time.Hour)
	if err := st.Put(rec); err != nil {
		t.Fatalf("tampering with baseline: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	second := runDrillbit(t, dir, "run", "--config", cfgPath, "--json")
	if second.ExitCode != 1 {
		t.Fatalf("exit=%d, want 1 (findings) after tampering with the baseline\nstdout=%s\nstderr=%s",
			second.ExitCode, second.Stdout, second.Stderr)
	}
	if !strings.Contains(second.Stdout, `"mismatches": 1`) {
		t.Errorf("stdout = %s, want a JSON report of 1 mismatch", second.Stdout)
	}

	status := runDrillbit(t, dir, "status", "--config", cfgPath)
	if status.ExitCode != 1 {
		t.Errorf("status exit=%d, want 1 while a mismatch remains unresolved", status.ExitCode)
	}
}

func TestE2E_NotifyWebhookAndHealthcheckAreCalled(t *testing.T) {
	dir := t.TempDir()
	repoDir, srcDir := resticRepo(t, dir)
	writeFile(t, filepath.Join(srcDir, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, srcDir)

	var mu sync.Mutex
	var webhookHits, healthcheckHits []string

	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		webhookHits = append(webhookHits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer webhookSrv.Close()

	healthcheckSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		healthcheckHits = append(healthcheckHits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer healthcheckSrv.Close()

	cfgPath := writeConfig(t, dir, func(c *config.Config) {
		c.Notify.Webhook = &config.WebhookConfig{URL: webhookSrv.URL, OnSuccess: true}
		c.Notify.Healthcheck = &config.HealthcheckConfig{PingURL: healthcheckSrv.URL}
	})

	res := runDrillbit(t, dir, "run", "--config", cfgPath)
	if res.ExitCode != 0 {
		t.Fatalf("run exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(webhookHits) != 1 {
		t.Errorf("webhook hits = %v, want exactly 1", webhookHits)
	}
	foundStart, foundBase := false, false
	for _, p := range healthcheckHits {
		if strings.HasSuffix(p, "/start") {
			foundStart = true
		}
		if p == "/" {
			foundBase = true
		}
	}
	if !foundStart || !foundBase {
		t.Errorf("healthcheck hits = %v, want both /start and the base success ping", healthcheckHits)
	}
}

// TestE2E_ConcurrentRunFailsCleanlyRatherThanCorruptingState holds the
// state db lock open in-process (simulating an overlapping `drillbit run`)
// and confirms a second real invocation fails fast and cleanly - exit 2,
// no panic, no partial/corrupt write - instead of racing the first.
func TestE2E_ConcurrentRunFailsCleanlyRatherThanCorruptingState(t *testing.T) {
	dir := t.TempDir()
	repoDir, srcDir := resticRepo(t, dir)
	writeFile(t, filepath.Join(srcDir, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, srcDir)
	cfgPath := writeConfig(t, dir, nil)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	holder, err := state.Open(cfg.State.Path)
	if err != nil {
		t.Fatalf("opening state db: %v", err)
	}
	defer holder.Close()

	start := time.Now()
	res := runDrillbit(t, dir, "run", "--config", cfgPath)
	elapsed := time.Since(start)

	if res.ExitCode != 2 {
		t.Fatalf("exit=%d, want 2 while the state db is held by another process\nstdout=%s\nstderr=%s",
			res.ExitCode, res.Stdout, res.Stderr)
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %v to fail; expected it to time out around bbolt's ~5s lock timeout, not hang", elapsed)
	}
}

// TestE2E_RunWorksWithAmbientResticEnvVars confirms drillbit falls back to
// restic's own ambient environment (as it would in a shell already set up
// to run `restic` directly) when repository/password aren't in the config
// at all - the "just works" path the README promises.
func TestE2E_RunWorksWithAmbientResticEnvVars(t *testing.T) {
	dir := t.TempDir()
	repoDir, srcDir := resticRepo(t, dir)
	writeFile(t, filepath.Join(srcDir, "a.txt"), []byte("hello"))
	resticBackup(t, repoDir, srcDir)

	cfgPath := writeConfig(t, dir, func(c *config.Config) {
		c.Restic.Repository = ""
		c.Restic.Env = nil
	})

	cmd := exec.Command(binPath, "run", "--config", cfgPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY="+repoDir, "RESTIC_PASSWORD="+testPassword)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run with ambient env vars failed: %v\n%s", err, out)
	}
}
