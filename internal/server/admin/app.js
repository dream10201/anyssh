const $ = (s) => document.querySelector(s);
const api = async (url, options = {}) => {
  const r = await fetch(url, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  if (!r.ok) throw new Error(await r.text());
  return r.json();
};
const esc = (s) =>
  String(s).replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
async function load() {
  const rows = await api("/api/admin/clients");
  $("#clients").innerHTML =
    rows
      .map(
        (c) =>
          `<tr><td><strong>${esc(c.hostname)}</strong><div class="muted">${esc(c.username)} · ${esc(c.id)}</div></td><td>${esc(c.os)}/${esc(c.arch)}</td><td>${c.disabled ? "已禁用" : "在线"}</td><td><input type="datetime-local" data-expire="${esc(c.id)}"></td><td><button data-open="${esc(c.link)}">打开</button><button data-rotate="${esc(c.id)}">换链接</button><button class="secondary" data-exp="${esc(c.id)}">设置过期</button><button class="${c.disabled ? "secondary" : "danger"}" data-disable="${esc(c.id)}" data-value="${!c.disabled}">${c.disabled ? "启用" : "禁用"}</button></td></tr>`,
      )
      .join("") || '<tr><td colspan="5">暂无连接</td></tr>';
}
document.addEventListener("click", async (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  try {
    if (b.dataset.open) open(b.dataset.open, "_blank");
    if (b.dataset.rotate)
      await api("/api/admin/clients/" + b.dataset.rotate + "/rotate", {
        method: "POST",
      });
    if (b.dataset.disable)
      await api("/api/admin/clients/" + b.dataset.disable + "/disable", {
        method: "POST",
        body: JSON.stringify({ disabled: b.dataset.value === "true" }),
      });
    if (b.dataset.exp) {
      const v = document.querySelector(
        `[data-expire="${b.dataset.exp}"]`,
      ).value;
      await api("/api/admin/clients/" + b.dataset.exp + "/expire", {
        method: "POST",
        body: JSON.stringify({
          expires_at: v ? new Date(v).toISOString() : "",
        }),
      });
    }
    await load();
  } catch (err) {
    $("#message").textContent = err.message;
  }
});
(async () => {
  const s = await api("/api/admin/settings");
  $("#rotation").value = s.rotate_seconds / 60;
  await load();
})();
setInterval(load, 5000);
$("#saveRotation").onclick = async () => {
  const minutes = Number($("#rotation").value);
  if (!Number.isFinite(minutes) || minutes < 0) {
    $("#message").textContent = "请输入不小于 0 的分钟数";
    return;
  }
  await api("/api/admin/settings", {
    method: "PUT",
    body: JSON.stringify({ rotate_seconds: Math.round(minutes * 60) }),
  });
  $("#message").textContent =
    minutes === 0 ? "已设置为永久链接" : "刷新时间已更新";
};
