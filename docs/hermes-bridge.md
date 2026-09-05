# Hermes bridge pilot

The first Hermes integration is intentionally read-only. wire-pod exposes a token-authenticated inventory and per-ESN enrollment status at `/bridge/v1/`; the native Hermes plugin exposes only `vector_status`. This verifies profile binding, local networking, and tool execution before speech, camera, or control actions are added.

## Provisioning

1. Generate an independent random token for each enrolled ESN in a privileged local shell, then add the ESN-to-token JSON map as `WIREPOD_HERMES_BRIDGE_TOKENS` in `/etc/wire-pod/wire-pod.env` on the Pi. Restart wire-pod and verify unauthenticated requests return 401 while each authenticated `GET /bridge/v1/robots` returns only that token's ESN and activation fields. A token for Vector A must get 403 for Vector B's status URL.
2. Create a fresh Hermes home/profile for one enrolled ESN. Do not clone a personal profile. Install or symlink `integrations/hermes/wirepod/` as that profile's `plugins/wirepod/` directory.
3. Configure `plugins.entries.wirepod.settings` with the reachable Pi `bridge_url`, the same token, and that profile's single `vector_esn`. The Pi must never be configured with `127.0.0.1:1234`; only the Hermes host calls its local llama-swap endpoint.
4. Start the profile with explicit local Qwen and auxiliary-model settings, then inspect its actual tool schema. It must expose `vector_status` and only the intentionally enabled toolsets.

The live Hermes processes currently run as a different OS account from this checkout. Provision a separate accessible service account or establish the owner's configuration/version before changing that live service. One Hermes profile is created only after its Vector has enrolled and its ESN is known.

## Contract

`GET /bridge/v1/robots` returns the build SHA and only the token-bound robot record. `GET /bridge/v1/robots/{ESN}/status` returns only that enrolled ESN and activation flag; a valid token for another ESN receives 403. Both require `Authorization: Bearer <token>`, reject non-GET methods, emit `Cache-Control: no-store`, and do not return robot IP addresses or GUIDs. `activated` is durable enrollment state, not proof of a live robot connection.

The plugin keeps the ESN in trusted profile configuration. The model cannot choose another robot or endpoint. It rejects redirects, applies a 1-15 second socket-inactivity timeout, limits the post-header response body to 64 KiB and its own elapsed deadline, and returns structured errors instead of raising exceptions. As with Python's standard HTTP client, a peer that continually sends partial response headers can defer completion; the bridge should therefore remain on the trusted local network.

## Validation

```bash
cd integrations/hermes/wirepod
python3 -m unittest discover -s tests -v
```

The Go contract tests are part of the normal chipper test command. Real acceptance requires one robot to enroll, the configured profile to return its matching ESN, an incorrect token/ESN to fail, and a profile restart to preserve its own identity without accessing the other robot's state.
