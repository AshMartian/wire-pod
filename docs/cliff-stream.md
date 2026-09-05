# Cliff status API

This imports the endpoint feature from [upstream PR #508](https://github.com/kercre123/wire-pod/pull/508), commit `d2c7f4a25cd65c98ace35878526ae87bc615e18b`, with separate lifecycle and data-contract corrections.

Each stream subscribes to the selected Vector's `robot_state` events. The supported SDK exposes the aggregate `ROBOT_STATUS_CLIFF_DETECTED` status bit, not raw readings from its four physical sensors. This API does not stop motors or implement a control watchdog.

## Requests

The existing SDK `serial=ESN` selection and these upstream paths are retained:

| Request | Result |
| --- | --- |
| `/api-sdk/begin_cliff_stream?serial=ESN` | `done`; starts one subscription per ESN, or leaves its existing subscription running |
| `/api-sdk/get_cliff_status?serial=ESN` | JSON snapshot when subscribed; otherwise `error: must start cliff stream` |
| `/api-sdk/stop_cliff_stream?serial=ESN` | `done`; cancels the subscription and discards its cached sample |

As with the existing SDK API, these paths accept GET or form POST requests. The legacy plain-text responses retain HTTP 200; consumers must inspect the response body. An empty serial on cached read/stop returns `error: must provide serial`. Stopping an already stopped stream is harmless and does not establish an SDK connection.

`begin` acknowledges starting the subscription, not receipt of a usable sample. Poll `get` and require `valid: true` before consuming the status. Continued status polling refreshes the existing SDK inactivity timer; if no SDK requests arrive for its five-minute timeout, robot removal also cancels the stream. Connection/subscription failures end the stream; a subsequent `begin` can retry.

## Aggregate snapshot

```json
{
  "any_detected": false,
  "status": 1056512,
  "stamp_ms": 1778543227694,
  "sample_age_ms": 35,
  "valid": true,
  "source": "aggregate_robot_status"
}
```

`status` is the last received robot status bitmask; `any_detected` decodes its aggregate cliff bit. `stamp_ms` is the server's Unix-millisecond receipt time, not a timestamp supplied by robot firmware. `sample_age_ms` uses elapsed time at the gateway. Samples become invalid after two seconds without a new robot-state event. This reporting threshold is not a safe stopping-distance guarantee.

Before the first sample, `valid` is false, `stamp_ms` is zero, and `sample_age_ms` is -1. A stale snapshot retains its previous data with `valid: false`. In either case, `any_detected: false` **does not mean a clear surface**. Consumers must treat absent, failed, or stale streams as unknown.

The upstream PR's `sensors[4]` and `detected[4]` fields are intentionally omitted: they contained invented zeros and copies of the same aggregate bit. Consumers must use the documented aggregate fields; per-corner cliff readings are unavailable.

## Lifecycle and verification

Subscriptions and samples live in a synchronized registry keyed by normalized ESN. They never capture an index into the mutable SDK robot slice. Stop and robot removal cancel the gRPC context, including a receiver waiting for its next event. Each subscription owns its cleanup; a retiring subscription cannot overwrite or remove a replacement for the same ESN. SDK robot contexts now have cancellation functions so a begin request racing with removal cannot keep a detached robot's subscription alive.

Focused tests cover two independent ESNs, repeated starts, cancellation while receiving, receive/connect failures, stop/start replacement, cancellation inherited from robot removal, absent/stale samples, concurrent operations, and the HTTP payload/stop path. Run:

```bash
cd chipper
go test -race -tags nolibopusfile ./pkg/wirepod/sdkapp
```

Hardware acceptance is still required: verify fresh timestamps and clear/detected transitions on each Vector, remove/reconnect one robot while polling the other, and stop/restart subscriptions repeatedly. Perform any physical test while supporting the robot; this telemetry endpoint alone does not provide an automatic cliff stop. The existing SDK robot registry and its other stream/timer paths remain outside this change's concurrency guarantees.
