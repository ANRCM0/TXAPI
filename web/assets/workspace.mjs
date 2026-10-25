const paths = {
  overview: ["M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z"],
  keys: ["M11 12l8 8 2-2-2-2 2-2-3-3-2 2", "M12 8a4 4 0 1 1-8 0 4 4 0 0 1 8 0"],
  models: ["M12 3l9 5-9 5-9-5 9-5M3 8v9l9 5 9-5V8M12 13v9"],
  logs: ["M3 12h4l3-8 4 16 3-8h4"],
  tasks: ["M12 7v5l3 2", "M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0"],
  channels: ["M12 3l9 5-9 5-9-5 9-5M3 12l9 5 9-5M3 16l9 5 9-5"],
  routing: ["M7 5h8a4 4 0 0 1 0 8H9a4 4 0 0 0 0 8h8M5 7v10"],
  billing: ["M20 8V4H6a3 3 0 0 0 0 6h14v11H4V7M20 14h-5v4h5"],
  users: ["M12 8a3 3 0 1 1-6 0 3 3 0 0 1 6 0M3 21v-3a6 6 0 0 1 12 0v3M16 5a3 3 0 0 1 0 6m2 3a5 5 0 0 1 3 5v2"],
  settings: ["M4 7h16M4 17h16M8 4v6M16 14v6"],
  search: ["M16 10a6 6 0 1 1-12 0 6 6 0 0 1 12 0M15 15l6 6"],
  menu: ["M4 6h16M4 12h16M4 18h16"],
  close: ["M6 6l12 12M6 18 18 6"],
  playground: ["M8 5l-6 7 6 7M16 5l6 7-6 7M13 3l-2 18"],
  copy: ["M8 8h12v12H8zM15 8V4H4v11h4"],
  arrow: ["M5 12h14M14 7l5 5-5 5"],
  help: ["M9 9a3 3 0 0 1 6 0c0 2-3 2-3 4M12 16h.01", "M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0"],
};

export function icon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  for (const d of paths[name] || paths.overview) {
    const path = document.createElementNS(svg.namespaceURI, "path");
    path.setAttribute("d", d);
    svg.append(path);
  }
  return svg;
}

export function knownCount(value) {
  if (typeof value !== "string" || !/^(0|[1-9]\d{0,18})$/.test(value) || BigInt(value) > 9223372036854775807n) return "未知";
  return BigInt(value).toLocaleString("zh-CN");
}

export function ownerAmounts(row) {
  const amount = value => {
    if (typeof value !== "string" || !/^(0|[1-9]\d{0,18})$/.test(value) || BigInt(value) > 9223372036854775807n) return "未知";
    const units = BigInt(value);
    const fraction = (units % 1000000000n).toString().padStart(9, "0").replace(/0+$/, "");
    return String(units / 1000000000n) + (fraction ? "." + fraction : "");
  };
  return {
    reserved: amount(row.original_reserved_units),
    held: amount(row.held_units),
    charge: row.state === "settled" ? amount(row.charge_units) : "未结算",
  };
}

// Counts describe only the supplied retained sample, never a full traffic history.
export function logSample(rows, now = Date.now(), hours = 24) {
  if (!Number.isInteger(hours) || hours < 1 || hours > 168) hours = 24;
  const start = now - hours * 3600000;
  const buckets = Array.from({ length: hours }, (_, i) => ({ time: start + i * 3600000, requests: 0 }));
  const models = new Map();
  let count = 0, unknownTime = 0;
  for (const row of rows) {
    const time = row.created_at === null || row.created_at === undefined || row.created_at === "" ? NaN : new Date(row.created_at).getTime();
    if (!Number.isFinite(time)) { unknownTime++; continue; }
    if (time < start || time > now) continue;
    const index = Math.min(hours - 1, Math.floor((time - start) / 3600000));
    if (!buckets[index]) continue;
    buckets[index].requests++;
    count++;
    const name = typeof row.origin_model === "string" && row.origin_model ? row.origin_model : "未记录模型";
    models.set(name, (models.get(name) || 0) + 1);
  }
  return {
    buckets, count, unknownTime,
    models: [...models].map(([name, requests]) => ({ name, requests })).sort((a, b) => b.requests - a.requests),
  };
}

export function sampleChart(sample, width = 800) {
  width = Math.max(220, Math.min(1000, Math.round(width)));
  const ns = "http://www.w3.org/2000/svg";
  const chart = document.createElementNS(ns, "svg");
  chart.setAttribute("viewBox", `0 0 ${width} 260`);
  chart.setAttribute("class", "trend-chart");
  chart.setAttribute("role", "img");
  const summary = `最近保留日志样本：${sample.count} 次请求，${sample.buckets.length} 小时，按小时分布；不代表全部请求`;
  chart.setAttribute("aria-label", summary);
  const make = (tag, attrs, text) => {
    const node = document.createElementNS(ns, tag);
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
    if (text !== undefined) node.textContent = text;
    chart.append(node);
    return node;
  };
  make("title", {}, summary);
  make("desc", {}, "时间按浏览器本地时区显示；日期在起点和跨日后标记。精确数量可展开下方按小时样本数据查看。");
  const peak = Math.max(1, ...sample.buckets.map(b => b.requests));
  const ticks = Math.min(3, peak);
  const max = Math.ceil(peak / ticks) * ticks;
  for (let i = 0; i <= ticks; i++) {
    const y = 18 + i * 174 / ticks;
    make("line", { x1: 42, y1: y, x2: width - 18, y2: y, class: "chart-grid" });
    make("text", { x: 32, y: y + 4, "text-anchor": "end", class: "chart-label" }, String(max - i * max / ticks));
  }
  const points = sample.buckets.map((b, i) => [42 + i * (width - 60) / Math.max(1, sample.buckets.length - 1), 192 - b.requests / max * 174]);
  if (sample.count) {
    make("path", { d: points.map(([x, y], i) => (i ? "L" : "M") + x + " " + y).join(" "), class: "chart-line" });
    sample.buckets.forEach((b, i) => {
      const dot = make("circle", { cx: points[i][0], cy: points[i][1], r: 4, class: "chart-point" });
      const title = document.createElementNS(ns, "title");
      title.textContent = new Date(b.time).toLocaleString("zh-CN") + " · 样本内 " + b.requests + " 次";
      dot.append(title);
    });
  }
  const step = Math.max(1, Math.ceil(sample.buckets.length / Math.max(2, Math.floor((width - 60) / 75))));
  let previousDate;
  for (let i = 0; i < sample.buckets.length; i += step) {
    const date = new Date(sample.buckets[i].time);
    const dateKey = `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
    make("text", { x: points[i][0], y: 225, "text-anchor": "middle", class: "chart-label" },
      date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" }));
    if (dateKey !== previousDate) {
      make("text", { x: points[i][0], y: 244, "text-anchor": "middle", class: "chart-label chart-date" },
        date.toLocaleDateString("zh-CN", { month: "2-digit", day: "2-digit" }));
    }
    previousDate = dateKey;
  }
  return chart;
}

export function csvText(columns, rows) {
  const quote = value => {
    let str = value === null || value === undefined ? "" : String(value);
    if (/^[\s]*[=+\-@\t\r]/.test(str)) str = "'" + str;
    return '"' + str.replaceAll('"', '""') + '"';
  };
  return "\uFEFF" + [columns, ...rows].map(row => row.map(quote).join(",")).join("\r\n");
}

export function downloadCSV(filename, columns, rows) {
  const csv = csvText(columns, rows);
  const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.hidden = true;
  try {
    document.body.append(link);
    link.click();
  } finally {
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}
