const $ = (selector) => document.querySelector(selector);

const api = async (url, options = {}) => {
  const response = await fetch(url, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  if (!response.ok) {
    const message = (await response.text()).trim();
    throw new Error(message || `请求失败（${response.status}）`);
  }
  return response.json();
};

const esc = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (char) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char],
  );

const installCommand = (base) =>
  `curl -fsSL ${base.replace(/\/$/, "")}/install | sudo bash`;

const toLocalInputValue = (value) => {
  if (!value) return "";
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
};

let toastTimer;
const showMessage = (message, type = "success") => {
  const toast = $("#message");
  toast.textContent = message;
  toast.className = `toast show${type === "error" ? " error" : ""}`;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toast.className = "toast";
  }, 3200);
};

const copyText = async (value) => {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("复制失败，请手动选择命令");
};

const showInstallCommands = (settings) => {
  const publicCommand = $("#publicUrlCommand");
  const publicCopy = document.querySelector('[data-copy="publicUrlCommand"]');
  if (settings.public_url) {
    publicCommand.textContent = installCommand(settings.public_url);
    publicCopy.disabled = false;
  } else {
    publicCommand.textContent = "未配置 ANYSSH_PUBLIC_URL";
  }
  $("#directUrlCommand").textContent = installCommand(settings.direct_url);
  document.querySelector('[data-copy="directUrlCommand"]').disabled = false;
};

const clientState = (client) => {
  if (client.disabled) return { label: "已禁用", className: "disabled" };
  if (client.expires_at && new Date(client.expires_at) <= new Date()) {
    return { label: "已过期", className: "expired" };
  }
  return { label: "可访问", className: "online" };
};

const clientRow = (client) => {
  const state = clientState(client);
  const expiry = toLocalInputValue(client.expires_at);
  const rotationMinutes = client.rotate_seconds / 60;
  const expiryNote = client.expires_at ? "到点后当前会话将断开" : "留空表示长期有效";
  return `
    <tr>
      <td data-label="设备">
        <strong class="device-name" title="${esc(client.hostname)}">${esc(client.hostname)}</strong>
        <div class="device-meta" title="${esc(client.username)} · ${esc(client.id)}">${esc(client.username)} · ${esc(client.id)}</div>
      </td>
      <td data-label="运行环境"><span class="runtime">${esc(client.os)}/${esc(client.arch)}</span></td>
      <td data-label="访问状态"><span class="status ${state.className}">${state.label}</span></td>
      <td data-label="链接轮换">
        <div class="rotation-control">
          <div class="compact-number">
            <input type="number" min="0" step="1" inputmode="numeric" value="${esc(rotationMinutes)}" data-rotation="${esc(client.id)}" aria-label="${esc(client.hostname)} 的链接轮换周期" />
            <span>分钟</span>
          </div>
          <button class="button subtle" type="button" data-set-rotation="${esc(client.id)}">应用</button>
        </div>
        <span class="expiry-note">${rotationMinutes === 0 ? "不自动轮换" : `每 ${esc(rotationMinutes)} 分钟更换`}</span>
      </td>
      <td data-label="访问有效期">
        <div class="expiry-control">
          <input class="expiry-input" type="datetime-local" value="${esc(expiry)}" data-expire="${esc(client.id)}" aria-label="${esc(client.hostname)} 的访问截止时间" />
          <button class="button subtle" type="button" data-exp="${esc(client.id)}">应用</button>
          ${client.expires_at ? `<button class="button subtle" type="button" data-clear-exp="${esc(client.id)}">清除</button>` : ""}
        </div>
        <span class="expiry-note">${expiryNote}</span>
      </td>
      <td data-label="操作">
        <div class="row-actions">
          <button class="button primary" type="button" data-open="${esc(client.link)}">打开终端</button>
          <button class="button ghost" type="button" data-rotate="${esc(client.id)}">更换链接</button>
          <button class="button ${client.disabled ? "subtle" : "danger"}" type="button" data-disable="${esc(client.id)}" data-value="${!client.disabled}">${client.disabled ? "启用" : "禁用"}</button>
        </div>
      </td>
    </tr>`;
};

let loadingClients = false;
async function loadClients({ force = false } = {}) {
  if (loadingClients) return;
  if (!force && document.activeElement?.matches(".expiry-input, [data-rotation]")) return;
  loadingClients = true;
  const refreshButton = $("#refreshClients");
  refreshButton.disabled = true;
  try {
    const rows = await api("/api/admin/clients");
    $("#clients").innerHTML = rows.length
      ? rows.map(clientRow).join("")
      : '<tr><td class="empty-state" colspan="6">暂无已连接客户端</td></tr>';
    const states = rows.map(clientState);
    $("#onlineCount").textContent = states.filter((state) => state.className === "online").length;
    $("#restrictedCount").textContent = states.filter((state) => state.className !== "online").length;
  } finally {
    loadingClients = false;
    refreshButton.disabled = false;
  }
}

const setButtonBusy = (button, busy) => {
  if (busy) {
    button.dataset.originalText = button.textContent;
    button.textContent = "处理中…";
    button.disabled = true;
  } else {
    button.textContent = button.dataset.originalText || button.textContent;
    button.disabled = false;
  }
};

document.addEventListener("click", async (event) => {
  const button = event.target.closest("button");
  if (!button) return;

  try {
    if (button.dataset.copy) {
      await copyText($("#" + button.dataset.copy).textContent);
      showMessage("安装命令已复制");
      return;
    }

    if (button.dataset.open) {
      const terminal = window.open(button.dataset.open, "_blank");
      if (terminal) {
        terminal.opener = null;
      } else {
        showMessage("浏览器拦截了新窗口，请允许弹窗后重试", "error");
      }
      return;
    }

    if (button.dataset.rotate) {
      if (!window.confirm("更换后，当前访问链接会立即失效。继续吗？")) return;
      setButtonBusy(button, true);
      await api(`/api/admin/clients/${encodeURIComponent(button.dataset.rotate)}/rotate`, { method: "POST" });
      showMessage("已通知客户端更换访问链接");
    }

    if (button.dataset.disable) {
      const disabling = button.dataset.value === "true";
      if (disabling && !window.confirm("禁用后，当前终端会话将断开。继续吗？")) return;
      setButtonBusy(button, true);
      await api(`/api/admin/clients/${encodeURIComponent(button.dataset.disable)}/disable`, {
        method: "POST",
        body: JSON.stringify({ disabled: disabling }),
      });
      showMessage(disabling ? "客户端访问已禁用" : "客户端访问已启用");
    }

    if (button.dataset.setRotation) {
      const id = button.dataset.setRotation;
      const input = document.querySelector(`[data-rotation="${CSS.escape(id)}"]`);
      const minutes = Number(input.value);
      if (!Number.isFinite(minutes) || minutes < 0) {
        showMessage("请输入不小于 0 的分钟数", "error");
        return;
      }
      setButtonBusy(button, true);
      await api(`/api/admin/clients/${encodeURIComponent(id)}/rotation`, {
        method: "POST",
        body: JSON.stringify({ rotate_seconds: Math.round(minutes * 60) }),
      });
      showMessage(minutes === 0 ? "该客户端已关闭自动轮换" : "该客户端的轮换周期已更新");
    }

    if (button.dataset.exp || button.dataset.clearExp) {
      const id = button.dataset.exp || button.dataset.clearExp;
      const input = document.querySelector(`[data-expire="${CSS.escape(id)}"]`);
      const value = button.dataset.clearExp ? "" : input.value;
      if (value && new Date(value) <= new Date()) {
        if (!window.confirm("所选时间已经过去，应用后访问会立即过期。继续吗？")) return;
      }
      setButtonBusy(button, true);
      await api(`/api/admin/clients/${encodeURIComponent(id)}/expire`, {
        method: "POST",
        body: JSON.stringify({ expires_at: value ? new Date(value).toISOString() : "" }),
      });
      showMessage(value ? "访问截止时间已更新" : "已设为长期有效");
    }

    if (button.id === "refreshClients") {
      await loadClients({ force: true });
      showMessage("客户端列表已刷新");
      return;
    }

    await loadClients({ force: true });
  } catch (error) {
    showMessage(error.message, "error");
    if (button.dataset.originalText) setButtonBusy(button, false);
  }
});

(async () => {
  try {
    const settings = await api("/api/admin/settings");
    showInstallCommands(settings);
    await loadClients();
  } catch (error) {
    showMessage(error.message, "error");
  }
})();

setInterval(() => {
  loadClients().catch((error) => showMessage(error.message, "error"));
}, 5000);
