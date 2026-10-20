"""The capacity gate must retain failure and reject unknown evidence."""
import copy
import unittest

from capacity_curve import acceptance


def complete_report():
    return {"planned_measurement_requests": 8, "started_measurement_requests": 8,
            "planned_cancellations": 2, "observed_cancellations": 2,
            "resources": [{"fixture_active_after_drain": 0}],
            "sustained": [{"started": 10, "active_after_drain": 0,
                "resource_budget": {"passed": True, "known": True, "enforced": True},
                "summary": {"completed": 10,
                    "successful_latency_ns": {"first_effective_text_ns": {"p95": 20_000_000},
                                              "elapsed_ns": {"p95": 900_000_000}},
                    "successful_event_latency_ns": {"latency_upper_ns": {"p95": 40_000_000}}}}]}


class CapacityAcceptanceTest(unittest.TestCase):
    def test_complete_declared_evidence_passes(self):
        self.assertTrue(acceptance(complete_report())["passed"])

    def test_missing_failed_and_unknown_evidence_never_passes(self):
        for failure in ("missing_start", "cancellation", "resource_unknown", "resource_not_enforced",
                        "active", "missing_latency", "slow_first_output", "missing_resources", "fatal", "failed_samples"):
            with self.subTest(failure=failure):
                report = copy.deepcopy(complete_report())
                sustained = report["sustained"][0]
                if failure == "missing_start":
                    report["started_measurement_requests"] = 7
                elif failure == "cancellation":
                    report["observed_cancellations"] = 1
                elif failure == "resource_unknown":
                    sustained["resource_budget"]["known"] = False
                elif failure == "resource_not_enforced":
                    sustained["resource_budget"]["enforced"] = False
                elif failure == "active":
                    report["resources"][0]["fixture_active_after_drain"] = 1
                elif failure == "missing_latency":
                    sustained["summary"]["successful_event_latency_ns"] = {}
                elif failure == "slow_first_output":
                    sustained["summary"]["successful_latency_ns"]["first_effective_text_ns"]["p95"] = 2_000_000_001
                elif failure == "missing_resources":
                    report["resources"] = []
                elif failure == "fatal":
                    report["fatal_error"] = "fixture failed"
                elif failure == "failed_samples":
                    sustained["summary"]["completed"] = 9
                result = acceptance(report)
                self.assertFalse(result["passed"], result)
                self.assertTrue(result["errors"], result)


if __name__ == "__main__":
    unittest.main()
