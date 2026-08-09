(function () {
  "use strict";

  const API = "/admin/modal";
  let pollTimer = null;

  async function apiCall(method, path, body) {
    const opts = { method, headers: {} };
    if (body) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const resp = await fetch(API + path, opts);
    if (resp.status === 204) return null;
    const text = await resp.text();
    let data;
    try { data = text ? JSON.parse(text) : null; } catch { data = text; }
    if (!resp.ok) throw new Error((data && data.error) || resp.statusText);
    return data;
  }

  function esc(s) {
    const d = document.createElement("div");
    d.textContent = s || "";
    return d.innerHTML;
  }

  function fmtTime(t) {
    if (!t) return "";
    const d = new Date(t);
    return d.toLocaleString();
  }

  function statusBadge(status) {
    const map = {
      queued: '<span class="badge" style="background:rgba(91,141,239,.15);color:var(--primary);">Queued</span>',
      running: '<span class="badge active">Running</span>',
      done: '<span class="badge active">Done</span>',
      failed: '<span class="badge disabled">Failed</span>',
      cancelled: '<span class="badge disabled">Cancelled</span>',
      active: '<span class="badge active">Active</span>',
    };
    return map[status] || '<span class="badge">' + esc(status) + "</span>";
  }

  // ---- Generic pagination + search ----
  function filterData(data, query, fields) {
    if (!query) return data;
    const q = query.toLowerCase();
    return data.filter(function (item) {
      return fields.some(function (f) {
        const val = String(item[f] || "").toLowerCase();
        return val.indexOf(q) >= 0;
      });
    });
  }

  function paginate(data, page, pageSize) {
    const total = data.length;
    const totalPages = Math.max(1, Math.ceil(total / pageSize));
    if (page > totalPages) page = totalPages;
    if (page < 1) page = 1;
    const start = (page - 1) * pageSize;
    const items = data.slice(start, start + pageSize);
    return { items: items, page: page, totalPages: totalPages, total: total };
  }

  function renderPagination(containerId, page, totalPages, total, onPage) {
    const el = document.getElementById(containerId);
    if (totalPages <= 1) { el.classList.add("hidden"); el.innerHTML = ""; return; }
    el.classList.remove("hidden");
    let html = "";
    html += '<button class="page-btn" data-page="1"' + (page <= 1 ? " disabled" : "") + '>&laquo;</button>';
    html += '<button class="page-btn" data-page="' + (page - 1) + '"' + (page <= 1 ? " disabled" : "") + '>&lsaquo;</button>';
    const maxButtons = 7;
    let startP = Math.max(1, page - 3);
    let endP = Math.min(totalPages, startP + maxButtons - 1);
    if (endP - startP < maxButtons - 1) startP = Math.max(1, endP - maxButtons + 1);
    for (let p = startP; p <= endP; p++) {
      html += '<button class="page-btn' + (p === page ? " active" : "") + '" data-page="' + p + '">' + p + '</button>';
    }
    html += '<button class="page-btn" data-page="' + (page + 1) + '"' + (page >= totalPages ? " disabled" : "") + '>&rsaquo;</button>';
    html += '<button class="page-btn" data-page="' + totalPages + '"' + (page >= totalPages ? " disabled" : "") + '>&raquo;</button>';
    html += '<span class="page-info">' + total + ' items</span>';
    el.innerHTML = html;
    el.querySelectorAll("[data-page]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        if (!this.disabled) onPage(parseInt(this.dataset.page, 10));
      });
    });
  }

  // ---- Table state ----
  const jobsTable = { data: [], page: 1, pageSize: 20, search: "" };
  const accountsTable = { data: [], page: 1, pageSize: 20, search: "" };
  const setupTable = { data: [], page: 1, pageSize: 20, search: "" };

  // ---- Load + render ----
  async function loadState() {
    try {
      const state = await apiCall("GET", "/state");
      jobsTable.data = (state.jobs || []).slice().reverse();
      accountsTable.data = state.accounts || [];
      setupTable.data = (state.setupJobs || []).slice().reverse();
      updateStats(state);
      renderJobsTable();
      renderAccountsTable();
      renderSetupTable();
      const hasActiveSetup = (state.setupJobs || []).some(j => j.status === "running");
      if ((state.running || hasActiveSetup) && !pollTimer) {
        pollTimer = setInterval(loadState, 3000);
      } else if (!state.running && !hasActiveSetup && pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
      }
    } catch (e) {
      console.error(e);
    }
  }

  function updateStats(state) {
    const jobs = state.jobs || [];
    document.getElementById("m-accounts").textContent = (state.accounts || []).length;
    document.getElementById("m-jobs-queued").textContent = jobs.filter(j => j.status === "queued").length;
    document.getElementById("m-jobs-running").textContent = state.active || 0;
    document.getElementById("m-jobs-done").textContent = jobs.filter(j => j.status === "done").length;
    document.getElementById("m-jobs-failed").textContent = jobs.filter(j => j.status === "failed" || j.status === "cancelled").length;
  }

  function renderJobsTable() {
    const filtered = filterData(jobsTable.data, jobsTable.search, ["email", "status", "message", "mode"]);
    const pg = paginate(filtered, jobsTable.page, jobsTable.pageSize);
    const tbody = document.getElementById("jobs-tbody");
    if (pg.items.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty">' + (jobsTable.search ? "No matching jobs." : "No jobs yet.") + '</td></tr>';
    } else {
      tbody.innerHTML = pg.items.map(function (job) {
        const actions = [];
        if (job.status === "queued" || job.status === "running") {
          actions.push('<button class="btn btn-sm btn-danger" data-jact="cancel" data-jid="' + job.id + '">Cancel</button>');
        }
        if (job.status === "failed" || job.status === "cancelled" || job.status === "done") {
          actions.push('<button class="btn btn-sm btn-primary" data-jact="retry" data-jid="' + job.id + '">Retry</button>');
        }
        if (job.accountId) {
          actions.push('<button class="btn btn-sm" data-jact="cookie" data-jid="' + job.id + '" data-aid="' + job.accountId + '">Cookie</button>');
        }
        return '<tr>' +
          '<td><b>' + esc(job.email) + '</b>' + (job.auxEmail ? '<div class="reason-cell">aux: ' + esc(job.auxEmail) + '</div>' : '') + (job.proxyUrl ? '<div class="reason-cell">proxy: ' + esc(job.proxyUrl) + '</div>' : '') + (job.mode ? '<div class="reason-cell">mode: ' + esc(job.mode) + '</div>' : '') + '</td>' +
          '<td>' + statusBadge(job.status) + '</td>' +
          '<td class="reason-cell" style="max-width:300px;white-space:normal;">' + esc(job.message) + '</td>' +
          '<td>' + fmtTime(job.createdAt) + '</td>' +
          '<td><div class="actions-cell">' + actions.join("") + '</div></td>' +
        '</tr>';
      }).join("");
    }
    renderPagination("jobs-pagination", pg.page, pg.totalPages, pg.total, function (p) { jobsTable.page = p; renderJobsTable(); });
  }

  function renderAccountsTable() {
    const filtered = filterData(accountsTable.data, accountsTable.search, ["email", "name", "plan", "workspace", "status", "balance"]);
    const pg = paginate(filtered, accountsTable.page, accountsTable.pageSize);
    const tbody = document.getElementById("accounts-tbody");
    if (pg.items.length === 0) {
      tbody.innerHTML = '<tr><td colspan="9" class="empty">' + (accountsTable.search ? "No matching accounts." : "No accounts yet. Use \"Batch Login\" to import Google accounts.") + '</td></tr>';
    } else {
      tbody.innerHTML = pg.items.map(function (acc) {
        return '<tr>' +
          '<td><b>' + esc(acc.email) + '</b>' + (acc.workspace ? '<div class="reason-cell">ws: ' + esc(acc.workspace) + '</div>' : '') + '</td>' +
          '<td>' + esc(acc.name) + '</td>' +
          '<td><b style="color:var(--green);">' + esc(acc.balance || '-') + '</b></td>' +
          '<td>' + esc(acc.creditsUsed || '-') + '</td>' +
          '<td>' + esc(acc.creditsLimit || '-') + '</td>' +
          '<td>' + esc(acc.plan || '-') + '</td>' +
          '<td class="key-cell">' + esc(acc.cookiePreview) + '</td>' +
          '<td>' + statusBadge(acc.status) + '</td>' +
          '<td><div class="actions-cell">' +
            '<button class="btn btn-sm" data-aact="setup" data-aid="' + acc.id + '">Setup</button>' +
            '<button class="btn btn-sm" data-aact="sync" data-aid="' + acc.id + '">Sync</button>' +
            '<button class="btn btn-sm" data-aact="verify" data-aid="' + acc.id + '">Verify</button>' +
            '<button class="btn btn-sm" data-aact="cookie" data-aid="' + acc.id + '">Cookie</button>' +
            '<button class="btn btn-sm btn-danger" data-aact="delete" data-aid="' + acc.id + '">Del</button>' +
          '</div></td>' +
        '</tr>';
      }).join("");
    }
    renderPagination("accounts-pagination", pg.page, pg.totalPages, pg.total, function (p) { accountsTable.page = p; renderAccountsTable(); });
  }

  function renderSetupTable() {
    const filtered = filterData(setupTable.data, setupTable.search, ["email", "status", "baseUrl", "stripeUrl", "message"]);
    const pg = paginate(filtered, setupTable.page, setupTable.pageSize);
    const tbody = document.getElementById("setup-tbody");
    if (pg.items.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">' + (setupTable.search ? "No matching setup jobs." : "No setup jobs yet.") + '</td></tr>';
    } else {
      tbody.innerHTML = pg.items.map(function (job) {
        return '<tr>' +
          '<td><b>' + esc(job.email) + '</b></td>' +
          '<td>' + statusBadge(job.status) + '</td>' +
          '<td class="url-cell" title="' + esc(job.baseUrl) + '">' + esc(job.baseUrl || '-') + '</td>' +
          '<td class="key-cell">' + esc(job.apiKey ? job.apiKey.substring(0, 12) + '...' : '-') + '</td>' +
          '<td class="url-cell" title="' + esc(job.stripeUrl) + '">' + (job.stripeUrl ? '<a href="' + esc(job.stripeUrl) + '" target="_blank" style="color:var(--primary);">Stripe Link</a>' : '-') + '</td>' +
          '<td class="reason-cell" style="max-width:250px;white-space:normal;">' + esc(job.message) + '</td>' +
        '</tr>';
      }).join("");
    }
    renderPagination("setup-pagination", pg.page, pg.totalPages, pg.total, function (p) { setupTable.page = p; renderSetupTable(); });
  }

  // ---- Search + page size wiring ----
  function wireTableControls(prefix, tableState, renderFn) {
    const searchEl = document.getElementById(prefix + "-search");
    const pageSizeEl = document.getElementById(prefix + "-page-size");
    let searchTimer = null;
    searchEl.addEventListener("input", function () {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(function () {
        tableState.search = searchEl.value.trim();
        tableState.page = 1;
        renderFn();
      }, 200);
    });
    pageSizeEl.addEventListener("change", function () {
      tableState.pageSize = parseInt(pageSizeEl.value, 10);
      tableState.page = 1;
      renderFn();
    });
  }
  wireTableControls("jobs", jobsTable, renderJobsTable);
  wireTableControls("accounts", accountsTable, renderAccountsTable);
  wireTableControls("setup", setupTable, renderSetupTable);

  // ---- Clear buttons ----
  document.getElementById("jobs-clear").addEventListener("click", async function () {
    try { await apiCall("DELETE", "/jobs"); loadState(); } catch (e) { alert(e.message); }
  });
  document.getElementById("setup-clear").addEventListener("click", async function () {
    if (!confirm("Clear all setup job history?")) return;
    try { await apiCall("DELETE", "/setup-jobs"); loadState(); } catch (e) { alert(e.message); }
  });

  // ---- Table action handlers ----
  document.getElementById("jobs-tbody").addEventListener("click", async function (e) {
    const btn = e.target.closest("[data-jact]");
    if (!btn) return;
    const jid = btn.dataset.jid;
    const act = btn.dataset.jact;
    try {
      if (act === "cancel" || act === "retry") {
        await apiCall("POST", "/jobs/action?id=" + jid + "&op=" + act);
        loadState();
      } else if (act === "cookie") {
        const aid = btn.dataset.aid;
        showCookie(aid, btn.closest("tr").querySelector("b").textContent);
      }
    } catch (err) { alert(err.message); }
  });

  document.getElementById("accounts-tbody").addEventListener("click", async function (e) {
    const btn = e.target.closest("[data-aact]");
    if (!btn) return;
    const aid = btn.dataset.aid;
    const act = btn.dataset.aact;
    try {
      if (act === "delete") {
        if (!confirm("Delete this account?")) return;
        await apiCall("DELETE", "/accounts/" + aid);
        loadState();
      } else if (act === "cookie") {
        showCookie(aid, btn.closest("tr").querySelector("b").textContent);
      } else if (act === "sync") {
        btn.textContent = "...";
        await apiCall("POST", "/accounts/" + aid + "/sync-balance");
        btn.textContent = "Sync";
        loadState();
      } else if (act === "setup") {
        await apiCall("POST", "/accounts/" + aid + "/setup", {});
        loadState();
      } else if (act === "verify") {
        btn.textContent = "...";
        try {
          const resp = await apiCall("POST", "/accounts/" + aid + "/verify-payment");
          if (resp.ok) {
            alert("Payment verification:\n" + JSON.stringify(resp.body, null, 2));
          } else {
            alert("Payment verification (HTTP " + resp.status_code + "):\n" + JSON.stringify(resp.body || resp.redirect, null, 2));
          }
        } catch (e) { alert(e.message); }
        btn.textContent = "Verify";
      }
    } catch (err) { alert(err.message); btn.textContent = act === "sync" ? "Sync" : btn.textContent; }
  });

  // ---- Cookie modal ----
  async function showCookie(aid, email) {
    try {
      const resp = await apiCall("GET", "/accounts/" + aid + "/cookie");
      document.getElementById("cookie-email").textContent = email;
      document.getElementById("cookie-text").value = resp.cookie || "";
      document.getElementById("cookie-modal").classList.remove("hidden");
    } catch (err) { alert(err.message); }
  }
  document.getElementById("cookie-close").addEventListener("click", function () {
    document.getElementById("cookie-modal").classList.add("hidden");
  });
  document.getElementById("cookie-copy").addEventListener("click", function () {
    const ta = document.getElementById("cookie-text");
    ta.select();
    document.execCommand("copy");
  });
  document.getElementById("cookie-modal").addEventListener("click", function (e) {
    if (e.target === this) this.classList.add("hidden");
  });

  // ---- Batch modal ----
  document.getElementById("batch-btn").addEventListener("click", function () {
    document.getElementById("batch-modal").classList.remove("hidden");
  });
  document.getElementById("batch-cancel").addEventListener("click", function () {
    document.getElementById("batch-modal").classList.add("hidden");
  });
  document.getElementById("batch-modal").addEventListener("click", function (e) {
    if (e.target === this) this.classList.add("hidden");
  });
  document.getElementById("batch-submit").addEventListener("click", async function () {
    const text = document.getElementById("batch-text").value.trim();
    const name = document.getElementById("batch-name").value.trim();
    const headless = document.getElementById("batch-headless").value === "true";
    const proxyUrl = document.getElementById("batch-proxy").value.trim();
    const mode = document.getElementById("batch-mode").value;
    if (!text) { alert("Please paste at least one account line."); return; }
    try {
      const resp = await apiCall("POST", "/batch", { text, name, proxyUrl, mode, headless });
      document.getElementById("batch-modal").classList.add("hidden");
      document.getElementById("batch-text").value = "";
      document.getElementById("batch-proxy").value = "";
      if (resp.errors && resp.errors.length > 0) {
        alert("Queued " + resp.queued + " jobs.\n\nErrors:\n" + resp.errors.join("\n"));
      }
      loadState();
    } catch (err) { alert(err.message); }
  });

  // ---- Settings modal ----
  document.getElementById("settings-btn").addEventListener("click", async function () {
    try {
      const s = await apiCall("GET", "/settings");
      document.getElementById("set-base-url").value = s.baseUrl || "https://modal.com";
      document.getElementById("set-proxy").value = s.proxyUrl || "";
      document.getElementById("set-concurrency").value = s.jobConcurrency || 1;
      document.getElementById("settings-modal").classList.remove("hidden");
    } catch (err) { alert(err.message); }
  });
  document.getElementById("settings-cancel").addEventListener("click", function () {
    document.getElementById("settings-modal").classList.add("hidden");
  });
  document.getElementById("settings-save").addEventListener("click", async function () {
    const body = {
      baseUrl: document.getElementById("set-base-url").value.trim(),
      proxyUrl: document.getElementById("set-proxy").value.trim(),
      jobConcurrency: parseInt(document.getElementById("set-concurrency").value, 10) || 1,
    };
    try {
      await apiCall("POST", "/settings", body);
      document.getElementById("settings-modal").classList.add("hidden");
    } catch (err) { alert(err.message); }
  });

  loadState();
})();
