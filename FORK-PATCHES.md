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

Live deployment record: `~/devops/docs/providers/tyooc2-cpa.md` and the dated
jobs under `~/devops/docs/jobs/`.

## Remotes and branch stack

- `origin` = upstream (`router-for-me/CLIProxyAPI`), `kslamph` = our fork.
- Upstream is merged by tag (`git merge vX.Y.Z`), never rebased, into local `main`.
- Patches are stacked as branches, each on top of the previous one:

```
main (upstream tag merges + P1 merged in)
  └─ fix/codex-free-unsupported-models   (+P2)
       └─ fix/plugin-owned-auth-id       (+P3)   <- deployed branch
```

`main` is NOT pushed to `kslamph` regularly; check `git branch -vv` before
assuming the fork on GitHub has the current state. Nothing from P2/P3 had been
pushed when this file was written (2026-11-01).

## Patch inventory

Status key: `local-only` = not in upstream, must be carried; `candidate` = worth
sending upstream (then drop the local copy after it lands).

### P1 - per-API-key model restriction (`local-only`)
- Commit: `2022ade1` (also branch `claude/per-key-model-restriction-5fnut2`).
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

1. `git fetch origin --tags`; `git checkout main`; `git merge vX.Y.Z`. Resolve
   conflicts using the hotspot notes above. Never take "theirs" wholesale on
   files listed in a patch.
2. Re-merge the patch branches in order (or rebase the stack onto new `main`):
   P2 branch, then P3 branch.
3. For each patch run its Verify command. If a patch's "Drop when" condition is
   met, remove the patch and update this file in the same commit.
4. `gofmt -l .`, `go build ./...`, `go test ./internal/api/handlers/management ./sdk/... ./internal/registry ./internal/access/... ./internal/config/...`.
5. Check sibling compatibility: CPAM still proxies GET/PUT
   `/v8/management/config/access` (CPAM P1) and the plugin ABI
   (`sdk/pluginapi`, `AuthParseRequest`/`AuthData`) did not change shape. If the
   plugin ABI changed, rebuild the plugin against the new SDK (see plugin doc).
6. Release build (CGO is mandatory, see below), deploy, post-deploy checks.
7. Update this file: new base tag, any dropped/changed patch, and the deployed
   commit. Also add a dated job log in `~/devops/docs/jobs/`.

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

## Deploy (tyooc2) and post-deploy checks

- Host `ubuntu@tyooc2.c2ph.com`; binary `/home/ubuntu/goproj/CLIProxyAPI/CLIProxyAPI`;
  user units `cliproxyapi.service`, then `cpa-manager.service`.
- Stage -> `sha256sum -c` -> back up old binary + `config.yaml` to
  `/home/ubuntu/deploy-backup-<UTC>-<tag>/` -> atomic `mv` -> restart CPA, wait
  for `:8143`, then restart the manager (otherwise its collector can fall back
  from `subscribe` to polling).
- Preserve `plugins/linux/amd64/` on the VPS across binary swaps.
- Checks: remote `CLIProxyAPI -h` shows the new commit; any management response
  carries `X-Cpa-Support-Plugin: 1`; journal shows no new `error`; collector
  `transport` is `subscribe`; unauthenticated `/v1/models` = 401;
  `GET /v0/management/auth-files` lists each plugin-owned file exactly once
  (for P3: one `opencode-go-key-*` entry per `opencode-go` file).
- Rollback: copy the backed-up binary back and restart both units.

## History

| Date | Base | Deployed commit | Notes |
|---|---|---|---|
| 2026-10-22 | v8.0.38 | `0db19025` | first VPS deploy (static build, later found wrong) |
| 2026-10-31 | v8.0.48 | `909b88f3` + dirty tree | P1 merged; CGO rebuild incl. P2 before it was committed |
| 2026-11-01 | v8.0.48 | `cf7f0f92` | P1 + P2 + P3; fixes duplicate opencode-go profile after saving excluded models |
