"""Plugin contract tests using only Python's standard library."""

from __future__ import annotations

import json
import sys
import threading
import time
import unittest
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parents[2]))
from wirepod import register  # noqa: E402
from wirepod.tools import BridgeConfig, vector_capture_image, vector_command, vector_status  # noqa: E402


class BridgeHandler(BaseHTTPRequestHandler):
    token = "test-token"
    esn = "ESN-A"
    mode = "normal"
    last_command = None
    snapshot_id = "0123456789abcdef0123456789abcdef"
    image = b"\xff\xd8\xff\xe0camera-test\xff\xd9"

    def do_GET(self):  # noqa: N802
        if self.headers.get("Authorization") != f"Bearer {self.token}":
            self.send_error(401)
            return
        if self.path == f"/bridge/v1/robots/ESN-A/camera/snapshots/{self.snapshot_id}":
            self.send_response(200)
            self.send_header("Content-Type", "image/jpeg")
            self.send_header("Content-Length", str(len(self.image)))
            self.end_headers()
            self.wfile.write(self.image)
            return
        if self.path != "/bridge/v1/robots/ESN-A/status":
            self.send_error(404)
            return
        if self.mode == "service_unavailable":
            body = b"robot observation is temporarily unavailable"
            self.send_response(503)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        body = json.dumps({"source_sha": "test-sha", "robot": {"esn": self.esn, "activated": True}}).encode()
        if self.mode == "trickle":
            body = b'{"robot":'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            for byte in body:
                self.wfile.write(bytes((byte,)))
                self.wfile.flush()
                time.sleep(0.55)
            return
        if self.mode == "truncated":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body) + 10))
            self.end_headers()
            self.wfile.write(body[:5])
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):  # noqa: N802
        if self.headers.get("Authorization") != f"Bearer {self.token}":
            self.send_error(401)
            return
        if self.path == "/bridge/v1/robots/ESN-A/camera/snapshots":
            body = json.dumps({"snapshot_id": self.snapshot_id, "captured_at_unix_ms": 1, "expires_at_unix_ms": 2}).encode()
            self.send_response(201)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path != "/bridge/v1/robots/ESN-A/commands":
            self.send_error(404)
            return
        type(self).last_command = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        body = json.dumps({"source_sha": "test-sha", "result": {"action": type(self).last_command["action"], "stop_scheduled": type(self).last_command["action"] != "stop"}}).encode()
        self.send_response(202)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        del fmt, args


class ToolsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), BridgeHandler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.thread.join()
        cls.server.server_close()

    def config(self, **overrides):
        values = {
            "bridge_url": f"http://127.0.0.1:{self.server.server_port}",
            "bridge_token": "test-token",
            "vector_esn": "ESN-A",
            "timeout_seconds": 2,
            "operation_timeout_seconds": 3,
        }
        values.update(overrides)
        return BridgeConfig.from_settings(values)

    def tearDown(self):
        BridgeHandler.mode = "normal"

    def test_profile_bound_status(self):
        result = json.loads(vector_status({}, self.config()))
        self.assertTrue(result["ok"])
        self.assertEqual(result["robot"], {"esn": "ESN-A", "activated": True})

    def test_wrong_token_and_robot_are_rejected(self):
        rejected = json.loads(vector_status({}, self.config(bridge_token="wrong")))
        self.assertFalse(rejected["ok"])
        wrong_robot = json.loads(vector_status({}, self.config(vector_esn="ESN-B")))
        self.assertFalse(wrong_robot["ok"])

    def test_invalid_configuration_never_uses_model_input(self):
        with self.assertRaisesRegex(ValueError, "absolute HTTP"):
            BridgeConfig.from_settings({"bridge_url": "file:///etc/passwd", "bridge_token": "x", "vector_esn": "ESN-A"})
        with self.assertRaisesRegex(ValueError, "between 1 and 15"):
            self.config(timeout_seconds=30)
        with self.assertRaisesRegex(ValueError, "between 1 and 45"):
            self.config(operation_timeout_seconds=60)

    def test_commands_and_camera_use_the_long_operation_deadline(self):
        config = self.config(timeout_seconds=1, operation_timeout_seconds=3)
        with patch("wirepod.tools._request_json", return_value={"result": {"action": "stop"}}) as request_json:
            vector_command({}, config, "stop")
        self.assertEqual(request_json.call_args.kwargs["timeout_seconds"], 3)

        with patch("wirepod.tools._request_json", return_value={"snapshot_id": BridgeHandler.snapshot_id}) as request_json, patch(
            "wirepod.tools._request_image", return_value=BridgeHandler.image
        ) as request_image:
            vector_capture_image({}, config)
        self.assertEqual(request_json.call_args.kwargs["timeout_seconds"], 3)
        self.assertEqual(request_image.call_args.kwargs["timeout_seconds"], 3)

    def test_commands_are_profile_bound_and_bounded(self):
        result = json.loads(vector_command({"text": "hello"}, self.config(), "say"))
        self.assertTrue(result["ok"])
        self.assertEqual(BridgeHandler.last_command, {"action": "say", "text": "hello"})
        expression = json.loads(vector_command({"expression": "happy"}, self.config(), "express"))
        self.assertTrue(expression["ok"])
        self.assertEqual(BridgeHandler.last_command, {"action": "express", "expression": "happy"})
        rejected_expression = json.loads(vector_command({"expression": "anim_arbitrary_01"}, self.config(), "express"))
        self.assertFalse(rejected_expression["ok"])
        self.assertEqual(rejected_expression["error"], "invalid bounded Vector command")
        rejected = json.loads(vector_command({"left_wheel_mmps": 999, "right_wheel_mmps": 0, "duration_ms": 50}, self.config(), "drive"))
        self.assertFalse(rejected["ok"])
        self.assertEqual(rejected["error"], "invalid bounded Vector command")

    def test_undock_and_scan_are_explicit_no_argument_commands(self):
        undock = json.loads(vector_command({}, self.config(), "undock"))
        self.assertTrue(undock["ok"])
        self.assertEqual(BridgeHandler.last_command, {"action": "undock"})
        scan = json.loads(vector_command({}, self.config(), "scan"))
        self.assertTrue(scan["ok"])
        self.assertEqual(BridgeHandler.last_command, {"action": "scan"})
        rejected = json.loads(vector_command({"duration_ms": 50}, self.config(), "scan"))
        self.assertFalse(rejected["ok"])
        self.assertEqual(rejected["error"], "invalid bounded Vector command")

    def test_capture_attaches_only_a_profile_scoped_multimodal_image(self):
        result = vector_capture_image({}, self.config())
        self.assertIsInstance(result, dict)
        self.assertTrue(result["_multimodal"])
        self.assertEqual(result["content"][0]["type"], "text")
        url = result["content"][1]["image_url"]["url"]
        self.assertTrue(url.startswith("data:image/jpeg;base64,"))
        self.assertNotIn(self.config().token, url)
        event_frame = vector_capture_image({"snapshot_id": BridgeHandler.snapshot_id}, self.config())
        self.assertIsInstance(event_frame, dict)
        rejected = json.loads(vector_capture_image({"snapshot_id": "other"}, self.config()))
        self.assertFalse(rejected["ok"])

    def test_post_header_deadline_rejects_trickled_response(self):
        BridgeHandler.mode = "trickle"
        started = time.monotonic()
        result = json.loads(vector_status({}, self.config(timeout_seconds=1)))
        self.assertFalse(result["ok"])
        self.assertLess(time.monotonic() - started, 1.35)

    def test_truncated_response_returns_structured_error(self):
        BridgeHandler.mode = "truncated"
        result = json.loads(vector_status({}, self.config()))
        self.assertFalse(result["ok"])
        self.assertEqual(result["error"], "wire-pod bridge is unavailable")

    def test_http_service_failure_is_distinguished_from_unreachable_bridge(self):
        BridgeHandler.mode = "service_unavailable"
        result = json.loads(vector_status({}, self.config()))
        self.assertFalse(result["ok"])
        self.assertEqual(result["error"], "wire-pod bridge returned HTTP 503")

    def test_register_binds_a_profile_configured_tool(self):
        class Context:
            def __init__(self):
                self.handlers = {}
                self.schemas = {}
                self.hooks = {}

            def get_config(self, name, default=""):
                settings = {"bridge_url": f"http://127.0.0.1:{self_server.server_port}", "bridge_token": "test-token", "vector_esn": "ESN-A"}
                return settings.get(name, default)

            def register_tool(self, **kwargs):
                self.handlers[kwargs["name"]] = kwargs["handler"]
                self.schemas[kwargs["name"]] = kwargs["schema"]

            def register_hook(self, name, handler):
                self.hooks[name] = handler

        self_server = self.server
        context = Context()
        register(context)
        self.assertEqual(context.schemas["vector_status"]["name"], "vector_status")
        self.assertEqual(context.schemas["vector_capture_image"]["name"], "vector_capture_image")
        self.assertEqual(
            context.schemas["vector_express"]["parameters"]["properties"]["expression"]["enum"],
            ["affectionate", "celebrate", "confused", "curious", "excited", "happy", "sad", "thinking"],
        )
        self.assertIn("vector_express", context.handlers)
        self.assertIn("vector_stop", context.handlers)
        self.assertEqual(context.schemas["vector_undock"]["parameters"]["properties"], {})
        self.assertEqual(context.schemas["vector_scan"]["parameters"]["properties"], {})
        self.assertEqual(set(context.hooks), {"pre_tool_call", "post_tool_call"})
        result = json.loads(context.handlers["vector_status"]({"vector_esn": "model-supplied-value"}))
        self.assertTrue(result["ok"])
        self.assertEqual(result["robot"]["esn"], "ESN-A")

    def test_audit_hooks_record_start_and_completion_without_sensitive_payloads(self):
        class Context:
            def __init__(self):
                self.hooks = {}

            def get_config(self, name, default=""):
                settings = {"vector_esn": "ESN-A"}
                return settings.get(name, default)

            def register_tool(self, **kwargs):
                pass

            def register_hook(self, name, handler):
                self.hooks[name] = handler

        context = Context()
        register(context)
        with patch("wirepod.audit.logger.info") as info:
            context.hooks["pre_tool_call"](
                "vector_say",
                {"text": "private check-in", "unexpected": "ignored"},
                task_id="task-1",
                tool_call_id="call-1",
            )
            context.hooks["post_tool_call"](
                "vector_say",
                {"text": "private check-in"},
                '{"ok":true,"result":{"action":"say"}}',
                task_id="task-1",
                tool_call_id="call-1",
                duration_ms=12.5,
            )
            context.hooks["pre_tool_call"]("vector_express", {"expression": "happy"})
            context.hooks["post_tool_call"](
                "vector_express",
                {"expression": "happy"},
                '{"ok":true,"result":{"action":"express","expression":"happy"}}',
            )

        records = [call.args[1] for call in info.call_args_list]
        self.assertEqual(len(records), 4)
        self.assertTrue(all(record.startswith("{") for record in records))
        self.assertIn('"event":"started"', records[0])
        self.assertIn('"event":"completed"', records[1])
        self.assertIn('"text_chars":16', records[0])
        self.assertNotIn("private check-in", "".join(records))
        self.assertIn('"duration_ms":12.5', records[1])
        self.assertIn('"expression":"happy"', records[2])
        self.assertIn('"expression":"happy"', records[3])


if __name__ == "__main__":
    unittest.main()
