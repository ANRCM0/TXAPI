import { test } from "node:test";
import assert from "node:assert/strict";
import { createClient, channelInput, keyInput, userInput, list, ledgerMoney, ledgerAmounts, resolutionInput } from "./api.mjs";

test("nullable lists and full-update payloads preserve restrictions", () => {
  assert.deepEqual(list(null), []);
  const user = userInput({ username: " alice ", password: "", role: "user", status: "disabled", model_mode: "allowlist",
    models: "m1, m2 m1", quota: "3.5", rpm_limit: "4", tpm_limit: "50", concurrency_limit: "2" });
  assert.equal(user.status, "disabled");
  assert.equal(user.password, "");
  assert.deepEqual(user.models, ["m1", "m2"]);
  assert.deepEqual(keyInput({ name: "restricted", status: "active", model_mode: "allowlist", models: "" }).models, []);
  assert.throws(() => keyInput({ rpm_limit: "1.5" }));
  assert.throws(() => keyInput({ quota: "Infinity" }));
  assert.equal(keyInput({ expiry_action: "keep", expires_in_seconds: "3600" }).preserve_expiry, true);
  assert.equal(keyInput({ expiry_action: "keep", expires_in_seconds: "3600" }).expires_in_seconds, 0);
});

test("editing channel does not send masked keys or wipe its pool", () => {
  const channel = channelInput({ name: "local", type: "openai", base_url: "http://localhost:9911", models: "m1",
    status: "enabled", priority: "0", weight: "1", model_mapping: '{"m1":"actual"}' });
  assert.equal("api_key" in channel, false);
  assert.deepEqual(channel.model_mapping, { m1: "actual" });
  assert.throws(() => channelInput({ model_mapping: "[]" }));
});

test("ledger amounts preserve precision and decisions require explicit confirmation and usage", () => {
  assert.equal(ledgerMoney("9223372036854775807"), "9223372036.854775807");
  assert.equal(ledgerMoney("1"), "0.000000001");
  assert.equal(ledgerMoney("100000000"), "0.1");
  assert.throws(() => ledgerMoney("unknown"));
  const id = "0123456789abcdef0123456789abcdef";
  const values = { confirmation: id, action: "settle_usage", reason: " verified ", evidence: "receipt",
    prompt_tokens: "11", completion_tokens: "0", actor_id: 999, actual_model: "free" };
  assert.deepEqual(resolutionInput(values, id), { action: "settle_usage", reason: "verified", evidence: "receipt",
    usage: { prompt_tokens: 11, completion_tokens: 0 } });
  assert.throws(() => resolutionInput({ ...values, confirmation: "wrong" }, id));
  assert.throws(() => resolutionInput({ ...values, completion_tokens: "" }, id));
  assert.throws(() => resolutionInput({ ...values, prompt_tokens: "9007199254740992" }, id));
  assert.throws(() => resolutionInput({ ...values, action: "waive" }, id));
  assert.deepEqual(resolutionInput({ ...values, action: "waive", prompt_tokens: "", completion_tokens: "" }, id),
    { action: "waive", reason: "verified", evidence: "receipt" });
});

test("ledger projection separates current retention, final charges and independent historical envelopes", () => {
  const recorded = Object.freeze({ reserved_units: "100000001", charge_units: "1650000",
    provider_held_units: "5760000", provider_budget_units: "100000000" });
  for (const [state, retainedBudget, finalCharge] of [
    ["reserved", "0.100000001", "Not finalized"],
    ["active", "0.100000001", "Not finalized"],
    ["pending", "0.100000001", "Not finalized"],
    ["settled", "0", "0.00165"],
    ["future-state", "Unknown", "Unknown"],
    [undefined, "Unknown", "Unknown"],
  ]) {
    const row = Object.freeze({ ...recorded, state });
    assert.deepEqual(ledgerAmounts(row), { originalReservation: "0.100000001", retainedBudget, finalCharge,
      providerEnvelope: "0.00576", providerBudget: "0.1" }, String(state));
  }
  assert.equal(ledgerAmounts({ ...recorded, state: "pending", charge_units: "0" }).finalCharge, "Not finalized");
  assert.deepEqual(ledgerAmounts({ state: "settled", reserved_units: "9223372036854775807", charge_units: "0",
    provider_held_units: "9223372036854775807", provider_budget_units: "9223372036854775807" }), {
    originalReservation: "9223372036.854775807", retainedBudget: "0", finalCharge: "0",
    providerEnvelope: "9223372036.854775807", providerBudget: "9223372036.854775807",
  });
});

test("missing or invalid ledger amounts remain unknown rather than becoming zero", () => {
  for (const invalid of [undefined, null, "", "unknown", "-1", "1.5", " 0 ", 0, 100000001, false, {}]) {
    const row = { state: "settled", reserved_units: invalid, charge_units: invalid,
      provider_held_units: invalid, provider_budget_units: invalid };
    assert.deepEqual(ledgerAmounts(row), { originalReservation: "Unknown", retainedBudget: "0", finalCharge: "Unknown",
      providerEnvelope: "Unknown", providerBudget: "Unknown" }, String(invalid));
    assert.equal(ledgerAmounts({ ...row, state: "pending" }).retainedBudget, "Unknown");
  }
  assert.deepEqual(ledgerAmounts({}), { originalReservation: "Unknown", retainedBudget: "Unknown", finalCharge: "Unknown",
    providerEnvelope: "Unknown", providerBudget: "Unknown" });
  assert.deepEqual(ledgerAmounts({ state: "reserved", reserved_units: "1", provider_held_units: "0",
    provider_budget_units: "1000000000" }), { originalReservation: "0.000000001", retainedBudget: "0.000000001",
    finalCharge: "Not finalized", providerEnvelope: "0", providerBudget: "1" });
});

test("API client separates sessions from call keys and warns about saved/pending config", async () => {
  const calls = [];
  let warnings = 0;
  const client = createClient({ onPending: () => warnings++, fetcher: async (path, options) => {
    calls.push({ path, ...options });
    return new Response('{"data":[]}', { headers: { "X-TXAPI-Publication-State": "pending" } });
  } });
  client.setSession("session-only");
  await client.request("/api/me");
  await client.request("/v1/chat/completions", { method: "POST", callKey: "call-only", body: { model: "m1" } });
  assert.equal(calls[0].headers.Authorization, "Bearer session-only");
  assert.equal(calls[1].headers.Authorization, "Bearer call-only");
  assert.equal(calls[1].credentials, "omit");
  assert.equal(warnings, 2);
  await assert.rejects(client.request("//other.example/api/me"));
});

test("API paths reject browser authority normalization before attaching credentials", async () => {
  const calls = [];
  const client = createClient({ fetcher: async (path, options) => {
    calls.push({ path, options });
    return new Response('{}');
  } });
  client.setSession("private-session");
  for (const path of ["//evil.example/api", "/\\evil.example/api", "/\n/evil.example/api",
    "/\t/evil.example/api", "https://evil.example/api", " /api/me", "/api/\rme", null]) {
    await assert.rejects(client.request(path));
  }
  assert.equal(calls.length, 0);
  for (const path of ["/api/me", "/api/me/keys?limit=20", "/api/me/keys/a%2Fb"]) await client.request(path);
  assert.equal(calls.length, 3);
  assert.ok(calls.every(call => call.options.headers.Authorization === "Bearer private-session"));
});

test("invalid call keys do not sign out a valid management session", async () => {
  let signedOut = 0;
  const client = createClient({ onUnauthorized: () => signedOut++, fetcher: async () =>
    new Response('{"error":{"message":"invalid"}}', { status: 401 }) });
  client.setSession("valid-session");
  await assert.rejects(client.request("/v1/models", { callKey: "invalid-call-key" }));
  assert.equal(signedOut, 0);
  await assert.rejects(client.request("/api/me"));
  assert.equal(signedOut, 1);
});

test("sign out cancels in-flight requests instead of displaying the previous user's data", async () => {
  const client = createClient({ fetcher: async (_path, { signal }) => new Promise((_resolve, reject) => {
    signal.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
  }) });
  client.setSession("old-session");
  const request = client.request("/api/me");
  client.setSession("");
  await assert.rejects(request, /canceled/);
});

test("saved failures retain mutation facts and publication failures retain current status", async () => {
  let warnings = 0;
  const saved = { saved: true, error: { type: "saved_configuration_error", message: "Saved; refresh failed." } };
  const publication = { channels: { desired_revision: 3, applied_revision: 1, last_error: "Unavailable" }, prices: {} };
  const client = createClient({ onPending: () => warnings++, fetcher: async path => path.endsWith("/apply")
    ? new Response(JSON.stringify({ data: publication }), { status: 503 })
    : new Response(JSON.stringify(saved), { status: 500, headers: { "X-TXAPI-Publication-State": "pending" } }) });
  await assert.rejects(client.request("/api/admin/channels", { method: "POST", body: {} }), error => {
    assert.equal(error.saved, true);
    assert.equal(error.type, "saved_configuration_error");
    assert.equal(error.status, 500);
    assert.equal(error.publication, "pending");
    assert.deepEqual(error.body, saved);
    return true;
  });
  assert.equal(warnings, 1);
  await assert.rejects(client.request("/api/admin/config-status/apply", { method: "POST" }), error => {
    assert.deepEqual(error.data, publication);
    return true;
  });
});

test("saved applied and unknown failures never report pending publication", async () => {
  for (const publication of ["applied", null]) {
    let warnings = 0;
    const client = createClient({ onPending: () => warnings++, fetcher: async () => new Response(
      '{"saved":true,"error":{"type":"saved_configuration_error","message":"Saved; readback failed."}}',
      { status: 500, headers: publication ? { "X-TXAPI-Publication-State": publication } : {} }) });
    await assert.rejects(client.request("/api/admin/channels", { method: "POST", body: {} }), error => {
      assert.equal(error.saved, true);
      assert.equal(error.publication, publication);
      assert.equal(error.status, 500);
      return true;
    });
    assert.equal(warnings, 0, `saved ${publication || "unknown"} was reported as pending`);
  }
});

test("old session responses cannot deliver data or publish warnings after a session switch", async () => {
  let complete;
  let warnings = 0;
  const client = createClient({ onPending: () => warnings++, fetcher: async () => new Promise(resolve => { complete = resolve; }) });
  client.setSession("previous");
  const pending = client.request("/api/me");
  client.setSession("current");
  complete(new Response('{"data":{"role":"admin"}}', { headers: { "X-TXAPI-Publication-State": "pending" } }));
  await assert.rejects(pending, /canceled/);
  assert.equal(warnings, 0);
});

test("forbidden management requests preserve the login session", async () => {
  let signedOut = 0;
  const client = createClient({ onUnauthorized: () => signedOut++, fetcher: async () =>
    new Response('{"error":{"type":"forbidden","message":"administrator required"}}', { status: 403 }) });
  client.setSession("current");
  await assert.rejects(client.request("/api/admin/users"), error => error.status === 403 && error.type === "forbidden");
  assert.equal(signedOut, 0);
});
