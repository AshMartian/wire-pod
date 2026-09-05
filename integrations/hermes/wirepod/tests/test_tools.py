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
from wirepod.tools import BridgeConfig, vector_status  # noqa: E402


class BridgeHandler(BaseHTTPRequestHandler):
    token = "test-token"
    esn = "ESN-A"
    mode = "normal"

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
                self.handler = None

            def get_config(self, name, default=""):
                settings = {"bridge_url": f"http://127.0.0.1:{self_server.server_port}", "bridge_token": "test-token", "vector_esn": "ESN-A"}
                return settings.get(name, default)

            def register_tool(self, **kwargs):
                self.handler = kwargs["handler"]
                self.schema = kwargs["schema"]

        self_server = self.server
        context = Context()
        register(context)
        self.assertEqual(context.schema["name"], "vector_status")
        result = json.loads(context.handler({"vector_esn": "model-supplied-value"}))
        self.assertTrue(result["ok"])
        self.assertEqual(result["robot"]["esn"], "ESN-A")


if __name__ == "__main__":
    unittest.main()
