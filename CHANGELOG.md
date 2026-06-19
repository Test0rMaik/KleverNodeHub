# Changelog

## [Unreleased]

### 2026-06-19
- **Fix: agents intermittently dropping ("heartbeat stalled")**: Agents on several servers went offline together at irregular intervals, then reconnected. Root cause was dashboard-side: the WebSocket read loop persisted heartbeat/metrics to SQLite synchronously through one global lock, so whenever that lock was held a while (notably the hourly metrics decimation, which ran in a single long transaction) every agent's read loop blocked, stopped sending WebSocket pongs, and all agents timed out at once. Fixed by (1) persisting heartbeat/node-metrics on a background worker so the read loop stays responsive, and (2) chunking the hourly decimation into short, bucket-aligned transactions that release the lock between slices. Submitted upstream as well (PR #63).
- **Synced to upstream v0.3.82** (incl. optional BLS key generation during provisioning), with all fork-only features preserved (validator monitoring, alerts, elections chart, custom agent-update source, Klever API override).

### 2026-06-18 (later)
- **Fix: "Update all agents" now uses your custom source on a fork**: The update banner/flow posted to the GitHub release endpoint with the dashboard's version tag, which doesn't exist as a release on a fork → HTTP 404 "release not found". Now, whenever an **Agent update URL** is set (Settings → Agents), the update-all flow downloads agent binaries from that source instead of GitHub. So the banner's "Update all agents" works end-to-end on a fork. (Hardening: the version label is validated before it's used in the on-disk binary path.)
- **Override the Klever API (use your own indexer)**: Settings → **Klever** now has a **Klever API URL** field. Leave it blank to use the official Klever indexer for the selected network, or set it to your own indexer's base URL (e.g. if you run your own indexer with different rate limits). The Validators page (blocks + validators) uses it. Applies **live** — no restart — and reverts to the official API when cleared.
- **Fix: custom agent download accepts a direct file URL**: The Agent update URL now also works as a direct file URL (e.g. `https://my.site/klever-agent-linux`), not only a base URL — earlier it always appended `klever-agent-<os>-<arch>` and 404'd. Three forms are supported: direct file URL (single platform), `{os}`/`{arch}` template, or a base URL ending in `/`.
- **Update agents from your own source (forks)**: The "Update Agents" GitHub flow can't work on a fork (no release assets). Settings → Agents now has an **Agent update URL** (and optional version) — host your agent builds there as `klever-agent-<os>-<arch>` (`.exe` for windows), then use the new **"Download from custom source"** button in the Agent Update dialog. The dashboard fetches the right binaries for your servers' platforms and you push them as usual — no more logging into each server. The URL is read from settings (not the request) and cross-host redirects are blocked.
- **Fix: election count over-counted at epoch boundaries**: The per-epoch count is now taken from each epoch's **block consensus group** (the elected set, straight from block data) instead of the validator list, which could still report the previous epoch's set for a moment after a boundary (an epoch with 1 elected validator could add 2 to the total). Each elected validator now gets exactly one +1 per epoch.
- **One Klever API call per poll for the timeline**: The block list is fetched with `limit=100` (the API's per-page max), so the whole 100-block window comes in a single request instead of two — fewer calls, less chance of rate-limiting. (Asking for more than 100 doesn't help; the API caps a page at 100.)
- **Elections chart: coloured, remembered tabs**: The Total / Individual tabs now highlight the active view (accent colour) and your choice is remembered across visits. Also fixed the chart rendering far too large on wide monitors.

### 2026-06-18
- **Generate a BLS validator key during provisioning**: The Provision modal has a new "Generate a fresh BLS validator key for each node" checkbox. When ticked, provisioning runs the Klever `keygenerator` and places a new `validatorKey.pem` into each node's config dir (before the permissions step, so the key is chown'd to the container user too). Works for single and batch provisioning — each node gets its own key. The capability already existed as a standalone step (`key.generate` on the node detail page); this wires it into the create flow. Left off, behavior is unchanged (import/generate the key afterwards).
- **Far fewer Klever API calls (batched blocks, no node API)**: The block-production timeline now fetches blocks in one batched `GET /v1.0/block/list` call (up to ~50/page) instead of one request per block, and reads the chain head + epoch from the newest block — so the separate `GET /node/overview` (the stricter node-API host that was returning 429s) is gone entirely. Steady state is ~1 block request per poll plus the validator list every 5th poll. This should resolve the intermittent rate-limit errors.
- **Fix: election count attributed to the wrong validator**: When several validators were elected in the same epoch, the per-month count could land on only one of them. The monitor only refreshed the validator set every ~5 polls, so on an epoch boundary it counted against a stale set. It now refreshes validator stats whenever the epoch changes, and **defers** counting an epoch until it has stats that actually reflect it (so a failed/rate-limited refresh on an epoch boundary is retried, not miscounted). Each elected validator now gets its own +1 per epoch.
- **Elections chart: line chart with Total / Individual tabs**: The "Elections per Month" chart is now a line chart with two switchable views — **Total** (one line, all your validators summed) and **Individual** (one coloured line per validator, with a legend).

### 2026-06-17
- **Fix: HTTP 429 (rate limiting) from the Klever API**: The validator monitor's client now paces requests (≥175ms apart), honors the server's `Retry-After` header, backs off longer (0.5→8s, up to 4 retries), and is gentler overall — lower concurrency (2), a smaller block-backfill burst (8/tick instead of 30), and an 8s default poll (was 6s). Poll cadence is overridable via the `klever_poll_secs` setting (floor 4s) if an endpoint is still strict. Note: running two dashboards from the same public IP (e.g. an old build alongside a new one) doubles the load on Klever's per-IP limit — run only one.
- **Validators page: slimmer "Elected (mo)" column**: it was an over-wide auto-sized column with a big empty gap on its left; it now shrinks to its content width.
- **Fix: managed validators wrongly shown OFF-CHAIN (and dropped from the timeline)**: The Klever validator-list API ignores the `pageSize` parameter and returns only 10 entries per page (controlled instead by `limit`). The monitor requested `pageSize=100` and stopped paging once a page held fewer than 100 entries — so it only ever saw the **first 10** validators (out of ~200). Any managed validator ranked outside the top 10 was reported off-chain — with no stats, no alerts, and no timeline row — and it flipped on/off as on-chain rankings shifted. Now uses `limit` and pages through the full list. Added a regression test for multi-page fetching.
- **Validators page: elections-per-month + chart, and 6 cards in one row**: New **Elected (mo)** column (first column, before the name) counting how many epochs each managed validator was elected this calendar month — it resets when a new month begins. The monthly totals are persisted as history, so a new **Elections per Month** bar chart shows the trend over time (builds up as epochs pass, ready to span years). History is exposed at `GET /api/validators/elections`. Also widened the validator summary cards so all six fit one row on wide monitors (falls back to the responsive grid on narrower screens).
- **Validator alerts (incl. Telegram)**: The validator monitor now feeds the alert engine. Each poll writes per-managed-validator metrics — `validator_missed_blocks` (missed signing this epoch), `validator_leader_misses`, and `validator_jailed` — into the metrics store, so they flow through the existing rule → notification → Telegram pipeline like any other metric. Two built-in rules ship enabled: **Validator Missed Blocks** (warning when >10 missed in an epoch; the count resets each epoch so it clears naturally and only re-fires on a genuinely bad epoch) and **Validator Jailed** (critical). Both are tunable on the Alerts page, and the three metrics are selectable when creating custom rules.
- **Single shared sidebar (fixes missing nav links)**: The sidebar was copy-pasted into every page template, so new links drifted out of sync — "Docker Cleanup" and "Validators" were missing from some pages. It's now defined once in `web/templates/partials/sidebar.html` and injected server-side (`servePage` replaces a `<!--#sidebar-->` marker), with the active link set client-side from the URL. One source of truth; every page shows the full nav.
- **Node log auto-refresh defaults to 5s** (was 10s).
- **Fork note — validator monitoring page kept**: Upstream reverted the validator monitoring page. This fork keeps it on purpose: the block-production timeline is genuinely useful for validator owners watching their own nodes. The feature (the **Validators** sidebar entry, the page, and the `internal/dashboard/klever` poller) is retained here and re-applied on top of every upstream sync.

### 2026-06-16 (later)
- **Restore Chain DB from a single node's detail page**: The chain-DB restore was only reachable from the overview's batch bar (after selecting nodes), which was easy to miss. There's now a **Chain Database** panel on each node's detail page — network selector, one button, live progress over WebSocket — so you can restore a single node directly. The batch variant on the overview stays for multiple nodes.
- **Validator timeline shows only elected validators**: Off-chain / non-elected validators don't produce blocks, so they no longer take up empty rows in the Block Production Timeline. The validator table above still lists them with their on-chain state.

### 2026-06-16
- **Validator monitoring page**: New "Validators" entry in the sidebar (under Overview) that brings back the old validator-overview dashboard, now built into NodeHub. It tracks, for the validators you manage here (matched to your nodes by BLS key), live block production on the Klever chain: stat cards (managed count, elected/jailed, total self-stake, total claimable allowance, blocks produced/missed this epoch, head block) and a per-validator **Block Production Timeline** of the last 100 blocks — green where the validator led the block, red where it was elected but missing from that block's consensus signer set, grey otherwise — wandering right-to-left as new blocks arrive. A background poller (`internal/dashboard/klever`) tails the Klever indexer/node API (`/node/overview`, `/v1.0/block/by-nonce`, `/v1.0/validator/list`), keeps a rolling block window, and refreshes authoritative per-epoch counts; it shares a small in-flight cap and backs off on 429/503 to respect Klever's per-IP rate limit. Defaults to mainnet; network and API base URLs are overridable via settings (`klever_network` / `klever_api_url` / `klever_node_url`). New endpoint `GET /api/validators`. Amounts use KLV's 6-decimal precision; commission is shown as a percentage.
- **Docker image cleanup tool**: Upgrading a node leaves the previous `kleverapp/klever-go` image behind, so old versions silently pile up and eat disk. New **Docker Cleanup** entry under the sidebar "Tools" section: pick a server, see every local Klever image with its tag(s), short ID, creation date and on-disk size, select the ones you don't need, and delete them in one go (with a confirm showing how much space you'll reclaim). Images still referenced by a container (running **or** stopped) are flagged "In use" and can't be selected, so you can't break a node by accident. The agent re-verifies on its side that each requested image is a klever-go image and not in use before deleting — the dashboard's checks are a convenience, not the security boundary. New agent actions `docker.images.list` / `docker.images.remove` (whitelisted), endpoints `GET /api/servers/{id}/images` and `POST /api/servers/{id}/images/remove`.
- **Fix: chain-DB restore left a gap by fast-bootstrapping**: After a restore the node was only restarted, so it kept its original `--start-in-epoch` flag and jumped to the latest epoch instead of resuming from the restored snapshot's height — leaving every block between the snapshot and now missing (confirmed in practice). The restore now **recreates** the container with `--start-in-epoch` removed (`RecreateWithoutStartInEpoch`: inspect → remove → recreate same config minus the flag → start), so the node resumes from the restored DB and syncs forward continuously. Docker only applies command args at create time, hence the recreate rather than a plain restart.

### 2026-06-15
- **Restore chain DB from the official Klever snapshot**: New "Restore DB" action (batch bar on the overview) that replaces a node's chain database with the official Klever FullNode snapshot (`kleverchain.<network>.latest.tar.gz`, tens of GB) — for when a node only has the latest epoch but you need the full archival DB (e.g. for an indexer). Per node: preflight free-disk check (refuses if the extracted DB wouldn't fit), stop container, rotate the old DB aside (`db.old`, kept for rollback if space allows, otherwise removed up front with a warning), stream-download straight through gzip+tar (never staging the archive on disk), extract **only** the `db/` subtree (a stray `config/` in the snapshot can't clobber live config), fix ownership to 999:999, start the container. Multiple nodes are processed **one at a time** to avoid saturating disk/bandwidth. Progress (preflight → downloading % → extracting → starting → done) streams live to the browser over WebSocket; the request is fire-and-forget so an hour-long restore doesn't hold an HTTP connection. New agent action `node.restore-db` (whitelisted), endpoint `POST /api/nodes/{id}/restore-db`. Long-running agent actions now get extended deadlines instead of the flat 60s command timeout (which would have killed provisioning and restores mid-flight).
- **Provisioning sync mode**: The Provision modal now offers a Sync Mode — **Fast bootstrap** (`--start-in-epoch`, the default), **Full DB snapshot** (downloads the FullNode archive before first start — an archival node in one step), or **Full sync from genesis** (no flag). The `--start-in-epoch` flag is now conditional instead of always-on, and `redundancy_level`/sync mode flow through to the container args.
- **No more alert spam when you stop nodes on purpose**: Stopping a node from the dashboard now marks it as in maintenance (a flag in node metadata), and the alert evaluator suppresses node-offline alerts for maintenance nodes. Cleared on start/restart, and self-healing — discovery clears it as soon as the node is seen running again (even if it was started outside the dashboard). Stopping two nodes for a quick operation no longer fires a wall of alerts.
- **Fresh nodes show "initializing", not "syncing"**: While a freshly started node initializes (network discovery, trie sync, consensus setup) its REST API isn't up yet, so no sync metrics are reported. A running node with no sync metric now shows an "initializing" badge instead of an empty status; once the API answers it shows syncing or synced as before. Signal-based, not a timer.
- **Fix: stale assets in a long-open tab (Service Worker)**: The PWA service worker cached CSS/JS cache-first under a fixed cache name, so a tab left open kept serving old styles after a deploy — new HTML collided with old CSS and broke layout until a hard refresh. CSS/JS/pages are now network-first (cache is only an offline fallback); only icons/fonts/manifest stay cache-first. Cache name bumped so old cache-first entries are dropped on activation.

### 2026-06-07
- **Provisioning overhaul (multi-node, role selection, live validation)**: The Provision Node modal could only create one node at a time and several things were broken or awkward. Now:
  - **Multi-node in one run**: a "Number of Nodes" field. At 1 it's the simple mask; above 1 it renders a table with one row per node — **Node Name | Role (Main/Fallback) | Port**. Nodes are provisioned sequentially with per-node progress and a final success/failure summary. A batch is deliberately all-Main or per-row-selectable, never a hidden mix.
  - **REST API port auto-increments**: enter the start port (8080), each subsequent node gets +1 (8081, 8082…). The node software handles its remaining ports itself.
  - **Main/Fallback is now actually settable and wired end-to-end**: `redundancy_level` was previously dropped on the floor — missing from `ProvisionRequest`, not forwarded by the handler, not read by the agent's `executeProvision`, not set on the container. Now it flows all the way to `--redundancy-level=N` on the validator container (0 = Main, flag omitted; 1 = Fallback).
  - **Live node-name validation**: names are checked against the agent's container-name regex as you type — the field turns red and Deploy is disabled on invalid or duplicate names, instead of the whole flow being rejected after it already started. The dashboard also validates server-side (clear 400) as defense in depth.
  - **Server dropdown shows the nickname**: it now uses `display_name || name || hostname` like the rest of the UI, instead of falling back to the hostname and forcing you to remember which host was which.
  - **Provision Node added to the sidebar** under "Add Server".
  - **Clicking outside the modal no longer discards it** — only the ✕ closes it, so a stray click doesn't wipe a half-filled form.
  - Test: `provision_test.go` covers rejection of invalid names, invalid redundancy level, and a well-formed request passing validation.

### 2026-06-04 (later)
- **Fix: RC of a higher X.Y.Z was hidden from the outdated-nodes pill**: v0.3.74 introduced a "stable beats RC" rule but applied it too broadly — `latestNodeTag` filtered the candidate pool down to stables whenever *any* stable existed on the track, which also dropped legitimate "next-release" RCs (e.g. `v1.7.19-rc1` while only `v1.7.18-0` was stable). Operators stopped seeing new RCs surface as "newer" in the header pill and had no install entry point. The filter is gone; `latestNodeTag` now picks the max via `KleverVersion.compare` directly, which already encodes the right semantics — same X.Y.Z prefers stable, different X.Y.Z prefers higher regardless of RC/stable. A node on `v1.7.18-0` with `v1.7.19-rc1` available is again flagged outdated; a node on `v1.7.18-0` with only `v1.7.18-rc6` "available" is correctly not flagged (no downgrade prompt).

### 2026-06-04
- **Check-for-updates also force-refreshes Docker tags**: The Docker Hub tag list was cached server-side for 15 min and browser-side for 5 min, so a freshly published Klever release could take up to 20 min to surface in the outdated-nodes pill. The header "Check for updates" button now bypasses both layers: new `force=1` query param on `GET /api/docker/tags` calls a new `TagCache.Invalidate()` which clears the in-memory cache; the frontend forwards the flag and re-runs the pill detection right after. Manual recheck is now end-to-end immediate — dashboard version, agent versions, Klever node tags all refresh in one click.
- **Klever-aware version comparison (fix: RC was flagged newer than stable)**: The outdated-nodes pill used the first non-`latest` tag returned by the Docker Hub API as "latest" — Docker Hub orders by push date, so a freshly pushed `v1.7.18-rc6` overtook the already-released `v1.7.18-0` and the pill recommended a downgrade to the RC. Replaced the heuristic with a Klever-aware comparator (new `web/static/js/version.js`): parses `vX.Y.Z[-N|-rcN]`, strips the optional `val-` prefix and `-g<hash>` git suffix, compares X.Y.Z numerically, and for equal X.Y.Z treats stable (`-N`) as higher precedence than any RC (`-rcN`). Iteration numbers break ties within each track. The pill target is now the highest **stable** on the track (RC fallback only if no stable exists yet). A node intentionally running an RC of a version newer than the latest stable is no longer flagged outdated — pushing it back to stable would be a downgrade. The version dropdowns in the Batch Upgrade modal and on the node detail page are also sorted by the same comparator so the visual order matches the actual precedence (stable above RC of the same X.Y.Z).

### 2026-06-01
- **Batch Upgrade modal: checkboxes instead of badges**: The "Selected nodes" list in the Batch Upgrade modal used to be a row of chip-pills with no way to deselect inside the modal — if you noticed a node shouldn't be upgraded, you had to close the modal and re-select in the main list. Now a row list (`batch-upgrade-row`) with checkbox, name, server and current version. Toggling updates the count live and disables the upgrade button at 0 selected. `executeBatchUpgrade` reads the modal-local `batchUpgradeSelected` set instead of the main-list selection, so changes inside the modal don't mutate the main selection. Visually aligned with the dashboard's other list patterns (border, hover, monospace version right-aligned). Old `.batch-upgrade-nodelist .node-chip` styles removed.

### 2026-05-21 (Agent hardening)
- **Security fix: dashboard CA is now actually verified (#58)**: The agent built its TLS config with `InsecureSkipVerify: true` — a real hole. mTLS authenticates the agent to the dashboard, not the other way around. With a stolen agent cert, anyone could MITM the dashboard connection and forge commands to the agent. The agent now verifies the dashboard cert against the CA it stored at registration (`Config.CACertPEM` was already there, just never used). `ServerName: "localhost"` pinned because `crypto.DashboardTLSConfig` issues the SAN that way regardless of the dial address. `MinVersion: TLS 1.3`. **Test** with two CAs proves: legitimate dashboard is accepted, imposter with different CA is rejected. **No re-registration needed** — existing agents already have `CACertPEM`.
- **Security fix: `.pem` writes blocked via `config.write` (#58)**: `config.write` may no longer write `.pem` files. Validator key rotation belongs on the explicit `key.import` path, not as a side effect of a config edit. Reads (`config.read`) remain allowed.
- **Security fix: `agent.info` raw log removed (#58)**: `log.Printf("agent info from %s: %s", ...)` dumped the entire message into dashboard logs. Removed — `handleAgentInfo` logs the relevant fields individually. Prophylactic against future sensitive fields.
- **Two data races in the agent fixed (#56)**:
  - **`Executor.OnProgress`**: Field was written by the read loop for the next command while a previous Execute goroutine could still read it. Classic struct-field race. Field removed, now a per-call parameter — no shared mutable state.
  - **`LogStreamManager.streams` map**: Writes from the caller and from the per-stream worker goroutine without a lock. Concurrent map writes panic at runtime in Go. `sync.Mutex` around all operations plus an `fnEq` guard so the worker, when exiting, only deletes its own map entry (not one written by a subsequent `StartStream`).
- **Heartbeat stalls eliminated: discovery on its own goroutine (#55)**: Until now Docker discovery ran on the same writer goroutine as heartbeats. Discovery can block up to 30 s on Docker inspect/stats — heartbeats don't go out during that window and the dashboard marks the agent offline even though it's healthy, just busy. Discovery now runs on its own goroutine (`runDiscoveryLoop`), results sent to the writer via a buffered channel.
- **WebSocket keepalive (#55)**: App-level ping every 25 s with a 10 s timeout. Detects half-open connections (NAT idle timeout, silent intermediary drop) that TCP alone misses. Plus: connections that held for more than 5 min reset the reconnect backoff to baseline — late disconnects no longer wait 60 s.
- **Tar traversal fix (#57)**: `extractTarGz` in the provisioner used `filepath.Clean` + `strings.Contains(name, "..")` to validate — misses edge cases. Now uses `filepath.Rel` between absolute paths: the target must land strictly inside the destination directory, otherwise skipped.
- **`IsPortAvailable` with timeout (#57)**: Used `Dial` without a timeout — on firewalled black-hole ports (SYN silently dropped) the call blocked indefinitely. `FindAvailablePort` calls it in a 100-port loop, a blocked port would have stalled the entire provisioning. `DialTimeout(500ms)` added.
- **`restart_container` field validated (#57)**: `config.write` payload optionally accepts `restart_container`. The field bypassed the `containerNamePattern` validation from `ValidateCommand`. Now checked against the same regex, can no longer smuggle in an unsafe name.
- **Public IP refresh (#57)**: Cached IP at agent start, never updated. On DHCP renewal or VPN flap the value stayed stale until agent restart. Now `atomic.Value` with its own refresher goroutine every 15 min, heartbeats read via a getter closure. Logs only on change.
- **Restart drain (#57)**: After `agent.update`/`agent.restart` the agent did `time.Sleep(500ms)` and then exec'd. Tight — under load the dashboard lost the command result. `drainBeforeRestart` now polls the result/progress channels (empty detection + 200ms settle, max 3 s) until they're empty — dashboard reliably sees the result before the re-exec happens.
- **Parallel node polling (#59)**: `CollectAll` iterated serially over all nodes — one unreachable node blocked the whole cycle for 5 s, with N down nodes the cycle ran N×5 s long. Now parallel via WaitGroup + mutex, each node has its own timeout. Real-timing test (5 nodes × 100 ms, 300 ms limit) proves parallelism. Slow-poll warning above `defaultNodePollTimeout`.
- **Discovery cadence 30 s → 60 s (#59)**: Discovery itself takes up to 30 s context timeout per cycle — 30 s cadence left no breathing room, back-to-back discovery starved other writes. 60 s is still plenty for node-list freshness and unloads the writer.

### 2026-05-21 (Docker self-update hardening, follow-up to #52)
- **`pullImage` now detects pull errors**: Docker `/images/create` always replies with HTTP 200, errors (auth, manifest not found, transport) come as `errorDetail` lines in the streaming NDJSON body. Previously we discarded the stream and only saw the error indirectly on the following `createContainer` call (with an unclear message). `parseImagePullStream` now parses each line and returns a clear `pull failed: <daemon-message>`.
- **Container ID now robust via `/proc/self/cgroup`**: `getSelfContainerID` reads the 64-char container ID from the cgroup path (works for cgroup v1 `/docker/<id>` and v2 `/system.slice/docker-<id>.scope`). Hostname stays as fallback, but is now checked (`looksLikeContainerID`) instead of accepted blindly — users with `--hostname my-server` get a clean error instead of a misleading 404 from `inspectContainer`.
- **Startup janitor removes stale finalize helpers**: On dashboard start, `SweepStaleFinalizeHelpers` scans for containers matching the name pattern `<self>-finalize-*` in non-running state and removes them. Bounded to siblings of the current container (via `inspectContainer(selfName)`). Best-effort, doesn't block startup. Closes the only loose end from the `AutoRemove=false` policy in PR #52: failed updates leave their helper logs for diagnosis, the next restart cleans up.
- **Tests**: `replaceImageTag` (incl. digest/registry-port edges), `parseImagePullStream` (success, errorDetail, error-field-only, mid-stream-fail, truncated-mid-object), `looksLikeContainerID`, `cgroupContainerIDRe` (cgroup v1 + v2 format examples), `isContainerID`.
- **Audit hardening**: `looksLikeContainerID` only accepts lowercase hex (Docker IDs are uniformly lowercase — prevents mixed-case hostname strings from passing as an ID), `parseImagePullStream` capped via `io.LimitReader` at 64 MB (defensive against a runaway daemon stream), `SweepStaleFinalizeHelpers` has a 30s deadline (hanging daemon calls would otherwise stall startup up to 5 min per helper), container filter in the janitor uses `json.Marshal` instead of `fmt %q` (correct JSON escaping regardless of container name).

### 2026-05-21
- **Fix: Docker self-update failed on port conflict**: The original flow tried to start the new container *before* the old one was stopped — Docker couldn't re-bind the host port (`9443`) and the start failed (`Bind for 0.0.0.0:9443 failed: port is already allocated`). Rollback ran, old container stayed on the old version, banner kept showing the update. Confirmed in production (CTJaeger#50 follow-up). The naive solution "stop old first, then start new" doesn't work: the goroutine orchestrating the update runs *inside* the old container and gets killed along with the stop-self — nobody starts the new container.
  - **New architecture**: short-lived sidecar container (finalize helper), started from the already-pulled new image with `--self-update-finalize <new_id> --replaces <old_id>`. Mounts only `/var/run/docker.sock`, no host ports. Flow: old pulls → rename old → create new (in "Created" state, port still free) → start helper → old stops itself. Helper survives the stop of the old container, waits until old is actually stopped, starts new (port is now free), removes old, removes itself.
  - **New CLI flags** (internal, not user-facing): `--self-update-finalize`, `--replaces`. Short-circuited before data dir / DB init in `cmd/dashboard/main.go`.
  - **Failure modes**: helper start fails → full rollback (rename old back, remove new). Helper fails *after* stop-self → old is stopped-but-renamed, new is created-but-not-started, helper stays visible (no AutoRemove). User can start manually: `docker start klever-node-hub`. Helper logs preserved for diagnosis.
  - **Hardening from code + security audit**: (a) per-poll timeout (5s) in `waitContainerStopped` so a wedged daemon doesn't blow past the 60s deadline, (b) `replaceImageTag` rejects digest pins (`@sha256:...`) and respects registry ports (`registry:5000/foo`), (c) single-flight mutex around `dockerSelfUpdate` — two parallel clicks now get 409 instead of stepping on each other, (d) `--self-update-finalize`/`--replaces` validate container IDs (12-64 hex, != self), (e) `stopContainer` errors on self-stop are logged instead of swallowed.

### 2026-05-14
- **Docker self-update surfaced in dashboard**: Update banner now shows the "Update Now" button in Docker mode too, provided `/var/run/docker.sock` is mounted — the backend flow had existed since 2026-03-25 but the UI hid it unconditionally. Without a socket mount the `docker pull` hint is shown (now with a note on how to enable one-click updates). Confirm dialog and reload delay (15s instead of 3s) adjusted for Docker mode because image pull + container recreate take longer than a binary restart. `GET /api/system/version` returns a new `docker_self_update_available` field. README Docker run example extended with socket mount + security note (root-equivalent control).
- **Restart agent from the dashboard**: New "Restart Agent" item in the server actions dropdown (`···` button) on the overview page. Sends an `agent.restart` command over the existing WebSocket connection; agent acknowledges, then re-execs via `syscall.Exec` (Unix) / `exec.Command`+exit (Windows). Systemd `Restart=always` (or Docker restart policy) brings the process back up and the WebSocket reconnects automatically. Uses the same `restartAgent()` path as the proven auto-restart after `agent.update`. Running Klever nodes are not affected — only the agent process is restarted. New endpoint `POST /api/agent/restart/{server_id}`, whitelist extended with `agent.restart`.
- **Reverse proxy setup guide**: New `docs/reverse-proxy.md` with step-by-step instructions for Apache, Nginx and Caddy incl. Let's Encrypt — so browsers can install the PWA (a self-signed cert blocks that otherwise). Important: reverse proxy belongs on a standalone host, agents connect directly to port 9443, bypassing the proxy (mTLS).
- **Version performance regression**: New RegressionDetector flags when a new node version is measurably slower. Compares the median of `klv_block_process_duration_ms` 24h before vs. after a version switch — earliest 12h after the switch, alarm only at +50% AND +30ms, then `evaluated` → exactly one warning per version switch, no spam. Migration 8: `node_version_history` table.
- **Passive performance report**: Node detail page shows a "Version Performance" card with the block-processing median before/after the last update as a +/-% value. No alarm, info only. New endpoint `GET /api/nodes/{id}/performance`.
- **Heartbeat timeout setting wired up**: The `heartbeat_timeout_sec` setting was stored but never read — two separate hardcoded 60s values ran instead. Now single source of truth: `SettingsStore.HeartbeatTimeout()` is used by the hub health check (live on every tick) and the agent-offline alert rule.
- **Overview search mode**: Typing in the search hides stat cards and the resources panel → server/node tables slide directly under the search field.
- **Global sidebar alert badge**: Red badge with the count of active alerts on every page, new "Tools" section with Batch Config and Slot Inspector.
- **Fix: duplicate "Synced"**: `top-shell-status` was being overwritten with "Synced HH:MM" — now only appears once on the right side of the header.
- **Fix: nonce written out fully**: Block nonce shown in full with thousands separators (`30,500,000`) instead of `30.5M` — in snapshot, status grid and sync text.

### 2026-04-01
- **Server hardware benchmark**: New "Benchmark" tab on the server detail page. Starts the official Klever benchmark tool in a Docker container. Tests disk I/O, network, CPU, memory, KV store with PASS/WARN/FAIL. Result shown as color-coded cards.

### 2026-03-28
- **Fix: nonce stall alert ineffective**: The 3x lookback was immediately overridden by clamping to the global 2-min window. Clamping removed — stall detection now actually looks back 6+ minutes.
- **Global alert badge in sidebar**: Red badge with the count of active alerts on the "Alerts" link — visible on EVERY page (Overview, Node, Server, Alerts, Settings). Pulses on critical alerts. Polls every 15s.
- **Config search field at 50% width**: Search field no longer takes up the full row.
- **PR #48 merged**: Flat/Grouped toggle persisted via localStorage.
- **PR #49 merged**: Masked credentials when editing notification channels (secrets never leave the backend unmasked).

### 2026-03-26
- **Fix: nonce stall alert didn't fire**: Lookback was only 2 minutes — at threshold 120s the last real nonce change was never found. Now 3x threshold (min. 5 min) as the lookback.
- **Fix: config save "Unknown error"**: Wrong API response parsing (API.request instead of API.put + JSON). Save and Save & Restart now work correctly.
- **Fix: config backups not visible**: Listing only showed version backups (folders), not editor backups (.bak files). Both types are now shown.
- **Log download cleaned up**: ANSI color codes and `[stdout]`/`[stderr]` prefixes are stripped on download.
- **Config search**: New search field in the config editor with match counter and forward/back navigation.

### 2026-03-25
- **Docker self-update**: Dashboard can now update itself inside a Docker container too, when `/var/run/docker.sock` is mounted. Flow: detect own container → pull new image → rename old container → create new → start → stop/remove old. Rollback on error.
- **Agent update modal redesign**: Server agents table + "Update All" button on top, release dropdown + manual selection under an "Expert Settings" separator.
- **Install script terminal hint**: Clear completion message after install — "You can safely close this terminal now."
- **Server nicknames**: New `display_name` field for servers (DB migration 7). Editable on the server detail page. Preferred everywhere in the UI. PATCH `/api/servers/{id}` endpoint.
- **Node rename**: Nodes can be renamed via the actions menu (···). PATCH `/api/nodes/{id}` endpoint.
- **Role column in node list**: New "Role" column shows Master (green) or Fallback (yellow) based on `redundancy_level`.
- **Grouped node view**: "Flat | Grouped" toggle above the node table. In grouped mode nodes are grouped by BLS key — master on top, fallbacks indented. Full table with all columns (Version, CPU, Memory, Actions).
- **Alert banner faster auto-dismiss**: Poll interval reduced from 30s to 15s.
- **Scrollbar matched to dark theme**: Thumb more visible (opacity 0.1 → 0.2), subtle track background.
- **Fingerprint / biometric login**: Button text "Sign in with Passkey" → "Sign in with Fingerprint / Passkey" with fingerprint icon. Setup text adjusted accordingly.
- **Quick Update All Agents**: Button in the agent update modal, shows update progress directly in the table (status column). Disabled when all agents are up to date.
- **Version shortened**: Docker image tags shown without git hash (`v1.7.16-0` instead of `v1.7.16-0-gcf9f612c`).
- **Fix: nonce stall false positives**: Threshold raised from 15s to 120s, DurationSec to 60s. Short pauses between epochs no longer trigger an alert. Existing rules migrated automatically.

### 2026-03-22
- **Batch upgrade progress bar**: Instead of a single batch request, nodes are now upgraded sequentially one by one, with a visual progress bar (current node, X/Y, percentage). Config updates have their own progress phase. Success/failure shown per node.
- **Version column in node list**: New "Version" column shows the Docker image tag (software version) of each node directly in the overview table. Hidden on narrow screens.
- **Fix: merge Klever metrics (nonce/sync) into node metadata**: `handleNodeMetrics` now also writes `klv_nonce`, `klv_is_syncing` etc. into the node metadata, so the overview table can display them. Previously they were only written to the MetricsStore (time series).
- **Fix: node metrics poller stops after reconnect**: `runAgentLoop` now uses a loop-specific context. Previously old and new poller goroutines shared the same top-level context → after reconnect the old poller wrote into a dead channel and the new one started with an empty node list.
- **Fix: discovery overwrote Klever metrics**: Discovery replaced the entire node metadata with only Docker stats, which wiped `klv_nonce`/`klv_is_syncing` from the metrics poller. Docker stats are now merged into existing metadata.
- **Debug: error logging for node-metrics container lookup**: Dashboard now logs explicitly when `GetByContainerID` fails for incoming node metrics. Helps diagnose why nodes show no metrics in the overview.
- **Fix: node metrics with identical container names on different servers**: Without server scope `GetByContainerID` always returned the first hit — with identical container names (e.g. `klever-node1` on server A and B) metrics from the wrong server landed on the wrong node. New `GetByContainerAndServer` with `WHERE container_name = ? AND server_id = ?`.

### 2026-03-13
- **Agent Update Modal Redesign** (v2):
  - "Available Versions": single dropdown + Download/Notes/Refresh (no table, no badges)
  - Downloaded versions show "(downloaded)" label in dropdown, button changes to "Ready"
  - "Server Agents": checkboxes per agent, offline agents greyed out + not selectable
  - "Select All" toggle (skips offline agents), "Update" applies to selected agents only
  - Headers left-aligned, clean minimal layout
  - UpdateStore now supports multiple versions per OS/arch (key: version/os/arch)
  - New endpoint: `POST /api/agent/download-release-auto` — smart download based on registered servers
  - `POST /api/agent/update/{server_id}` and `/all` now accept optional `{"version":"..."}` body
  - `GET /api/agent/binaries` returns `downloaded_versions` list
- **Fix: node search finds server names**: `server_name` is mapped onto node objects before the search so the DataTable search also filters by server name
- **Manual Update Check Button**: icon button next to refresh in header, forces immediate GitHub release re-check
- **Node Action Confirmations & Status Feedback**:
  - Confirm dialogs before Start/Stop/Restart/Delete actions (styled modal, not browser `confirm()`)
  - Delete uses red "Danger" button, Stop uses orange "Warning" button
  - Container status dot (green/grey/yellow) next to node name in overview table
  - Toast notifications: success/error/info feedback after every node action
  - Batch operations also use styled confirm dialogs + toast feedback
  - Added `badge-restarting` CSS style (yellow, pulsing dot)
- **Log viewer: auto-refresh instead of Docker timestamps**:
  - Docker timestamps checkbox removed (redundant)
  - New auto-refresh dropdown: Off / 5s / 10s / 30s (default: 10s)
  - Logs auto-reload when the tab is opened
- **Agent Update Improvements**: GitHub release integration, outdated highlighting, auto-rollback
  - `GET /api/agent/releases` — List GitHub releases with agent binary assets
  - `POST /api/agent/download-release` — One-click download of agent binary from GitHub (SSRF-protected)
  - Agent version highlighting: red for `dev`/unknown, orange for outdated, green for current
  - Auto-rollback: if `VerifyAndReplaceBinary` fails after backup, automatically restores previous binary
  - Changelog display: expandable release notes per version in Agent Update modal
  - `GET /api/agent/binaries` now includes `latest_release_version` from GitHub
- **Issue #35 — Self-Update for Dashboard**: Automatic version checking via GitHub Releases API + binary self-update
  - `internal/dashboard/version_checker.go` — Periodic GitHub release checker (30 min interval), semver comparison, asset finder
  - `internal/dashboard/version_checker_test.go` — Tests for isNewer, compareVersions, FindAsset
  - `internal/dashboard/handlers/system.go` — SystemHandler: GET /api/system/version (version info + update check), POST /api/system/update (download + SHA256 verify + replace + restart)
  - `internal/dashboard/handlers/restart_unix.go` — `syscall.Exec` for in-place restart (preserves PID, nohup, systemd)
  - `internal/dashboard/handlers/restart_windows.go` — `exec.Command` + `os.Exit` fallback for Windows
  - `cmd/dashboard/main.go` — VersionChecker + SystemHandler wiring, new routes
  - `web/templates/overview.html` — Update banner with Docker-aware rendering (update button vs docker pull hint), dismiss per version
  - `web/static/css/style.css` — Update banner styles
  - `deploy/` — systemd service files for dashboard and agent
- **Fix: Log viewer not working on node detail page** (multiple cascading bugs):
  - `internal/agent/docker.go` — Added `Tty` field to container config for TTY detection
  - `internal/agent/log_stream.go` — Added `parseRawLogStream()` for TTY containers (no multiplexed headers)
  - `internal/dashboard/ws/agent_handler.go` — Set WebSocket read limit to 1MB (was 32KB default, caused `StatusMessageTooBig`)
  - `web/templates/node.html` — Fixed `API.fetch()` → `API.get()`, proper error display on log load failure
- **Fix: ANSI escape codes rendered raw in logs** — Added `ansiToHtml()` parser converting ANSI color codes to HTML spans
- **Fix: Duplicate timestamps in log viewer** — Docker timestamps checkbox defaults to OFF
- **Fix: Batch upgrade tag dropdown showing `[object Object]`** — Extract `tag.name` from DockerTag objects in overview.html
- **Fix: DataTable search input focus loss** — Preserve focus and cursor position across re-renders in datatable.js
- **Fix: Dashboard version display** — Show current version (e.g. v0.3.2) in header next to page title
- **Fix: Duplicate alerts on restart** — Hydrate in-memory alert state from DB on startup + dedup check in fireAlert()
- **Notification "Send Test" button** — Test channel credentials inline without saving (POST /api/notifications/test-inline)
- **PWA support** — manifest.json, Service Worker (sw.js), icons, meta tags across all templates → installable on desktop/mobile
- **Web Push Notifications** — Real-time push alerts even when tab is closed
  - `internal/notify/vapid.go` — VAPID key generation/loading (P-256 ECDSA)
  - `internal/notify/webpush.go` — Full RFC 8291 encryption (ECDH + HKDF + AES-128-GCM) + VAPID auth (RFC 8292)
  - `internal/dashboard/handlers/push.go` — API: subscribe, unsubscribe, test, status, VAPID public key
  - `web/static/sw.js` — Push event handler + notification click (focus/open app)
  - `web/templates/settings.html` — Push notification toggle + test button in Notifications tab
  - `cmd/dashboard/main.go` — VAPID key persistence, WebPushChannel wiring, push routes

### 2026-03-12
- **Docker Hub**: Automated multi-arch Docker image builds (linux/amd64, linux/arm64) in release workflow
  - Dashboard: `ctjaeger/klever-node-hub`
  - Agent: `ctjaeger/klever-agent`
  - Dockerfiles updated with version ldflags via build args
- **Klever Extension Login fix**: Correct signature verification using Klever's signed message format
  - Extension uses: `0x17 + "Klever Signed Message:\n" + len + message → Keccak-256 → Ed25519`
  - Replaced incorrect raw/SHA-256 verify with `kleverSignedMessageHash()` + Keccak-256 (`x/crypto/sha3`)
- **Wallet linking security**: Linking now requires Challenge-Response proof of ownership (new `POST /api/setup/klever/challenge` endpoint)
- **README**: Added CI badge, Go version badge, MIT license badge
- **CI fix**: Fixed goimports formatting in klever.go and recovery.go (const/var alignment)
- **Issue #31 — Password Login (Phase 1)**: Dashboard unusable via IP address (WebAuthn requires domain)
  - `internal/auth/argon2.go` — Extracted shared Argon2id helpers (HashArgon2id, VerifyArgon2id)
  - `internal/auth/password.go` — PasswordManager with Argon2id hashing, min 8 chars, SetPassword/Verify/HasPassword
  - `internal/auth/ratelimit.go` — In-memory sliding window rate limiter (5 attempts per 15 min per IP)
  - `internal/auth/recovery.go` — Refactored to use shared Argon2id helpers (no behavior change)
  - `internal/dashboard/handlers/auth.go` — New handlers: POST /api/setup/password, POST /api/auth/password, PUT /api/auth/password
  - `cmd/dashboard/main.go` — PasswordManager + RateLimiter wiring, persistence callback, new routes
  - `web/templates/login.html` — Setup wizard: Dashboard Name → Password → Optional Passkey → Recovery Codes → Notifications
  - `web/static/js/login.js` — Password-first login flow, passkey conditional, skip button, toggleRecovery
  - `web/templates/settings.html` — Password change UI in Security tab
  - 10 new tests (password, rate limiter, handler tests)
- **Issue #31 — Klever Extension Login (Phase 2)**: Challenge-response auth via Klever browser wallet
  - `internal/auth/klever.go` — KleverAuthManager: bech32 address decoding, challenge nonce generation (5 min TTL), Ed25519 signature verification
  - `internal/auth/klever_test.go` — 10 tests (address validation, challenge/verify, full sign/verify with real Ed25519 keypair, challenge consumed)
  - `internal/dashboard/handlers/auth.go` — New handlers: GET /api/auth/klever/challenge, POST /api/auth/klever/verify, POST /api/setup/klever, DELETE /api/auth/klever, GET /api/auth/klever
  - `cmd/dashboard/main.go` — KleverAuthManager wiring + persistence callback + routes
  - `web/static/js/klever.js` — Klever Extension client (initialize, getAddress, signMessage challenge-response flow)
  - `web/templates/login.html` — "Sign in with Klever Wallet" button (conditional: extension + address registered)
  - `web/static/js/login.js` — loginKlever() function, Klever button visibility based on setup status
  - `web/templates/settings.html` — Klever Wallet section in Security tab (link/unlink/detect from extension)
- **README overhaul**: Complete rewrite with accurate tech stack, CLI flags, installation guide, deploy instructions
- **Dockerfile fix**: Updated Go version from 1.22 to 1.26 in both `Dockerfile` and `Dockerfile.agent`

- **Issue #30**: Pagination and filtering for data tables
  - `web/static/js/datatable.js` — reusable DataTable class (no dependencies)
  - Client-side pagination (10/25/50/100 per page), global text search with debounce, column dropdown filters
  - Page size persisted in localStorage, page navigation with Prev/Next and numbered buttons
  - `renderHeader`/`renderFooter` support for HTML table wrapping
  - Overview: servers rendered via DataTable with status filter and search across name/hostname/IP/region/agent
  - Overview: agent binaries and server agent version tables use DataTable
  - `window._dt` global registry for onclick handlers in innerHTML context

- **Issue #29**: Multi-channel notification credentials and per-channel alert routing
  - `ChannelFilter` struct with severity and alert type filtering
  - `Manager.AddChannelWithFilter()`, `UpdateChannelFilter()`, `ChannelsWithFilters()` — per-channel filter support
  - `Manager.Send()` now respects channel filters (empty filter = all alerts, backward-compatible)
  - `Alert.AlertType` field for routing (node_down, nonce_stall, resource, metric, resolved)
  - `alertTypeFromRule()` derives alert type from rule's metric name in evaluator
  - `namedChannel` wrapper for multiple instances of same channel type
  - `HandleUpdateChannel` (PUT) for filter/credential updates, new `notify_ch_{name}` storage format
  - Settings UI: Notifications tab with channel management (add/edit/delete/test, severity + alert type filter checkboxes)
  - Backward-compatible: legacy `notify_channel_{type}` configs still load, channels with no filter receive all alerts
  - 6 new tests for filter matching, filtered send, filter update, channels-with-filters

- **Issue #28**: Server public IP and region detection
  - Agent: `ipdetect.go` — detects public IP via `api.ipify.org` (with fallbacks to `ifconfig.me`, `icanhazip.com`)
  - Agent sends public IP in `agent.info` (on connect) and `agent.heartbeat` (periodic refresh)
  - Dashboard: `geoip.go` — resolves IP to region via `ip-api.com` with in-memory cache
  - Migration 4: adds `public_ip` and `region` columns to `servers` table
  - `ServerStore.UpdatePublicIP()` for targeted IP/region updates
  - Agent handler processes IP on connect and on heartbeat (only updates on change)
  - Overview UI shows public IP (fallback to private IP) and region on server cards
  - Tests: IP detection (success, fallback, all-fail), GeoIP resolver (success, city-only, fail, cache, empty), server store

- **Issue #26**: Settings page UI and dashboard configuration API
  - `SettingsHandler`: GET/PUT /api/settings (grouped by category), GET/PUT /api/settings/{key}, POST /api/settings/reset
  - Settings categories: general (dashboard name), metrics (intervals, retention), notifications (severity filter), agents (heartbeat timeout, discovery interval)
  - Default values for all settings, key validation (rejects unknown keys)
  - Settings page (`settings.html`): tabbed interface (General, Metrics, Notifications, Agents), save per section, reset to defaults
  - First-run setup wizard: dashboard name step, passkey registration, recovery codes, optional notification channel (Telegram/webhook)
  - API client: added `put()` method
  - Settings link in sidebar navigation
  - 8 unit tests (get all, update, unknown key rejection, get/update single, reset defaults, invalid category)

- **Issue #25**: Agent auto-update mechanism
  - `updater.go`: SHA-256 checksum verification, binary backup + atomic replacement (Windows fallback), rollback support
  - `UpdateStore`: Binary storage on disk with JSON index, store/get/list by OS/arch, persistence across restarts
  - `UpdateHandler`: Upload binary (multipart, 100MB limit), list binaries, update single agent, update all agents, latest version endpoint
  - Agent executor: `agent.update` command receives base64-encoded binary via WebSocket, verifies + replaces
  - Whitelist: `agent.update` command registered (no container required)
  - Dashboard API routes: POST upload, GET binaries, GET version, POST update/{server_id}, POST update/all
  - Agent Update UI panel: upload form (version/os/arch/file), binary list table, server agent version list with per-server update buttons, update-all button
  - 14 unit tests (SHA256Hex, UpdateStore CRUD/persistence/overwrite/checksum, ParseOSArch)

- **Issue #24**: Alert rules engine with configurable thresholds
  - Migration 3: `alert_rules` and `alerts` tables with indexes
  - `AlertStore` with full CRUD for rules and alert records (create, update, delete, list, query)
  - `Evaluator`: periodic rule evaluation engine (15s interval, configurable)
  - Alert state machine: Normal → Pending → Firing → Resolved
  - Duration-based rules: threshold must breach for configured seconds before firing
  - Cooldown period prevents notification spam (per-rule configurable)
  - Recovery notifications when alerts resolve
  - 7 built-in default rules: nonce stall, node offline, high CPU, high memory, disk full, low peers, sync lag
  - System metrics evaluation (CPU/memory/disk per server) and node metrics evaluation
  - Heartbeat stale detection for agent offline alerts
  - Stall detection for nonce and heartbeat metrics
  - Integration with notification manager from Issue #23
  - Dashboard API: GET active alerts, GET history, GET/POST/DELETE rules, POST acknowledge
  - Alert banner on overview page (active alerts with severity colors)
  - Alert history panel with acknowledge button
  - Alert rules configuration UI (add/edit/delete, built-in rules editable)
  - 22 unit tests (evaluator: threshold, resolve, pending→firing, system metrics, heartbeat stale, defaults, start/stop; store: CRUD, enabled filter, ack, count, active queries)

- **Issue #23**: Notification system — Telegram, Pushover, and webhook channels
  - `Channel` interface with `Send`, `Validate`, `Name` methods
  - `TelegramChannel`: Bot API, Markdown formatting, rate limiting (20 msg/min)
  - `PushoverChannel`: Priority mapping (critical=emergency, warning=high, info=normal)
  - `WebhookChannel`: Configurable URL/headers, retry with exponential backoff (3 attempts)
  - `Manager`: Fan-out to all enabled channels, test endpoint, in-memory history (500 entries)
  - Dashboard API: CRUD channels, test send, history
  - Channel config persisted in settings store, auto-loaded on startup
  - 15 unit tests (manager ops, fan-out, partial failure, history, validation, webhook send/retry)

- **Issue #22**: Validator key management — generate, import, export
  - Key generation via klever-go keygenerator Docker entrypoint
  - Import/export with PEM format validation (BLS public key extraction)
  - Auto-backup before key operations, timestamped backups in `config/key-backups/`
  - 6 executor commands: `key.info`, `key.generate`, `key.import`, `key.export`, `key.backup`, `key.backups`
  - Dashboard API: GET key info, POST generate, POST import, GET export, GET backups
  - Key management UI: generate/import/export buttons, key info display on node detail page
  - 10 unit tests (get info, import, invalid PEM, backup on import, export, backup/list)

- **Issue #21**: Real-time log streaming from node containers
  - `FetchLogs`: Docker API log reader with multiplexed stream parsing (stdout/stderr)
  - `StreamLogs`: Live log follow with context cancellation and 30-min timeout
  - `LogStreamManager`: Manages active streams (one per container, auto-cleanup)
  - `node.logs` executor command with tail and since parameters
  - Dashboard API: `GET /api/nodes/{id}/logs?tail=100&since=<timestamp>`
  - Log viewer UI: terminal-style display, log level highlighting (ERROR/WARN/INFO/DEBUG)
  - Text search filter, timestamp toggle, line count selector, auto-scroll
  - Download logs as text file
  - 5 unit tests (timestamp parsing, Docker stream parsing, empty stream, manager lifecycle)

- **Issue #20**: Remote node configuration management (read/write/diff)
  - Agent-side config ops: `ListConfigFiles`, `ReadConfigFile`, `WriteConfigFile`, `BackupConfigFile`, `RestoreConfigBackup`
  - Path traversal prevention, allowed extension whitelist (.toml, .json, .pem, .yaml, .yml, .cfg)
  - Auto-backup before every write, timestamped backup files in `config/backups/`
  - 6 new executor commands: `config.list`, `config.read`, `config.write`, `config.backup`, `config.backups`, `config.restore`
  - Dashboard API: GET/PUT config files, GET backups, POST restore, POST multi-push
  - Config editor UI on node detail page: file selector, textarea editor, Save & Restart, backup/restore
  - Multi-node config push: `POST /api/config/push` with optional container restart
  - 12 unit tests (list, read, write with backup, traversal prevention, extension validation, restore)

- **Issue #19**: Complete upgrade/downgrade flow with progress tracking
  - `UpgradeContainerWithRollback`: 6-step upgrade with health verification and automatic rollback
  - Progress callback (`UpgradeProgress`) reports each step: snapshot, pulling, stopping, removing, creating, verifying
  - Executor uses rollback-aware upgrade (replaces plain `UpgradeContainer`)
  - Batch upgrade: `POST /api/nodes/batch/upgrade` — sequential upgrade to maintain quorum
  - Node detail UI: version selector dropdown, upgrade/downgrade buttons, progress bar
  - Added `node.provision` to command whitelist
  - 5 new tests: success with progress, create-fail rollback, no-progress, total steps, rollback helper

- **Issue #18**: Node provisioning wizard — install Klever node from scratch
  - Multi-step `Provisioner` (7 steps): preflight, pull, dirs, config, container, start, verify
  - Progress reporting, cleanup on failure, `node.provision` executor command
  - Dashboard handler `POST /api/nodes/provision`, UI wizard with live progress bar
  - Config download from official Klever backup endpoints (mainnet/testnet)

- **Issue #17**: Metrics dashboard UI — charts, gauges, and historical graphs
  - Custom lightweight charting module (`charts.js`) — SVG ring gauges, Canvas time-series, sparklines
  - Overview page: CPU/Memory/Disk gauges per server, node status breakdown (running/stopped/syncing)
  - Node detail page: status header (nonce, epoch, peers, consensus), sync progress bar
  - 6 time-series charts: block nonce, peers, transactions, network I/O, CPU, memory
  - Time range selector (1h, 6h, 24h, 7d, 30d), charts auto-resize on window resize
  - Auto-refresh every 15s, WebSocket push for real-time updates
  - Responsive layout: charts stack vertically on mobile, 2-column grid on desktop
  - No external dependencies, all embedded via Go `embed.FS`

- **Issue #16**: Metrics storage — hot/cold tables with retention and decimation
  - Migration 2: `metrics_recent`, `metrics_archive`, `system_metrics` tables with indexes
  - `MetricsStore` with batch insert, query (recent/archive/auto-resolution), decimation, purge
  - `Scheduler` with 3 background jobs: decimation (1h), archive purge (24h), system cleanup (6h)
  - WebSocket agent handler persists `node.metrics` and heartbeat system metrics to DB
  - Metrics query API: `GET /api/nodes/{id}/metrics`, `GET /api/servers/{id}/metrics`
  - Auto-resolution: recent queries use hot table, older use archive, spans merge both
  - 10 unit tests for store operations

### 2026-03-11
- **Issue #15**: Klever node metrics polling from `/node/status` endpoint
  - New `NodeMetricsCollector` polls each discovered node's REST API
  - Parses all 76+ metrics from `/node/status` JSON response into `map[string]any`
  - Configurable poll interval (default 15s) and HTTP timeout (5s)
  - Nonce stall detection: alerts when `klv_nonce` stops incrementing (configurable threshold)
  - `node.metrics` and `node.nonce_stall` WebSocket events
  - Auto-updates node list from discovery reports
  - `RunPoller()` background goroutine for continuous polling
  - 15 unit tests with mock HTTP server (success, errors, stall detection, serialization)
  - `NodeMetricsEvent` and `NodeNonceStallEvent` models
  - Integrated into agent main loop with dedicated channels
  - Fixed pre-existing lint issue in `webauthn.go` (unchecked `rand.Read`)

- **Issue #14**: Agent system metrics collection (CPU, memory, disk, load average)
  - New `MetricsCollector` with `/proc` parsing for Linux, graceful fallback for macOS/Windows
  - CPU% via delta between two `/proc/stat` samples
  - Memory from `/proc/meminfo` (MemTotal, MemAvailable)
  - Disk via `syscall.Statfs` (build tags: unix vs windows)
  - Load average from `/proc/loadavg`
  - Metrics attached to heartbeat payload (`HeartbeatPayload.Metrics`)
  - `SystemMetrics` model with all fields
  - 8 unit tests including mock `/proc` data tests (skip on non-Linux)

- **Issue #12/#13 completion**: Implement missing acceptance criteria
  - Added `HandlePasskeyFinishRegister` and `HandlePasskeyFinishLogin` to complete WebAuthn ceremony
  - Added `POST /api/agent/register` endpoint — validates token, creates server, issues mTLS certificate
  - Implemented `registerWithDashboard()` HTTP client in agent (replaces placeholder)
  - Agent now saves `KeyPEM` from registration response
  - CA initialization in dashboard main (load or create, encrypted private key storage)
  - Passkey credential persistence via `onCredentialsChanged` callback
  - Added `RegistrationResponse.KeyPEM` field for agent private key delivery
  - 5 new registration handler tests (success, invalid token, single-use, invalid body, generate token)

- **Lint fixes**: Resolve all 15 staticcheck issues
  - Migrated `nhooyr.io/websocket` → `github.com/coder/websocket` (SA1019 deprecated)
  - Merged `if ctx.Err() != nil { break }` into `for ctx.Err() == nil` loop condition (QF1006)
  - Removed ineffective `break` in `select` case (SA4011)
  - `docker_test.go` nolint directive already had `staticcheck` (QF1002 — no change needed)

- **Issue #13**: Wire agent main() — lifecycle, WebSocket, and command execution
  - CLI flags: `--config-dir`, `--dashboard-url`, `--register-token`, `--docker-socket`
  - Config load/save with registration flow
  - WebSocket connection to dashboard with auto-reconnect (exponential backoff)
  - Message pump: read commands, execute via Executor, send results back
  - Heartbeat every 30s, auto-discovery every 5 min
  - Graceful shutdown on SIGINT/SIGTERM
  - Added `Agent.Config()` getter

- **Issue #12**: Wire dashboard main() — connect all Phase 1 components
  - CLI flags: `--addr`, `--data-dir` (default `~/.klever-node-hub/`)
  - Full dependency chain: DB → stores → auth → hub → handlers → routes
  - JWT signing key persisted in settings store (auto-generated on first run)
  - WebAuthn + recovery codes loaded from settings store
  - Initial recovery codes printed to console on first run
  - Auth middleware on all protected API routes
  - WebSocket agent handler (`/ws/agent`) with message dispatch
  - Discovery report processing: creates/updates nodes in DB
  - Graceful shutdown on SIGINT/SIGTERM
  - Added `nhooyr.io/websocket` dependency for WebSocket support

- **Lint Fix**: Fixed all 53 golangci-lint issues (50 errcheck, 2 staticcheck, 1 unused)
  - `internal/store/`: Checked rows.Close, tx.Rollback, json.Unmarshal, db.Close returns
  - `internal/agent/`: Checked resp.Body.Close, io.Copy, StopContainer; replaced loop with append spread
  - `internal/crypto/mtls_test.go`: Checked all deferred Close, Serve, Write, Fprintf calls
  - `internal/dashboard/`: Checked SetupRoutes, w.Write returns
  - `internal/dashboard/handlers/nodes.go`: Removed unused `nodeActionRequest` type

- **CI Fix**: Updated Go version from 1.22 to 1.26 in CI workflow (matching project go.mod 1.25+)
  - Removed `-race` flag (requires CGO, we use CGO_ENABLED=0)
  - Added explicit `CGO_ENABLED=0` for cross-compilation builds
  - Fixes lint, security scan, and test job failures

- **Issue #10**: Docker operations — pull image, create/remove containers, upgrade/downgrade
  - Docker image pull via Engine API with progress streaming
  - Container creation with validated params (matching KleverNodeManagement script)
  - Container removal with graceful stop
  - Upgrade/downgrade flow: inspect → pull → remove → create → start
  - Docker Hub tag listing with 15-min cache (filters dev/testnet/alpine tags)
  - Dashboard API: POST upgrade/downgrade, GET /api/docker/tags
  - Port auto-assignment, data directory management
  - Extended command whitelist: create, remove, upgrade, pull, discovery
  - 25+ new tests (container ops, validation, upgrade, config parsing)

- **Issue #9**: Web UI shell — login, overview, and basic node list
  - Embedded HTTPS server with auto-generated self-signed cert
  - Security headers (CSP, X-Frame-Options, HSTS, XSS-Protection)
  - Mobile-first responsive CSS framework (dark theme, 768px/1200px breakpoints)
  - Login page: Passkey authentication, recovery code fallback, first-run setup
  - Overview page: server/node cards, status badges, add-server flow
  - Node detail page: status, actions (start/stop/restart), info display
  - Frontend JS: API client with JWT auto-refresh, WebSocket client, Passkey helpers
  - Auth API handlers: setup status, passkey begin/finish, recovery login, refresh, logout
  - Server/node API handlers: list, get, filter by server
  - Registration token API handler
  - 18 new tests (server, auth handler, server handler)

- **Issue #8**: Basic node operations — start, stop, restart via dashboard
  - Command whitelist with container name validation (injection prevention)
  - Docker operations: start, stop (graceful 30s), restart via Engine API
  - Command executor/dispatcher with timeout handling (60s default)
  - WebSocket hub: SendCommand with pending tracking, timeout, result matching
  - Dashboard API handlers: POST start/stop/restart + batch operations
  - End-to-end flow: API → WebSocket → Agent → Docker → result back
  - 30+ new tests (whitelist, executor, hub commands, handler HTTP tests)

- **Issue #7**: Agent auto-discovery — scan existing Klever nodes on server
  - Docker Engine API client via Unix socket (no CLI dependency)
  - Node discovery: list containers, extract params (port, display name, redundancy, image tag, data dir)
  - BLS public key extraction from `validatorKey.pem`
  - Discovery report message type for WebSocket communication
  - 19 tests (mock Docker socket, parsing, BLS extraction, edge cases)

- **Issue #6**: Agent registration — one-time token, certificate issuance, WebSocket connection
  - WebSocket message envelope and payload types
  - Connection hub for tracking active agent connections
  - One-time token manager for secure registration
  - Agent config persistence and registration flow
  - Install script for automated Linux deployment (systemd)

### 2026-03-10
- **Issue #5**: SQLite store with models and migrations
- **Issue #4**: Auth module — JWT, recovery codes, WebAuthn, middleware
- **Issue #3**: Crypto module — Ed25519, AES-256-GCM, mTLS, CA management
- **Issue #2**: Project scaffolding — Go module, directory structure, Makefile, Dockerfiles
