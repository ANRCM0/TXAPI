import { test } from "node:test";
import assert from "node:assert/strict";
import { knownCount, ownerAmounts, logSample, csvText } from "./workspace.mjs";

test("native quantities preserve exact int64 values, known zero and unknown", () => {
  assert.equal(knownCount("0"), "0");
  assert.equal(knownCount("9223372036854775807"), "9,223,372,036,854,775,807");
  for (const value of [null, undefined, 0, "", "00", "-1", "1.5", "9223372036854775808"]) {
    assert.equal(knownCount(value), "未知");
  }
});

test("owner ledger separates original reservation, actual retention and finalization", () => {
  const original = Object.freeze({ original_reserved_units: "100000001", held_units: "100000001", charge_units: "0" });
  assert.deepEqual(ownerAmounts({ ...original, state: "pending" }), { reserved: "0.100000001", held: "0.100000001", charge: "未结算" });
  assert.deepEqual(ownerAmounts({ ...original, state: "settled", held_units: "0" }), { reserved: "0.100000001", held: "0", charge: "0" });
  assert.equal(ownerAmounts({ ...original, state: "settled", charge_units: null }).charge, "未知");
  assert.equal(ownerAmounts({ state: "settled", held_units: "9223372036854775807" }).held, "9223372036.854775807");
});

test("time and model charts use only the supplied, valid retained sample", () => {
  const now = Date.UTC(2026, 9, 23, 12);
  const sample = logSample([
    { created_at: new Date(now).toISOString(), origin_model: "m1" },
    { created_at: new Date(now - 3600000).toISOString(), origin_model: "m2" },
    { created_at: new Date(now - 3600000).toISOString(), origin_model: "m1" },
    { created_at: "not-time", origin_model: "m3" },
    { created_at: null, origin_model: "m3" },
    { created_at: new Date(now + 1).toISOString(), origin_model: "future" },
    { created_at: new Date(now - 25 * 3600000).toISOString(), origin_model: "old" },
  ], now);
  assert.equal(sample.count, 3);
  assert.equal(sample.unknownTime, 2);
  assert.equal(sample.buckets.reduce((sum, b) => sum + b.requests, 0), 3);
  assert.deepEqual(sample.models, [{ name: "m1", requests: 2 }, { name: "m2", requests: 1 }]);
  assert.equal(logSample([], now).count, 0);
  assert.equal(logSample([], now, 1000000).buckets.length, 24);
});

test("rolling sample range includes both endpoints and the partial leading hour", () => {
  const now = Date.UTC(2026, 9, 23, 12, 37, 29);
  for (const hours of [6, 24]) {
    const start = now - hours * 3600000;
    const sample = logSample([start - 1, start, start + 1, now, now + 1].map(time => ({
      created_at: new Date(time).toISOString(), origin_model: "boundary-model",
    })), now, hours);
    assert.equal(sample.count, 3);
    assert.equal(sample.buckets[0].time, start);
    assert.equal(sample.buckets[0].requests, 2);
    assert.equal(sample.buckets.at(-1).requests, 1);
    assert.equal(sample.buckets.reduce((sum, bucket) => sum + bucket.requests, 0), sample.count);
  }
});

test("metadata CSV exports quote values and neutralize spreadsheet formulas", () => {
  const text = csvText(["模型", "数量"], [['=HYPERLINK("https://invalid")', "0"], ["+cmd", null], [" @SUM(1)", 'with "quotes"']]);
  assert.ok(text.startsWith("\uFEFF"));
  assert.ok(text.includes('\"\'=HYPERLINK(\"\"https://invalid\"\")\"'));
  assert.ok(text.includes('\"\' @SUM(1)\"'));
  assert.ok(text.includes('"with ""quotes"""'));
  assert.ok(text.includes('"0"'));
});
