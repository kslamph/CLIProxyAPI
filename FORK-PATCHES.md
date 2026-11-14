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
`~/orca/workspaces/CLIProxyAPI/custom`; the directory was renamed from the old
`v8.0.55-full-integration`, so ignore that name if it appears in old notes). `~/goproj/CLIProxyAPI` stays on `main` as the upstream mirror. Make
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

State at 2026-11-08: `custom` = upstream v8.0.60 merged (`2c8dd94e`, no conflicts;
includes v8.0.59). **Live on LXC 115: `2c8dd94e` (`v8.0.60-custom`, tag
`deploy/v8.0.60-1`)**; previous `deploy/v8.0.58-1` (`0965505e`) is the rollback.
Drop-when re-check at v8.0.60: P2 unaffected (`gpt-6.1-terra` still in the
upstream catalog); P3 still needed (upstream `buildAuthFromFileData` has no
plugin-parser consult); P1/P4/P5: `git grep` of the v8.0.60 tag finds none of
their symbols (`api-key-model-access`, `ReconcileQuota`, `quota/observation`,
`gatesRouting`, `overload-retry-after-seconds`, `ErrorCodeOverloadDelegated`), so
no upstream equivalent; their Verify tests are green on `custom` after upstream
touched `conductor_cooldown.go`, `conductor_execution.go`, `codex_websockets_*`.
15-minute watch verdict: pass (see History).

State at 2026-11-11: `custom` = upstream v8.0.66 merged (`d31669dd`; four adjacent-edit
conflicts in `sdk/cliproxy/auth/errors.go`, `helps/payload_finalizer.go`, `config_v8_test.go`,
`codex_executor_auth_test.go`, both sides kept); full `go test ./...` green. Carries P1-P9.
Behaviour change from upstream `fa11a573`: cloaked Claude requests no longer get an injected
`fallbacks` entry unless `upstream.claude.server-side-fallback: true` (live config has no such
setting and no explicit fallbacks, so it now runs with the new default off).

The former known upstream failure (`TestInPlaceByteWritesAreReviewed`) no longer
reproduces: `go test ./...` is fully green at `2c8dd94e`. `gofmt -l` still flags
two upstream test files (`claude_thinking_replay_test.go`,
`codex_stream_bootstrap_buffering_test.go`); leave them to avoid merge noise.

State at 2026-11-15: P6 (request-body admission) retired by owner decision.
Production had admission disabled; aggregate admission and its configuration are removed,
including the body-copy optimizations introduced in the original P6 commit. P7 binary
framing and spool pruning remain. Before deploying this change, remove the obsolete
`server.request-body-admission` block (or legacy top-level `request-body-admission`)
from configuration: strict v8 validation no longer accepts it. This change does not
alter `server.max-request-body-bytes` or introduce a decoded-size limit.
Active patches: P1-P5 and P7-P9; patch numbers remain stable.
Deployed 2026-11-14T22:31Z on LXC 115: `213cd057`, tag `deploy/v8.0.66-1`.
The obsolete admission block was removed in place; current-build baseline and the
15-minute post-deploy watch passed. See `~/devops/docs/jobs/2026-11-14/2026-11-14T222440Z-cpa-custom-p6-drop-deploy.md`.

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
- Accepted scope (owner decision): the ACL covers model catalogs and the shared
  non-stream, stream and count execution paths, not every provider capability.
  Direct Codex Alpha Search (`/v1/alpha/search` and its
  `/backend-api/codex/alpha/search` alias), Live and Realtime forwarding retain
  API-key authentication but do not enforce this model policy. In an established
  native Responses duplex WebSocket, later turns re-authenticate the API key but
  do not check the newly returned `ModelPolicy`; narrowing the key's model access
  requires a reconnect to take effect there. Keep these gaps documented rather
  than expanding enforcement as part of this patch.
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
- Upstream: [PR #7554](https://github.com/router-for-me/CLIProxyAPI/pull/7554),
  submitted 2026-11-15; retain this patch until an equivalent upstream release is merged.

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

### P3 - keep plugin-assigned auth IDs on v8 management writes (`candidate`)
- Commit: `cf7f0f92` (`fix(management): keep plugin-assigned auth IDs ...`).
- Review fixes (2026-11-15): plugin-provided `FileName` values are retained (only empty names default to the source basename). New strict parser, multi-auth identity reconciliation, atomic upload writes and delete-all virtual-record cleanup are enabled only by the maintained v8 upload/delete wrappers; deprecated v0 retains its original single-record/path-ID/direct-write behavior. Shared OAuth and field-save helpers retain upstream behavior, and the existing SIWC mutation fencing remains independent. Temp-file errors use method-specific names. Regression tests cover raw/multipart v0/v8 uploads for multi-record, parser-error and handled-empty results, and names against watcher synthesis.
- Bug: saving or uploading a plugin-owned auth file through
  `POST /v0/management/auth-files` (this is what the CPAM UI does when editing
  excluded models) registered an auth with the path-derived ID `<file>.json`,
  while the file watcher registered the plugin-assigned ID (for opencode-go:
  `opencode-go-key-<sha256(api_key)>`) for the same file -> the credential
  appeared twice until restart. Cause: `buildAuthFromFileData` built the
  `SynthesisContext` without `PluginAuthParser` and then forced
  `auth.ID = authIDForPath(path)`.
- Change (single production file, `internal/api/handlers/management/auth_files_crud.go`):
  v8 upload synthesis passes the plugin host as `PluginAuthParser` through
  a small `pluginOwnershipRecorder` and preserves all plugin-assigned IDs,
  including multi-auth files. A handled-empty result suppresses fallback records;
  a successful unhandled result keeps the historical path-derived ID. The
  `GetByID` lookup for the CreatedAt/refresh carry-over uses the final ID.
  V8 upload synthesis uses `StrictFileParsing: true`, matching the watcher's single-file
  snapshot path: a parser error rejects the write before file replacement,
  record upsert/removal or post-persist hooks, rather than silently falling back
  to a path-derived identity. Existing files and auth records remain unchanged.
- Tests: `auth_files_plugin_identity_test.go` -
  `TestBuildAuthFromFileData_KeepsPluginAssignedID`,
  `TestWriteAuthFile_PluginOwnedFileDoesNotDuplicate`,
  `TestBuildAuthFromFileData_UnclaimedFileKeepsPathID`, multi-auth/handled-empty
  coverage, and `TestWriteAuthFileRejectsPluginParserErrorWithoutMutation`
  (single/multi parsers, handled/unhandled errors, new uploads and overwrites).
- Verify: `go test ./internal/api/handlers/management`.
- Related but separate: the opencode-go plugin carries a start-up reconcile for
  already-duplicated records (plugin P1/P2). P3 stops new duplicates from the
  host side; the plugin reconcile only repairs old state at (re)configure time.
  Both are needed until upstream fixes the host.
- Drop when: upstream `buildAuthFromFileData` consults the plugin parser and
  preserves plugin IDs. Check by re-running the three tests above against
  upstream's code; if they pass without our change, delete the patch (keep the
  test file as a regression guard if upstream has no equivalent).
- Upstream: [PR #7555](https://github.com/router-for-me/CLIProxyAPI/pull/7555),
  submitted 2026-11-15; retain this patch until an equivalent upstream release is merged.

### P4 - quota observations set/clear a credential's out-of-quota routing state (`local-only`)
- Commits: merge of branch `quota-refresh-clears-cooldown` (`c8a8d37c`, `40e89820`,
  `c10a4425`, `d37b97f0`, `8f5096e3`).
- Problem (owner-observed 2026-11-04/05): fresh quota readings never reached the
  routing state. (a) codex: a credential WITH quota was avoided because a stale
  in-memory `credential_quota` block (reset time far ahead, persisted in the
  cooldown store, retained across auth refresh) survived an early replenish
  (reset card); (b) opencode-go: an exhausted credential carried no block, so it
  was requested first and then fell back. The management quota fetch discarded
  its data, and core providers (codex/claude) are fetched by the CPAM frontend
  itself, which CPA never sees.
- Change: `Manager.ReconcileQuota(authID, []QuotaWindow)` +
  `EvaluateQuotaWindows` (`sdk/cliproxy/auth/conductor_quota_reconcile.go`).
  Rules: any window with remaining <= 0 and an unreached reset = exhausted;
  `recover_at` = the LATEST reset **among the exhausted windows only**; exhausted
  with a recover time -> SET auth-level `credential_quota` until then
  (authoritative both ways, overrides a shorter or longer existing block, works
  with no prior block); no exhausted window -> RELEASE any active quota block
  immediately; unknown readings / exhausted without a usable reset -> unchanged.
  Wired into both plugin quota fetch paths (`fetchQuotaForPlugin`,
  `FetchCredentialQuota`) and a new endpoint
  `POST /v8/management/routing/quota/observation` (CPAM reports its parsed
  codex/claude windows; `internal/api/handlers/management/quota_observation.go`).
  `pluginapi.QuotaBucket` gains the optional `gatesRouting` (absent/true = counts;
  false = informational, shown in the UI but ignored for routing; decoder accepts
  `gates_routing`): z.ai's `monthly-tools` window must not take a credential out
  of rotation. Incoming 429 errors retain their existing handling; the release
  policy below determines which recorded errors a later quota refresh clears.
  Every observation writes one info line from `Manager.ReconcileQuota`, e.g.
  `quota observation: result=blocked reason="exhausted window with a future reset"
  recover_at=2026-11-09T00:00:00Z exhausted=weekly windows=rolling=1.00@...,weekly=0.00@...
  provider=opencode-go auth_id="..."` (`result` = blocked|released|unchanged, `windows` =
  `name=remaining@reset`). The detail is in the MESSAGE on purpose: the text log
  formatter (`internal/logging` `logFieldOrder`) silently drops fields outside its
  allow-list, so only `auth_id` and `provider` are logrus fields. Nothing is logged
  when the reconcile itself fails (the callers already warn).
- Accepted release policy (owner decision): when a capacity observation releases
  an active quota block, use the full `ResetQuota`, not a quota-only cleanup.
  This clears all model error/cooldown states, including independent transient
  limits and other model errors; existing auth-level unauthorized protection and
  disabled-credential checks remain. Quota recovery may coincide with upstream
  recovery from other errors. Returning a healthy credential to rotation is
  preferred over retaining a possibly stale block; a persistent upstream error
  can be marked again on use at low cost. Keep this behavior and revisit only
  if actual usage shows a problem.
- Files: `sdk/cliproxy/auth/conductor_quota_reconcile.go` (+test),
  `sdk/cliproxy/auth/conductor_cooldown.go` (ResetQuota tail extracted into
  `publishQuotaChange`), `internal/api/handlers/management/plugin_quota.go`
  (+test), `internal/api/handlers/management/quota_observation.go`,
  `internal/api/server_management_v8.go` (+test), `sdk/pluginapi/types.go` (+test),
  `docs/management-api-v8.md`.
- Conflict hotspots: `conductor_cooldown.go` (ResetQuota / publishQuotaChange),
  `plugin_quota.go` (`quotaWindowsFromResponse`), `server_management_v8.go` route
  table.
- Verify: `go test ./sdk/pluginapi/ ./sdk/cliproxy/auth/ ./internal/api/...`
  - the rule that most needs guarding is `recover_at` = latest reset among
  EXHAUSTED windows only: `TestEvaluateQuotaWindows` (examples 1-3); routing proof
  `TestManagerReconcileQuota_RoutingExcludesBlockedAuth`; the log line
  `TestReconcileQuotaLogsObservationOutcome`; gating
  `TestFetchCredentialQuota_NonGating*`.
- Siblings that must stay in step: CPAM P3 (reports codex/claude windows to the
  endpoint and its manager-server proxy exact-path route) and zai-provider v0.1.3
  (marks `monthly-tools` `gatesRouting:false`). opencode-go needs no change (all
  its windows gate requests).
- Drop when: upstream applies fresh provider quota readings to credential routing
  state in both directions (set and release) and exposes a way for an external
  client to report a quota observation. Re-run the tests above against upstream's
  code; keep them as regression guards if upstream lacks equivalents.
- Upstream: not filed.

### P5 - Codex overload rejections delegated to the client (`local-only`)
- Commit: branch `codex-overload-retry-hint` (this change).
- Problem (owner-observed 2026-11-05, LXC 115): the Codex upstream intermittently answers with
  `server_is_overloaded` / `service_unavailable_error` inside an HTTP 200 SSE stream right after
  the handshake events, carrying `error.headers` = `x-retry-metadata NO_MORE_RETRY`, `retry-after
  316`. Owner data says it is capacity/time-of-day noise, unrelated to the credential. CPA used to
  (a) cool the reporting credential, (b) answer 503 instantly once both paid credentials cooled
  (69/943 requests, median 270 ms), (c) fail over to the other credential (prompt-cache miss for
  nothing), and (d) forward the 316 s hint so the official Codex client (0.166.0) slept exactly
  316 s per its `stream_max_retries` while never reading `x-retry-metadata`.
- What: new config `upstream.codex.overload-retry-after-seconds` (aliases `codex.` and
  `oauth.providers.codex.`; `*int`, unset = 5, explicit <= 0 = kill switch). For the overload class
  ONLY (`error.code server_is_overloaded` / `error.type service_unavailable_error`, rate-limit /
  quota codes excluded): the statusErr is built as 429 + retryAfter 5 s + `overloadDelegated` marker
  and its body's `error.headers` is rewritten to `retry-after "5"` / `retry-after-ms "5000"` with
  `x-retry-metadata` removed (both the bare error and response.failed layouts; the rewrite happens
  in the executor's error construction, so every client renderer - Responses SSE
  `response.failed`/`error` frames, uncommitted JSON bodies, chat-completions, Claude - renders the
  rewritten body; the frame Codex reads is the `response.failed` event's `response.error.headers`).
  The conductor (`sdk/cliproxy/auth`): new `ErrorCodeOverloadDelegated` classification, cooldown skip
  via `shouldSkipCredentialCooldown` (no NextRetryAfter/Unavailable/Quota write, quota backoff
  unmoved), session-affinity rebind skip, no retry rounds / no model-pool hop / no credential
  failover (stream, non-stream, handler bootstrap retry). When response headers are uncommitted the
  client gets HTTP 429 + `Retry-After: 5` with the rewritten body. Bootstrap buffering
  (`codex.stream-bootstrap-buffering`) is unchanged as a feature, but for this class it no longer
  fails the attempt over - it delivers in-stream; every other overload-shaped class keeps the
  failover. `codex.stream-bootstrap-buffering` stays default-off.
- Accepted rewrite gaps (owner decision): classification and raw-frame rewriting
  do not cover identical WebSocket layouts. The WebSocket parser normalizes
  `body.error` to a canonical `error`, but the duplex raw-frame detector/rewriter
  only checks `error` and `response.error`; a forwarded wrapped `body.error`
  frame can therefore retain the upstream long retry hint despite internal
  delegated-overload classification. For a raw `response.failed` frame without
  an error headers object, the rewrite fallback creates top-level `error.headers`
  instead of `response.error.headers`, so the expected nested short hint may be
  absent. These are static-review findings, not reproduced production incidents.
  Keep the current implementation; fix these cases only if observed in use.
- Files: `internal/config/config_types.go` (field + default + accessor), `internal/config/config_v8.go`
  (aliases), `internal/watcher/diff/config_diff.go` (reload log line), `config.example.yaml`,
  `internal/runtime/executor/{codex_executor_terminal,codex_executor_stream,codex_executor_execute,
  codex_executor,codex_openai_images,codex_websockets_errors,codex_websockets_stream,
  codex_websockets_execute,codex_websockets_duplex,codex_websockets_executor,openai_compat_executor}.go`,
  `sdk/cliproxy/executor/types.go` (`CodexOverloadDelegatedError`),
  `sdk/cliproxy/auth/{errors,conductor_cooldown,conductor_selection,conductor_execution,
  conductor_stream}.go`, `sdk/api/handlers/handlers_stream.go`.
- Tests: `internal/config/codex_overload_config_test.go`,
  `internal/runtime/executor/codex_overload_retry_hint_test.go`,
  `sdk/cliproxy/auth/conductor_overload_delegation_test.go`,
  `test/codex_overload_retry_hint_test.go` (full-stack /v1/responses with the recorded journal
  bytes; committed-stream rewrite + no cooldown/failover, uncommitted 429 + Retry-After 5 with a
  pre-cooled sibling, kill switch restores 316 passthrough + cooldown).
- Conflict hotspots: `newCodexStatusErrWithHeadersAndCooling` (now `newCodexStatusErrForPolicy`) and
  `codexTerminalFailureErr*` in `codex_executor_terminal.go`; the failover `continue` sites in
  `conductor_stream.go` / `conductor_execution.go`; `shouldSkipCredentialCooldown`;
  `isCodexOverloadBootstrapFailure`. Upstream refactors of the codex error constructors are the
  usual cause; re-apply the policy check at the constructor and the marker checks at the retry
  decision points.
- Verify: `go test ./internal/config/ ./internal/runtime/executor/ ./sdk/cliproxy/auth/ ./test/ -run 'Overload'`
  plus the full gate (`gofmt -l .`, `go build -o /tmp/cpa-check ./cmd/server`, `go test ./...`).
- Drop when: upstream stops treating a `server_is_overloaded` rejection as cooling/failover-able
  (no cooldown, no failover, client-directed retry), or the Codex client stops needing a short
  `retry-after` to retry promptly (e.g. it starts honouring `NO_MORE_RETRY` semantics or its own
  capped backoff). Re-run the tests above against upstream's code; keep them as regression guards
  if upstream lacks equivalents.
- Upstream: not filed. The behaviour is fork policy (owner decision: simple, no cooldown, sticky).

### P7 - CPBF binary body frames and request-log spool pruning (`local-only`)
- Commits: `7244ab69` (original), `23daa206` and `f84b6116` (review fixes and protocol limits).
- What: plugins opt in with the `binary_body_frames` capability. Executor execute / stream / count
  / http requests and `host.http.do`/`do_stream` callbacks then carry JSON metadata plus raw body
  segments in a CPBF frame (`sdk/pluginabi/body_frames.go`, `internal/pluginhost/body_frames.go`)
  instead of Base64 JSON; non-opted-in plugins keep legacy JSON. Frames are bounded (128MiB whole
  frame, 16 segments, length/version/kind checks). `host.http.*` frames are decoded straight from
  the raw segment (no Base64 re-encode). `request-log-parts-*` spools older than 24h are removed
  when a new `FileBodySource` is created, but never spools owned by a live source in this process,
  and age is the newest mtime of the dir or its direct children (appends do not bump the dir).
- Residual limits (unresolved protocol policy): there is NO JSON fallback for requests too large to frame
  (>128MiB total of metadata + OriginalRequest + Payload): the call fails with
  `plugin body frame is too large`, because the ABI only guarantees that an opted-in plugin accepts
  CPBF, not mixed per-call JSON. Likewise a framed instance rejects legacy-JSON `host.http.*`
  callbacks, so a plugin cannot fall back for oversize callbacks either. Sizing `max-request-body-bytes`
  below ~64MiB avoids the case; lifting it needs a protocol change (e.g. a second capability or
  host-side magic sniffing) coordinated with the plugin repos. Another CPA process sharing the log
  dir is only protected while its spool files are being appended; a spool idle for >24h can be
  swept. `EncodeBodyFrame` copies into a buffer then clones, so it is not zero-copy.
- Verify: `go test ./internal/pluginhost ./internal/logging ./sdk/pluginabi -run 'BodyFrame|DecodeHostHTTPCall|HostHTTPFramed|CleanupStaleRequestLogParts|NewFileBodySourceInDir'`
- Drop when: upstream ships an equivalent binary-body plugin transport (then re-check the
  opencode-go plugin's own copy, which advertises the capability), or plugins stop needing
  large-body transport. Sibling impact: `opencode-go-cliproxyapi` mirrors the frame format and its
  cold-bridge retry depends on the host's `invalid plugin body frame` error text.

### P8 - non-stream keep-alive limited to media JSON responses (`local-only`)
- Commits: `b2529e84` (scope), `33736a0d` and `20f1ccae` (binary download, regression tests, scope docs).
- Problem: upstream `nonstream-keepalive-interval` writes a blank line every N seconds while any
  non-stream request waits. The first blank line commits HTTP 200 and the headers, so a later
  upstream 429/502 reaches the client as 200 and upstream headers are lost; for the video content
  download it also prefixed the raw video bytes with `\n` and pre-empted `Content-Type`/`Content-Length`.
- What: the helper is `BaseAPIHandler.StartMediaNonStreamingKeepAlive` and is called only by the
  xAI image/video JSON handlers. Chat, Completions, Responses, Compact, Claude, Gemini and
  Interactions never call it, so they keep real statuses. `VideosContent` (binary download) does not
  call it. Tradeoff: a plain chat request has no proxy idle protection from this setting.
  Limitation: an error that arrives after the first tick on a media endpoint is a JSON body inside
  HTTP 200. Tick source is injectable for tests (`SetNonStreamingKeepAliveTickerFactoryForTest`).
- Files: `sdk/api/handlers/handlers.go`, `sdk/api/handlers/openai/{openai_handlers,openai_responses_handlers,openai_images_handlers,openai_videos_handlers}.go`, `sdk/api/handlers/{claude/code_handlers,gemini/gemini_handlers,gemini/interactions_handlers}.go`, `internal/config/sdk_config.go`, `config.example.yaml`.
- Tests: `sdk/api/handlers/openai/openai_media_keepalive_test.go`.
- Conflict hotspots: any upstream re-adding `StartNonStreamingKeepAlive` calls to text handlers, or
  new non-stream binary endpoints starting a keep-alive.
- Verify: `go test ./sdk/api/handlers/... -run 'KeepAlive|Videos'`.
- Drop when: upstream scopes the non-stream keep-alive away from text and binary responses, or
  removes it.
- Upstream: not filed.

### P9 - `siwc_mode: always` forces SIWC for Codex client requests (`local-only`)
- Commit: `edc380c7`.
- What: a Codex credential with `use_siwc: true` and `siwc_mode: always` sends every request
  through the nested SIWC partition, including requests from Codex clients (codex-tui, Codex
  Desktop), which upstream skips SIWC for by design. `IsCodexUseSIWCForRequest` honours
  `siwc_mode` before the client-UA exclusion; `ensureCodexSIWCCredentialsForRequest` requires the
  SIWC token whenever the request resolves to SIWC (error `siwc access_token is missing`);
  `WithCodexOuterCredentialRequired` keeps OpenAI image execution on the outer partition
  regardless of the mode (SIWC does not serve it); `siwc_mode` is preserved across credential
  rewrites/refreshes like `use_siwc` and shown in the management auth-files listing.
- Files: `internal/api/handlers/management/auth_files.go`, `internal/auth/codex/token.go`,
  `internal/runtime/executor/codex_executor_auth.go` (+test), `sdk/cliproxy/auth/{types,
  conductor_execution,conductor_stream,metadata_merge}.go`, `sdk/cliproxy/auth/codex_siwc_selection_test.go`.
- Conflict hotspots: `sdk/cliproxy/auth/types.go` (`IsCodexUseSIWCForRequest`, siwc_mode
  helpers), the SIWC selection call sites in `conductor_execution.go` / `conductor_stream.go`,
  `ensureCodexSIWCCredentialsForRequest`. Upstream changes to the SIWC partition logic are the
  usual cause; re-apply "siwc_mode before the client-UA exclusion" and "image requests keep the
  outer partition".
- Verify: `go test ./sdk/cliproxy/auth/ ./internal/runtime/executor/ -run 'SIWC'`.
- Drop when: upstream lets a credential force SIWC for Codex client requests (an equivalent
  `siwc_mode`/always option), or stops excluding Codex clients from SIWC.
- Upstream: not filed.

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
| 2026-11-14 | v8.0.66 | `213cd057` | LXC 115, `deploy/v8.0.66-1`: P6 retired, obsolete admission config removed in place; opencode-go v0.2.4 and zai-provider v0.1.7 rebuilt against custom SDK and loaded. Watch pass: 59/59 POSTs 2xx; no restart, OOM, PID change or high events. Backup `/var/tmp/cpa-v8066-deploy-213cd057/backup/`; job `~/devops/docs/jobs/2026-11-14/2026-11-14T222440Z-cpa-custom-p6-drop-deploy.md`. |
| 2026-10-22 | v8.0.38 | `0db19025` | first VPS deploy (static build, later found wrong) |
| 2026-10-31 | v8.0.48 | `909b88f3` + dirty tree | P1 merged; CGO rebuild incl. P2 before it was committed |
| 2026-11-01 | v8.0.48 | `cf7f0f92` | P1 + P2 + P3; fixes duplicate opencode-go profile after saving excluded models |
| 2026-11-04 | v8.0.53 | `605d9ed5` | Upstream v8.0.53 merged into local main (moego port, slug/name detail selection, multi-auth & virtual-source reconciliation). **Never deployed to tyooc2** — it kept running v8.0.48 |
| 2026-11-03 | — | cutover | **Production moved from tyooc2 to LXC 115 `cpa-lxc`** (tunnel `cpa-lxc-test` → `cpa.c2ph.com`/`cpam.c2ph.com`); tyooc2 retired as rollback |
| 2026-11-04 | v8.0.53 | `1f4aa8b9`-dirty | On **LXC 115** (observed by the 2026-11-05 deploy job) |
| 2026-11-05 | v8.0.55-custom | `09c8d426` | On **LXC 115**: `scratch/v8.0.55-full-integration`; plugins opencode-go v0.2.2 + zai v0.1.2. Same day: streaming keepalive 20→80s, request-log temporarily on for a capture |
| 2026-11-05 | v8.0.56-custom | `1f846852` | On **LXC 115**, branch `custom`, tag `deploy/v8.0.56-1`; plugins unchanged (opencode-go v0.2.3, zai v0.1.2); config adds `observability.logs.error-logs-max-files: 100`. First deploy under the 15-min watch rule: 60 API POSTs, 0 failures, no restarts/OOM, CPU ~0.8 %, goroutines flat (baseline `~/devops/ops/cpa-baselines/`) |
| 2026-11-05 | v8.0.56-custom | `5613ef03` | On **LXC 115**, branch `custom`, tag `deploy/v8.0.56-2`; adds **P4** (quota observations set/clear a credential's out-of-quota routing state; `POST /v8/management/routing/quota/observation`; `QuotaBucket.gatesRouting`). Shipped with CPAM `78e90e3c` (P3: reports codex/claude refreshes + proxy route; must be deployed together) and zai-provider v0.1.3 (`monthly-tools` non-gating). 15-min watch pass: 43 API POSTs, 0 failures, no restarts/OOM, CPU ~0.6 %, goroutines declining (baseline `~/devops/ops/cpa-baselines/`). Known open issue: cold-plugin quota fetch 502 (see `providers/cpa-lxc.md`) |
| 2026-11-05 | v8.0.56-custom | `f598ea69` | On **LXC 115**, branch `custom`, tag `deploy/v8.0.56-3`; P4 plus an info log line per quota observation. Shipped with opencode-go 0.2.4 and zai-provider 0.1.4 (host bridge self-corrects framing: fixes the cold-plugin quota-fetch 502) and CPAM `ba201f3a` (P2 plugin-quota Refresh UI restored). Watch pass but thin (21 API POSTs, no chat traffic through the two plugins). The deployed log line drops its detail (formatter allow-list); fix `b6518a69` is on `custom`, not yet deployed |
| 2026-11-05 | v8.0.56-custom | `ea8e7bd4` | On **LXC 115**, branch `custom`, tag `deploy/v8.0.56-4`; ships `b6518a69` (quota-observation log detail moved into the message) + **P5** (Codex `server_is_overloaded` delegated to the client: no cooldown, no failover/retry-round/pool-hop/session rebind; client hint rewritten to `retry-after: 5` with `x-retry-metadata` removed; uncommitted headers fail as 429 + Retry-After 5; `overload-retry-after-seconds` default 5, ≤0 kill switch). Plugins unchanged (opencode-go 0.2.4, zai 0.1.4); config unchanged. 17 new tests; the two full-stack tests reproduce the production symptom on the base commit. Gate: full `go test ./...` with only the known flakes (`TestResponsesInterruptStopsHTTPUpstream` intermittent, `internal/util` byte-write review) |
| 2026-11-06 | v8.0.58-custom | `0965505e` | On **LXC 115**, branch `custom`, tag `deploy/v8.0.58-1`; CPA binary and opencode-go/zai plugins rebuilt against the custom SDK and loaded/registered. `commercial-mode: false` applied with a 19:23Z restart so error-file logging remains enabled. Post-restart watch: pass (thin sampler; retro report 4,171 API POSTs, 0.12% 5xx, 0.91% non-2xx, no restart/PID/OOM/`memory.high` events). Known upstream `internal/util` byte-write review test remains the only full-suite failure. |
| 2026-11-08 | v8.0.60-custom | `2c8dd94e` | On **LXC 115**, branch `custom`, tag `deploy/v8.0.60-1`; upstream v8.0.59+v8.0.60 merged with no conflicts, P1-P5 kept (P3 re-verified as still needed). Plugins NOT rebuilt (deviation from runbook 1.4: `internal/pluginhost` changed but `sdk/pluginapi`/`pluginabi` did not, SchemaVersion 1, plugins are c-shared and import only those two); opencode-go 0.2.4 and zai 0.1.4 loaded and registered. Config unchanged. Watch 12:28:15-12:42:45Z: pass, 118 API POSTs all 2xx (p50 4.4 s, p95 14.9 s) vs baseline 46 all 2xx; no restart/PID/OOM/`memory.high`; three transient memory spikes to ~170 MB reclaimed. **`cpa-health.py` reads journald, but since the 2026-11-06 19:23Z restart the guest logs requests to `main.log` (`logging-to-file: true`), so its own POST count has been 0 since then (this watch, and the v8.0.58 post-restart watch); outcomes here were recomputed from `main.log`. The v8.0.58 row's "4,171 API POSTs" could not be reproduced: `main.log` shows 48 POSTs after the 19:23:02Z restart (46 in the watch window), so do not use 4,171 as a baseline.** At 12:45:26Z another session restarted CPA to deploy zai-provider v0.1.5; that restart is not part of this deploy ([job](~/devops/docs/jobs/2026-11-08/2026-11-08T122651Z-cpa-v8.0.60-deploy.md)). |
