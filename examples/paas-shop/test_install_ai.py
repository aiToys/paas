"""共用安装器的本机 HTTP 回归；不访问模型或业务服务。"""
import importlib.util
import json
from pathlib import Path
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("install_ai", Path(__file__).with_name("install_ai.py"))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.items = {"skills": [], "tools": [], "agents": []}
        self.calls, self.fail, self.redirect, self.stolen = [], None, False, False
        state = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def handle_request(self):
                state.calls.append((self.command, self.path))
                if self.path == "/steal":
                    state.stolen = True
                if state.redirect:
                    self.send_response(302)
                    self.send_header("Location", "/steal")
                    self.end_headers()
                    return
                if self.path == state.fail:
                    self.send_response(503)
                    self.end_headers()
                    return
                parts = self.path.strip("/").split("/")
                if len(parts) < 2 or parts[1] not in state.items:
                    self.send_response(404)
                    self.end_headers()
                    return
                rows = state.items[parts[1]]
                if self.command == "GET":
                    data = rows if len(parts) == 2 else next(x for x in rows if x["id"] == parts[2])
                else:
                    data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    if self.command == "POST":
                        data["id"] = parts[1] + "-" + str(len(rows) + 1)
                        rows.append(data)
                    else:
                        rows[:] = [data if x["id"] == parts[2] else x for x in rows]
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps({"data": data}).encode())

            do_GET = handle_request
            do_POST = handle_request
            do_PUT = handle_request

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.client = installer.Client(f"http://127.0.0.1:{self.server.server_port}", "local-test-only")

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def test_repeated_install_preserves_edits_and_removes_refund_from_daily_agent(self):
        first = installer.install(self.client, "http://shop-mcp.local")
        self.items["skills"][0]["instructions"] = "用户定制的内容"
        agent = self.items["agents"][0]
        agent.update(model="custom-model", promptRef="custom-prompt", maxSteps=8, knowledgeBases=["custom-kb"])
        self.items["tools"].append({"id": "legacy-refund", "name": "refund_order", "type": "mcp"})
        agent["tools"].append("legacy-refund")
        second = installer.install(self.client, "http://shop-mcp.local")
        self.assertEqual(first["agentId"], second["agentId"])
        self.assertEqual(len(self.items["skills"]), 2)
        self.assertEqual(len(self.items["agents"]), 1)
        self.assertEqual(self.items["skills"][0]["instructions"], "用户定制的内容")
        saved = self.items["agents"][0]
        self.assertNotIn("legacy-refund", saved["tools"])
        self.assertEqual(saved["model"], "custom-model")
        self.assertEqual(saved["knowledgeBases"], ["custom-kb"])
        self.assertEqual(saved["promptRef"], "custom-prompt")
        self.assertEqual(set(saved["skills"]), set(first["skillIds"]))
        self.assertFalse(second["inferenceVerified"])

    def test_list_failure_stops_before_creating_resources(self):
        self.fail = "/api/skills"
        with self.assertRaises(installer.InstallError):
            installer.install(self.client, "http://shop-mcp.local")
        self.assertTrue(all(method == "GET" for method, _ in self.calls))

    def test_redirect_never_receives_credentials(self):
        self.redirect = True
        with self.assertRaises(installer.InstallError):
            installer.install(self.client, "http://shop-mcp.local")
        self.assertFalse(self.stolen)

    def test_disabled_skill_is_not_silently_enabled(self):
        installer.install(self.client, "http://shop-mcp.local")
        self.items["skills"][0]["enabled"] = False
        with self.assertRaises(installer.InstallError):
            installer.install(self.client, "http://shop-mcp.local")
        self.assertFalse(self.items["skills"][0]["enabled"])

    def test_disabled_agent_stops_setup_without_reenabling(self):
        installer.install(self.client, "http://shop-mcp.local")
        self.items["agents"][0]["enabled"] = False
        with self.assertRaises(installer.InstallError):
            installer.install(self.client, "http://shop-mcp.local")
        self.assertFalse(self.items["agents"][0]["enabled"])


if __name__ == "__main__":
    unittest.main()
