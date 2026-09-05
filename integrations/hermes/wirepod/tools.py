"""Profile-bound command and observation client for the wire-pod bridge."""

from __future__ import annotations

import json
from http.client import HTTPException
import time
from dataclasses import dataclass
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener


@dataclass(frozen=True)
class BridgeConfig:
    url: str
    token: str
    esn: str
    timeout_seconds: int

    @classmethod
    def from_settings(cls, settings: dict[str, Any]) -> "BridgeConfig":
        url = str(settings.get("bridge_url", "")).strip().rstrip("/")
        token = str(settings.get("bridge_token", "")).strip()
        esn = str(settings.get("vector_esn", "")).strip()
        try:
            timeout = int(settings.get("timeout_seconds", 5))
        except (TypeError, ValueError):
            timeout = 5
        parsed = urlparse(url)
        if parsed.scheme not in {"http", "https"} or not parsed.netloc:
            raise ValueError("bridge_url must be an absolute HTTP(S) URL")
        if not token:
            raise ValueError("bridge_token is not configured")
        if not esn:
            raise ValueError("vector_esn is not configured")
        if not 1 <= timeout <= 15:
            raise ValueError("timeout_seconds must be between 1 and 15")
        return cls(url=url, token=token, esn=esn, timeout_seconds=timeout)


class NoRedirect(HTTPRedirectHandler):
    """Avoid forwarding the bearer token to another origin."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[override]
        del req, fp, code, msg, headers, newurl
        return None


def vector_status(args: dict[str, Any], config: BridgeConfig) -> str:
    """Return a structured result and never allow model-supplied routing."""
    del args
    url = f"{config.url}/bridge/v1/robots/{quote(config.esn, safe='')}/status"
    payload = _request_json("GET", url, config)
    if payload is None:
        return json.dumps({"ok": False, "error": "wire-pod bridge is unavailable"})
    if not isinstance(payload.get("robot"), dict):
        return json.dumps({"ok": False, "error": "wire-pod bridge returned an invalid response"})
    robot = payload["robot"]
    if str(robot.get("esn", "")).casefold() != config.esn.casefold():
        return json.dumps({"ok": False, "error": "bridge returned a different robot"})
    return json.dumps({"ok": True, "source_sha": payload.get("source_sha"), "robot": robot})


def vector_observe(args: dict[str, Any], config: BridgeConfig) -> str:
    """Return a live, profile-scoped observation without accepting a model route."""
    del args
    url = f"{config.url}/bridge/v1/robots/{quote(config.esn, safe='')}/observation"
    payload = _request_json("GET", url, config)
    if payload is None:
        return json.dumps({"ok": False, "error": "wire-pod bridge is unavailable"})
    observation = payload.get("observation")
    if not isinstance(observation, dict):
        return json.dumps({"ok": False, "error": "wire-pod bridge returned an invalid observation"})
    return json.dumps({"ok": True, "source_sha": payload.get("source_sha"), "observation": observation})


def vector_command(args: dict[str, Any], config: BridgeConfig, action: str) -> str:
    """Send only a whitelisted, schema-validated command to the profile's Vector."""
    command = dict(args)
    command["action"] = action
    if not _valid_command(command):
        return json.dumps({"ok": False, "error": "invalid bounded Vector command"})
    url = f"{config.url}/bridge/v1/robots/{quote(config.esn, safe='')}/commands"
    payload = _request_json("POST", url, config, command)
    if payload is None:
        return json.dumps({"ok": False, "error": "wire-pod bridge is unavailable"})
    result = payload.get("result")
    if not isinstance(result, dict) or result.get("action") != action:
        return json.dumps({"ok": False, "error": "wire-pod bridge returned an invalid command result"})
    return json.dumps({"ok": True, "source_sha": payload.get("source_sha"), "result": result})


def _valid_command(command: dict[str, Any]) -> bool:
    action = command.get("action")
    if action == "say":
        text = command.get("text")
        return isinstance(text, str) and 1 <= len(text.strip()) <= 280 and set(command) == {"action", "text"}
    if action == "drive":
        keys = {"action", "left_wheel_mmps", "right_wheel_mmps", "duration_ms"}
        return set(command) == keys and all(isinstance(command[key], int) and not isinstance(command[key], bool) for key in keys - {"action"}) and -200 <= command["left_wheel_mmps"] <= 200 and -200 <= command["right_wheel_mmps"] <= 200 and 50 <= command["duration_ms"] <= 2000
    if action in {"head", "lift"}:
        keys = {"action", "speed_rad_per_sec", "duration_ms"}
        return set(command) == keys and all(isinstance(command[key], int) and not isinstance(command[key], bool) for key in keys - {"action"}) and -2 <= command["speed_rad_per_sec"] <= 2 and 50 <= command["duration_ms"] <= 2000
    return action == "stop" and set(command) == {"action"}


def _request_json(method: str, url: str, config: BridgeConfig, body: dict[str, Any] | None = None) -> dict[str, Any] | None:
    encoded = json.dumps(body, separators=(",", ":")).encode() if body is not None else None
    request = Request(url, data=encoded, method=method, headers={"Accept": "application/json", "Authorization": f"Bearer {config.token}", "Content-Type": "application/json"})
    deadline = time.monotonic() + config.timeout_seconds
    try:
        with build_opener(NoRedirect()).open(request, timeout=_remaining_timeout(deadline)) as response:
            if response.status not in {200, 202}:
                raise ValueError("bridge returned an unexpected status")
            payload = json.loads(_read_bounded(response, deadline))
    except HTTPError as error:
        try:
            return None
        finally:
            error.close()
    except (HTTPException, URLError, TimeoutError, ValueError, OSError):
        return None
    return payload if isinstance(payload, dict) else None


def _remaining_timeout(deadline: float) -> float:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("wire-pod bridge deadline elapsed")
    return remaining


def _read_bounded(response: Any, deadline: float) -> bytes:
    """Read at most 64 KiB before the post-header elapsed deadline.

    urllib's header parsing timeout is an inactivity timeout. Once headers
    arrive, setting the underlying socket deadline before every byte prevents a
    peer from extending a response by slowly trickling otherwise-valid JSON.
    """
    chunks = bytearray()
    while len(chunks) <= 65536:
        _set_response_timeout(response, _remaining_timeout(deadline))
        chunk = response.read(1)
        if not chunk:
            return bytes(chunks)
        chunks.extend(chunk)
    raise ValueError("wire-pod bridge response exceeds 64 KiB")


def _set_response_timeout(response: Any, timeout: float) -> None:
    raw = getattr(getattr(response, "fp", None), "raw", None)
    sock = getattr(raw, "_sock", None)
    if sock is not None:
        sock.settimeout(timeout)
