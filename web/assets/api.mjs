export const list = value => Array.isArray(value) ? value : [];
export const models = value => [...new Set(String(value || "").split(/[\s,]+/).filter(Boolean))];

export function policyInput(values) {
  return { model_mode: values.model_mode, models: values.model_mode === "all" ? [] : models(values.models) };
}

function limit(value, integer = false) {
  const number = Number(value || 0);
  if (!Number.isFinite(number) || number < 0 || (integer && !Number.isSafeInteger(number))) {
    throw new Error("限制必须是有限的非负数；请求和并发上限必须为整数。");
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
  let mapping;
  try { mapping = JSON.parse(values.model_mapping || "{}"); }
  catch { throw new Error('模型映射必须是有效 JSON，例如 {"公开模型":"上游模型"}。'); }
  if (!mapping || Array.isArray(mapping) || typeof mapping !== "object" ||
      Object.values(mapping).some(value => typeof value !== "string")) {
    throw new Error("模型映射必须是仅包含文本值的 JSON 对象。");
  }
  const priority = Number(values.priority || 0);
  if (!Number.isSafeInteger(priority)) throw new Error("渠道优先级必须为整数。");
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
  if (!/^\d+$/.test(String(value))) throw new Error("账本金额格式无效。");
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
  if (values.confirmation !== id) throw new Error("请输入完整财务 ID，确认对这条记录执行处理。");
  if (!["settle_usage", "cap_usage", "waive"].includes(values.action)) throw new Error("请选择处理方式。");
  const result = { action: values.action, reason: String(values.reason || "").trim(), evidence: String(values.evidence || "").trim() };
  for (const [name, max] of [["reason", 1000], ["evidence", 2000]]) {
    if (!result[name] || new TextEncoder().encode(result[name]).length > max) {
      throw new Error(`${name === "reason" ? "处理原因" : "证据说明"}不能为空，且不能超过 ${max} 个 UTF-8 字节。`);
    }
  }
  const prompt = String(values.prompt_tokens ?? "").trim();
  const output = String(values.completion_tokens ?? "").trim();
  if (values.action === "waive") {
    if (prompt || output) throw new Error("免除未知费用时，请先清空输入和输出 Token 数量。");
  } else {
    if (!/^\d+$/.test(prompt) || !/^\d+$/.test(output)) throw new Error("请填写已核实的输入和输出 Token 数量；确认是零时请填 0。");
    const counts = [Number(prompt), Number(output)];
    if (counts.some(value => !Number.isSafeInteger(value)) || !Number.isSafeInteger(counts[0] + counts[1])) {
      throw new Error("Token 数量超出浏览器可精确处理的整数范围。");
    }
    result.usage = { prompt_tokens: counts[0], completion_tokens: counts[1] };
  }
  return result;
}

function requestError(code, message, details = {}) {
  return Object.assign(new Error(message), { code, ...details }, code === "canceled" ? { name: "AbortError" } : {});
}

export function createClient({ onUnauthorized = () => {}, onPending = () => {}, fetcher = fetch } = {}) {
  let session = "";
  let sessionVersion = 0;
  const pending = new Set();
  return {
    setSession(value) {
      sessionVersion++;
      for (const controller of pending) controller.abort("session");
      pending.clear();
      session = value;
    },
    async request(path, { method = "GET", body, callKey, signal, timeout = 20000 } = {}) {
      // Browsers normalize backslashes and remove ASCII whitespace before resolving URLs.
      if (typeof path !== "string" || !path.startsWith("/") || path.startsWith("//") || /[\\\u0000-\u0020\u007f]/.test(path)) {
        throw new Error("请求地址必须是本站路径。");
      }
      const controller = new AbortController();
      const capturedSession = session;
      const capturedVersion = sessionVersion;
      const mutation = !["GET", "HEAD", "OPTIONS"].includes(String(method).toUpperCase());
      let dispatched = false;
      const uncertain = message => mutation && dispatched
        ? message + " 更改结果尚未确认，请先刷新列表或状态，避免重复提交。" : message;
      const aborted = () => requestError(controller.signal.reason === "timeout" ? "timeout" : "canceled",
        uncertain(controller.signal.reason === "timeout" ? "请求等待超时。" : "请求已取消。"),
        { operationUnknown: mutation && dispatched });
      const abort = () => controller.abort("canceled");
      signal?.addEventListener("abort", abort, { once: true });
      if (signal?.aborted) controller.abort();
      pending.add(controller);
      const timer = setTimeout(() => controller.abort("timeout"), timeout);
      try {
        if (controller.signal.aborted) throw aborted();
        const headers = { Accept: "application/json" };
        const token = callKey === undefined ? capturedSession : callKey;
        if (token) headers.Authorization = `Bearer ${token}`;
        if (body !== undefined) headers["Content-Type"] = "application/json";
        const options = {
          method, headers, body: body === undefined ? undefined : JSON.stringify(body),
          cache: "no-store", credentials: "omit", redirect: "error", signal: controller.signal,
        };
        dispatched = true;
        const response = await fetcher(path, options);
        if (controller.signal.aborted || capturedVersion !== sessionVersion) throw aborted();
        const publication = response.headers.get("X-TXAPI-Publication-State");
        const requestId = response.headers.get("X-Request-Id");
        const details = { status: response.status, publication };
        if (/^[A-Za-z0-9_.:-]{1,64}$/.test(requestId || "")) details.requestId = requestId;
        const unauthorized = response.status === 401 && callKey === undefined && capturedSession && capturedSession === session;
        if (unauthorized) {
          // Authorization is known at headers; a stalled body cannot retain an expired session.
          const error = requestError("http", "登录已过期，请重新登录。", { ...details, operationUnknown: false });
          controller.abort("unauthorized");
          onUnauthorized();
          throw error;
        }
        let text;
        try { text = await response.text(); }
        catch (error) { if (response.ok) throw error; }
        if (capturedVersion !== sessionVersion || (controller.signal.aborted &&
            (controller.signal.reason !== "timeout" || ![401, 403].includes(response.status)))) throw aborted();
        let value, valid = false;
        try {
          value = text === "" ? {} : JSON.parse(text);
          valid = value !== null && typeof value === "object" && !Array.isArray(value);
        } catch { /* An unreadable body must not hide the HTTP authorization status. */ }
        if (publication === "pending") onPending();
        if (!response.ok) {
          const fallback = response.status === 401 ? "登录凭据已失效，请重新登录或检查调用 Key。"
            : response.status === 403 ? "当前账户没有此操作权限，请刷新状态。"
              : `服务未能完成请求（HTTP ${response.status}）。`;
          const message = valid && typeof value.error?.message === "string" && value.error.message
            ? value.error.message : valid ? fallback : uncertain(fallback);
          const error = requestError("http", message, { ...details, operationUnknown: mutation && !valid });
          if (valid) {
            error.data = value.data;
            error.body = value;
            error.type = value.error?.type;
            error.saved = value.saved === true;
          }
          throw error;
        }
        if (!valid) throw requestError("invalid_response", uncertain(`服务返回了无法识别的内容（HTTP ${response.status}）。`),
          { ...details, operationUnknown: mutation });
        return value;
      } catch (error) {
        if (["canceled", "timeout", "http", "invalid_response"].includes(error.code)) throw error;
        if (controller.signal.aborted || capturedVersion !== sessionVersion) throw aborted();
        if (dispatched) throw requestError("network", uncertain("网络连接中断，请检查网络后再读取状态。"),
          { operationUnknown: mutation });
        throw error;
      } finally {
        clearTimeout(timer);
        pending.delete(controller);
        signal?.removeEventListener("abort", abort);
      }
    },
  };
}
