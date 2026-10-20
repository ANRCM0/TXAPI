"""Read-only container sampling rejects gaps and process replacement."""
import json
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from native_ws_delivery import ContainerResources


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


if __name__ == "__main__":
    unittest.main()
