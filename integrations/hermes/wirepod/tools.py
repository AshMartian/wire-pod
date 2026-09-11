"""Profile-bound command and observation client for the wire-pod bridge."""

from __future__ import annotations

import base64
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
    operation_timeout_seconds: int

    @classmethod
    def from_settings(cls, settings: dict[str, Any]) -> "BridgeConfig":
        url = str(settings.get("bridge_url", "")).strip().rstrip("/")
        token = str(settings.get("bridge_token", "")).strip()
        esn = str(settings.get("vector_esn", "")).strip()
        try:
            timeout = int(settings.get("timeout_seconds", 5))
        except (TypeError, ValueError):
            timeout = 5
        try:
            operation_timeout = int(settings.get("operation_timeout_seconds", 35))
        except (TypeError, ValueError):
            operation_timeout = 35
        parsed = urlparse(url)
        if parsed.scheme not in {"http", "https"} or not parsed.netloc:
            raise ValueError("bridge_url must be an absolute HTTP(S) URL")
        if not token:
            raise ValueError("bridge_token is not configured")
        if not esn:
            raise ValueError("vector_esn is not configured")
        if not 1 <= timeout <= 15:
            raise ValueError("timeout_seconds must be between 1 and 15")
        if not 1 <= operation_timeout <= 45:
            raise ValueError("operation_timeout_seconds must be between 1 and 45")
        return cls(
            url=url,
            token=token,
            esn=esn,
            timeout_seconds=timeout,
            operation_timeout_seconds=operation_timeout,
        )


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
    if error := _request_error(payload, "wire-pod bridge is unavailable"):
        return error
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
    if error := _request_error(payload, "wire-pod bridge is unavailable"):
        return error
    observation = payload.get("observation")
    if not isinstance(observation, dict):
        return json.dumps({"ok": False, "error": "wire-pod bridge returned an invalid observation"})
    return json.dumps({"ok": True, "source_sha": payload.get("source_sha"), "observation": observation})


def vector_capture_image(args: dict[str, Any], config: BridgeConfig) -> dict[str, Any] | str:
    """Capture one fresh profile-scoped Vector image for the current context."""
    base_url = f"{config.url}/bridge/v1/robots/{quote(config.esn, safe='')}/camera/snapshots"
    snapshot_id = args.get("snapshot_id") if set(args) == {"snapshot_id"} else None
    if args and (not isinstance(snapshot_id, str) or len(snapshot_id) != 32 or any(character not in "0123456789abcdef" for character in snapshot_id)):
        return json.dumps({"ok": False, "error": "invalid camera snapshot reference"})
    if snapshot_id is None:
        reference = _request_json("POST", base_url, config, timeout_seconds=config.operation_timeout_seconds)
        if error := _request_error(reference, "Vector camera is unavailable"):
            return error
        if not isinstance(reference.get("snapshot_id"), str):
            return json.dumps({"ok": False, "error": "Vector camera is unavailable"})
        snapshot_id = reference["snapshot_id"]
    url = f"{base_url}/{snapshot_id}"
    image = _request_image(url, config, timeout_seconds=config.operation_timeout_seconds)
    if image is None:
        return json.dumps({"ok": False, "error": "Vector camera is unavailable"})
    encoded = base64.b64encode(image).decode("ascii")
    return {
        "_multimodal": True,
        "content": [
            {"type": "text", "text": "Fresh camera frame captured from this profile's Vector."},
            {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{encoded}"}},
        ],
        "text_summary": "A fresh camera frame from this Vector was attached to the current context.",
    }


def vector_command(args: dict[str, Any], config: BridgeConfig, action: str) -> str:
    """Send only a whitelisted, schema-validated command to the profile's Vector."""
    command = dict(args)
    command["action"] = action
    if not _valid_command(command):
        return json.dumps({"ok": False, "error": "invalid bounded Vector command"})
    url = f"{config.url}/bridge/v1/robots/{quote(config.esn, safe='')}/commands"
    payload = _request_json("POST", url, config, command, timeout_seconds=config.operation_timeout_seconds)
    if error := _request_error(payload, "wire-pod bridge is unavailable"):
        return error
    result = payload.get("result")
    if not isinstance(result, dict) or result.get("action") != action:
        return json.dumps({"ok": False, "error": "wire-pod bridge returned an invalid command result"})
    return json.dumps({"ok": True, "source_sha": payload.get("source_sha"), "result": result})


def _valid_command(command: dict[str, Any]) -> bool:
    action = command.get("action")
    if action == "say":
        text = command.get("text")
        return isinstance(text, str) and 1 <= len(text.strip()) <= 280 and set(command) == {"action", "text"}
    if action == "express":
        expression = command.get("expression")
        return isinstance(expression, str) and expression in {
            "affectionate", "celebrate", "confused", "curious", "excited", "happy", "sad", "thinking"
        } and set(command) == {"action", "expression"}
    if action == "drive":
        keys = {"action", "left_wheel_mmps", "right_wheel_mmps", "duration_ms"}
        return set(command) == keys and all(isinstance(command[key], int) and not isinstance(command[key], bool) for key in keys - {"action"}) and -200 <= command["left_wheel_mmps"] <= 200 and -200 <= command["right_wheel_mmps"] <= 200 and 50 <= command["duration_ms"] <= 2000
    if action in {"head", "lift"}:
        keys = {"action", "speed_rad_per_sec", "duration_ms"}
        return set(command) == keys and all(isinstance(command[key], int) and not isinstance(command[key], bool) for key in keys - {"action"}) and -2 <= command["speed_rad_per_sec"] <= 2 and 50 <= command["duration_ms"] <= 2000
    return action in {"stop", "undock", "scan"} and set(command) == {"action"}


def _request_error(payload: dict[str, Any] | None, unavailable_message: str) -> str | None:
    if payload is None:
        return json.dumps({"ok": False, "error": unavailable_message})
    error = payload.get("_wirepod_error")
    if isinstance(error, str):
        return json.dumps({"ok": False, "error": error})
    return None


def _request_json(
    method: str,
    url: str,
    config: BridgeConfig,
    body: dict[str, Any] | None = None,
    *,
    timeout_seconds: int | None = None,
) -> dict[str, Any] | None:
    encoded = json.dumps(body, separators=(",", ":")).encode() if body is not None else None
    request = Request(url, data=encoded, method=method, headers={"Accept": "application/json", "Authorization": f"Bearer {config.token}", "Content-Type": "application/json"})
    deadline = time.monotonic() + (timeout_seconds or config.timeout_seconds)
    try:
        with build_opener(NoRedirect()).open(request, timeout=_remaining_timeout(deadline)) as response:
            if response.status not in {200, 201, 202}:
                raise ValueError("bridge returned an unexpected status")
            payload = json.loads(_read_bounded(response, deadline))
    except HTTPError as error:
        try:
            return {"_wirepod_error": f"wire-pod bridge returned HTTP {error.code}"}
        finally:
            error.close()
    except (HTTPException, URLError, TimeoutError, ValueError, OSError):
        return None
    return payload if isinstance(payload, dict) else None


def _request_image(url: str, config: BridgeConfig, *, timeout_seconds: int | None = None) -> bytes | None:
    request = Request(url, method="GET", headers={"Accept": "image/jpeg", "Authorization": f"Bearer {config.token}"})
    deadline = time.monotonic() + (timeout_seconds or config.timeout_seconds)
    try:
        with build_opener(NoRedirect()).open(request, timeout=_remaining_timeout(deadline)) as response:
            if response.status != 200 or response.headers.get_content_type() != "image/jpeg":
                raise ValueError("bridge returned an unexpected image response")
            length = response.headers.get("Content-Length")
            if length is not None and (not length.isdecimal() or int(length) > 8 * 1024 * 1024):
                raise ValueError("bridge image exceeds 8 MiB")
            image = _read_bounded(response, deadline, 8 * 1024 * 1024)
    except HTTPError as error:
        try:
            return None
        finally:
            error.close()
    except (HTTPException, URLError, TimeoutError, ValueError, OSError):
        return None
    if len(image) < 4 or not image.startswith(b"\xff\xd8\xff") or not image.endswith(b"\xff\xd9"):
        return None
    return image


def _remaining_timeout(deadline: float) -> float:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("wire-pod bridge deadline elapsed")
    return remaining


def _read_bounded(response: Any, deadline: float, maximum: int = 65536) -> bytes:
    """Read a bounded response before the post-header elapsed deadline.

    urllib's header parsing timeout is an inactivity timeout. Once headers
    arrive, setting the underlying socket deadline before every byte prevents a
    peer from extending a response by slowly trickling otherwise-valid JSON.
    """
    chunks = bytearray()
    while len(chunks) <= maximum:
        _set_response_timeout(response, _remaining_timeout(deadline))
        chunk = response.read(1)
        if not chunk:
            return bytes(chunks)
        chunks.extend(chunk)
    raise ValueError("wire-pod bridge response exceeds the allowed size")


def _set_response_timeout(response: Any, timeout: float) -> None:
    raw = getattr(getattr(response, "fp", None), "raw", None)
    sock = getattr(raw, "_sock", None)
    if sock is not None:
        sock.settimeout(timeout)
