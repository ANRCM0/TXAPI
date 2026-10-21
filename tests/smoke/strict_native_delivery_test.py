"""Consumer evidence must distinguish unknown cost from proven zero usage."""
import copy
import unittest

from strict_native_delivery import validate_ledger, redact_report, CLIENT_KEY, PROVIDER_KEY, ADMIN


def evidence(usage):
    known = usage in ("known", "zero")
    charge = 900_000 if usage == "known" else 0
    return {"billing_reservations": [{"id": "r1", "reserved_units": 1_000_000_000,
                "provider_budget_units": 100_000_000, "provider_held_units": 4_160_000,
                "provider_policy_version": "fixture-v1", "state": "settled" if known else "pending",
                "charge_units": charge, "lease_until": 0}],
            "billing_attempts": [{"reservation_id": "r1", "number": 1,
                "provider_bound_units": 4_160_000, "cost_known": int(known),
                "cost_units": 900_000 if usage == "known" else 0,
                "prompt_tokens": 2 if usage == "known" else 0, "output_tokens": 1 if usage == "known" else 0}],
            "billing_balances": [{"owner_type": owner, "owner_id": 1, "used_units": charge,
                "reserved_units": 0 if known else 1_000_000_000} for owner in ("user", "key")],
            "billing_resolutions": [], "background_jobs": []}


class StrictConsumerEvidenceTest(unittest.TestCase):
    def test_failure_diagnostics_redact_synthetic_credentials(self):
        value = {"error": "command -key " + CLIENT_KEY,
                 "logs": [PROVIDER_KEY, ADMIN["password"]]}
        result = redact_report(value)
        self.assertNotIn(CLIENT_KEY, str(result))
        self.assertNotIn(PROVIDER_KEY, str(result))
        self.assertNotIn(ADMIN["password"], str(result))
        self.assertIn("[REDACTED]", result["error"])

    def test_known_zero_and_unknown_are_distinct(self):
        for usage in ("known", "zero", "unknown"):
            with self.subTest(usage=usage):
                self.assertTrue(validate_ledger(evidence(usage), {"r1": {"usage": usage, "scenario": "fixture"}}))

    def test_provider_tariff_arithmetic_is_not_the_attempt_selling_cost(self):
        value = evidence("known")
        value["billing_attempts"][0]["cost_units"] = 4_000
        with self.assertRaises(AssertionError):
            validate_ledger(value, {"r1": {"usage": "known", "scenario": "fixture"}})
        result = validate_ledger(evidence("known"), {"r1": {"usage": "known", "scenario": "fixture"}})
        self.assertEqual(result["reservations"][0]["attempt_selling_cost_units"], 900_000)

    def test_unknown_refund_duplicate_attempt_and_one_sided_balance_fail(self):
        original = evidence("unknown")
        for kind in ("refund", "zero_invention", "provider_hold", "duplicate", "owner_balance", "resolution"):
            with self.subTest(kind=kind):
                value = copy.deepcopy(original)
                if kind == "refund":
                    value["billing_reservations"][0]["state"] = "settled"
                elif kind == "zero_invention":
                    value["billing_attempts"][0]["cost_known"] = 1
                elif kind == "provider_hold":
                    value["billing_reservations"][0]["provider_held_units"] = 0
                elif kind == "duplicate":
                    value["billing_attempts"].append(copy.deepcopy(value["billing_attempts"][0]))
                elif kind == "owner_balance":
                    value["billing_balances"][0]["reserved_units"] = 0
                elif kind == "resolution":
                    value["billing_resolutions"] = [{"reservation_id": "r1"}]
                with self.assertRaises(AssertionError):
                    validate_ledger(value, {"r1": {"usage": "unknown", "scenario": "fixture"}})


if __name__ == "__main__":
    unittest.main()
