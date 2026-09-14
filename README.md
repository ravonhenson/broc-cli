# drillbit

[![CI](https://github.com/ravonhenson/drillbit/actions/workflows/ci.yml/badge.svg)](https://github.com/ravonhenson/drillbit/actions/workflows/ci.yml)

**Your backup job has been reporting success for two years. Have you actually restored a file from it recently?**

`drillbit` runs restore drills automatically. On a schedule, it restores a rotating sample of real files from real snapshots in your backup repository into scratch space, hashes them, and diffs that hash against a baseline recorded the first time it ever checked that exact file. Because snapshots are immutable, a baseline that stops matching means something is wrong - and drillbit fails loudly (non-zero exit code, structured JSON, a webhook, or a missed healthcheck ping) instead of leaving you to notice during an actual disaster.

Coverage accumulates incrementally: each run samples a bit more of the repository, so you get full coverage over weeks of cheap scheduled runs instead of one expensive full restore.

Supports **restic** today. Borg and Kopia are on the roadmap - see [Extensibility](#extensibility).

## Why not just trust `restic check`?

`restic check` validates that the repository's pack files are internally consistent. It's necessary, but it never actually restores a file - it can't catch a broken restore path, a misconfigured target, permissions that silently prevent recovery, or a client that reads the repo fine but writes garbage back to disk. `drillbit` performs the full recovery round-trip a real disaster would require, on real data, and remembers whether it worked.

## Install

```sh
go install github.com/ravonhenson/drillbit/cmd/drillbit@latest
```

Or build from source:

```sh
git clone https://github.com/ravonhenson/drillbit
cd drillbit
go build -o drillbit ./cmd/drillbit
```

Requires the `restic` binary on `PATH` (or configured via `restic.binary`).

## Quick start

```sh
# Point drillbit at a repo. Repository/password can also come from the
# usual RESTIC_REPOSITORY / RESTIC_PASSWORD_COMMAND env vars if you leave
# these flags off - drillbit works out of the box in any shell already set
# up to run restic.
drillbit init --name my-backups \
  --repository s3:s3.amazonaws.com/my-bucket/restic \
  --password-command "pass show restic/my-backups"

# Run a drill: restores a sample of files, verifies them, records coverage.
drillbit run

# See what's been covered so far and the recent run history.
drillbit status
```

Put `drillbit run --config /path/to/my-backups.yaml` in cron/systemd-timer next to your backup job, pointed at a *different* schedule than the backup itself (there's no reason to restore-check a snapshot the moment it's created; the value is in continuously reverifying the whole history).

## How verification works

1. **Sample.** Each run picks up to `sample.files_per_run` (snapshot, file) pairs to check, preferring pairs that have never been verified, spread round-robin across snapshots so early runs get breadth rather than exhausting one snapshot first. Once everything has been seen at least once, drillbit starts reverifying the oldest-checked pairs (`sample.reverify_after_days`) to catch bit rot / silent storage corruption over the long run.
2. **Restore.** Each sampled file is restored into scratch space with `restic restore --include <path>` - the same code path a real recovery uses, not a shortcut like `restic dump`.
3. **Diff against baseline.** The restored file is hashed (SHA-256). The first time a given (snapshot, path) pair is seen, that hash becomes its permanent baseline (snapshots are immutable, so it should never legitimately change). Every later check of that same pair is compared against the stored baseline, not against the live source file.
4. **Record & report.** Results go into a local coverage ledger (`state.path`, a single-file embedded DB) and are reported loudly - text or `--json`, a non-zero exit code, and/or your configured webhook / healthcheck ping.

A mismatch is never silently "healed": once a baseline stops matching, drillbit keeps reporting the mismatch on every run until a human investigates and clears it.

## Exit codes

| Code | Meaning |
|------|---------|
| `0`  | Every file checked this run matched its baseline (or was baselined for the first time). |
| `1`  | Findings: at least one restored file didn't match its baseline. **Your backups may not be trustworthy.** |
| `2`  | Operational error: drillbit couldn't complete the run (backend unreachable, bad config, etc.) - it didn't get to check anything. |

Scripts/monitoring should treat 1 and 2 differently: a 1 is a data-integrity emergency, a 2 just means try again / check the logs.

`drillbit status` also exits non-zero if any file currently has an unresolved mismatch, so a monitoring check can catch a lingering problem even between runs.

## Notifications

```yaml
notify:
  healthcheck:
    ping_url: https://hc-ping.com/your-uuid   # healthchecks.io / cronitor-compatible
  webhook:
    url: https://example.com/hooks/drillbit
    on_success: false   # only POST on failure; omit/true to post every run
```

- **Healthcheck ping**: pings `/start` when a run begins (so a hung or crashed run trips the healthcheck's own missed-ping alerting), then the base URL on success or `/fail` on failure.
- **Webhook**: POSTs a JSON body with the full run result (`{"repo", "ok", "run": {...}}`) to any URL - wire it into Slack, PagerDuty, or your own alerting.

## Configuration reference

```yaml
name: my-backups
backend: restic

restic:
  binary: restic                    # optional, default "restic"
  repository: s3:...                # optional, falls back to $RESTIC_REPOSITORY
  password_command: "pass show ..." # optional, falls back to $RESTIC_PASSWORD_COMMAND
  password_file: /path/to/pw        # optional, falls back to $RESTIC_PASSWORD_FILE
  extra_args: ["--limit-download", "5000"]
  env:
    AWS_ACCESS_KEY_ID: "..."

scratch:
  dir: /var/tmp/drillbit/my-backups # default: $TMPDIR/drillbit/<name>
  keep: false                       # keep restored files after each run (debugging)

sample:
  files_per_run: 15
  max_file_size_mb: 500             # skip files larger than this; 0 = no limit
  reverify_after_days: 30           # 0 or omitted both mean "use the default 30" (YAML
                                     # can't tell an explicit 0 from an unset field) -
                                     # there's currently no way to force reverification
                                     # on every single run

state:
  path: ~/.local/state/drillbit/my-backups.db

notify:
  healthcheck:
    ping_url: https://hc-ping.com/your-uuid
  webhook:
    url: https://example.com/hooks/drillbit
    on_success: false
```

Run `drillbit init --help` to generate one of these interactively.

## Extensibility

`drillbit` talks to backup tools through a small `backend.Backend` interface (`internal/backend/backend.go`): list snapshots, list files in a snapshot, restore one file. The restic implementation is the reference (`internal/backend/restic`); adding Borg or Kopia is a matter of implementing that interface against their respective CLIs/APIs and registering it - no changes needed to sampling, state, verification, or reporting. Contributions for either are very welcome.

## Testing

```sh
go test ./...
```

Three layers, all under `go test ./...`:

- **Unit tests** (`internal/**/*_test.go`) - pure logic (sampler selection, state ledger, config defaults, report formatting, notify payloads), plus the restic backend's `--json` parsing exercised against fake `restic` shell scripts so it needs no real binary.
- **Restic integration tests** (`internal/backend/restic/restic_integration_test.go`) - drive the real `restic` binary against real local repositories: unicode/space-y filenames, byte-exact restore round-trips, wrong passwords, an `--include` that matches nothing, and a deliberately corrupted repository (confirms restic's own blob-checksum verification fails the restore rather than silently handing back different bytes - the mismatch-baseline path in `internal/verify` is defense in depth on top of that).
- **End-to-end tests** (`e2e/`) - build the actual `drillbit` binary and drive it as a subprocess exactly as a user would: `init`/`run`/`status`, exit codes 0/1/2, `--json` schema, webhook/healthcheck delivery, and two overlapping `run`s against the same state db failing cleanly instead of racing.

Tests that need the real `restic` binary skip themselves (not fail) when it isn't on `PATH`, so `go test ./...` still works without it installed - CI installs restic so the full matrix always runs there.

## Roadmap

- [ ] Borg backend
- [ ] Kopia backend
- [ ] Partial verification for very large files (byte-range sampling instead of skipping them outright)
- [ ] Smarter snapshot rotation for repositories with very large snapshot counts
- [ ] `drillbit clear` to acknowledge/reset a mismatch after investigation

## License

MIT
