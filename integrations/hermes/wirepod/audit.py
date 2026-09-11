"""Profile-local audit hooks for the Vector Hermes tools.

Hermes already records the conversation and tool result, but the wire-pod
plugin needs a small structured breadcrumb that joins the requested tool call
to its bridge outcome.  Keep this deliberately metadata-only: speech text,
camera bytes, bearer credentials, and face data must not be copied into the
audit stream.
"""

from __future__ import annotations

import hashlib
import json
import logging
import time
from typing import Any, Callable


logger = logging.getLogger("wirepod.audit")

WIREPOD_TOOLS = frozenset(
    {
        "vector_status",
        "vector_observe",
        "vector_capture_image",
        "vector_say",
        "vector_express",
        "vector_drive",
        "vector_move_head",
        "vector_move_lift",
        "vector_stop",
        "vector_undock",
        "vector_scan",
    }
)

_CORRELATION_FIELDS = (
    "task_id",
    "session_id",
    "turn_id",
    "tool_call_id",
    "api_request_id",
)


def _safe_args(args: Any) -> dict[str, Any]:
    """Return bounded, non-sensitive call metadata."""
    if not isinstance(args, dict):
        return {"type": type(args).__name__}

    summary: dict[str, Any] = {"keys": sorted(str(key) for key in args)}
    for key in ("left_wheel_mmps", "right_wheel_mmps", "speed_rad_per_sec", "duration_ms"):
        value = args.get(key)
        if isinstance(value, int) and not isinstance(value, bool):
            summary[key] = value

    text = args.get("text")
    if isinstance(text, str):
        summary["text_chars"] = len(text)
        summary["text_sha256"] = hashlib.sha256(text.encode("utf-8")).hexdigest()[:16]

    expression = args.get("expression")
    if isinstance(expression, str):
        summary["expression"] = expression[:32]

    snapshot_id = args.get("snapshot_id")
    if isinstance(snapshot_id, str):
        summary["snapshot_id_present"] = bool(snapshot_id)
        summary["snapshot_id_sha256"] = hashlib.sha256(snapshot_id.encode("utf-8")).hexdigest()[:16]
    return summary


def _safe_result(result: Any) -> dict[str, Any]:
    """Summarize a handler result without logging its payload."""
    if isinstance(result, str):
        summary: dict[str, Any] = {"type": "json_string", "chars": len(result)}
        try:
            parsed = json.loads(result)
        except (TypeError, ValueError):
            return summary
    elif isinstance(result, dict):
        parsed = result
        summary = {"type": "object", "keys": sorted(str(key) for key in result)}
    else:
        return {"type": type(result).__name__}

    if not isinstance(parsed, dict):
        return summary
    if isinstance(parsed.get("ok"), bool):
        summary["ok"] = parsed["ok"]
    if isinstance(parsed.get("error"), str):
        summary["error"] = parsed["error"][:160]
    command_result = parsed.get("result")
    if isinstance(command_result, dict):
        if isinstance(command_result.get("action"), str):
            summary["action"] = command_result["action"]
        if isinstance(command_result.get("expression"), str):
            summary["expression"] = command_result["expression"][:32]
        if isinstance(command_result.get("stop_scheduled"), bool):
            summary["stop_scheduled"] = command_result["stop_scheduled"]
    if parsed.get("_multimodal") is True:
        summary["multimodal"] = True
    return summary


def _correlation(kwargs: dict[str, Any]) -> dict[str, str]:
    return {
        field: value[:160]
        for field in _CORRELATION_FIELDS
        if isinstance(value := kwargs.get(field), str) and value
    }


def _emit(event: str, esn: str, tool_name: str, *, args: Any = None, result: Any = None, kwargs: dict[str, Any] | None = None, duration_ms: Any = None) -> None:
    record: dict[str, Any] = {
        "event": event,
        "tool": tool_name,
        "vector_esn": esn,
        "at_unix_ms": int(time.time() * 1000),
    }
    if kwargs:
        record.update(_correlation(kwargs))
    if args is not None:
        record["args"] = _safe_args(args)
    if result is not None:
        record["result"] = _safe_result(result)
    if isinstance(duration_ms, (int, float)) and not isinstance(duration_ms, bool):
        record["duration_ms"] = round(duration_ms, 3)
    try:
        logger.info("WIREPOD_TOOL_AUDIT %s", json.dumps(record, ensure_ascii=True, sort_keys=True, separators=(",", ":")))
    except Exception:
        # Audit logging must never turn a robot tool result into a tool failure.
        logger.exception("WIREPOD_TOOL_AUDIT failed to serialize a record")


def make_hooks(esn: str) -> tuple[Callable[..., None], Callable[..., None]]:
    """Build Hermes lifecycle callbacks for this profile's assigned Vector."""

    def on_pre_tool_call(tool_name: str = "", args: Any = None, **kwargs: Any) -> None:
        if tool_name in WIREPOD_TOOLS:
            _emit("started", esn, tool_name, args=args, kwargs=kwargs)

    def on_post_tool_call(tool_name: str = "", args: Any = None, result: Any = None, duration_ms: Any = None, **kwargs: Any) -> None:
        if tool_name in WIREPOD_TOOLS:
            _emit("completed", esn, tool_name, args=args, result=result, kwargs=kwargs, duration_ms=duration_ms)

    return on_pre_tool_call, on_post_tool_call
