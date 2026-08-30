"use strict";

const $ = (sel) => document.querySelector(sel);

/* ---------- helpers ---------- */

async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  let body = null;
  try { body = await res.json(); } catch (e) { /* ignore */ }
  if (res.status === 401) {
    window.location.href = "/login";
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    throw new Error((body && body.msg) || ("HTTP " + res.status));
  }
  return body && body.data !== undefined ? body.data : body;
}

function fmt(n) {
  if (n == null) return "0";
  return Number(n).toLocaleString("en-US");
}

function pct(v) {
  if (v == null || isNaN(v)) return "-";
  return v.toFixed(2) + "%";
}

function esc(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

/* ---------- charts ---------- */

function barChart(el, values, labels, opts = {}) {
  const w = 720, h = 200, pad = { top: 8, right: 8, bottom: 22, left: 8 };
  const innerW = w - pad.left - pad.right;
  const innerH = h - pad.top - pad.bottom;
  const max = Math.max(...values, 1);
  const n = values.length;
  const slot = innerW / n;
  const barW = Math.min(slot * 0.62, 28);

  let svg = `<svg viewBox="0 0 ${w} ${h}" role="img" aria-label="bar chart">`;

  // horizontal grid lines
  for (let i = 0; i <= 4; i++) {
    const y = pad.top + innerH - (innerH * i) / 4;
    svg += `<line class="bar-grid" x1="${pad.left}" y1="${y}" x2="${w - pad.right}" y2="${y}"/>`;
    svg += `<text class="axis-label" x="${pad.left + 2}" y="${y - 3}">${Math.round((max * i) / 4)}</text>`;
  }

  values.forEach((v, i) => {
    const bh = v > 0 ? Math.max((v / max) * innerH, 1.5) : 0;
    const x = pad.left + slot * i + (slot - barW) / 2;
    const y = pad.top + innerH - bh;
    const label = labels[i];
    svg += `<rect class="bar" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${barW}" height="${bh.toFixed(1)}" rx="2">`;
    svg += `<title>${esc(label)}: ${fmt(v)}</title></rect>`;
    if (n <= 8 || i % Math.ceil(n / 8) === 0 || i === n - 1) {
      svg += `<text class="axis-label" x="${(x + barW / 2).toFixed(1)}" y="${h - 6}" text-anchor="middle">${esc(label)}</text>`;
    }
  });

  svg += "</svg>";
  el.innerHTML = svg;
}

/* ---------- rendering ---------- */

function renderUptime(upstreams) {
  const grid = $("#uptime-grid");
  if (!upstreams || upstreams.length === 0) {
    grid.innerHTML = `<div class="card" style="grid-column:1/-1">暂无上游配置</div>`;
    return;
  }
  grid.innerHTML = upstreams.map((u) => {
    let cls = "offline";
    let stateText = "离线";
    if (u.online) { cls = "online"; stateText = "在线"; }
    if (!u.online && u.backoff) { cls = "backoff"; stateText = "退避中"; }

    const regions = (u.regions && u.regions.length)
      ? u.regions.map((r) => `<span class="badge">${esc(r.toUpperCase())}</span>`).join("")
      : `<span class="badge empty">无区域</span>`;

    const uptime = u.uptime_all != null ? pct(u.uptime_all) : "-";
    const latency = u.online && u.latency_ms != null ? u.latency_ms + " ms" : "-";
    const last = u.last_check ? new Date(u.last_check).toLocaleString() : "-";
    const err = u.last_error && !u.online ? `<div class="uc-err" title="${esc(u.last_error)}">${esc(u.last_error)}</div>` : "";

    return `
      <div class="uptime-card ${cls}">
        <div class="uc-head">
          <span class="status-dot ${cls}"></span>
          <span class="uc-name" title="${esc(u.name)}">${esc(u.name)}</span>
          <span class="uc-state ${cls}">${stateText}</span>
        </div>
        <div class="uc-regions">${regions}</div>
        <div class="uc-metrics">
          <div class="uc-metric"><span class="uc-label">延迟</span><span class="uc-value">${esc(latency)}</span></div>
          <div class="uc-metric"><span class="uc-label">可用率</span><span class="uc-value">${esc(uptime)}</span></div>
          <div class="uc-metric"><span class="uc-label">失败次数</span><span class="uc-value">${fmt(u.consecutive_failures)}</span></div>
        </div>
        <div class="uc-foot"><span>最后检查 ${esc(last)}</span></div>
        ${err}
      </div>`;
  }).join("");
}

function renderBreakdown(el, data, emptyText) {
  const entries = Object.entries(data || {}).sort((a, b) => b[1] - a[1]);
  if (entries.length === 0) {
    el.innerHTML = `<div class="table-empty">${esc(emptyText || "暂无数据")}</div>`;
    return;
  }
  const max = Math.max(...entries.map(([, v]) => v), 1);
  el.innerHTML = entries.map(([name, v]) => {
    const p = Math.round((v / max) * 100);
    return `
      <div class="table-row">
        <span class="table-name" title="${esc(name)}">${esc(name)}</span>
        <div class="table-bar"><div class="table-bar-fill" style="width:${p}%"></div></div>
        <span class="table-value">${fmt(v)}</span>
      </div>`;
  }).join("");
}

function renderStats(data) {
  const today = data.today || {};
  $("#stat-total").textContent = fmt(data.total);
  $("#stat-today").textContent = fmt(today.total);

  const lastHour = (today.hourly && today.hourly.length) ? today.hourly[today.hourly.length - 1] : 0;
  $("#stat-today-sub").textContent = `最近 1 小时 ${fmt(lastHour)} 次`;

  // hourly chart
  const hourly = today.hourly || new Array(24).fill(0);
  const hourLabels = hourly.map((_, i) => (i % 4 === 0 ? `${i}时` : ""));
  barChart($("#hourly-chart"), hourly, hourLabels);
  const hourTotal = hourly.reduce((a, b) => a + b, 0);
  $("#hourly-legend").textContent = `今日 ${fmt(hourTotal)} 次 · 按服务器本地时区分`;

  // daily chart
  const days = data.days || [];
  const dayLabels = days.map((d) => d.date.slice(5));
  barChart($("#daily-chart"), days.map((d) => d.total), dayLabels);
  $("#daily-legend").textContent = days.length
    ? `近 ${days.length} 天 · ${days[0].date} ~ ${days[days.length - 1].date}`
    : "";

  renderBreakdown($("#upstream-table"), data.upstreams_total, "暂无上游请求记录");
  renderBreakdown($("#endpoint-table"), today.endpoints, "今日暂无端点请求");
}

function renderStatus(data) {
  const upstreams = data.upstreams || [];
  const onlineCount = upstreams.filter((u) => u.online).length;
  $("#stat-online").textContent = fmt(onlineCount);
  $("#stat-online-sub").textContent = `共 ${upstreams.length} 个上游`;

  const regions = data.regions || [];
  $("#stat-regions").textContent = fmt(regions.length);
  $("#stat-regions-list").textContent = regions.length
    ? regions.map((r) => r.toUpperCase()).join(" · ")
    : "-";

  renderUptime(upstreams);
}

/* ---------- boot ---------- */

function bootDashboard() {
  let busy = false;
  async function refresh() {
    if (busy) return;
    busy = true;
    try {
      const [status, stats] = await Promise.all([
        api("/api/status"),
        api("/api/stats?days=7"),
      ]);
      renderStatus(status);
      renderStats(stats);
      $("#last-update").textContent = "更新于 " + new Date().toLocaleTimeString();
    } catch (e) {
      if (e.message !== "unauthorized") {
        $("#last-update").textContent = "刷新失败: " + e.message;
      }
    } finally {
      busy = false;
    }
  }
  refresh();
  setInterval(refresh, 10000);

  $("#logout-btn").addEventListener("click", async () => {
    try { await api("/api/logout", { method: "POST" }); } catch (e) { /* ignore */ }
    window.location.href = "/login";
  });
}

function bootLogin() {
  const form = $("#login-form");
  const errBox = $("#login-error");
  const btn = $("#login-btn");
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    errBox.hidden = true;
    btn.disabled = true;
    try {
      await api("/api/login", {
        method: "POST",
        body: JSON.stringify({
          username: $("#username").value.trim(),
          password: $("#password").value,
        }),
      });
      window.location.href = "/";
    } catch (err) {
      errBox.textContent = err.message || "登录失败";
      errBox.hidden = false;
      btn.disabled = false;
    }
  });
}

document.addEventListener("DOMContentLoaded", () => {
  if ($("#login-form")) bootLogin();
  if ($("#logout-btn")) bootDashboard();
});
