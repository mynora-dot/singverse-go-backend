import importlib.util
import json
from pathlib import Path
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import unittest
import urllib.error
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location(
    "lan_discovery", Path(__file__).resolve().parents[1] / "lan-discovery.py")
discovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(discovery)
INSTANCE = "01234567-89ab-4cde-8fab-0123456789ab"


class LanDiscoveryTests(unittest.TestCase):
    def test_only_the_actual_private_http_binding_is_published(self):
        ports = {"8080/tcp": [{"HostIp": "192.168.1.102", "HostPort": "18080"}],
                 "8090/tcp": [{"HostIp": "127.0.0.1", "HostPort": "18090"}]}
        self.assertEqual(discovery.published_entry(ports), ("192.168.1.102", 18080))
        command = discovery.publisher_command(("192.168.1.102", 18080, INSTANCE))
        self.assertEqual(command[1], "-P")
        self.assertEqual(command[-3:], ["192.168.1.102", "api=1", "scheme=http"])
        self.assertIn("singverse-0123456789ab.local.", command)

    def test_missing_ambiguous_public_or_unbound_ports_are_rejected(self):
        with self.assertRaises(ValueError):
            discovery.published_entry(None)
        for bindings in (None, [],
                         [{"HostIp": ip, "HostPort": "18080"} for ip in ("10.0.0.2", "10.0.0.3")],
                         *[[{"HostIp": ip, "HostPort": "18080"}] for ip in
                           ("0.0.0.0", "127.0.0.1", "8.8.8.8", "169.254.1.2", "192.0.2.1", "::", "192.168.1.256")],
                         [{"HostIp": "192.168.1.2", "HostPort": "0"}],
                         [{"HostIp": "192.168.1.2", "HostPort": "65536"}]):
            with self.subTest(bindings=bindings), self.assertRaises(ValueError):
                discovery.published_entry({"8080/tcp": bindings})

    def test_identity_is_verified_without_proxy_or_redirects(self):
        class Handler(BaseHTTPRequestHandler):
            body = json.dumps({"service": "singverse", "apiVersion": 1, "instanceId": INSTANCE}).encode()
            status = 200

            def do_GET(self):
                self.send_response(self.status)
                if self.status == 302:
                    self.send_header("Location", "/redirect-target")
                self.end_headers()
                self.wfile.write(self.body)

            def log_message(self, *args):
                pass

        with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            try:
                with patch.dict("os.environ", {"http_proxy": "http://127.0.0.1:1", "no_proxy": ""}):
                    self.assertEqual(discovery.server_identity("127.0.0.1", server.server_port), INSTANCE)
                for body in ({"service": "other", "apiVersion": 1, "instanceId": INSTANCE},
                             {"service": "singverse", "apiVersion": True, "instanceId": INSTANCE},
                             {"service": "singverse", "apiVersion": 1, "instanceId": "bad-id"},
                             {"service": "singverse", "apiVersion": 1, "instanceId": None}, []):
                    Handler.body = json.dumps(body).encode()
                    with self.subTest(body=body), self.assertRaises((ValueError, TypeError)):
                        discovery.server_identity("127.0.0.1", server.server_port)
                Handler.body = b"x" * (discovery.MAX_BODY + 1)
                with self.assertRaises(ValueError):
                    discovery.server_identity("127.0.0.1", server.server_port)
                Handler.status = 302
                with self.assertRaises(urllib.error.HTTPError):
                    discovery.server_identity("127.0.0.1", server.server_port)
            finally:
                server.shutdown()
                worker.join()

    def test_unhealthy_server_withdraws_and_recovery_republishes(self):
        target = ("192.168.1.102", 18080, INSTANCE)
        first, second = Mock(), Mock()
        first.poll.return_value = second.poll.return_value = None
        with patch.object(discovery.subprocess, "Popen", side_effect=[first, second]) as publish, \
                patch.object(discovery, "verified_target", side_effect=[ValueError("offline"), target]), \
                patch.object(discovery.time, "sleep", side_effect=[None, None, KeyboardInterrupt]):
            with self.assertRaises(KeyboardInterrupt):
                discovery.advertise("test-only-container", target)
        self.assertEqual(publish.call_count, 2)
        first.terminate.assert_called_once()
        second.terminate.assert_called_once()
        first.wait.assert_called_once_with(timeout=5)
        second.wait.assert_called_once_with(timeout=5)


if __name__ == "__main__":
    unittest.main()
