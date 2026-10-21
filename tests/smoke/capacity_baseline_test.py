"""Finite fixture receipts must cover all phases without hiding Key overload."""
import unittest

from capacity_baseline import responses_limits, RECOVERY_PROBE_CAP, SUSTAINED_REQUEST_CAP


class ResponsesCapacityBudgetTest(unittest.TestCase):
    def test_largest_supported_plans_fit_production_bounds(self):
        for workers in (1, 4, 8, 16):
            for rounds in (1, 4):
                requests = min(200, 3200 // (workers * rounds))
                for sustained in (False, True):
                    with self.subTest(workers=workers, rounds=rounds, sustained=sustained):
                        limits = responses_limits(workers, requests, rounds, sustained)
                        phases = rounds + int(sustained)
                        accepted = workers * requests * rounds + phases * (3 * workers + 1 + RECOVERY_PROBE_CAP)
                        accepted += SUSTAINED_REQUEST_CAP if sustained else 0
                        self.assertGreaterEqual(limits["owner_records"], accepted)
                        self.assertLess(limits["owner_records"], limits["instance_records"])
                        self.assertLessEqual(limits["instance_records"], 65536)
                        self.assertGreater(limits["owner_requests"], workers)
                        self.assertLess(limits["owner_requests"], limits["active_requests"])
                        self.assertLessEqual(limits["active_requests"], 128)


if __name__ == "__main__":
    unittest.main()
