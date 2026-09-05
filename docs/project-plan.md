# Vector / wire-pod / Hermes project plan

Status: implementation started, 2026-09-05. Physical robot validation remains pending.

## Operating model

- This workstation is the development and build host. Reviewed work is committed to local `main`; the AshMartian fork is the publication remote and `public` is upstream. Temporary integration branches or worktrees are permitted for isolated work, with one coordinator integrating onto local `main`.
- `ash@escapepod.local` is production. Deploy explicit, tested commit artifacts; never develop there or update production by pulling a moving branch.
- Hermes and llama-swap run on this workstation initially. The bridge must support moving Hermes to a different host by configuration.
- Each physical robot is mapped by ESN to its own Hermes profile, persistent identity, memories, sessions, and tool credentials. Friendly names can change without changing that mapping.
- An initial pilot uses one Vector. Two-robot operation and long-term autonomy follow separate acceptance gates.

## Verified baseline and corrections

| Area | Evidence | Consequence |
| --- | --- | --- |
| Source | Local `main` and `public/main` are `347c45f7a4dba9adba7fa003c08248d301f19393`; clean working tree before this plan. Remote fork `main` is `84b98e17b7f24e67fafe907df6edf7e3d5b9e416`. | Record/reconcile fork ancestry before publication. Matching upstream is not evidence of a successful build. |
| Pi | Raspberry Pi 3 Model B Rev 1.2, Debian 13, aarch64, about 905 MiB usable RAM; no Docker or wire-pod service found. | Keep inference off the Pi. Budget camera buffering, STT, and concurrent robot sessions. |
| Access | SSH and passwordless `sudo -n true` verified after the user's sudoers update. | Production bootstrap/deployment can proceed through the existing SSH account. |
| Wi-Fi | NetworkManager reports onboard `wlan0` AP support=yes and MT7601U USB `wlan1` AP support=no. Current management connection uses `wlan0`. | Candidate topology: USB Wi-Fi uplink, onboard Wi-Fi robot AP. Establish and verify the new management route before switching onboard Wi-Fi. |
| Hermes | Inspectable Ash installation: `/home/ash/.hermes/hermes-agent`, version 0.21.0, commit `9dd6634c5635321cf38840cc30e9b51226689128`. Live Hermes processes run as `sophie`, whose runtime configuration is inaccessible to this account. | Establish the intended service ownership and live version before installation. The Ash checkout supports architecture research but does not prove the live runtime version. |
| Inference | `GET http://127.0.0.1:1234/v1/models` lists `Qwen3.8-27B-UD-Q4_K_M`. | Exact model ID is known; successful inference, vision, structured tools, and latency still require probes. Loopback is valid on the Hermes host only. |
| Existing defaults | Ash's personal Hermes configuration selects another provider/model; Sophie's configuration was not inspected. | Configure Vector profiles explicitly; preserve personal profiles. Audit auxiliary vision/summarization routes too. |
| Vision assets | The Qwen llama-swap entry in `/mnt/TeeBee/llms/config.yaml` includes an existing `mmproj-F16.gguf` projector. | Vision is configured, but requires a real image-inference test. Shared GPU use and model unloading require cold-start measurements. |
| Health | `/ok` exists on the robot-facing listener and the shared SDK/web mux, but is only a connection check. | Add separate local liveness/readiness and installed-commit checks; connection liveness alone does not prove STT and robot service readiness. |
| State | `WIREPOD_DATA_DIR` is handled by the container entrypoint, while core code uses relative paths. | Native deployment needs an explicit state-path solution; setting this variable alone does not relocate state. |

## Incremental delegation and delivery gates

Research has been delegated into three independent assignments: upstream patch assessment, installed Hermes discovery, and robot bridge capability assessment. The coordinator owns production/network design and this plan. Future implementation assignments start only after their prerequisites are resolved; each returns a small diff, validation evidence, and limitations for coordinator integration.

### 0. Baseline, build, and release contract

Owner: coordinator with a bounded build/deployment assignment.

1. Record upstream/fork commit ancestry and the unmodified build/test baseline. Preserve attribution and distinguish inherited failures from new failures.
2. Establish a repeatable ARM64 build on this workstation. Use a pinned container toolchain if useful; the Pi runtime can still be native systemd. Go/CGO, libopus, Vosk, and target libc compatibility require a real artifact execution test. Existing Docker recipes are starting material, not a validated release pipeline.
3. Define an artifact manifest with full source SHA, build inputs, target architecture, checksum, and release version. Build from a committed tree and test the same artifact promoted to production.
4. Add local verification, release, deploy, rollback, and smoke-test commands. Production promotion is an explicit action separate from a commit or push.
5. Prefer a native service invoking the built binary directly, with a dedicated user and only necessary privileges. The current `start.sh` requires root and may compile missing binaries; it is not the intended production launcher.
6. Use immutable release directories and separately backed-up writable state. Include enrollment records, credentials/certificates, configuration, custom intents, models, and chat data. Resolve all relative paths and directory permissions before claiming non-root support.
7. Treat rollback of executable code and rollback of state as separate operations. Snapshot state before migration, document backward compatibility, and demonstrate restore without re-pairing robots.

Exit gate: repeatable ARM64 artifact build; staged binary runs on the Pi; readiness identifies missing setup/dependencies correctly; rollback procedure verified. No model downloads or source builds in normal service startup.

### 1. Import requested upstream improvements

Owner: one upstream integration assignment at a time; separate attributed import and follow-up fixes where practical.

| PR | Import strategy | Required validation |
| --- | --- | --- |
| [#516](https://github.com/kercre123/wire-pod/pull/516) action-tag parsing | Import authored commit `f55afa87756095f6598ef760aca81bcdadfab0ac` with provenance. Patch applies to baseline. | Malformed, partial, unknown, and valid commands; streaming boundaries; ordinary speech containing command-like words; valid actions still execute exactly once. |
| [#495](https://github.com/kercre123/wire-pod/pull/495) remote Whisper | Adapt feature commit `d3d5898ec923584def50b2879e14fb888aca71da`. Preserve current language/vocabulary prompt support from #524. Exclude the reverted speech-inactivity experiment. | Multipart contract, language/prompt retention, optional authentication as supported by server, timeouts/cancellation, invalid responses, endpoint down. Verify with an actual transcription server. |
| [#508](https://github.com/kercre123/wire-pod/pull/508) cliff stream | Import only feature tip `d2c7f4a25cd65c98ace35878526ae87bc615e18b`, an additive 89-line change in two files. Its patch applies despite the large stale PR comparison. | Correct aggregate flag semantics; no fabricated raw readings; stream synchronization/cancellation; repeated begin/stop; disconnect/reconnect; two-robot isolation. |
| [#517](https://github.com/kercre123/wire-pod/pull/517) UI | Import authored commit `e3cc3dbfb98ef6800b05ecd5d4c1ad580395af22`. Relevant assets match the current PR merge tip; patch applies. | Setup/settings, both robot selectors, camera, mobile pointer/touch cancellation, browser focus loss, keyboard/manual control, stop/release behavior, visual layouts. |

#508 currently repeats one aggregate `CLIFF_DETECTED` flag into an array and supplies zero raw readings. Expose aggregate detection honestly and mark unavailable per-sensor data as unavailable. This feature is telemetry, not a verified collision/cliff safety controller. Its stream captures mutable robot slice indices, lacks synchronization, and cannot reliably cancel blocked reads; fix these before concurrent use.

#517 changes motor-control JavaScript as well as styling. Fix handling of a rejected control request, release while a pointer is still held, and keyboard motion during tab hiding. Its GitHub `UNSTABLE` metadata was not accompanied by observed failing checks. Vendor needed remote UI assets or demonstrate useful operation without them when WAN is unavailable.

#495 needs explicit endpoint/model/credential handling: normalize the `/v1/audio/transcriptions` path, avoid forwarding a cloud API key to an unrelated custom host, and handle HTTP/status/JSON errors. #516's broad text scrubbing must not remove legitimate command-like words from ordinary speech.

Sequence small parser/STT work first. Cliff-stream and UI control changes depend on a shared control/stream ownership design; they must not independently introduce competing event consumers. A baseline production release may precede completion of all four PRs so regressions are measurable.

Exit gate: focused regression tests plus build verification for each import; hardware-dependent checks recorded as pending until performed. Publish the fork and release tags after local integration is validated. Upstream contributions use focused branches based on `public/main`, containing only the relevant contribution.

### 2. Production network and offline operation

Owner: coordinator; Pi changes serialized with deployment work.

1. Validate USB station-mode connectivity and stable SSH through it. Prefer an Ethernet recovery path when available. Stage rollback for network changes before changing the current management interface.
2. Configure onboard Wi-Fi as the dedicated 2.4 GHz Vector AP, with a deliberate SSID, WPA2 credentials, subnet, DHCP, local name resolution, and explicit robot/admin/Hermes access rules. One stable robot SSID with optional uplink is the default; clarify if two simultaneous broadcast SSIDs are actually required.
3. Bind discovery to intended interfaces. Existing mDNS code obtains an address using an outbound route to `8.8.8.8:80`; it can advertise the uplink address or return `0.0.0.0` without a route. Fix and test advertisement on the AP and LAN instead of assuming default-route selection works.
4. Use stable addressing for the workstation/Hermes endpoint. The Pi must call a reachable bridge address, never `127.0.0.1:1234`; keep llama-swap local to Hermes where possible.
5. Verify robot SDK connections from the Pi into the AP subnet, discovery on relevant interfaces, cold boot without WAN, and reconnection after uplink loss.

Define offline behavior explicitly:

- **Internet unavailable, workstation reachable:** local Hermes, Qwen, STT/TTS can work only if all required models/assets and auxiliary providers are local. Test with WAN blocked.
- **Workstation/Hermes unavailable:** Pi offers a clear unavailable response or an explicitly configured lightweight local STT/basic-intent fallback. Stop agent control and allow native behavior. A second STT backend is an implementation choice, not an existing automatic failover feature.
- **Pi isolated with robots only:** the robot AP and local wire-pod remain usable; full Qwen intelligence is unavailable unless the inference host also has a route to that network.

Exit gate: both robots reconnect after Pi reboot, remain reachable without WAN, and do not lose management access during the network transition.

### 3. Hermes profiles and a read-only bridge

Owners: bridge and Hermes-plugin assignments, after agreeing a versioned contract.

Suggested boundary:

```text
Vector A/B <-> wire-pod on Pi <-> authenticated Hermes connector on workstation
                                      |-> profile for ESN A
                                      |-> profile for ESN B
                                             |
                                       local llama-swap :1234/v1
```

1. Establish which OS account/service will own the robot agents, then create fresh Vector Hermes homes/profiles with separate memory, conversation storage, identity files, and credentials, preferably separate gateway processes initially. Share read-only model weights and plugin source. Profiles isolate application state, not arbitrary filesystem access; do not clone personal profiles. Learning initially means persistent experiences/preferences and reviewed skill evolution; it does not imply weight training or uncontrolled code changes.
2. Explicitly set the provider base URL to `http://127.0.0.1:1234/v1` and model ID to `Qwen3.8-27B-UD-Q4_K_M` in each Vector profile. Probe text, structured tool calls, image processing, context limits, and cold/warm latency before choosing turn limits. Configure local auxiliary providers or report unsupported capabilities.
3. Use a native Hermes plugin (`plugin.yaml`, `register(ctx)`, `ctx.register_tool`) for robot tools, profile-scoped `ctx.state` for cursors, and the existing `/v1/responses` API with named conversations for incoming robot turns. Use existing signed webhook routes for coalesced low-rate context events. Confirm session continuation and actual tool execution in the selected live version. Defer a native platform adapter until speech delivery and interruption handling demonstrate a need for one.
4. Keep plugin source, non-secret profile templates, schemas, and a pinned compatibility manifest in the fork. Install a versioned plugin on Hermes with a health/capability handshake; expose configuration and compatibility status in wire-pod. Do not turn robot-facing configuration into arbitrary remote shell execution.
5. Start with inventory, battery/status, aggregate cliff/proximity data actually available, camera snapshots, and voice-request transcripts. Continuous ambient microphone capture on stock Vector 1.0 remains a firmware-specific research gate; speaker playback and voice request audio are different capabilities.
6. Every envelope includes schema version, robot ESN, event/request ID, timestamp, expiry, and correlation ID. Authenticate peers; bind credentials to allowed robots/capabilities; reject cross-ESN requests. Use bounded queues, cancellation, duplicate suppression, and stale-event expiry.
7. Establish initial inbound robot audio at the voice request/STT boundary. Keep per-robot turn state and interruption/cancellation isolated. Do not replay old speech or movement requests after a reconnect.
8. Restrict Vector profiles to robot-specific tools and explicitly chosen memory tools initially. Verify the actual runtime tool schema and handler authorization: this Hermes version can auto-enable newly discovered plugin toolsets, so checking a saved YAML allowlist alone is insufficient. Test default shell/filesystem/network tools and inherited plugins are not accidentally available. Treat camera text and speech as observations, not authority to alter tools or credentials.

Keep a separate robot-gateway API on the Pi, with inventory/state/events, snapshot, control lease, typed action, and stop operations under a versioned namespace such as `/bridge/v1`. This is distinct from Hermes's existing agent API. The current legacy SDK/control handlers have no authentication and share web listeners; protect those access paths too and route their commands through the same control manager. A protected Hermes endpoint alone does not close the legacy bypass.

Exit gate: one robot observation reaches its isolated agent, an actual local model turn returns, no personal profile state changes, and a second robot cannot access its sessions/memory/tools. Restart Hermes and verify identity and conversational continuity.

### 4. Speech, expression, and bounded physical control

Owners: control manager and Hermes tool assignments; coordinator runs supervised hardware checks.

1. Implement one per-ESN control manager shared by UI, voice actions, and Hermes. Track grants and losses throughout the BehaviorControl stream; use leases, cancel outstanding actions on takeover, and serialize conflicting actions.
2. Add speech and verified stationary animation/head/lift tools first; some animations move the wheels and need restrictions too. Add finite distance/angle drive/turn actions only after control ownership and stop behavior are measured. Use `DEFAULT` behavior priority and preserve mandatory firmware reactions. Cancel identified high-level actions as well as stopping low-level motors; releasing control allows native behavior to resume and does not mean the robot stays motionless.
3. Run watchdogs and duration limits on the Pi, independent of model latency. Reject movement with expired control or stale state, and stop on lease expiry, manual takeover, or a detected fault. If the Pi-to-robot connection is already lost, a stop command may not arrive: validate the robot's own action timeout/disconnect behavior before claiming a guaranteed stop.
4. Give a person an immediate stop/manual takeover control. Test browser close, touch release/cancel, focus loss, interrupted speech, robot pickup, low battery, and reconnect behavior in a contained ground-level test area.
5. Use one model request queue initially because two profiles share inference resources. Measure simultaneous voice interactions, model swapping/cold loads, speech latency, memory usage, dropped events, and whether camera work starves commands.

Exit gate: one robot reliably observes, speaks, and completes short actions; manual takeover works; disconnect tests leave no replayed movements. Repeat with both robots concurrently before expanding autonomy.

### 5. Ongoing experience and autonomous sessions

1. Persist per-robot episodic summaries, stable preferences, identity changes, and approved skills with timestamps and provenance. Provide inspect/export/reset controls and restore tests.
2. Keep cross-robot sharing explicit. Record shared experiences separately from private memory. Give agents bounded scheduled observation/reflection sessions with energy and inference budgets.
3. Default to snapshots and short-lived audio/image buffers. Define opt-in recording and retention, redact credentials from logs, and make sensing status visible.
4. Expand exploration only after hardware evidence supports perception and stopping requirements. Camera images and an aggregate cliff flag do not establish mapping or reliable autonomous navigation.

Exit gate: identities remain independent across restarts, simultaneous use, and backups/restores; supervised sessions produce useful retained experiences without unbounded sensing or action loops.

## Review and release evidence

Each increment records: source commit, artifact checksum, checks run and outcomes, hardware/firmware used, open limitations, and rollback target. Local automated tests cover parsers, endpoint contracts, session routing, queue/lease lifecycles, and state migrations. Fake robot interfaces enable repeatable disconnect, cancellation, and two-robot tests; only real robot checks establish physical behavior.

The first useful release is a reproducible Pi installation with both robots paired and baseline voice/control working. The first Hermes release is a one-robot, profile-isolated observation/speech pilot. These are separate milestones; the first release does not require completing autonomous navigation.

## Inputs needed at their implementation gates

- Robot ESNs, firmware versions, current activation/pairing state, and availability for supervised tests.
- AP SSID/password and confirmation whether "dual SSID" means uplink plus robot AP or two broadcast AP networks. Store passwords outside Git.
- Privileged Pi installation path resolved: the user enabled unattended sudo for Ash and it was verified.
- Actual local STT endpoint and speech output choice. The provided llama-swap language-model endpoint does not establish a transcription service.
- Intended ownership/service entry point for live Hermes under `sophie`, or a separately configured robot runtime under an accessible service account.
- Successful vision inference with the configured projector and explicit local auxiliary provider routes.

## Evidence sources

- Local wire-pod code: `chipper/pkg/initwirepod/startserver.go`, `chipper/pkg/vars/vars.go`, `chipper/pkg/wirepod/config-ws/webserver.go`, `chipper/pkg/mdnshandler/mdns.go`, `chipper/pkg/wirepod/sdkapp/`, `docker/entrypoint.sh`, `chipper/start.sh`.
- Live read-only SSH: Pi model/OS, NetworkManager device capabilities, USB driver, interface routes, service inventory, and sudo availability. No network/service configuration was changed during research.
- Requested PR links and feature commits in the import table; patch applicability checked against the baseline without applying changes.
- [Linux MT7601U driver](https://github.com/torvalds/linux/blob/v6.18/drivers/net/wireless/mediatek/mt7601u/init.c), corroborating the adapter's station-only interface declaration; actual Pi NetworkManager reports AP=no.
- [Hermes upstream](https://github.com/NousResearch/hermes-agent) and installed source at the pinned commit; llama-swap model inventory is discovery evidence, not an inference or vision acceptance test.
- [Hermes profiles](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/profiles.md), [plugin API](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/developer-guide/plugins/index.md), [agent HTTP API](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/api-server.md), and [webhooks](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/messaging/webhooks.md). Implementation must pin these contracts to the selected live runtime.
- [Official Vector robot/audio implementation](https://github.com/anki/vector-python-sdk/blob/master/anki_vector/robot.py) and [behavior priorities](https://github.com/anki/vector-python-sdk/blob/master/anki_vector/messaging/behavior.proto). Protocol definitions do not replace firmware-specific physical verification.
