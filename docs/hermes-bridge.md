# Hermes × Vector bridge

WirePod is the local boundary between each physical Vector and its own Hermes profile. It is deliberately per-ESN: a token and a webhook target can operate on one enrolled robot only. The fleet console at `/hermes.html` is an operator overview; it never receives bridge credentials.

## Flow

```text
Vector event stream → WirePod (Pi) → signed Hermes webhook → isolated Hermes profile
                                             ↓
Vector SDK ← bounded bridge command ← profile-bound Hermes tool
Vector knowledge-graph request → profile-scoped Hermes API → Vector speech
```

Vector face events are coalesced for fifteen seconds per face/type before they reach Hermes. The event payload contains only a Vector ESN, event type, face ID, optional user-assigned face name, expression, and timestamp. Camera frames, landmark geometry, robot IPs, and SDK GUIDs never leave the Pi through this contract.

Supported events are `wirepod.face_observed`, `wirepod.face_recognized`, and `wirepod.face_identity_updated`. A recognised name comes from Vector's enrolled-face store; Hermes must never infer a person's identity from an unknown face.

## Provisioning

1. Generate independent random values for every enrolled ESN. In `/etc/wire-pod/wire-pod.env` on the Pi, set `WIREPOD_HERMES_BRIDGE_TOKENS` to a JSON map of ESN to 32+-character bearer token.
2. Create an isolated Hermes profile for each ESN. Configure the local model endpoint on the Hermes host (not the Pi), then install `integrations/hermes/wirepod/` as its `plugins/wirepod/` directory and enable the plugin.
3. Enable the Hermes webhook platform for each profile on a distinct local-network port. Subscribe a route that accepts the three `wirepod.*` events and uses a distinct 32+-character secret. The Pi must be able to reach the Hermes host's port.
4. Add `WIREPOD_HERMES_WEBHOOKS` to the Pi environment as a JSON map. Each value has the exact Hermes route URL and matching secret:

   ```json
   {"00603f9b":{"url":"http://hermes-host.example:8644/webhooks/vector-events","secret":"replace-with-a-random-32-character-minimum-secret"}}
   ```

5. Restart `wire-pod.service`. WirePod signs each body with Hermes generic HMAC V2 headers: `X-Webhook-Timestamp` and `X-Webhook-Signature-V2 = HMAC-SHA256(secret, timestamp + "." + body)`. It sets an event name and unique delivery ID in the GitHub-compatible headers Hermes already understands. No redirect is followed; delivery has a three-second deadline and a bounded queue. A saturated queue drops coalesced events rather than blocking the robot stream.

## Daily conversations and knowledge graph

To make an ordinary Vector voice request continue the correct agent rather than a shared anonymous chat, enable the Hermes API server for each profile and add `WIREPOD_HERMES_CONVERSATIONS` on the Pi. Its values are profile-specific API targets, keys, and model names:

```json
{"00603f9b":{"url":"http://hermes-host.example:8650","key":"replace-with-a-random-32-character-minimum-api-key","model":"vector-n8a4"}}
```

WirePod transcribes the knowledge-graph request, posts only that text to the matching profile, and speaks the final answer. Each request carries a stable `X-Hermes-Session-Key` (`wirepod:vector:<esn>`) plus a daily `X-Hermes-Session-Id`. The day turns over at 04:00 local time, matching the configured Hermes profile reset; consented durable facts remain explicitly profile-scoped. A failed Hermes response does not fall back to another Vector or a public model.

## Bridge API

All bridge routes require `Authorization: Bearer <profile token>`, apply `Cache-Control: no-store`, and restrict the token to its own ESN. A cross-ESN request receives `403`; robot network addresses and GUIDs are never returned.

| Route | Purpose |
| --- | --- |
| `GET /bridge/v1/robots` | Durable enrollment state for the profile's Vector. |
| `GET /bridge/v1/robots/{ESN}/status` | Same per-Vector enrollment record. Activation is not a connectivity guarantee. |
| `GET /bridge/v1/robots/{ESN}/observation` | Live battery/charger reading and enrolled-face roster. The roster is not a recognition event. |
| `POST /bridge/v1/robots/{ESN}/commands` | Bounded embodiment command. |

The command body is one of:

```json
{"action":"say","text":"1 to 280 characters"}
{"action":"drive","left_wheel_mmps":100,"right_wheel_mmps":100,"duration_ms":500}
{"action":"head","speed_rad_per_sec":1,"duration_ms":300}
{"action":"lift","speed_rad_per_sec":1,"duration_ms":300}
{"action":"stop"}
```

Wheel speeds are limited to ±200 mm/s; head/lift speeds to ±2 rad/s; moving commands run from 50–2000 ms and receive their stop before the default-priority Vector behavior-control lease is released. Commands are serialized per WirePod process, so a newer action cannot cancel another axis's safety stop. These are wheel controls, not autonomous navigation: named locations need an explicit map/pose safety contract before they are exposed.

## Identity and memory

An agent starts with a separate Hermes home, database, plugin credential and SOUL prompt. Do not clone one Vector's memory into another. Let a profile choose a name and develop a character over genuine interaction; treat face labels and personal memories as consent-sensitive, editable records. Webhook prompts should ask the agent to observe first and only make a small embodied acknowledgement when context makes it appropriate.

## Full-charge autonomy and nighttime reflection

Optional full-charge autonomy is a Pi-owned safety monitor, not an LLM polling loop. `WIREPOD_HERMES_AUTONOMY` is a per-ESN JSON map; every enabled robot has a poll interval of at least 60 seconds, a stable period of at least five minutes, and a daytime window. Production uses 08:00–21:59 local time. During the 22:00–07:59 night window the monitor neither emits readiness events nor initiates movement.

When a Vector has reported `BATTERY_LEVEL_FULL`, charging, and on its charger continuously for the stable period, WirePod emits one signed `wirepod.autonomy_ready` event. The event expires after one minute and contains only compact battery/charger state. Hermes must independently re-read `vector_observe` before using `vector_undock`; the bridge repeats the exact-full, charging, and on-charger check immediately before the native SDK operation. `vector_scan` is stationary `LookAroundInPlace` plus face search. Neither tool provides free-roam, map navigation, or a return-to-charger guarantee.

Nighttime is reserved for the profiles' existing daily reflection jobs. Those jobs use low reasoning, preserve only consented durable memory, and must not move or speak through a robot. Keep the Hermes cron toolset limited to `wirepod` and `no_mcp` rather than inheriting broad host capabilities. Camera frames remain local until a separate authenticated snapshot/vision contract is enabled; raw images and base64 data never go into autonomy webhooks.

## Validation

```bash
python3 -m unittest discover -s integrations/hermes/wirepod/tests -v
hermes plugins doctor --ci integrations/hermes/wirepod
docker run --rm -v "$PWD:/src:ro" -w /src/chipper wire-pod-toolchain:go1.22.4 \
  go test -race -count=1 -tags nolibopusfile ./pkg/wirepod/bridge ./pkg/wirepod/sdkapp
```

Live acceptance needs each profile to observe only its own Vector; short speech and drive commands to work and stop; a signed webhook test to receive `202`; an invalid signature to receive `401`; and a real face event to produce one profile-scoped Hermes run without exposing camera imagery.

## Local Whisper transcription

Production artifacts include both the Vosk fallback and the remote OpenAI-compatible Whisper client. The root-owned WirePod environment selects the latter with `STT_SERVICE=whisper`; set `STT_HOST`, `STT_MODEL`, `STT_KEY`, `STT_TIMEOUT`, and `STT_LANGUAGE` there. The launcher rejects an unknown STT service rather than silently changing speech recognition behavior. Do not enable it until its local endpoint has completed an authenticated real-audio transcription and the Pi can reach it.
