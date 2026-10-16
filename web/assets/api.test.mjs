import { test } from "node:test";
import assert from "node:assert/strict";
import { createClient, channelInput, keyInput, userInput, list } from "./api.mjs";

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
