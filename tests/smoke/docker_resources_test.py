"""Read-only container sampling rejects gaps and process replacement."""
import json
import copy
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from native_ws_delivery import ContainerResources, assess_protocol_soak


class ContainerResourcesTest(unittest.TestCase):
    @patch("native_ws_delivery.os.sysconf", return_value=100, create=True)
    @patch("native_ws_delivery.run")
    def test_same_nonroot_process_is_sampled_and_sampler_joins(self, run, _):
        def sample(*args, **kwargs):
            self.assertEqual(args[0], ["docker", "exec", "isolated-gateway", "/smoke-nativews", "-mode", "resources"])
            return SimpleNamespace(stdout=json.dumps({"process_identity": "same-process", "cpu_ticks": 100,
                                                       "rss_bytes": 4096, "fd_count": 12}))
        run.side_effect = sample
        collector = ContainerResources(SimpleNamespace(gateway="isolated-gateway"))
        collector.sample()
        result = collector.finish()
        self.assertTrue(result["known"], result)
        self.assertFalse(collector.thread.is_alive())
        self.assertEqual(result["average_cpu_cores"], 0)
        self.assertEqual(result["rss_bytes"], 4096)
        self.assertEqual(result["fd_count"], 12)

    @patch("native_ws_delivery.os.sysconf", return_value=100, create=True)
    @patch("native_ws_delivery.run")
    def test_query_failure_and_changed_identity_remain_unknown(self, run, _):
        for failure in ("query", "identity"):
            with self.subTest(failure=failure):
                first = SimpleNamespace(stdout=json.dumps({"process_identity": "first", "cpu_ticks": 100,
                                                          "rss_bytes": 4096, "fd_count": 12}))
                second = (RuntimeError("query unavailable") if failure == "query" else
                          SimpleNamespace(stdout=json.dumps({"process_identity": "replacement", "cpu_ticks": 100,
                                                             "rss_bytes": 4096, "fd_count": 12})))
                run.side_effect = [first, second, first]
                collector = ContainerResources(SimpleNamespace(gateway="isolated-gateway"))
                collector.sample()
                result = collector.finish()
                self.assertFalse(result["known"], result)
                self.assertTrue(result["errors"], result)
                self.assertFalse(collector.thread.is_alive())


class ProtocolSoakEvidenceTest(unittest.TestCase):
    def phase(self):
        return {"protocol": 2, "requested_seconds": 900,
                "limits": {"rss_bytes": 256 << 20, "fd_count": 256, "average_cpu_cores": 1,
                           "first_text_p95_ns": 2_000_000_000, "complete_p95_ns": 3_000_000_000,
                           "event_upper_p95_ns": 1_000_000_000},
                "resources": {"known": True, "rss_bytes": 4096, "fd_count": 12, "average_cpu_cores": .1},
                "report": {"errors": None, "started": 1, "completed": 1, "active_after": 0,
                           "elapsed_ns": 900_000_000_000,
                           "samples": [{"protocol": 2, "provider_protocol": 2, "provider_alpn": "h2",
                                        "tls": "TLS 1.3", "provider_tls": "TLS 1.3", "attempts": 1,
                                        "drained": True, "first_text_ns": 100, "complete_ns": 200,
                                        "event_upper_ns": [10] * 10}]}}

    def test_complete_evidence_passes(self):
        phase = self.phase()
        assess_protocol_soak(phase)
        self.assertTrue(phase["passed"], phase)

    def test_missing_or_malformed_events_preserve_failure_report(self):
        for malformed in (None, "not-an-array", [None] * 10, ["10"] * 10, [True] * 10, [10] * 9):
            with self.subTest(malformed=malformed):
                phase = copy.deepcopy(self.phase())
                phase["report"]["samples"][0]["event_upper_ns"] = malformed
                phase["report"]["samples"][0].update(status=429, attempts=0, drained=False,
                                                     error="original no-send rejection")
                phase["report"].update(completed=0, active_after=1, errors=["original failure"])
                assess_protocol_soak(phase)
                self.assertFalse(phase["passed"])
                self.assertIn("TLS/protocol/attempt/event correctness failed", phase["errors"])
                self.assertIsNone(phase["observed_p95_ns"]["event_upper_p95_ns"])
                self.assertEqual(phase["report"]["errors"], ["original failure"])


if __name__ == "__main__":
    unittest.main()
