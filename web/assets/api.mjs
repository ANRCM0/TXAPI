export const list = value => Array.isArray(value) ? value : [];
export const models = value => [...new Set(String(value || "").split(/[\s,]+/).filter(Boolean))];

export function policyInput(values) {
  return { model_mode: values.model_mode, models: values.model_mode === "all" ? [] : models(values.models) };
}

function limit(value, integer = false) {
  const number = Number(value || 0);
  if (!Number.isFinite(number) || number < 0 || (integer && !Number.isSafeInteger(number))) {
    throw new Error("Limits must be finite, non-negative numbers; request and concurrency limits must be integers.");
  }
  return number;
}

export function userInput(values) {
  return {
    username: values.username.trim(), password: values.password || "",
    role: values.role, status: values.status, ...policyInput(values),
    quota: limit(values.quota), rpm_limit: limit(values.rpm_limit, true),
    tpm_limit: limit(values.tpm_limit, true), concurrency_limit: limit(values.concurrency_limit, true),
  };
}

export function keyInput(values) {
  return {
    name: values.name, status: values.status, ...policyInput(values),
    quota: limit(values.quota), rpm_limit: limit(values.rpm_limit, true),
    tpm_limit: limit(values.tpm_limit, true), concurrency_limit: limit(values.concurrency_limit, true),
    expires_in_seconds: values.expiry_action === "keep" ? 0 : limit(values.expires_in_seconds, true),
    preserve_expiry: values.expiry_action === "keep",
  };
}

export function channelInput(values) {
  const mapping = JSON.parse(values.model_mapping || "{}");
  if (!mapping || Array.isArray(mapping) || typeof mapping !== "object" ||
      Object.values(mapping).some(value => typeof value !== "string")) {
    throw new Error("Model mapping must be a JSON object with string values.");
  }
  const priority = Number(values.priority || 0);
  if (!Number.isSafeInteger(priority)) throw new Error("Priority must be an integer.");
  const result = {
    name: values.name.trim(), type: values.type, base_url: values.base_url.trim(),
    models: models(values.models), model_mapping: mapping, group_name: values.group_name || "default",
    priority, weight: limit(values.weight, true), status: values.status,
  };
  // An edited channel must not send its masked key back or replace the pool.
  if (values.api_key) result.api_key = values.api_key;
  return result;
}

export function ledgerMoney(value) {
  if (!/^\d+$/.test(String(value))) throw new Error("Invalid ledger amount.");
  const units = BigInt(value);
  const fraction = (units % 1000000000n).toString().padStart(9, "0").replace(/0+$/, "");
  return `${units / 1000000000n}${fraction ? `.${fraction}` : ""}`;
}

export function ledgerAmounts(row) {
  const amount = value => typeof value === "string" && /^\d+$/.test(value) ? ledgerMoney(value) : "Unknown";
  const retained = ["reserved", "active", "pending"].includes(row.state);
  const settled = row.state === "settled";
  return {
    originalReservation: amount(row.reserved_units),
    retainedBudget: settled ? "0" : retained ? amount(row.reserved_units) : "Unknown",
    finalCharge: settled ? amount(row.charge_units) : retained ? "Not finalized" : "Unknown",
    providerEnvelope: amount(row.provider_held_units),
    providerBudget: amount(row.provider_budget_units),
  };
}

export function resolutionInput(values, id) {
  if (values.confirmation !== id) throw new Error("Enter the full billing ID to confirm this decision.");
  if (!["settle_usage", "cap_usage", "waive"].includes(values.action)) throw new Error("Choose a resolution action.");
  const result = { action: values.action, reason: String(values.reason || "").trim(), evidence: String(values.evidence || "").trim() };
  for (const [name, max] of [["reason", 1000], ["evidence", 2000]]) {
    if (!result[name] || new TextEncoder().encode(result[name]).length > max) {
      throw new Error(`${name} is required and must fit within ${max} UTF-8 bytes.`);
    }
  }
  const prompt = String(values.prompt_tokens ?? "").trim();
  const output = String(values.completion_tokens ?? "").trim();
  if (values.action === "waive") {
    if (prompt || output) throw new Error("Clear usage counts when waiving an uncertain charge.");
  } else {
    if (!/^\d+$/.test(prompt) || !/^\d+$/.test(output)) throw new Error("Enter both verified token counts, including explicit zeros.");
    const counts = [Number(prompt), Number(output)];
    if (counts.some(value => !Number.isSafeInteger(value)) || !Number.isSafeInteger(counts[0] + counts[1])) {
      throw new Error("Token counts exceed the supported browser integer range.");
    }
    result.usage = { prompt_tokens: counts[0], completion_tokens: counts[1] };
  }
  return result;
}

export function createClient({ onUnauthorized = () => {}, onPending = () => {}, fetcher = fetch } = {}) {
  let session = "";
  let sessionVersion = 0;
  const pending = new Set();
  return {
    setSession(value) {
      sessionVersion++;
      for (const controller of pending) controller.abort();
      pending.clear();
      session = value;
    },
    async request(path, { method = "GET", body, callKey, signal, timeout = 20000 } = {}) {
      // Browsers normalize backslashes and remove ASCII whitespace before resolving URLs.
      if (typeof path !== "string" || !path.startsWith("/") || path.startsWith("//") || /[\\\u0000-\u0020\u007f]/.test(path)) {
        throw new Error("Only same-origin API paths are allowed.");
      }
      const controller = new AbortController();
      const capturedSession = session;
      const capturedVersion = sessionVersion;
      const abort = () => controller.abort();
      signal?.addEventListener("abort", abort, { once: true });
      if (signal?.aborted) controller.abort();
      pending.add(controller);
      const timer = setTimeout(abort, timeout);
      try {
        const headers = { Accept: "application/json" };
        const token = callKey === undefined ? capturedSession : callKey;
        if (token) headers.Authorization = `Bearer ${token}`;
        if (body !== undefined) headers["Content-Type"] = "application/json";
        const response = await fetcher(path, {
          method, headers, body: body === undefined ? undefined : JSON.stringify(body),
          cache: "no-store", credentials: "omit", redirect: "error", signal: controller.signal,
        });
        const text = await response.text();
        if (controller.signal.aborted || capturedVersion !== sessionVersion) throw new Error("Request canceled or timed out.");
        let value;
        try { value = text ? JSON.parse(text) : {}; }
        catch { throw new Error(`Unexpected server response (HTTP ${response.status}).`); }
        const publication = response.headers.get("X-TXAPI-Publication-State");
        if (publication === "pending") onPending();
        if (!response.ok) {
          if (response.status === 401 && callKey === undefined && capturedSession && capturedSession === session) {
            onUnauthorized();
          }
          const error = new Error(value.error?.message || `Request failed (HTTP ${response.status}).`);
          error.status = response.status;
          error.data = value.data;
          error.body = value;
          error.type = value.error?.type;
          error.saved = value.saved === true;
          error.publication = publication;
          throw error;
        }
        return value;
      } catch (error) {
        if (controller.signal.aborted) throw new Error("Request canceled or timed out.");
        throw error;
      } finally {
        clearTimeout(timer);
        pending.delete(controller);
        signal?.removeEventListener("abort", abort);
      }
    },
  };
}
