"""TLS harness checks; these do not validate the production gateway process."""
import http.client
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import threading
import unittest

from local import ROOT
from stream_baseline import Fixture, UPSTREAM_KEY, call, fixture_tls


class StreamTLSFixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="txapi-stream-tls-test-")
        cls.addClassCleanup(cls.work.cleanup)
        directory = Path(cls.work.name)
        helper = directory / ("fixture.exe" if os.name == "nt" else "fixture")
        environment = dict(os.environ, GOARCH="amd64")
        subprocess.run(["go", "build", "-o", str(helper), "./tests/upstream/nativews"],
                       cwd=ROOT, env=environment, check=True, timeout=120)
        cls.certificates = directory / "certificates"
        subprocess.run([str(helper), "-mode", "prepare", "-directory", str(cls.certificates)],
                       check=True, timeout=15)

    def test_verified_tls_streams_and_absolute_receive_deadline(self):
        for operation in ("chat", "responses"):
            with self.subTest(operation=operation):
                fixture = Fixture(.005, operation)
                fixture.tls_context, client = fixture_tls(self.certificates)
                thread = threading.Thread(target=fixture.serve_forever, daemon=True)
                thread.start()
                connection = http.client.HTTPSConnection("127.0.0.1", fixture.server_port, context=client)
                try:
                    # Neither an unrelated root set nor an unrelated host name
                    # can reach the authenticated HTTP fixture handler.
                    untrusted = http.client.HTTPSConnection("127.0.0.1", fixture.server_port, timeout=2)
                    try:
                        with self.assertRaises(ssl.SSLCertVerificationError):
                            untrusted.connect()
                    finally:
                        untrusted.close()
                    with socket.create_connection(("127.0.0.1", fixture.server_port), timeout=2) as raw:
                        with self.assertRaises(ssl.SSLCertVerificationError):
                            client.wrap_socket(raw, server_hostname="wrong.invalid")
                    self.assertEqual(fixture.records, {})
                    normal = call(connection, UPSTREAM_KEY, fixture, 2)
                    self.assertTrue(normal["success"], normal)
                    self.assertEqual(normal["upstream_attempts"], 1)
                    self.assertEqual(normal["client_tls"]["alpn"], "http/1.1")
                    self.assertEqual(normal["upstream_transport"][0]["tls"], normal["client_tls"]["version"])
                    self.assertGreater(normal["first_effective_text_ns"], normal["first_body_byte_ns"])
                    truncated = call(connection, UPSTREAM_KEY, fixture, 2, "truncated")
                    self.assertFalse(truncated["success"], truncated)
                    timeout = call(connection, UPSTREAM_KEY, fixture, .1, "timeout")
                    self.assertEqual(timeout["failure"], "timeout", timeout)
                    self.assertLess(timeout["elapsed_ns"], 500_000_000)
                    self.assertTrue(all(s["upstream_handler_finished"] and not s["drain_failure"]
                                        for s in (normal, truncated, timeout)))
                finally:
                    connection.close()
                    fixture.stop_event.set()
                    fixture.shutdown()
                    fixture.server_close()
                    thread.join(2)
                self.assertFalse(thread.is_alive())


if __name__ == "__main__":
    unittest.main()
