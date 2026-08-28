const login = document.getElementById("login");
const meter = document.getElementById("meter");
const loginError = document.getElementById("loginError");
const meterError = document.getElementById("meterError");
const rangeControl = document.getElementById("range");
const timezoneControl = document.getElementById("timezone");

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
  if (!response.ok) throw Error(data.error || "Request failed");
  return data;
}

function showError(element, error) {
  element.textContent = error?.message || String(error);
}

function initializeTimezones() {
  const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  if (browserTimezone !== "UTC") {
    const option = document.createElement("option");
    option.value = browserTimezone;
    option.textContent = browserTimezone;
    timezoneControl.append(option);
    timezoneControl.value = browserTimezone;
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
    throw Error("Select a valid start and end time.");
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

async function load() {
  try {
    const me = await api("/api/user/me");
    login.classList.add("hidden");
    meter.classList.remove("hidden");
    document.getElementById("accountName").textContent = me.account?.access_key_id || "Account";
    document.getElementById("accountId").textContent = me.account?.account_id || "--";
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
    document.getElementById("freshness").textContent = pending === 0 ? "Up to date" : `${pending} pending`;
    document.getElementById("updated").textContent = new Date(freshness.as_of).toLocaleTimeString();
  } catch (_) {
    document.getElementById("freshness").textContent = "Unavailable";
    document.getElementById("updated").textContent = "--";
  }
}

async function refresh() {
  meterError.textContent = "";
  document.getElementById("freshness").textContent = "Checking";
  document.getElementById("usage").innerHTML = '<div class="loading">Loading usage...</div>';
  document.getElementById("bills").innerHTML = '<div class="loading">Loading charges...</div>';
  document.getElementById("requests").innerHTML = '<div class="loading">Loading requests...</div>';
  try {
    const query = selectedWindow();
    const balance = await api("/api/user/balance");
    const snapshot = balance.balance || {};
    document.getElementById("balance").textContent = snapshot.available_balance || snapshot.balance || "0";
    document.getElementById("currency").textContent = balance.currency || snapshot.currency || "";
    await refreshFreshness();

    const usageQuery = new URLSearchParams(query);
    usageQuery.set("bucket", document.getElementById("bucket").value);
    const usage = await api(`/api/user/usage?${usageQuery}`);
    renderUsage(usage.rows || []);
    const bills = await api(`/api/user/bills?${query}`);
    renderBills(bills.bills || [], bills.currency || "");
    const requests = await api(`/api/user/requests?${query}`);
    renderRequests(requests.requests || []);
  } catch (error) {
    showError(meterError, error);
    document.getElementById("usage").innerHTML = "";
  }
}

function renderUsage(rows) {
  if (!rows.length) {
    document.getElementById("usage").innerHTML = '<div class="loading">No usage recorded for this window.</div>';
    return;
  }
  document.getElementById("usage").innerHTML = '<div class="usage-grid">' + rows.map(row => {
    const dimensions = row.dimensions || {};
    const measures = row.measures || {};
    return `<article class="usage-card"><h3>${escapeText(dimensions.model || "All models")}</h3><strong>${escapeText(measures.quantity || "0")}</strong><small>${escapeText(formatBucket(dimensions.bucket))}</small></article>`;
  }).join("") + "</div>";
}

function renderRequests(rows) {
  if (!rows.length) {
    document.getElementById("requests").innerHTML = '<div class="loading">No requests recorded for this window.</div>';
    return;
  }
  document.getElementById("requests").innerHTML = rows.map(row => `<div class="request-row"><small>${escapeText(formatTimestamp(row.occurred_at))}</small><span>${escapeText(row.model || "Model unavailable")}</span><small>${escapeText(row.request_id || "Request unavailable")}</small></div>`).join("");
}

function renderBills(rows, currency) {
  if (!rows.length) {
    document.getElementById("bills").innerHTML = '<div class="loading">No charges recorded for this window.</div>';
    return;
  }
  document.getElementById("bills").innerHTML = rows.map(row => `<article class="usage-card"><h3>${escapeText(row.model || "All models")}</h3><strong>${escapeText(row.amount || "0")} ${escapeText(currency)}</strong><small>${escapeText(row.request_count || 0)} requests</small></article>`).join("");
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
