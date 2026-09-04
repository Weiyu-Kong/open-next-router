const login = document.getElementById("login");
const meter = document.getElementById("meter");
const loginError = document.getElementById("loginError");
const meterError = document.getElementById("meterError");
const rangeControl = document.getElementById("range");
const timezoneControl = document.getElementById("timezone");
const bridgeNotice = document.getElementById("bridgeNotice");
let displayDimension = "tokens";
let usageRows = [];
let requestRows = [];
let meterModels = [];
let billingCurrency = "";
let requestUnit = "m";
let usageChart;
let usageModelChart;
let requestTable;
let bridgeNoticePending = new URLSearchParams(window.location.search).get("bridge") || "";

const escapeText = value => String(value ?? "").replace(/[&<>\"']/g, character => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;",
}[character]));

async function api(path, options = {}) {
  const response = await fetch(path, options);
  let data = {};
  try {
    data = await response.json();
  } catch (_) {
    // The status code remains the source of truth for a non-JSON response.
  }
  if (!response.ok) throw Error(data.error || "请求失败");
  return data;
}

function showError(element, error) {
  element.textContent = error?.message || String(error);
}

function initializeTimezones() {
  const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const zones = typeof Intl.supportedValuesOf === "function"
    ? Intl.supportedValuesOf("timeZone")
    : ["Africa/Cairo", "America/Los_Angeles", "America/New_York", "Asia/Shanghai", "Asia/Tokyo", "Australia/Sydney", "Europe/Berlin", "Europe/London", "Pacific/Auckland"];
  const allZones = [...new Set(["UTC", ...zones, browserTimezone])].sort((left, right) => {
    if (left === "UTC") return -1;
    if (right === "UTC") return 1;
    return left.localeCompare(right);
  });
  timezoneControl.innerHTML = "";
  allZones.forEach(zone => {
    const option = document.createElement("option");
    option.value = zone;
    option.textContent = timezoneLabel(zone);
    timezoneControl.append(option);
  });
  timezoneControl.value = allZones.includes(browserTimezone) ? browserTimezone : "UTC";
}

function timezoneLabel(zone) {
  if (zone === "UTC") return "协调世界时 (UTC)";
  try {
    const parts = new Intl.DateTimeFormat("zh-CN", {timeZone: zone, timeZoneName: "long"})
      .formatToParts(new Date(Date.UTC(2026, 0, 15, 12)));
    const name = parts.find(part => part.type === "timeZoneName")?.value || zone;
    return `${name} (${zone})`;
  } catch (_) {
    return zone;
  }
}

function dateTimeInputValue(date, timezone) {
  if (timezone === "UTC") return date.toISOString().slice(0, 16);
  const pad = value => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function setCustomDefaults() {
  const end = new Date();
  const start = new Date(end.getTime() - 7 * 86400000);
  document.getElementById("startTime").value = dateTimeInputValue(start, timezoneControl.value);
  document.getElementById("endTime").value = dateTimeInputValue(end, timezoneControl.value);
}

function parseDateTimeInput(value, timezone) {
  if (!value) return NaN;
  return new Date(value + (timezone === "UTC" ? "Z" : "")).getTime();
}

function selectedWindow() {
  const timezone = timezoneControl.value || "UTC";
  let start;
  let end;
  if (rangeControl.value === "custom") {
    start = parseDateTimeInput(document.getElementById("startTime").value, timezone);
    end = parseDateTimeInput(document.getElementById("endTime").value, timezone);
  } else {
    end = Date.now();
    start = end - Number(rangeControl.value || 7) * 86400000;
  }
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) {
    throw Error("请选择有效的开始和结束时间。");
  }
  return new URLSearchParams({
    start: String(Math.floor(start / 1000)),
    end: String(Math.floor(end / 1000)),
    timezone,
  });
}

function formatTimestamp(unixSeconds) {
  if (!unixSeconds) return "--";
  return new Date(Number(unixSeconds) * 1000).toLocaleString([], {timeZone: timezoneControl.value || "UTC"});
}

function formatBucket(value) {
  const numeric = Number(value);
  return Number.isFinite(numeric) && numeric > 1000000000 ? formatTimestamp(numeric) : String(value || "");
}

function decimalValue(value) {
  const number = Number(value || 0);
  return Number.isFinite(number) ? number : 0;
}

function formatMeasure(value, dimension) {
  const number = decimalValue(value);
  if (dimension !== "cost") return (number / 1000000).toLocaleString([], {maximumFractionDigits: 2});
  const maximumFractionDigits = dimension === "cost" ? 6 : 2;
  return number.toLocaleString([], {maximumFractionDigits});
}

function formatToken(value) {
  const number = decimalValue(value);
  if (requestUnit === "raw") return number.toLocaleString();
  const divisor = requestUnit === "k" ? 1000 : 1000000;
  return (number / divisor).toLocaleString([], {maximumFractionDigits: 2}) + (requestUnit === "k" ? " K" : " M");
}

function requestMeasure(metrics, dimension) {
  return Object.entries(metrics || {}).reduce((total, [key, metric]) => {
    if (typeof metric === "number" || typeof metric === "string") return dimension === "cost" ? (key === "amount" ? total + decimalValue(metric) : total) : total + decimalValue(metric);
    const value = dimension === "cost" ? metric?.amount : metric?.value;
    if (value != null) return total + decimalValue(value);
    return total;
  }, 0);
}

function requestTokenMeasure(metrics) {
  if (metrics?.quantity != null) return decimalValue(metrics.quantity);
  if (metrics?.total_tokens != null) return decimalValue(metrics.total_tokens);
  const keys = ["prompt_tokens", "completion_tokens", "cached_tokens", "input_tokens", "output_tokens"];
  return keys.reduce((sum, key) => sum + decimalValue(metrics?.[key]), 0);
}

function renderDimension() {
  const cost = displayDimension === "cost";
  meter.classList.toggle("cost-mode", cost);
  document.getElementById("metricLabel").textContent = cost ? "已扣费用" : "令牌用量";
  document.getElementById("metricUnit").textContent = cost ? (billingCurrency || "货币未知") : "M（百万）";
  document.querySelectorAll(".dimension-option").forEach(button => {
    const selected = button.dataset.dimension === displayDimension;
    button.classList.toggle("active", selected);
    button.setAttribute("aria-pressed", String(selected));
  });
  renderUsage(usageRows);
  renderRequests(requestRows);
  renderOverviewRequests(requestRows);
}

function showBridgeNotice() {
  if (!bridgeNoticePending || !bridgeNotice) return;
  const created = bridgeNoticePending === "created";
  document.getElementById("bridgeNoticeTitle").textContent = created ? "已创建新的用量账户" : "用量账户已就绪";
  document.getElementById("bridgeNoticeText").textContent = created
    ? "已为当前身份配置新的访问密钥和计费账户。"
    : "已使用现有用量账户登录。";
  bridgeNotice.classList.remove("hidden", "fade-out");
  bridgeNoticePending = "";
  window.history.replaceState({}, document.title, window.location.pathname);
  window.setTimeout(() => {
    bridgeNotice.classList.add("fade-out");
    window.setTimeout(() => bridgeNotice.classList.add("hidden"), 320);
  }, 2200);
}

async function load() {
  try {
    const me = await api("/api/user/me");
    login.classList.add("hidden");
    meter.classList.remove("hidden");
    document.getElementById("accountName").textContent = me.account?.access_key_id || "账户";
    document.getElementById("accountId").textContent = me.account?.account_id || "--";
    showBridgeNotice();
    await refresh();
  } catch (_) {
    login.classList.remove("hidden");
    meter.classList.add("hidden");
  }
}

async function refreshFreshness() {
  try {
    const freshness = await api("/api/user/freshness");
    const pending = Number(freshness.pending_billing_events || 0);
    const status = pending === 0 ? "已同步" : `${pending} 条待处理`;
    const time = new Date(freshness.as_of).toLocaleTimeString();
    document.getElementById("freshness").textContent = status;
    document.getElementById("updated").textContent = time;
    document.getElementById("overviewFreshness").textContent = status;
    document.getElementById("overviewUpdated").textContent = time;
  } catch (_) {
    document.getElementById("freshness").textContent = "不可用";
    document.getElementById("overviewFreshness").textContent = "不可用";
    document.getElementById("updated").textContent = "--";
  }
}

async function refresh() {
  meterError.textContent = "";
  document.getElementById("freshness").textContent = "检查中";
  document.getElementById("usage").innerHTML = '<div class="loading">正在加载用量...</div>';
  document.getElementById("requests").innerHTML = '<div class="loading">正在加载请求...</div>';
  try {
    const query = selectedWindow();
    const balance = await api("/api/user/balance");
    const snapshot = balance.balance || {};
    document.getElementById("balance").textContent = snapshot.available_balance || snapshot.balance || "0";
    billingCurrency = balance.currency || snapshot.currency || "";
    document.getElementById("currency").textContent = billingCurrency;
    await refreshFreshness();
    await refreshModels();

    const usageQuery = new URLSearchParams(query);
    usageQuery.set("bucket", document.getElementById("bucket").value);
    const usage = await api(`/api/user/usage?${usageQuery}`);
    usageRows = usage.rows || [];
    billingCurrency = usage.currency || billingCurrency;
    const requests = await api(`/api/user/requests?${query}`);
    requestRows = requests.requests || [];
    billingCurrency = requests.currency || billingCurrency;
    renderDimension();
  } catch (error) {
    showError(meterError, error);
    document.getElementById("usage").innerHTML = "";
  }
}

async function refreshModels() {
  const target = document.getElementById("models");
  if (!target) return;
  try {
    const data = await api("/api/user/models");
    const models = data.models || [];
    meterModels = models;
    refreshModelFilter();
    target.innerHTML = models.length ? models.map(model => {
      const pricing = model.pricing || {};
      const prices = [pricing.input && `输入 ${pricing.input}`, pricing.output && `输出 ${pricing.output}`, pricing.cache_hit && `缓存命中 ${pricing.cache_hit}`].filter(Boolean).join(" · ");
      return `<article class="model-card"><div class="model-card-head"><h3>${escapeText(model.id)}</h3><span class="model-status ${model.available ? "available" : "unavailable"}">${model.available ? "可调用" : "待接入"}</span></div><p>${escapeText(model.provider || "通用模型")}</p><strong>${escapeText(prices || "价格暂未配置")}</strong><small>${escapeText(pricing.unit || "")}</small></article>`;
    }).join("") : '<div class="loading">暂无模型。</div>';
  } catch (error) {
    target.innerHTML = `<div class="error">${escapeText(error.message)}</div>`;
  }
}

function refreshModelFilter() {
  const modelFilter = document.getElementById("modelFilter");
  if (!modelFilter) return;
  const selected = modelFilter.value;
  const models = meterModels
    .filter(model => model.available === true)
    .map(model => model.id)
    .filter(Boolean)
    .sort();
  modelFilter.innerHTML = '<option value="">全部模型</option>' + models
    .map(model => `<option value="${escapeText(model)}">${escapeText(model)}</option>`)
    .join("");
  modelFilter.value = models.includes(selected) ? selected : "";
}

function renderUsage(rows) {
  const selectedModel = document.getElementById("modelFilter")?.value || "";
  rows = rows.filter(row => !selectedModel || (row.dimensions || {}).model === selectedModel);
  if (!rows.length) {
    usageChart?.destroy();
    usageModelChart?.destroy();
    usageChart = undefined;
    usageModelChart = undefined;
    document.getElementById("usage").innerHTML = '<div class="loading">此时间范围内暂无用量记录。</div>';
    return;
  }
  const measureKey = displayDimension === "cost" ? "amount" : "quantity";
  const buckets = rows.reduce((map, row) => {
    const key = row.dimensions?.bucket || "";
    map[key] = (map[key] || 0) + decimalValue(row.measures?.[measureKey]);
    return map;
  }, {});
  const labels = Object.keys(buckets).sort();
  const models = [...new Set(rows.map(row => row.dimensions?.model || "全部模型"))].sort();
  const chart = '<div class="usage-chart"><canvas id="usageChart"></canvas></div><div class="usage-chart usage-model-chart"><canvas id="usageModelChart"></canvas></div>';
  document.getElementById("usage").innerHTML = chart;
  if (window.Chart) {
    usageChart?.destroy();
    usageModelChart?.destroy();
    const colors = ["#176b63", "#c16a25", "#3568a8", "#9b4d85", "#6f7d32", "#b64242"];
    const chartOptions = {responsive: true, maintainAspectRatio: false, animation: {duration: 650, easing: "easeOutQuart"}, plugins: {tooltip: {callbacks: {label: context => displayDimension === "cost" ? `${context.dataset.label}: ${formatMeasure(context.raw, "cost")} ${billingCurrency}` : `${context.dataset.label}: ${formatToken(context.raw)}`}}}, scales: {x: {grid: {display: false}}, y: {beginAtZero: true, ticks: {callback: value => displayDimension === "cost" ? formatMeasure(value, "cost") : formatToken(value)}, grid: {color: "#e5ecef"}}}};
    usageChart = new Chart(document.getElementById("usageChart"), {type: "bar", data: {labels, datasets: [{label: displayDimension === "cost" ? "消费" : "Token", data: labels.map(label => buckets[label]), backgroundColor: displayDimension === "cost" ? "#c16a25" : "#176b63", borderRadius: 5, borderSkipped: false, maxBarThickness: 42}]}, options: {...chartOptions, plugins: {...chartOptions.plugins, legend: {display: false}}}});
    usageModelChart = new Chart(document.getElementById("usageModelChart"), {type: "line", data: {labels, datasets: models.map((model, index) => ({label: model, data: labels.map(label => rows.filter(row => (row.dimensions?.model || "全部模型") === model && (row.dimensions?.bucket || "") === label).reduce((total, row) => total + decimalValue(row.measures?.[measureKey]), 0)), borderColor: colors[index % colors.length], backgroundColor: colors[index % colors.length], pointRadius: 3, pointHoverRadius: 5, tension: 0.25, fill: false}))}, options: {...chartOptions, plugins: {...chartOptions.plugins, legend: {display: true, position: "top", labels: {usePointStyle: true, boxWidth: 8}}}}});
  }
}

function renderRequests(rows) {
  const selectedModel = document.getElementById("modelFilter")?.value || "";
  const search = (document.getElementById("requestSearch")?.value || "").toLowerCase();
  rows = rows.filter(row => (!selectedModel || row.model === selectedModel) && (!search || `${row.model || ""} ${row.request_id || ""}`.toLowerCase().includes(search)));
  if (!rows.length) {
    document.getElementById("requests").innerHTML = '<div class="loading">此时间范围内暂无请求记录。</div>';
    return;
  }
  const tableRows = rows.map(row => {
    const measure = displayDimension === "cost" ? requestMeasure(row.metrics, displayDimension) : requestTokenMeasure(row.metrics);
    const detail = displayDimension === "cost" ? `${formatMeasure(measure, displayDimension)} ${billingCurrency}` : formatToken(measure);
    const metrics = row.metrics || {};
    return {time: formatTimestamp(row.occurred_at), model: row.model || "模型未知", request_id: row.request_id || "请求未知", input: requestTokenMeasure({input_tokens: metrics.input_tokens ?? metrics.prompt_tokens}), output: requestTokenMeasure({output_tokens: metrics.output_tokens ?? metrics.completion_tokens}), reasoning: decimalValue(metrics.reasoning_tokens ?? metrics.completion_tokens_details?.reasoning_tokens), cached: decimalValue(metrics.cached_tokens ?? metrics.prompt_tokens_details?.cached_tokens), total: measure, detail, occurred_at: row.occurred_at};
  });
  if (window.Tabulator) {
    requestTable?.destroy();
    requestTable = new Tabulator("#requests", {data: tableRows, layout: "fitColumns", pagination: true, paginationSize: 10, movableColumns: true, initialSort: [{column: "occurred_at", dir: "desc"}], columns: [{title: "时间", field: "time", sorter: "string"}, {title: "模型", field: "model"}, {title: "请求 ID", field: "request_id"}, {title: "输入", field: "input", formatter: cell => formatToken(cell.getValue())}, {title: "输出", field: "output", formatter: cell => formatToken(cell.getValue())}, {title: "推理", field: "reasoning", formatter: cell => formatToken(cell.getValue())}, {title: "缓存", field: "cached", formatter: cell => formatToken(cell.getValue())}, {title: "总用量", field: "detail", hozAlign: "right"}]});
  }
}

function renderOverviewRequests(rows) {
  const target = document.getElementById("overviewRequests");
  if (!target) return;
  const recent = rows.slice(-5).reverse();
  target.innerHTML = recent.length ? recent.map(row => { const tokens = requestTokenMeasure(row.metrics); const cost = requestMeasure(row.metrics, "cost"); return `<div class="request-row"><small>${escapeText(formatTimestamp(row.occurred_at))}</small><span>${escapeText(row.model || "模型未知")}</span><strong><b>${escapeText(formatToken(tokens))}</b><em>${escapeText(formatMeasure(cost, "cost"))} ${escapeText(billingCurrency)}</em></strong></div>`; }).join("") : '<div class="loading">暂无请求记录。</div>';
}

document.getElementById("loginForm").addEventListener("submit", async event => {
  event.preventDefault();
  loginError.textContent = "";
  try {
    await api("/api/user/login", {method: "POST", headers: {"content-type": "application/json"}, body: JSON.stringify({access_key: document.getElementById("accessKey").value})});
    document.getElementById("accessKey").value = "";
    await load();
  } catch (error) {
    showError(loginError, error);
  }
});

document.getElementById("logout").onclick = async () => {
  await api("/api/user/logout", {method: "POST"});
  load();
};
document.getElementById("refresh").onclick = refresh;
document.getElementById("bucket").onchange = refresh;
document.getElementById("modelFilter").onchange = renderDimension;
document.getElementById("requestSearch").oninput = renderRequests;
function changeUnit(value) { requestUnit = value; document.getElementById("requestUnit").value = value; document.getElementById("usageUnit").value = value; renderRequests(requestRows); renderOverviewRequests(requestRows); renderUsage(usageRows); }
document.getElementById("requestUnit").onchange = event => changeUnit(event.target.value);
document.getElementById("usageUnit").onchange = event => changeUnit(event.target.value);
document.getElementById("overviewRefresh").onclick = refresh;
document.getElementById("modelsRefresh").onclick = refreshModels;
document.querySelectorAll(".meter-nav-item,[data-goto]").forEach(button => button.onclick = () => { const panel = button.dataset.panel || button.dataset.goto; document.querySelectorAll(".meter-panel").forEach(item => item.classList.toggle("active", item.id === (panel === "overview" ? "overview" : panel))); document.querySelectorAll(".meter-nav-item").forEach(item => item.classList.toggle("active", item.dataset.panel === panel)); });
document.querySelectorAll(".dimension-option").forEach(button => {
  button.onclick = () => {
    displayDimension = button.dataset.dimension === "cost" ? "cost" : "tokens";
    renderDimension();
  };
});
rangeControl.onchange = () => {
  const custom = rangeControl.value === "custom";
  document.getElementById("customRange").classList.toggle("hidden", !custom);
  if (custom) setCustomDefaults();
  refresh();
};
timezoneControl.onchange = () => {
  if (rangeControl.value === "custom") setCustomDefaults();
  refresh();
};

initializeTimezones();
load();

const meterTranslations={"Checking":"检查中","Unavailable":"不可用","Loading usage...":"正在加载用量...","Loading requests...":"正在加载请求...","Model unavailable":"模型未知","Request unavailable":"请求未知","currency unavailable":"货币未知"};
function translateMeterUI(){const walker=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);let n;while(n=walker.nextNode()){const original=n.nodeValue;let value=original;Object.entries(meterTranslations).forEach(([from,to])=>{value=value.split(from).join(to)});value=value.replace(/ tokens/g," 令牌").replace(/ All models/g," 全部模型");if(value!==original)n.nodeValue=value;}}
new MutationObserver(translateMeterUI).observe(document.body,{subtree:true,childList:true,characterData:true});translateMeterUI();
