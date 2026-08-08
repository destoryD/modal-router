(function () {
  "use strict";

  const API = "/admin/api";
  let token = localStorage.getItem("admin_token") || "";

  function headers(json) {
    const h = {};
    if (json) h["Content-Type"] = "application/json";
    if (token) h["X-Admin-Token"] = token;
    return h;
  }

  async function apiCall(method, path, body) {
    const opts = { method, headers: headers(!!body) };
    if (body) opts.body = JSON.stringify(body);
    const resp = await fetch(API + path, opts);
    if (resp.status === 401) {
      showTokenOverlay();
      throw new Error("unauthorized");
    }
    if (resp.status === 204) return null;
    const text = await resp.text();
    let data;
    try { data = text ? JSON.parse(text) : null; } catch { data = text; }
    if (!resp.ok) throw new Error((data && data.error) || resp.statusText);
    return data;
  }

  function showTokenOverlay() {
    document.getElementById("token-overlay").classList.remove("hidden");
  }

  function hideTokenOverlay() {
    document.getElementById("token-overlay").classList.add("hidden");
  }

  document.getElementById("token-submit").addEventListener("click", function () {
    token = document.getElementById("token-input").value.trim();
    localStorage.setItem("admin_token", token);
    hideTokenOverlay();
    loadAll();
  });

  document.getElementById("token-input").addEventListener("keydown", function (e) {
    if (e.key === "Enter") document.getElementById("token-submit").click();
  });

  function fmtNum(n) {
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
    return String(n || 0);
  }

  function esc(s) {
    const d = document.createElement("div");
    d.textContent = s || "";
    return d.innerHTML;
  }

  async function loadStats() {
    try {
      const s = await apiCall("GET", "/stats");
      document.getElementById("stat-total").textContent = s.total_channels;
      document.getElementById("stat-active").textContent = s.active_channels;
      document.getElementById("stat-disabled").textContent = s.disabled_count;
      document.getElementById("stat-req").textContent = fmtNum(s.total_requests);
      document.getElementById("stat-ok").textContent = fmtNum(s.total_success);
      document.getElementById("stat-fail").textContent = fmtNum(s.total_fail);
    } catch (e) { /* ignore if token overlay shown */ }
  }

  async function loadChannels() {
    try {
      const channels = await apiCall("GET", "/channels");
      renderChannels(channels);
    } catch (e) { /* ignore */ }
  }

  function renderChannels(channels) {
    const tbody = document.getElementById("channel-tbody");
    if (!channels || channels.length === 0) {
      tbody.innerHTML = '<tr><td colspan="10" class="empty">No channels yet. Click "Add Channel" to create one.</td></tr>';
      return;
    }
    tbody.innerHTML = channels.map(function (ch) {
      const status = ch.enabled
        ? '<span class="badge active">Active</span>'
        : '<span class="badge disabled">Disabled</span>';
      const reason = ch.disabled_reason ? '<div class="reason-cell">' + esc(ch.disabled_reason) + '</div>' : "";
      const toggleBtn = ch.enabled
        ? '<button class="btn btn-sm btn-danger" data-act="disable" data-id="' + ch.id + '">Disable</button>'
        : '<button class="btn btn-sm btn-primary" data-act="enable" data-id="' + ch.id + '">Enable</button>';
      return '<tr>' +
        '<td><b>' + esc(ch.name) + '</b>' + reason + '</td>' +
        '<td class="url-cell" title="' + esc(ch.url) + '">' + esc(ch.url) + '</td>' +
        '<td class="key-cell">' + esc(ch.key) + '</td>' +
        '<td>' + status + '</td>' +
        '<td>' + (ch.weight || 1) + '</td>' +
        '<td>' + fmtNum(ch.request_count) + '</td>' +
        '<td>' + fmtNum(ch.success_count) + '</td>' +
        '<td>' + fmtNum(ch.fail_count) + '</td>' +
        '<td>' + esc((ch.disable_on_status || []).join(",")) + '</td>' +
        '<td><div class="actions-cell">' +
          toggleBtn +
          '<button class="btn btn-sm" data-act="edit" data-id="' + ch.id + '">Edit</button>' +
          '<button class="btn btn-sm" data-act="test" data-id="' + ch.id + '" data-name="' + esc(ch.name) + '">Test</button>' +
          '<button class="btn btn-sm" data-act="reset" data-id="' + ch.id + '">Reset</button>' +
          '<button class="btn btn-sm btn-danger" data-act="delete" data-id="' + ch.id + '">Del</button>' +
        '</div></td>' +
      '</tr>';
    }).join("");
  }

  document.getElementById("channel-tbody").addEventListener("click", async function (e) {
    const btn = e.target.closest("[data-act]");
    if (!btn) return;
    const id = btn.dataset.id;
    const act = btn.dataset.act;
    try {
      if (act === "enable") { await apiCall("POST", "/channels/" + id + "/enable"); }
      else if (act === "disable") { await apiCall("POST", "/channels/" + id + "/disable"); }
      else if (act === "reset") { await apiCall("POST", "/channels/" + id + "/reset"); }
      else if (act === "delete") {
        if (!confirm("Delete this channel?")) return;
        await apiCall("DELETE", "/channels/" + id);
      }
      else if (act === "edit") {
        const ch = await apiCall("GET", "/channels/" + id);
        openModal(ch);
        return;
      }
      else if (act === "test") {
        openTestModal(id, btn.dataset.name);
        return;
      }
      loadAll();
    } catch (err) { alert(err.message); }
  });

  document.getElementById("add-btn").addEventListener("click", function () {
    openModal(null);
  });

  function openModal(ch) {
    const modal = document.getElementById("channel-modal");
    const title = document.getElementById("modal-title");
    document.getElementById("ch-id").value = ch ? ch.id : "";
    document.getElementById("ch-name").value = ch ? ch.name : "";
    document.getElementById("ch-url").value = ch ? ch.url : "";
    document.getElementById("ch-key").value = ch ? ch.key : "";
    document.getElementById("ch-key").placeholder = ch ? "Leave unchanged" : "sk-...";
    document.getElementById("ch-weight").value = ch ? (ch.weight || 1) : 1;
    document.getElementById("ch-auth-header").value = ch ? (ch.auth_header || "Authorization") : "Authorization";
    document.getElementById("ch-auth-prefix").value = ch ? (ch.auth_prefix != null ? ch.auth_prefix : "Bearer ") : "Bearer ";
    document.getElementById("ch-disable-status").value = ch ? (ch.disable_on_status || [402]).join(",") : "402";
    document.getElementById("ch-auto-reenable").value = ch ? (ch.auto_reenable_sec || 0) : 0;
    document.getElementById("ch-headers").value = ch && ch.headers ? JSON.stringify(ch.headers, null, 2) : "";
    title.textContent = ch ? "Edit Channel" : "Add Channel";
    modal.classList.remove("hidden");
  }

  function closeModal() {
    document.getElementById("channel-modal").classList.add("hidden");
  }

  document.getElementById("modal-cancel").addEventListener("click", closeModal);

  document.getElementById("channel-form").addEventListener("submit", async function (e) {
    e.preventDefault();
    const id = document.getElementById("ch-id").value;
    const disableStr = document.getElementById("ch-disable-status").value.trim();
    const disableCodes = disableStr ? disableStr.split(",").map(function (s) { return parseInt(s.trim(), 10); }).filter(function (n) { return !isNaN(n); }) : [];
    let headersJson = {};
    const headersStr = document.getElementById("ch-headers").value.trim();
    if (headersStr) {
      try { headersJson = JSON.parse(headersStr); }
      catch { alert("Custom Headers is not valid JSON"); return; }
    }
    const body = {
      name: document.getElementById("ch-name").value.trim(),
      url: document.getElementById("ch-url").value.trim(),
      key: document.getElementById("ch-key").value,
      weight: parseInt(document.getElementById("ch-weight").value, 10) || 1,
      auth_header: document.getElementById("ch-auth-header").value.trim() || "Authorization",
      auth_prefix: document.getElementById("ch-auth-prefix").value,
      disable_on_status: disableCodes,
      auto_reenable_sec: parseInt(document.getElementById("ch-auto-reenable").value, 10) || 0,
      headers: headersJson,
    };
    try {
      if (id) {
        await apiCall("PUT", "/channels/" + id, body);
      } else {
        if (!body.key) { alert("API Key is required"); return; }
        await apiCall("POST", "/channels", body);
      }
      closeModal();
      loadAll();
    } catch (err) { alert(err.message); }
  });

  document.getElementById("channel-modal").addEventListener("click", function (e) {
    if (e.target === this) closeModal();
  });

  let testChannelId = "";

  function openTestModal(id, name) {
    testChannelId = id;
    document.getElementById("test-channel-name").textContent = name;
    document.getElementById("test-model").value = "kimi-k3";
    const result = document.getElementById("test-result");
    result.classList.add("hidden");
    result.innerHTML = "";
    document.getElementById("test-modal").classList.remove("hidden");
  }

  function closeTestModal() {
    document.getElementById("test-modal").classList.add("hidden");
  }

  document.getElementById("test-cancel").addEventListener("click", closeTestModal);

  document.getElementById("test-modal").addEventListener("click", function (e) {
    if (e.target === this) closeTestModal();
  });

  document.getElementById("test-send").addEventListener("click", async function () {
    const model = document.getElementById("test-model").value.trim() || "kimi-k3";
    const result = document.getElementById("test-result");
    const sendBtn = document.getElementById("test-send");
    sendBtn.disabled = true;
    sendBtn.textContent = "Testing...";
    result.classList.remove("hidden");
    result.innerHTML = '<div class="test-pending">Sending request...</div>';
    try {
      const res = await apiCall("POST", "/channels/" + testChannelId + "/test", { model: model });
      renderTestResult(res);
    } catch (err) {
      result.innerHTML = '<div class="test-fail">Error: ' + esc(err.message) + "</div>";
    }
    sendBtn.disabled = false;
    sendBtn.textContent = "Send Test";
  });

  function renderTestResult(res) {
    const result = document.getElementById("test-result");
    const ok = res.success;
    const statusClass = ok ? "test-ok" : "test-fail";
    const statusText = ok ? "SUCCESS" : "FAILED";
    let html = '<div class="test-summary ' + statusClass + '">' +
      '<span class="test-status-badge ' + statusClass + '">' + statusText + '</span>' +
      '<span class="test-meta">HTTP ' + (res.status_code || 0) + ' &middot; ' + (res.latency_ms || 0) + 'ms</span>';
    if (res.would_disable) {
      html += '<span class="test-warn">Would trigger disable</span>';
    }
    html += '</div>';
    if (res.error) {
      html += '<div class="test-error-msg">Error: ' + esc(res.error) + '</div>';
    }
    if (res.response) {
      let pretty = res.response;
      try { pretty = JSON.stringify(JSON.parse(res.response), null, 2); } catch {}
      html += '<pre class="test-response">' + esc(pretty) + '</pre>';
    }
    result.innerHTML = html;
  }

  async function loadAll() {
    await Promise.all([loadStats(), loadChannels()]);
  }

  loadAll().catch(function () {
    showTokenOverlay();
  });

  setInterval(loadStats, 5000);
})();
