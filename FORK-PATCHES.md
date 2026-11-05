# Fork patches (kslamph/CLIProxyAPI)

This file is the single source of truth for what this fork carries on top of
upstream `router-for-me/CLIProxyAPI`, and how to keep it alive across upstream
syncs. It is a new file on purpose: upstream never touches it, so it never
conflicts. Keep it updated in the same commit as any patch change.

Sibling forks that must stay compatible with this one:

| Component | Repo / location | Patch doc |
|---|---|---|
| CPA (this repo) | `~/goproj/CLIProxyAPI` | this file |
| CPAM (CPA-Manager-Plus) | `~/goproj/CPA-Manager-Plus` | `FORK-PATCHES.md` in that repo |
| opencode-go plugin | `~/goproj/opencode-go-cliproxyapi` | `FORK-PATCHES.md` in that repo |

Live deployment record: **production since 2026-11-03 is Proxmox LXC 115
(`cpa-lxc`)** — durable record `~/devops/docs/providers/cpa-lxc.md`, jobs under
`~/devops/docs/jobs/`. tyooc2 is retired as the rollback host
(`~/devops/docs/providers/tyooc2-cpa.md`); do not deploy or configure it as if
it were live.

## Branch model and protocol (development -> deployment)

This fork wants upstream updates and keeps its own patches until upstream
implements them, so (unlike the plugin repos, where `main` is the working and
deploy branch) `main` here is **not** ours.

| Ref | Role | Rules |
|---|---|---|
| `origin/main` | upstream | read-only, never push |
| `main` | exact mirror of `origin/main` | fast-forward only (`git branch -f main origin/main`); never commit, never merge into it. `git log main..custom` must list exactly our patches |
| `custom` | **the working and deployment branch** = upstream + our patches | every deploy is built from a commit on it; keep it releasable |
| `feat/<topic>`, `fix/<topic>` | short-lived work | branch from `custom` in their own worktree; merge back, then delete |
| `sync/vX.Y.Z` | short-lived upstream sync | branch from `custom`, merges the upstream tag; fast-forwarded into `custom` once green |
| tag `deploy/vX.Y.Z-N` | what is live | annotated tag on the deployed commit, `N` counts redeploys of the same upstream version |

Remotes: `origin` = upstream (`router-for-me/CLIProxyAPI`), `kslamph` = our fork.
Push `custom` and the `deploy/*` tags to `kslamph` after a deploy; never push `main`
or anything else to `origin`.

Worktrees: `custom` is checked out in exactly one worktree (currently
`~/orca/workspaces/CLIProxyAPI/v8.0.55-full-integration`; the directory name is
historical). `~/goproj/CLIProxyAPI` stays on `main` as the upstream mirror. Make
every other worktree from `custom` through Orca (`orca worktree create`,
`orca worktree rm`), and remove it when its branch is merged. Do not leave
long-lived `scratch/*` branches.

### 1. Develop a change

1. `orca worktree create` (or `git worktree add -b feat/<topic> <dir> custom`).
2. Keep the change small; follow `AGENTS.md` (ojson/base64 rules, executor
   layout, `gofmt -w .`).
3. Add or update its entry in **Patch inventory** below in the same commit:
   commit, what, files, conflict hotspots, Verify command, **Drop when**. A
   patch with no "Drop when" is not allowed.
4. Run the entry's Verify command, then the full gate:
   `gofmt -l .`, `go build -o /tmp/cpa-check ./cmd/server`,
   `go test ./...`.
5. Merge into `custom` (`git merge --no-ff feat/<topic>` from the `custom`
   worktree, or fast-forward when it is a single commit), then remove the
   worktree and branch.

### 2. Sync upstream (do this per upstream tag, not per commit)

1. `git fetch origin --tags`, then `git -C ~/goproj/CLIProxyAPI merge --ff-only origin/main`
   (that worktree is the one that has `main` checked out).
2. New worktree + branch `sync/vX.Y.Z` from `custom`.
3. In it: `git merge vX.Y.Z` (merge, not rebase, because `custom` is deployed
   and pushed). Resolve with the hotspot notes in **Patch inventory**; never take
   "theirs" wholesale on files a patch lists.
4. Check each patch's **Drop when** condition: run its Verify command against
   upstream's code (e.g. in a throwaway worktree at the upstream tag). If upstream
   now provides it, delete the patch and its inventory entry in the same commit.
5. Full gate (see 1.4) plus sibling compatibility: CPAM still proxies GET/PUT
   `/v8/management/config/access`; plugin ABI (`sdk/pluginapi`,
   `AuthParseRequest`/`AuthData`) unchanged, else rebuild the plugins against the
   new SDK (see the plugin repos' docs).
6. `git -C <custom worktree> merge --ff-only sync/vX.Y.Z`, delete the sync
   worktree and branch.

### 3. Release and deploy

1. From a **clean** `custom` worktree (the commit stamped into the binary must
   exist on `custom`): build per **Release build** below.
2. Read `~/devops/docs/README.md` first and follow `~/devops/docs/providers/cpa-lxc.md`
   (production is LXC 115; never infer the target from this repo's docs alone).
3. Deploy and run the post-deploy checks (see **Deploy and post-deploy checks**).
   **Mandatory 15-minute watch:** every deployment is monitored for 15 minutes
   after the new process is listening, and compared with the last accepted
   baseline (request success, latency, CPU, memory, goroutines, restarts, new
   error shapes). No baseline for the running version -> establish one first.
   Tool, guard rails and baseline files: `~/devops/docs/runbooks/cpa-deploy.md`,
   `~/devops/ops/cpa-health.py`, `~/devops/ops/cpa-baselines/`. A deploy is not
   done, and its tag/push/History row is not written, until the watch verdict
   is `pass`; a `fail` verdict means roll back.
4. Only after the watch passes:
   `GIT_EDITOR=true git tag -a deploy/vX.Y.Z-N -m "<host> <date>: <summary>" <commit>`;
   `git push kslamph custom deploy/vX.Y.Z-N`.
5. Update this file (History row, base tag, dropped/changed patches), commit it
   on `custom`, and add the dated job log under `~/devops/docs/jobs/`.

### 4. Roll back

Redeploy the previous `deploy/*` tag's binary using the host's own rollback
directory/snapshot (see the cpa-lxc host record; the guest has no other backup).
If the bad deploy needs a code fix, branch `fix/<topic>` from `custom`, never
from the old tag, so `custom` keeps moving forward.

### Branch hygiene

- Before deleting a branch: `git merge-base --is-ancestor <branch> custom`, or
  `git cherry custom <branch>` shows only `-` lines.
- Every commit stamped into a deployed binary is reachable from `custom`; this
  is what makes `main.Commit` meaningful.
- Dirty trees are never deployed (the 2026-10-31 deploy was a mistake of this kind).
- `docs/superpowers/` specs and plans stay uncommitted; do not bundle them
  into patch commits.

State at 2026-11-05: `custom` = upstream v8.0.56 merged (`687e9395`) on top of
`09c8d426` (upstream v8.0.55 + P1-P3 + local pluginhost/memory work). **Live on
LXC 115: `1f846852` (`v8.0.56-custom`, tag `deploy/v8.0.56-1`)**; previous
`deploy/v8.0.55-1` (`09c8d426`) is the rollback. P1-P3 verified unaffected by
the sync (no conflicts; upstream still lists `gpt-6.1-terra` in its catalog, so
P2 stays). 15-minute watch verdict: pass (see History).

Known upstream failure (not ours, reproduces at the bare `v8.0.56` tag):
`go test ./internal/util` -> `TestInPlaceByteWritesAreReviewed` flags
`copy(buf[envelopeHeaderSize:], value)` in `internal/kvstore/bolt_store.go` as an
unreviewed in-place write. Everything else in `go test ./...` passes. Also,
`gofmt -l` flags two upstream test files (`claude_thinking_replay_test.go`,
`codex_stream_bootstrap_buffering_test.go`); leave them to avoid merge noise.

## Patch inventory

Status key: `local-only` = not in upstream, must be carried; `candidate` = worth
sending upstream (then drop the local copy after it lands).

### P1 - per-API-key model restriction (`local-only`)
- Commit: `2022ade1` (formerly branch `claude/per-key-model-restriction-5fnut2`, deleted after being merged into `custom`).
- What: new config `access.api-key-model-access` (client API key ->
  `allowed-models` / `denied-models`, case-insensitive `*`/`?`, deny wins). The
  config-api-key provider attaches a `ModelPolicy` to the auth result; policy
  filters `/v1/models`, Codex/Responses/Claude/Gemini model lists and rejects
  disallowed models with 403 on non-stream, stream and count paths. Keys with no
  entry are unrestricted.
- Files (23): `sdk/access/model_policy.go` (+test), `sdk/access/registry.go`,
  `sdk/api/handlers/model_access.go` (+test), `sdk/api/handlers/handlers_execution.go`,
  `handlers_stream.go`, `claude/code_handlers.go`, `gemini/gemini_handlers.go`,
  `openai/{openai_handlers,openai_responses_handlers,openai_responses_websocket,codex_client_models}.go`,
  `internal/access/config_access/provider.go` (+test), `internal/api/server_middleware.go`,
  `internal/config/{config_v8,sdk_config}.go` (+test), `internal/watcher/diff/config_diff.go`,
  `sdk/cliproxy/service_config.go`, `sdk/config/config.go`, `config.example.yaml`.
- Merge-conflict hotspots: the `sdk/api/handlers/*` list/execute call sites and
  `internal/access/config_access/provider.go`. Upstream refactors there are the
  usual cause; re-apply the policy check at the same three points (model list,
  pre-execute, pre-stream/count).
- Verify after every sync: `go test ./sdk/access/... ./sdk/api/handlers/... ./internal/access/... ./internal/config/...`
  (tests named `*ModelPolicy*`, `*ModelAccess*`, `provider_policy_test.go`).
- Pairs with CPAM P1 (UI editor). CPAM reads/writes it through upstream's
  `/v8/management/config/access` endpoint, so that endpoint's shape must stay
  compatible. Check after a sync that `api-key-model-access` still round-trips
  through GET/PUT `/v8/management/config/access`.
- Drop when: upstream ships an equivalent per-key model ACL. Then migrate the
  live `config.yaml` key and re-test CPAM's editor.

### P2 - drop `gpt-6.1-terra` from codex-free (`local-only`, time-limited)
- Commit: `3914533b`.
- What: removes `gpt-6.1-terra` from the embedded `codex-free` section of
  `internal/registry/models/models.json` and filters it in `GetCodexFreeModels`
  (`internal/registry/model_definitions.go`) so a remote catalog refresh cannot
  bring it back. OpenAI rejects it for free ChatGPT accounts and free accounts
  are scheduled first.
- Conflict hotspot: `models.json` (upstream regenerates it). If it conflicts,
  take upstream's file and re-run only the `GetCodexFreeModels` filter part.
- Verify: `go test ./internal/registry -run CodexFree`.
- Drop when: upstream's catalog stops listing the model for free, or OpenAI
  accepts it for free accounts. Check the model in the upstream catalog first.

### P3 - keep plugin-assigned auth IDs on management writes (`candidate`)
- Commit: `cf7f0f92` (`fix(management): keep plugin-assigned auth IDs ...`).
- Bug: saving or uploading a plugin-owned auth file through
  `POST /v0/management/auth-files` (this is what the CPAM UI does when editing
  excluded models) registered an auth with the path-derived ID `<file>.json`,
  while the file watcher registered the plugin-assigned ID (for opencode-go:
  `opencode-go-key-<sha256(api_key)>`) for the same file -> the credential
  appeared twice until restart. Cause: `buildAuthFromFileData` built the
  `SynthesisContext` without `PluginAuthParser` and then forced
  `auth.ID = authIDForPath(path)`.
- Change (single file, `internal/api/handlers/management/auth_files_crud.go`):
  `buildAuthFromFileData` now delegates to `buildAuthFromFileDataWithParser`,
  passes the plugin host as `PluginAuthParser` through a small
  `pluginOwnershipRecorder`, and only keeps the path-derived ID when no plugin
  claimed the file. The `GetByID` lookup for the CreatedAt/refresh carry-over now
  uses the final ID.
- Tests (new file, no conflict risk): `auth_files_plugin_identity_test.go` -
  `TestBuildAuthFromFileData_KeepsPluginAssignedID`,
  `TestWriteAuthFile_PluginOwnedFileDoesNotDuplicate`,
  `TestBuildAuthFromFileData_UnclaimedFileKeepsPathID`.
- Verify: `go test ./internal/api/handlers/management`.
- Related but separate: the opencode-go plugin carries a start-up reconcile for
  already-duplicated records (plugin P1/P2). P3 stops new duplicates from the
  host side; the plugin reconcile only repairs old state at (re)configure time.
  Both are needed until upstream fixes the host.
- Drop when: upstream `buildAuthFromFileData` consults the plugin parser and
  preserves plugin IDs. Check by re-running the three tests above against
  upstream's code; if they pass without our change, delete the patch (keep the
  test file as a regression guard if upstream has no equivalent).
- Upstream: not yet filed. Suggested issue title: "Management auth-file save
  registers a path-derived ID for plugin-owned credentials (duplicate entry)".

## Untracked local files (not patches, do not lose)

`rebuild.sh`, `run.sh` (local dev stack), `deploy-tyooc2-manifest.md` (historical
deployment plan + CGO correction), `config.yaml` (tracked template is
`config.example.yaml`; the live config lives on the VPS), `CPA` (local dev
binary). The live server uses `CLIProxyAPI` as the binary name; local dev uses
`CPA`.

## Upstream sync procedure

See **Branch model and protocol**, section 2. Minimum Verify set after a sync:
`go test ./internal/api/handlers/management ./sdk/... ./internal/registry ./internal/access/... ./internal/config/...`
plus each patch's own Verify command.

## Release build

```bash
# from ~/goproj/CLIProxyAPI, glibc of build host must be <= VPS glibc
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.Version=vX.Y.Z -X main.Commit=$(git rev-parse --short HEAD) -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o CLIProxyAPI ./cmd/server
file CLIProxyAPI   # must say "dynamically linked"
```

- `CGO_ENABLED=0` builds report `X-CPA-SUPPORT-PLUGIN: 0` and hide the Plugins
  pages (incident 2026-10-31). Never build CPA statically.
- Keep `main.Version` equal to the upstream tag; CPAM and the panel read it.
  The fork is identified by `main.Commit`, which must be a commit on the
  deployed branch.

## Deploy and post-deploy checks

- **Production host: Proxmox LXC 115 `cpa-lxc`** — `ssh -J cloudai root@10.10.60.12`.
  Layout under `/home/cpa/` mirrors the old one (`goproj/CLIProxyAPI/`,
  `goproj/CPA-Manager-Plus/.dev-data/`, auth dir `/home/cpa/.cli-proxy-api`).
  Services are **system** units: `cliproxyapi.service`, `cpa-manager.service`
  (waits for :8143), `cloudflared-cpa-lxc.service`, `disk-guard.timer`.
  **Write `config.yaml` in place** (`cat new > config.yaml`); `sed -i` swaps the
  inode and permanently breaks the config watcher on that guest.
- tyooc2 is hard-decommissioned (2026-11-05); do not deploy to it.
- Stage -> `sha256sum -c` -> back up old binary + `config.yaml` to a
  timestamped `deploy-backup-<UTC>-<tag>/` directory on the host -> atomic `mv`
  -> restart CPA, wait for `:8143`, then restart the manager (otherwise its
  collector can fall back from `subscribe` to polling).
- Preserve `plugins/linux/amd64/` on the host across binary swaps.
- Checks: remote `CLIProxyAPI -h` shows the new commit; any management response
  carries `X-Cpa-Support-Plugin: 1`; journal shows no new `error`; collector
  `transport` is `subscribe`; unauthenticated `/v1/models` = 401;
  `GET /v0/management/auth-files` lists each plugin-owned file exactly once
  (for P3: one `opencode-go-key-*` entry per `opencode-go` file).
- Rollback: copy the backed-up binary back and restart both units (see
  section 4 of the branch protocol).

## History

| Date | Base | Deployed commit | Notes |
|---|---|---|---|
| 2026-10-22 | v8.0.38 | `0db19025` | first VPS deploy (static build, later found wrong) |
| 2026-10-31 | v8.0.48 | `909b88f3` + dirty tree | P1 merged; CGO rebuild incl. P2 before it was committed |
| 2026-11-01 | v8.0.48 | `cf7f0f92` | P1 + P2 + P3; fixes duplicate opencode-go profile after saving excluded models |
| 2026-11-04 | v8.0.53 | `605d9ed5` | Upstream v8.0.53 merged into local main (moego port, slug/name detail selection, multi-auth & virtual-source reconciliation). **Never deployed to tyooc2** — it kept running v8.0.48 |
| 2026-11-03 | — | cutover | **Production moved from tyooc2 to LXC 115 `cpa-lxc`** (tunnel `cpa-lxc-test` → `cpa.c2ph.com`/`cpam.c2ph.com`); tyooc2 retired as rollback |
| 2026-11-04 | v8.0.53 | `1f4aa8b9`-dirty | On **LXC 115** (observed by the 2026-11-05 deploy job) |
| 2026-11-05 | v8.0.55-custom | `09c8d426` | On **LXC 115**: `scratch/v8.0.55-full-integration`; plugins opencode-go v0.2.2 + zai v0.1.2. Same day: streaming keepalive 20→80s, request-log temporarily on for a capture |
| 2026-11-05 | v8.0.56-custom | `1f846852` | On **LXC 115**, branch `custom`, tag `deploy/v8.0.56-1`; plugins unchanged (opencode-go v0.2.3, zai v0.1.2); config adds `observability.logs.error-logs-max-files: 100`. First deploy under the 15-min watch rule: 60 API POSTs, 0 failures, no restarts/OOM, CPU ~0.8 %, goroutines flat (baseline `~/devops/ops/cpa-baselines/`) |
