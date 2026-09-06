"""Hermes entry point for profile-bound wire-pod observation and control."""

import json

from .schemas import (
    VECTOR_DRIVE,
    VECTOR_CAPTURE_IMAGE,
    VECTOR_MOVE_HEAD,
    VECTOR_MOVE_LIFT,
    VECTOR_OBSERVE,
    VECTOR_SAY,
    VECTOR_SCAN,
    VECTOR_STATUS,
    VECTOR_STOP,
    VECTOR_UNDOCK,
)
from .tools import BridgeConfig, vector_capture_image, vector_command, vector_observe, vector_status


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

    def command_handler(action: str):
        def handler(args: dict, **kwargs) -> str:
            del kwargs
            try:
                return vector_command(args, BridgeConfig.from_settings(settings), action)
            except ValueError as error:
                return json.dumps({"ok": False, "error": str(error)})
        return handler

    def observe_handler(args: dict, **kwargs) -> str:
        del kwargs
        try:
            return vector_observe(args, BridgeConfig.from_settings(settings))
        except ValueError as error:
            return json.dumps({"ok": False, "error": str(error)})

    def capture_image_handler(args: dict, **kwargs):
        del kwargs
        try:
            return vector_capture_image(args, BridgeConfig.from_settings(settings))
        except ValueError as error:
            return json.dumps({"ok": False, "error": str(error)})

    ctx.register_tool(name="vector_status", toolset="wirepod", schema=VECTOR_STATUS, handler=handler)
    ctx.register_tool(name="vector_observe", toolset="wirepod", schema=VECTOR_OBSERVE, handler=observe_handler)
    ctx.register_tool(name="vector_capture_image", toolset="wirepod", schema=VECTOR_CAPTURE_IMAGE, handler=capture_image_handler)
    ctx.register_tool(name="vector_say", toolset="wirepod", schema=VECTOR_SAY, handler=command_handler("say"))
    ctx.register_tool(name="vector_drive", toolset="wirepod", schema=VECTOR_DRIVE, handler=command_handler("drive"))
    ctx.register_tool(name="vector_move_head", toolset="wirepod", schema=VECTOR_MOVE_HEAD, handler=command_handler("head"))
    ctx.register_tool(name="vector_move_lift", toolset="wirepod", schema=VECTOR_MOVE_LIFT, handler=command_handler("lift"))
    ctx.register_tool(name="vector_stop", toolset="wirepod", schema=VECTOR_STOP, handler=command_handler("stop"))
    ctx.register_tool(name="vector_undock", toolset="wirepod", schema=VECTOR_UNDOCK, handler=command_handler("undock"))
    ctx.register_tool(name="vector_scan", toolset="wirepod", schema=VECTOR_SCAN, handler=command_handler("scan"))
