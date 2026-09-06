"""Plugin contract tests using only Python's standard library."""

from __future__ import annotations

import json
import sys
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parents[2]))
from wirepod import register  # noqa: E402
from wirepod.tools import BridgeConfig, vector_command, vector_status  # noqa: E402


class BridgeHandler(BaseHTTPRequestHandler):
    token = "test-token"
    esn = "ESN-A"
    mode = "normal"
    last_command = None

    def do_GET(self):  # noqa: N802
        if self.headers.get("Authorization") != f"Bearer {self.token}":
            self.send_error(401)
            return
        if self.path != "/bridge/v1/robots/ESN-A/status":
            self.send_error(404)
            return
        body = json.dumps({"source_sha": "test-sha", "robot": {"esn": self.esn, "activated": True}}).encode()
        if self.mode == "trickle":
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
        values = {"bridge_url": f"http://127.0.0.1:{self.server.server_port}", "bridge_token": "test-token", "vector_esn": "ESN-A", "timeout_seconds": 2}
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

    def test_commands_are_profile_bound_and_bounded(self):
        result = json.loads(vector_command({"text": "hello"}, self.config(), "say"))
        self.assertTrue(result["ok"])
        self.assertEqual(BridgeHandler.last_command, {"action": "say", "text": "hello"})
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

    def test_register_binds_a_profile_configured_tool(self):
        class Context:
            def __init__(self):
                self.handlers = {}
                self.schemas = {}

            def get_config(self, name, default=""):
                settings = {"bridge_url": f"http://127.0.0.1:{self_server.server_port}", "bridge_token": "test-token", "vector_esn": "ESN-A"}
                return settings.get(name, default)

            def register_tool(self, **kwargs):
                self.handlers[kwargs["name"]] = kwargs["handler"]
                self.schemas[kwargs["name"]] = kwargs["schema"]

        self_server = self.server
        context = Context()
        register(context)
        self.assertEqual(context.schemas["vector_status"]["name"], "vector_status")
        self.assertIn("vector_stop", context.handlers)
        self.assertEqual(context.schemas["vector_undock"]["parameters"]["properties"], {})
        self.assertEqual(context.schemas["vector_scan"]["parameters"]["properties"], {})
        result = json.loads(context.handlers["vector_status"]({"vector_esn": "model-supplied-value"}))
        self.assertTrue(result["ok"])
        self.assertEqual(result["robot"]["esn"], "ESN-A")


if __name__ == "__main__":
    unittest.main()
