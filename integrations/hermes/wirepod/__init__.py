"""Hermes entry point for profile-bound wire-pod status."""

import json

from .schemas import VECTOR_STATUS
from .tools import BridgeConfig, vector_status


def register(ctx) -> None:
    settings = {
        "bridge_url": ctx.get_config("bridge_url", default=""),
        "bridge_token": ctx.get_config("bridge_token", default=""),
        "vector_esn": ctx.get_config("vector_esn", default=""),
        "timeout_seconds": ctx.get_config("timeout_seconds", default=5),
    }

    def handler(args: dict, **kwargs) -> str:
        del kwargs
        try:
            return vector_status(args, BridgeConfig.from_settings(settings))
        except ValueError as error:
            return json.dumps({"ok": False, "error": str(error)})

    ctx.register_tool(name="vector_status", toolset="wirepod", schema=VECTOR_STATUS, handler=handler)
