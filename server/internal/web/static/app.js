// SCS-System · giao diện quản trị.
// Không dùng thư viện ngoài, không có script/style inline (CSP chặn). Mọi giá trị
// đưa vào HTML đều đi qua esc() để không bị chèn mã.
'use strict';

(() => {
  const THEME_KEY = 'scs-theme';
  const NAME_KEY = 'scs-login-name';

  // Người đang đăng nhập, lấy từ /auth/me: { id, name, role }.
  let me = null;
  let pendingCount = 0; // số máy chờ duyệt
  const isAdmin = () => !!me && me.role === 'admin';

  // ---------------------------------------------------------------- tiện ích
  const $ = (sel, el = document) => el.querySelector(sel);
  const $$ = (sel, el = document) => [...el.querySelectorAll(sel)];
  const esc = (v) => String(v ?? '').replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  const local = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* bỏ qua */ } },
  };

  const nf = new Intl.NumberFormat('vi-VN');
  const fmtNum = (n) => nf.format(n);
  const fmtTime = (d) => d.toLocaleString('vi-VN', { hour: '2-digit', minute: '2-digit', day: '2-digit', month: '2-digit' });
  function ago(d) {
    const s = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000));
    if (s < 60) return `${s} giây trước`;
    if (s < 3600) return `${Math.floor(s / 60)} phút trước`;
    if (s < 86400) return `${Math.floor(s / 3600)} giờ trước`;
    return `${Math.floor(s / 86400)} ngày trước`;
  }

  const ICON = {
    check: '<svg viewBox="0 0 24 24"><path d="M5 12l5 5 9-10"/></svg>',
    warn: '<svg viewBox="0 0 24 24"><path d="M12 3 2 20h20L12 3zM12 10v4M12 17h.01"/></svg>',
    copy: '<svg viewBox="0 0 24 24"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>',
    clock: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>',
  };

  // ---------------------------------------------------------------- API
  // Phiên nằm trong cookie HttpOnly do server đặt, trình duyệt tự gửi kèm.
  async function api(path, opts = {}) {
    return fetch(path, { ...opts, cache: 'no-store', credentials: 'same-origin' });
  }

  // ---------------------------------------------------------------- dữ liệu mẫu
  // Sinh cố định theo seed để lần nào mở cũng giống nhau. Chỉ dùng khi admin API chưa có.

  const SEV = {
    critical: ['pill-bad', 'Nghiêm trọng'],
    high: ['pill-warn', 'Cao'],
    medium: ['pill-info', 'Trung bình'],
    low: ['', 'Thấp'],
  };
  const pill = ([cls, label]) => `<span class="pill ${cls}">${esc(label)}</span>`;

  // ---------------------------------------------------------------- khung
  // Vùng nội dung; được thay bằng phần tử mới mỗi lần đổi trang để bỏ listener của trang trước.
  let view = $('#view');
  let refreshTimer = null;

  function setTitle(t) {
    $('#page-title').textContent = t;
    document.title = `${t} · SCS`;
  }

  function toast(msg) {
    const el = document.createElement('div');
    el.className = 'toast';
    el.textContent = msg;
    $('#toasts').append(el);
    setTimeout(() => el.remove(), 3600);
  }

  // Hộp thoại xác nhận; trả Promise<boolean>.
  function confirmBox(title, bodyHTML, okLabel, danger) {
    const dlg = $('#dialog');
    $('#dialog-title').textContent = title;
    $('#dialog-body').innerHTML = bodyHTML;
    $('#dialog-actions').innerHTML =
      `<button class="btn" value="cancel">Hủy</button>` +
      `<button class="btn ${danger ? 'btn-danger' : 'btn-primary'}" value="ok">${esc(okLabel)}</button>`;
    return new Promise((resolve) => {
      dlg.addEventListener('close', () => resolve(dlg.returnValue === 'ok'), { once: true });
      dlg.showModal();
    });
  }

  function infoBox(title, bodyHTML) {
    const dlg = $('#dialog');
    $('#dialog-title').textContent = title;
    $('#dialog-body').innerHTML = bodyHTML;
    $('#dialog-actions').innerHTML = `<button class="btn btn-primary" value="ok">Đóng</button>`;
    dlg.showModal();
  }

  function openDrawer(title, html) {
    $('#drawer-title').textContent = title;
    $('#drawer-body').innerHTML = html;
    const d = $('#drawer');
    d.classList.add('open');
    d.setAttribute('aria-hidden', 'false');
  }
  function closeDrawer() {
    const d = $('#drawer');
    d.classList.remove('open');
    d.setAttribute('aria-hidden', 'true');
  }


  // Thuộc tính cho nút ghi: quyền Chỉ xem thì khóa nút (server cũng chặn bằng 403).
  const adminOnly = () => (isAdmin() ? '' : ' disabled title="Cần quyền Admin"');

  // ---------------------------------------------------------------- gọi API
  // Đọc JSON; lỗi thì ném Error mang câu báo của server.
  async function getJSON(path) {
    const r = await api(path);
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || `lỗi ${r.status}`);
    return j;
  }

  async function postJSON(path, body) {
    const r = await api(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : '',
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || `lỗi ${r.status}`);
    return j;
  }

  function showLoadError(page, err) {
    page.innerHTML = `<div class="card soon">${ICON.warn}<h2>Không tải được dữ liệu</h2><p>${esc(err.message)}</p></div>`;
  }

  const KIND = {
    enroll_request: 'Máy mới chờ duyệt',
    hostname_invalid: 'Hostname sai quy ước',
    wan_ip_mismatch: 'IP khác IP WAN của siêu thị',
    fingerprint_changed: 'Phần cứng / hệ điều hành thay đổi',
    position_conflict: 'Trùng vị trí TN trong siêu thị',
    offline: 'Mất liên lạc',
    event: 'Sự kiện log',
  };

  // alertText: diễn giải phần chi tiết của cảnh báo thành một câu.
  function alertText(a) {
    const d = a.detail || {};
    switch (a.kind) {
      case 'enroll_request': return [d.ip && `Gửi từ IP ${d.ip}`, d.key_id && `mã khóa ${d.key_id}`].filter(Boolean).join(', ');
      case 'hostname_invalid': return d.error || '';
      case 'wan_ip_mismatch': return d.got ? `Gửi từ ${d.got}, IP WAN của siêu thị là ${d.expected}` : '';
      default: return Object.entries(d).map(([k, v]) => `${k}: ${v}`).join(', ');
    }
  }

  function setAlertCount(alerts) {
    $('#nav-alerts').textContent = alerts.length || '';
  }

  async function refreshAlertCount() {
    try { setAlertCount((await getJSON('/admin/v1/alerts')).alerts); } catch { /* bỏ qua */ }
  }

  // ---------------------------------------------------------------- trang: Tổng quan
  async function pageOverview() {
    setTitle('Tổng quan');
    const page = view;
    page.innerHTML = '<p class="hint">Đang tải…</p>';
    let approved, pending, alerts;
    try {
      [approved, pending, alerts] = await Promise.all([
        getJSON('/admin/v1/devices?status=approved').then((j) => j.devices),
        getJSON('/admin/v1/devices?status=pending').then((j) => j.devices),
        getJSON('/admin/v1/alerts').then((j) => j.alerts),
      ]);
    } catch (err) {
      if (page === view) showLoadError(page, err);
      return;
    }
    if (page !== view) return;
    setPendingCount(pending.length);
    setAlertCount(alerts);

    const serious = alerts.filter((a) => a.severity === 'high' || a.severity === 'critical').length;
    const byStore = {};
    approved.forEach((d) => {
      const s = (byStore[d.store_code] ||= { store: d.store_code, count: 0, pos: [] });
      s.count++;
      if (d.tn) s.pos.push(`TN${d.tn}`);
    });
    const stores = Object.values(byStore).sort((a, b) => a.store.localeCompare(b.store));

    page.innerHTML = `
      <div class="grid-kpi">
        ${kpi('Máy đang hoạt động', fmtNum(approved.length), `${fmtNum(stores.length)} siêu thị`)}
        ${kpi('Chờ duyệt', fmtNum(pending.length), 'yêu cầu đăng ký mới', pending.length ? 'is-warn' : '')}
        ${kpi('Cảnh báo đang mở', fmtNum(alerts.length), `${fmtNum(serious)} mức cao / nghiêm trọng`, serious ? 'is-bad' : (alerts.length ? 'is-warn' : ''))}
        ${kpi('Online / offline', '—', 'có khi agent gửi heartbeat (đang làm)')}
      </div>

      <div class="grid-2">
        <div class="card">
          <div class="card-head"><h2>Cảnh báo mới</h2><div class="right"><a href="#/canh-bao" class="btn btn-sm">Xem tất cả</a></div></div>
          <ul class="list">${alerts.slice(0, 6).map(alertItem).join('') || '<li><div class="grow empty">Không có cảnh báo đang mở.</div></li>'}</ul>
        </div>
        <div class="card">
          <div class="card-head"><h2>Siêu thị</h2><span class="sub">Máy đang hoạt động theo siêu thị</span></div>
          <div class="table-wrap"><table>
            <thead><tr><th>Siêu thị</th><th class="num">Số máy</th><th class="hide-sm">Vị trí</th></tr></thead>
            <tbody>${stores.map((s) => `
              <tr class="clickable" data-store="${esc(s.store)}">
                <td class="strong">${esc(s.store)}</td><td class="num">${s.count}</td>
                <td class="hide-sm mono">${esc(s.pos.sort().join(', ') || '—')}</td>
              </tr>`).join('') || '<tr><td colspan="3" class="empty">Chưa có máy nào được duyệt.</td></tr>'}
            </tbody></table></div>
        </div>
      </div>`;
    $$('tr[data-store]', page).forEach((tr) => tr.addEventListener('click', () => {
      location.hash = `#/may?q=${encodeURIComponent(tr.dataset.store)}`;
    }));
  }

  function kpi(label, value, meta, cls = '') {
    return `<div class="card kpi ${cls}"><span class="label">${esc(label)}</span><span class="value">${esc(value)}</span><span class="meta">${esc(meta)}</span></div>`;
  }

  function alertItem(a) {
    return `<li>${pill(SEV[a.severity] || SEV.low)}<div class="grow"><div class="title">${esc(KIND[a.kind] || a.kind)} · <span class="mono">${esc(a.hostname || a.store_code || '—')}</span></div><div class="desc">${esc(alertText(a))}</div></div><span class="time">${esc(ago(new Date(a.last_at)))}</span></li>`;
  }

  // ---------------------------------------------------------------- trang: Máy
  async function pageDevices(params) {
    setTitle('Máy');
    const page = view;
    const state = { q: params.get('q') || '', status: params.get('status') || 'approved', page: 0, list: [] };
    const PAGE = 15;

    page.innerHTML = `
      <div class="card">
        <div class="toolbar">
          <input type="search" id="dev-q" placeholder="Tìm hostname, siêu thị, IP, mã khóa…" value="${esc(state.q)}" aria-label="Tìm máy">
          <select id="dev-status" aria-label="Lọc trạng thái">
            <option value="approved">Đang hoạt động</option><option value="revoked">Đã thu hồi</option>
            <option value="rejected">Đã từ chối</option>
          </select>
          <span class="count" id="dev-count"></span>
        </div>
        <div class="table-wrap"><table>
          <thead><tr><th>Máy</th><th>Siêu thị</th><th>Vị trí</th><th>Liên lạc lần cuối</th><th class="hide-sm">IP</th><th class="hide-sm">Agent</th></tr></thead>
          <tbody id="dev-rows"><tr><td colspan="6" class="empty">Đang tải…</td></tr></tbody>
        </table></div>
        <div class="pager"><span id="dev-range"></span><div class="btns">
          <button class="btn btn-sm" id="dev-prev">Trước</button><button class="btn btn-sm" id="dev-next">Sau</button></div></div>
      </div>`;
    $('#dev-status').value = state.status;

    const render = () => {
      const q = state.q.trim().toLowerCase();
      const rows = state.list.filter((d) => !q ||
        [d.hostname, d.store_code, d.last_ip, d.enroll_ip, d.key_id].some((v) => (v || '').toLowerCase().includes(q)));
      const pages = Math.max(1, Math.ceil(rows.length / PAGE));
      state.page = Math.min(state.page, pages - 1);
      const slice = rows.slice(state.page * PAGE, state.page * PAGE + PAGE);
      $('#dev-count').textContent = `${fmtNum(rows.length)} máy`;
      $('#dev-range').textContent = rows.length ? `Trang ${state.page + 1} / ${pages}` : '';
      $('#dev-prev').disabled = state.page === 0;
      $('#dev-next').disabled = state.page >= pages - 1;
      $('#dev-rows').innerHTML = slice.map((d) => `
        <tr class="clickable" data-id="${d.id}">
          <td><span class="strong mono">${esc(d.hostname)}</span><span class="sub mono">${esc(d.key_id)}</span></td>
          <td>${esc(d.store_code || '—')}</td>
          <td class="mono">${esc(d.tn ? `TN${d.tn}${d.role ? ' · ' + d.role : ''}` : '—')}</td>
          <td>${d.last_seen ? esc(ago(new Date(d.last_seen))) : '—'}</td>
          <td class="hide-sm mono">${esc(d.last_ip || d.enroll_ip)}</td>
          <td class="hide-sm mono">${esc(d.agent_version || '—')}</td>
        </tr>`).join('') || `<tr><td colspan="6" class="empty">${state.list.length ? 'Không có máy nào khớp tìm kiếm.' : 'Không có máy nào.'}</td></tr>`;
    };

    const load = async () => {
      try {
        state.list = (await getJSON(`/admin/v1/devices?status=${encodeURIComponent(state.status)}`)).devices;
      } catch (err) {
        if (page === view) showLoadError(page, err);
        return;
      }
      if (page === view) render();
    };

    $('#dev-q').addEventListener('input', (e) => { state.q = e.target.value; state.page = 0; render(); });
    $('#dev-status').addEventListener('change', (e) => { state.status = e.target.value; state.page = 0; load(); });
    $('#dev-prev').addEventListener('click', () => { state.page--; render(); });
    $('#dev-next').addEventListener('click', () => { state.page++; render(); });
    $('#dev-rows').addEventListener('click', (e) => {
      const tr = e.target.closest('tr[data-id]');
      if (tr) showDevice(state.list.find((d) => String(d.id) === tr.dataset.id), load);
    });
    load();
  }

  const DEV_STATUS = {
    approved: ['pill-ok', 'Đang hoạt động'],
    pending: ['pill-warn', 'Chờ duyệt'],
    rejected: ['', 'Đã từ chối'],
    revoked: ['pill-bad', 'Đã thu hồi'],
  };

  function showDevice(d, reload) {
    if (!d) return;
    const when = (t) => (t ? `${fmtTime(new Date(t))} (${ago(new Date(t))})` : '—');
    openDrawer(d.hostname, `
      <div class="drawer-actions">${pill(DEV_STATUS[d.status] || ['', d.status])}${d.store_code ? `<span class="pill no-dot">Siêu thị ${esc(d.store_code)}</span>` : ''}</div>
      <section><h3>Thông tin</h3><dl class="kv">
        <dt>Hostname</dt><dd class="mono">${esc(d.hostname)}</dd>
        <dt>Quy ước tên</dt><dd>${d.hostname_ok ? 'Đúng' : `<span class="pill pill-warn no-dot">Sai</span> ${esc(d.hostname_error || '')}`}</dd>
        <dt>Vị trí</dt><dd class="mono">${esc(d.tn ? `TN${d.tn}${d.role ? ' · ' + d.role : ''}` : '—')}</dd>
        <dt>Hệ điều hành</dt><dd>${esc(d.os_version || '—')}</dd>
        <dt>Agent</dt><dd class="mono">${esc(d.agent_version || '—')}</dd>
        <dt>Liên lạc lần cuối</dt><dd>${esc(when(d.last_seen))}</dd>
      </dl></section>
      <section><h3>Nhận dạng</h3><dl class="kv">
        <dt>Mã khóa</dt><dd class="mono">${esc(d.key_id)}</dd>
        <dt>Fingerprint</dt><dd class="mono">${esc(d.fingerprint)}</dd>
      </dl></section>
      <section><h3>Mạng</h3><dl class="kv">
        <dt>IP lúc đăng ký</dt><dd class="mono">${esc(d.enroll_ip)}</dd>
        <dt>IP lần cuối</dt><dd class="mono">${esc(d.last_ip || '—')}</dd>
        <dt>IP WAN siêu thị</dt><dd class="mono">${esc(d.store_wan_ip || '—')}</dd>
      </dl></section>
      <section><h3>Lịch sử</h3><dl class="kv">
        <dt>Gửi đăng ký</dt><dd>${esc(when(d.requested_at))}</dd>
        <dt>${d.status === 'approved' ? 'Duyệt' : 'Quyết định'}</dt><dd>${esc(when(d.decided_at))}${d.decided_by ? ` · ${esc(d.decided_by)}` : ''}</dd>
      </dl></section>
      ${d.status === 'approved' ? `<section><h3>Thao tác</h3><div class="drawer-actions">
        <button class="btn btn-sm" data-dev-action="test-ring"${adminOnly()}>${d.test_ring ? 'Bỏ khỏi nhóm thử' : 'Thêm vào nhóm thử'}</button>
        <button class="btn btn-sm btn-danger" data-dev-action="revoke"${adminOnly()}>Thu hồi máy</button>
      </div><p class="hint">${d.test_ring ? 'Máy đang trong nhóm thử: nhận bản agent mới trước các máy khác.' : 'Máy trong nhóm thử nhận bản agent mới trước để kiểm tra.'}</p></section>` : ''}`);

    $('#drawer-body').querySelectorAll('[data-dev-action="test-ring"]').forEach((b) => b.addEventListener('click', async () => {
      b.disabled = true;
      try {
        await postJSON(`/admin/v1/devices/${d.id}/test-ring`, { on: !d.test_ring });
        toast(d.test_ring ? `Đã bỏ ${d.hostname} khỏi nhóm thử` : `Đã thêm ${d.hostname} vào nhóm thử`);
        closeDrawer();
        if (reload) reload();
      } catch (err) {
        b.disabled = false;
        toast(`Không đổi được: ${err.message}`);
      }
    }));

    $('#drawer-body').querySelectorAll('[data-dev-action="revoke"]').forEach((b) => b.addEventListener('click', async () => {
      const ok = await confirmBox('Thu hồi máy này?',
        `<p>Máy <strong class="mono">${esc(d.hostname)}</strong> sẽ không được gửi dữ liệu nữa. Muốn dùng lại phải cài lại agent (khóa mới) và duyệt lại.</p>`,
        'Thu hồi', true);
      if (!ok) return;
      b.disabled = true;
      try {
        await postJSON(`/admin/v1/devices/${d.id}/revoke`);
        toast(`Đã thu hồi ${d.hostname}`);
        closeDrawer();
        if (reload) reload();
      } catch (err) {
        b.disabled = false;
        toast(`Không thu hồi được: ${err.message}`);
      }
    }));
  }

  // ---------------------------------------------------------------- trang: Cảnh báo
  async function pageAlerts() {
    setTitle('Cảnh báo');
    const page = view;
    const state = { sev: '', all: false, list: [] };
    page.innerHTML = `
      <div class="card">
        <div class="toolbar">
          <div class="chips" id="sev-chips">
            <button class="chip active" data-sev="">Tất cả</button>
            ${Object.entries(SEV).map(([k, [, l]]) => `<button class="chip" data-sev="${k}">${esc(l)}</button>`).join('')}
          </div>
          <label class="check"><input type="checkbox" id="al-all"> Hiện cả cảnh báo đã xác nhận</label>
          <span class="count" id="al-count"></span>
        </div>
        <div class="table-wrap"><table>
          <thead><tr><th>Mức</th><th>Loại</th><th>Máy</th><th class="hide-sm">Chi tiết</th><th class="num hide-sm">Số lần</th><th>Lần cuối</th><th></th></tr></thead>
          <tbody id="al-rows"><tr><td colspan="7" class="empty">Đang tải…</td></tr></tbody></table></div>
      </div>`;

    const render = () => {
      const rows = state.list.filter((a) => !state.sev || a.severity === state.sev);
      $('#al-count').textContent = `${fmtNum(rows.length)} cảnh báo${state.all ? '' : ' đang mở'}`;
      $('#al-rows').innerHTML = rows.map((a) => `
        <tr>
          <td>${pill(SEV[a.severity] || SEV.low)}</td>
          <td>${esc(KIND[a.kind] || a.kind)}</td>
          <td><span class="strong mono">${esc(a.hostname || '—')}</span>${a.store_code ? `<span class="sub">Siêu thị ${esc(a.store_code)}</span>` : ''}</td>
          <td class="hide-sm">${esc(alertText(a))}</td>
          <td class="num hide-sm">${a.occurrences}</td>
          <td>${esc(ago(new Date(a.last_at)))}</td>
          <td>${a.acked_at
            ? `<span class="sub">Đã xác nhận · ${esc(a.acked_by)}</span>`
            : `<button class="btn btn-sm" data-ack="${a.id}"${adminOnly()}>Xác nhận</button>`}</td>
        </tr>`).join('') || `<tr><td colspan="7" class="empty">Không có cảnh báo${state.all ? '' : ' đang mở'}.</td></tr>`;
    };

    const load = async () => {
      try {
        state.list = (await getJSON(`/admin/v1/alerts${state.all ? '?state=all' : ''}`)).alerts;
      } catch (err) {
        if (page === view) showLoadError(page, err);
        return;
      }
      if (page !== view) return;
      if (!state.all) setAlertCount(state.list);
      render();
    };

    $('#sev-chips').addEventListener('click', (e) => {
      const b = e.target.closest('[data-sev]'); if (!b) return;
      state.sev = b.dataset.sev;
      $$('.chip', $('#sev-chips')).forEach((c) => c.classList.toggle('active', c === b));
      render();
    });
    $('#al-all').addEventListener('change', (e) => { state.all = e.target.checked; load(); });
    $('#al-rows').addEventListener('click', async (e) => {
      const b = e.target.closest('[data-ack]'); if (!b || b.disabled) return;
      b.disabled = true;
      try {
        await postJSON(`/admin/v1/alerts/${b.dataset.ack}/ack`);
        toast('Đã xác nhận cảnh báo');
        load();
      } catch (err) {
        b.disabled = false;
        toast(`Không xác nhận được: ${err.message}`);
      }
    });
    load();
  }

  // ---------------------------------------------------------------- trang: Chờ duyệt
  function setPendingCount(n) {
    pendingCount = n;
    $('#nav-pending').textContent = n || '';
  }

  async function refreshPendingCount() {
    try {
      const r = await api('/admin/v1/devices?status=pending');
      if (r.ok) setPendingCount((await r.json()).devices.length);
    } catch { /* bỏ qua: trang Chờ duyệt sẽ báo lỗi rõ */ }
  }

  async function pagePending() {
    setTitle('Chờ duyệt');
    const page = view;
    page.innerHTML = '<p class="hint">Đang tải…</p>';
    let list = [];
    try {
      const r = await api('/admin/v1/devices?status=pending');
      const j = await r.json();
      if (!r.ok) throw new Error(j.error || r.status);
      list = j.devices;
      setPendingCount(list.length);
    } catch (err) {
      if (page !== view) return;
      page.innerHTML = `<div class="card soon">${ICON.warn}<h2>Không tải được danh sách</h2><p>${esc(err.message)}</p></div>`;
      return;
    }
    if (page !== view) return; // đã chuyển sang trang khác trong lúc tải

    const check = (state, text) => `<li class="${state}">${state === 'ok' ? ICON.check : ICON.warn}${esc(text)}</li>`;
    const ipCheck = (d) => {
      if (!d.store_code) return check('warn', 'Chưa biết siêu thị nên chưa so được IP WAN');
      if (!d.store_wan_ip) return check('ok', `Siêu thị chưa có IP WAN: duyệt máy này sẽ đặt ${d.enroll_ip} làm IP WAN`);
      return d.store_wan_ip === d.enroll_ip
        ? check('ok', 'IP khớp IP WAN của siêu thị')
        : check('warn', `IP ${d.enroll_ip} khác IP WAN của siêu thị (${d.store_wan_ip})`);
    };
    const position = (d) => (d.tn ? `TN${d.tn}${d.role ? ' · ' + d.role : ''}` : '—');

    page.innerHTML = `
      <p class="hint">Mọi máy kết nối lần đầu đều phải được admin duyệt mới được gửi dữ liệu. Đối chiếu <strong>mã khóa</strong> với kết quả lệnh <span class="mono">scs-agent fingerprint</span> trên máy. Hostname đúng mẫu <span class="mono">mã-TN1-PRI</span>, <span class="mono">mã-TN2-SEC</span>, <span class="mono">mã-TN3</span>…</p>
      ${list.length ? `<div class="cards">${list.map((d) => {
        const ipOk = d.store_code && (!d.store_wan_ip || d.store_wan_ip === d.enroll_ip);
        return `<div class="card req">
          <div class="req-head"><div><h3 class="mono">${esc(d.hostname)}</h3><span class="sub">${esc(ago(new Date(d.requested_at)))}</span></div>
            ${!d.hostname_ok ? '<span class="pill pill-bad">Hostname sai</span>' : ipOk ? '<span class="pill pill-ok">Đủ điều kiện</span>' : '<span class="pill pill-warn">Cần kiểm tra</span>'}</div>
          <dl class="kv">
            <dt>Siêu thị</dt><dd>${esc(d.store_code || '—')}</dd>
            <dt>Vị trí</dt><dd>${esc(position(d))}</dd>
            <dt>Mã khóa</dt><dd class="mono">${esc(d.key_id)}</dd>
            <dt>IP gửi</dt><dd class="mono">${esc(d.enroll_ip)}</dd>
            <dt>Hệ điều hành</dt><dd>${esc(d.os_version || '—')}</dd>
            <dt>Agent</dt><dd class="mono">${esc(d.agent_version || '—')}</dd>
          </dl>
          <ul class="checks">
            ${d.hostname_ok ? check('ok', 'Hostname đúng quy ước') : check('warn', d.hostname_error || 'Hostname sai quy ước')}
            ${ipCheck(d)}
          </ul>
          ${d.hostname_ok ? '' : `<label class="store-field">Mã siêu thị (bắt buộc khi hostname sai)
            <input class="store-input" id="store-${d.id}" data-store-for="${d.id}" inputmode="numeric" maxlength="10" placeholder="12345" autocomplete="off"${adminOnly()}></label>`}
          <div class="req-actions">
            <button class="btn btn-danger" data-req="reject" data-id="${d.id}"${adminOnly()}>Từ chối</button>
            <button class="btn btn-primary" data-req="approve" data-id="${d.id}"${adminOnly()}>Duyệt</button>
          </div></div>`;
      }).join('')}</div>` : `<div class="card soon">${ICON.check}<h2>Không có yêu cầu nào</h2><p>Không có máy nào đang chờ duyệt.</p></div>`}`;

    page.addEventListener('click', async (e) => {
      const b = e.target.closest('[data-req]'); if (!b || b.disabled) return;
      const d = list.find((x) => String(x.id) === b.dataset.id); if (!d) return;
      const approve = b.dataset.req === 'approve';
      let store = '';
      if (approve && !d.hostname_ok) {
        store = (page.querySelector(`[data-store-for="${d.id}"]`)?.value || '').trim();
        if (!/^[0-9]{1,10}$/.test(store)) { toast('Nhập mã siêu thị (chỉ gồm chữ số) trước khi duyệt'); return; }
      }
      const ok = await confirmBox(approve ? 'Duyệt máy này?' : 'Từ chối yêu cầu?',
        approve
          ? `<p>Máy <strong class="mono">${esc(d.hostname)}</strong>${store ? ` (siêu thị ${esc(store)})` : ''} sẽ được cấp chứng chỉ ở lần hỏi lại tiếp theo (tối đa khoảng 5 phút) và bắt đầu gửi dữ liệu.</p>`
          : `<p>Yêu cầu của <strong class="mono">${esc(d.hostname)}</strong> bị từ chối; máy sẽ dừng gửi. Muốn dùng lại máy này phải cài lại agent (khóa mới) và duyệt lại.</p>`,
        approve ? 'Duyệt' : 'Từ chối', !approve);
      if (!ok) return;
      b.disabled = true;
      try {
        const r = await api(`/admin/v1/devices/${d.id}/${approve ? 'approve' : 'reject'}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: approve && store ? JSON.stringify({ store_code: store }) : '',
        });
        const j = await r.json().catch(() => ({}));
        if (!r.ok) throw new Error(j.error || `lỗi ${r.status}`);
        toast(approve ? `Đã duyệt ${d.hostname}` : `Đã từ chối ${d.hostname}`);
        pagePending();
      } catch (err) {
        b.disabled = false;
        toast(`Không ${approve ? 'duyệt' : 'từ chối'} được: ${err.message}`);
      }
    });
  }

  // ---------------------------------------------------------------- trang: Hệ thống
  async function pageSystem() {
    setTitle('Hệ thống');
    view.innerHTML = `
      <div class="status-grid" id="sys-grid"><div class="card status"><p>Đang kiểm tra…</p></div></div>
      <div class="card">
        <div class="card-head"><h2>Cổng dịch vụ</h2></div>
        <div class="table-wrap"><table>
          <thead><tr><th>Cổng</th><th>Dùng cho</th><th class="hide-sm">Ghi chú</th></tr></thead>
          <tbody>
            <tr><td class="mono strong">6222</td><td>Agent: đăng ký, heartbeat, log, rule, cập nhật</td><td class="hide-sm">Mở cho mạng cửa hàng. Bắt buộc chứng chỉ client trừ /v1/enroll.</td></tr>
            <tr><td class="mono strong">7222</td><td>Web quản trị (trang này)</td><td class="hide-sm">Luôn HTTPS. Chỉ VPN hoặc máy server (SCS_ADMIN_ALLOW); IP khác bị đóng kết nối ngay.</td></tr>
          </tbody></table></div>
      </div>
      ${isAdmin() ? `<div class="card">
        <div class="card-head"><h2>Phiên đăng nhập</h2><span class="sub">Mất khi tắt trình duyệt, tự hết sau tối đa 24 giờ</span></div>
        <div class="table-wrap"><table>
          <thead><tr><th>Người dùng</th><th>Quyền</th><th class="hide-sm">IP</th><th class="hide-sm">Đăng nhập lúc</th><th>Hoạt động cuối</th><th></th></tr></thead>
          <tbody id="sess-rows"><tr><td colspan="6" class="empty">Đang tải…</td></tr></tbody>
        </table></div>
      </div>` : ''}
      <p class="hint" id="sys-updated"></p>`;
    if (isAdmin()) {
      $('#sess-rows').addEventListener('click', onRevokeClick);
      loadSessions();
    }
    await refreshSystem();
    refreshTimer = setInterval(refreshSystem, 10000);
  }

  async function refreshSystem() {
    const grid = $('#sys-grid'); if (!grid) return;
    let health = null, ready = null, readyCode = 0;
    try { health = await (await api('/healthz')).json(); } catch { /* server không trả lời */ }
    try { const r = await api('/readyz'); readyCode = r.status; ready = await r.json(); } catch { /* bỏ qua */ }

    const cards = [];
    cards.push(statusCard('Web quản trị', health ? ['pill-ok', 'Đang chạy'] : ['pill-bad', 'Không phản hồi'],
      health ? `Phiên bản ${health.version}` : 'Không gọi được /healthz'));
    const checks = ready?.checks || {};
    const names = { postgres: 'PostgreSQL', nats: 'NATS' };
    Object.keys(checks).forEach((k) => cards.push(statusCard(names[k] || k,
      checks[k] === 'ok' ? ['pill-ok', 'Kết nối tốt'] : ['pill-bad', 'Lỗi kết nối'],
      checks[k] === 'ok' ? 'Truy vấn thử thành công' : 'Chi tiết lỗi nằm trong log server')));
    cards.push(statusCard('Sẵn sàng nhận việc', readyCode === 200 ? ['pill-ok', 'Sẵn sàng'] : ['pill-bad', 'Chưa sẵn sàng'],
      readyCode === 200 ? 'Mọi phụ thuộc hoạt động' : 'Load balancer sẽ bỏ qua node này (/readyz trả 503)'));
    grid.innerHTML = cards.join('');
    $('#sys-updated').textContent = `Tự làm mới mỗi 10 giây · lần cuối ${new Date().toLocaleTimeString('vi-VN')}`;
    setHealthDot(readyCode === 200);
  }

  async function loadSessions() {
    const body = $('#sess-rows'); if (!body) return;
    let list = [];
    try {
      const r = await api('/admin/v1/sessions');
      if (!r.ok) throw new Error(String(r.status));
      list = (await r.json()).sessions || [];
    } catch {
      body.innerHTML = '<tr><td colspan="6" class="empty">Không tải được danh sách phiên.</td></tr>';
      return;
    }
    body.innerHTML = list.map((s) => `
      <tr class="${s.current ? 'current' : ''}">
        <td><span class="strong">${esc(s.name)}</span>${s.current ? '<span class="sub">phiên này</span>' : ''}</td>
        <td>${s.role === 'admin' ? '<span class="pill pill-ok no-dot">Admin</span>' : '<span class="pill pill-info no-dot">Chỉ xem</span>'}</td>
        <td class="hide-sm mono">${esc(s.ip)}</td>
        <td class="hide-sm">${esc(fmtTime(new Date(s.created)))}</td>
        <td>${esc(ago(new Date(s.last_seen)))}</td>
        <td>${s.current ? '' : `<button class="btn btn-sm btn-danger" data-revoke="${esc(s.id)}" data-name="${esc(s.name)}">Thu hồi</button>`}</td>
      </tr>`).join('') || '<tr><td colspan="6" class="empty">Không có phiên nào.</td></tr>';
  }

  async function onRevokeClick(e) {
    const b = e.target.closest('[data-revoke]'); if (!b) return;
    const ok = await confirmBox('Thu hồi phiên đăng nhập?',
      `<p>Phiên của <strong>${esc(b.dataset.name)}</strong> sẽ bị đăng xuất ngay.</p>`, 'Thu hồi', true);
    if (!ok) return;
    const r = await api(`/admin/v1/sessions/${encodeURIComponent(b.dataset.revoke)}/revoke`, { method: 'POST' });
    toast(r.ok ? 'Đã thu hồi phiên.' : `Không thu hồi được (${r.status}).`);
    loadSessions();
  }

  function statusCard(title, state, desc) {
    return `<div class="card status"><div class="row"><h3>${esc(title)}</h3>${pill(state)}</div><p>${esc(desc)}</p></div>`;
  }

  function setHealthDot(ok) {
    const dot = $('#nav-health');
    dot.className = `dot ${ok ? 'ok' : 'bad'}`;
    dot.title = ok ? 'Hệ thống sẵn sàng' : 'Có phụ thuộc chưa sẵn sàng';
  }

  // ---------------------------------------------------------------- trang: Agent (cập nhật + cấu hình)
  // Bản agent server giữ, phát hành cho nhóm thử rồi cho tất cả; cấu hình agent theo cấp
  // (chung / siêu thị / máy). Agent nhận cả hai qua heartbeat (5 phút).
  const splitList = (s) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

  async function pageAgent() {
    setTitle('Agent: cập nhật và cấu hình');
    const page = view;
    page.innerHTML = '<p class="hint">Đang tải…</p>';
    let rel, cfg, devices;
    try {
      [rel, cfg, devices] = await Promise.all([
        getJSON('/admin/v1/agent/releases'),
        getJSON('/admin/v1/agent/config'),
        getJSON('/admin/v1/devices?status=approved').then((j) => j.devices),
      ]);
    } catch (err) {
      if (page === view) showLoadError(page, err);
      return;
    }
    if (page !== view) return;

    const ro = rel.rollout || {};
    const running = {};
    devices.forEach((d) => { const v = d.agent_version || '—'; running[v] = (running[v] || 0) + 1; });
    const testers = devices.filter((d) => d.test_ring);
    const globalCfg = cfg.configs.find((c) => c.scope === 'global');
    const overrides = cfg.configs.filter((c) => c.scope !== 'global');
    const hostOf = (id) => devices.find((d) => String(d.id) === String(id))?.hostname || `máy #${id}`;
    const ringPill = (v) => (v ? `<span class="pill pill-ok no-dot mono">${esc(v)}</span>` : '<span class="pill no-dot">Chưa phát hành</span>');

    page.innerHTML = `
      ${rel.signing ? '' : `<div class="card soon">${ICON.warn}<h2>Server chưa có khóa phát hành</h2><p>Chạy menu server mục 3 (cài lại dịch vụ) để tạo khóa; chưa có khóa thì không phát hành được bản agent.</p></div>`}
      <div class="card">
        <div class="card-head"><h2>Đang phát hành</h2><span class="sub">Máy nhóm thử theo bản "Nhóm thử" nếu có, các máy khác theo bản "Tất cả máy"</span></div>
        <dl class="kv">
          <dt>Nhóm thử (${fmtNum(testers.length)} máy)</dt><dd>${ringPill(ro.test)} ${ro.test ? `<button class="btn btn-sm" data-ring-stop="test"${adminOnly()}>Dừng</button>` : ''}</dd>
          <dt>Tất cả máy</dt><dd>${ringPill(ro.all)} ${ro.all ? `<button class="btn btn-sm" data-ring-stop="all"${adminOnly()}>Dừng</button>` : ''}</dd>
        </dl>
        <p class="hint">Dừng phát hành thì máy giữ bản đang chạy. Bản mới khởi động lỗi 3 lần liền thì agent tự quay về bản cài ban đầu.</p>
      </div>
      <div class="card">
        <div class="card-head"><h2>Bản agent trên server</h2><span class="sub">Mỗi lần cập nhật server (menu mục 4) có thêm bản agent cùng phiên bản</span></div>
        <div class="table-wrap"><table>
          <thead><tr><th>Phiên bản</th><th class="hide-sm">Kiến trúc</th><th>Đang chạy</th><th class="hide-sm">Có từ</th><th></th></tr></thead>
          <tbody>${rel.releases.map((r) => `
            <tr><td class="mono strong">${esc(r.version)}</td>
              <td class="hide-sm mono">${esc(r.arches.join(', '))}</td>
              <td>${fmtNum(running[r.version] || 0)} máy</td>
              <td class="hide-sm">${esc(fmtTime(new Date(r.created_at)))}</td>
              <td><div class="btns">
                <button class="btn btn-sm" data-rollout="test" data-version="${esc(r.version)}"${adminOnly()}${ro.test === r.version ? ' disabled' : ''}>Phát hành nhóm thử</button>
                <button class="btn btn-sm btn-primary" data-rollout="all" data-version="${esc(r.version)}"${adminOnly()}${ro.all === r.version ? ' disabled' : ''}>Phát hành tất cả</button>
              </div></td></tr>`).join('') || '<tr><td colspan="5" class="empty">Chưa có bản agent nào. Cập nhật server (menu mục 4) để có bản đầu tiên.</td></tr>'}
          </tbody></table></div>
        <p class="hint">Phiên bản đang chạy trên các máy: ${Object.entries(running).map(([v, n]) => `<span class="mono">${esc(v)}</span> × ${fmtNum(n)}`).join(', ') || '—'}</p>
      </div>
      <div class="card">
        <div class="card-head"><h2>Nhóm thử</h2><span class="sub">Đánh dấu trong chi tiết máy (trang Máy)</span></div>
        <p>${testers.map((d) => `<span class="pill no-dot mono">${esc(d.hostname)}</span>`).join(' ') || 'Chưa có máy nào trong nhóm thử.'}</p>
      </div>
      <div class="card">
        <div class="card-head"><h2>Cấu hình agent</h2><span class="sub">Máy &gt; siêu thị &gt; chung. Agent nhận trong vòng 5 phút</span></div>
        <div class="form-grid">
          <label class="field full">Dịch vụ cần báo trạng thái, dùng chung cho mọi máy
            <input id="cfg-global" placeholder="pos.service, printer.service" value="${esc((globalCfg?.watch_services || []).join(', '))}"${adminOnly()}>
            <small>Tên dịch vụ systemd, cách nhau dấu phẩy. Để trống và lưu = không theo dõi dịch vụ nào.</small></label>
        </div>
        <div class="form-actions"><button class="btn btn-primary btn-sm" id="cfg-global-save"${adminOnly()}>Lưu cấu hình chung</button></div>
        <h3>Ghi đè theo siêu thị / máy</h3>
        <div class="table-wrap"><table>
          <thead><tr><th>Áp cho</th><th>Dịch vụ</th><th class="hide-sm">Sửa lúc</th><th></th></tr></thead>
          <tbody>${overrides.map((c) => `
            <tr><td>${c.scope === 'store' ? `Siêu thị <span class="mono">${esc(c.key)}</span>` : `Máy <span class="mono">${esc(hostOf(c.key))}</span>`}</td>
              <td class="mono">${esc((c.watch_services || []).join(', ') || '(không theo dõi)')}</td>
              <td class="hide-sm">${esc(c.updated_at ? fmtTime(new Date(c.updated_at)) : '')}${c.updated_by ? ` · ${esc(c.updated_by)}` : ''}</td>
              <td><button class="btn btn-sm btn-danger" data-cfg-del="${esc(c.scope)}" data-key="${esc(c.key)}"${adminOnly()}>Bỏ ghi đè</button></td></tr>`).join('') || '<tr><td colspan="4" class="empty">Chưa có ghi đè nào.</td></tr>'}
          </tbody></table></div>
        <div class="form-grid">
          <label class="field">Áp cho
            <select id="ov-scope"${adminOnly()}><option value="store">Siêu thị (mã)</option><option value="device">Máy (hostname)</option></select></label>
          <label class="field">Mã siêu thị / hostname
            <input id="ov-key" placeholder="12345 hoặc 12345-TN1-PRI"${adminOnly()}></label>
          <label class="field full">Dịch vụ
            <input id="ov-watch" placeholder="pos.service"${adminOnly()}></label>
        </div>
        <div class="form-actions"><button class="btn btn-sm" id="ov-save"${adminOnly()}>Thêm / sửa ghi đè</button></div>
      </div>`;

    const saveCfg = async (scope, key, watch, okMsg) => {
      try {
        await postJSON('/admin/v1/agent/config', { scope, key, watch_services: watch });
        toast(okMsg);
        pageAgent();
      } catch (err) { toast(`Không lưu được: ${err.message}`); }
    };

    page.addEventListener('click', async (e) => {
      const ro2 = e.target.closest('[data-rollout]');
      if (ro2 && !ro2.disabled) {
        const ring = ro2.dataset.rollout, v = ro2.dataset.version;
        const ok = await confirmBox(ring === 'test' ? 'Phát hành cho nhóm thử?' : 'Phát hành cho tất cả máy?',
          `<p>Bản <strong class="mono">${esc(v)}</strong> sẽ được ${ring === 'test' ? `${fmtNum(testers.length)} máy nhóm thử` : 'mọi máy đã duyệt'} tải về ở lần heartbeat tới (tối đa khoảng 5 phút), kiểm chữ ký rồi tự khởi động lại.</p>`,
          'Phát hành', ring === 'all');
        if (!ok) return;
        try { await postJSON('/admin/v1/agent/rollout', { ring, version: v }); toast(`Đã phát hành ${v}`); pageAgent(); }
        catch (err) { toast(`Không phát hành được: ${err.message}`); }
        return;
      }
      const stop = e.target.closest('[data-ring-stop]');
      if (stop && !stop.disabled) {
        try { await postJSON('/admin/v1/agent/rollout', { ring: stop.dataset.ringStop, version: '' }); toast('Đã dừng phát hành'); pageAgent(); }
        catch (err) { toast(`Không dừng được: ${err.message}`); }
        return;
      }
      const del = e.target.closest('[data-cfg-del]');
      if (del && !del.disabled) { saveCfg(del.dataset.cfgDel, del.dataset.key, null, 'Đã bỏ ghi đè'); return; }
      if (e.target.closest('#cfg-global-save')) {
        saveCfg('global', '', splitList($('#cfg-global').value), 'Đã lưu cấu hình chung');
        return;
      }
      if (e.target.closest('#ov-save')) {
        const scope = $('#ov-scope').value;
        let key = $('#ov-key').value.trim();
        if (scope === 'device') {
          const d = devices.find((x) => x.hostname.toLowerCase() === key.toLowerCase());
          if (!d) { toast('Không có máy đang hoạt động với hostname này'); return; }
          key = String(d.id);
        } else if (!/^[0-9]{1,10}$/.test(key)) { toast('Mã siêu thị chỉ gồm chữ số'); return; }
        saveCfg(scope, key, splitList($('#ov-watch').value), 'Đã lưu ghi đè');
      }
    });
  }

  // ---------------------------------------------------------------- trang: sắp có
  function pageSoon(title, text) {
    setTitle(title);
    view.innerHTML = `<div class="card soon">${ICON.clock}<h2>${esc(title)}</h2><p>${esc(text)}</p></div>`;
  }

  // ---------------------------------------------------------------- điều hướng
  const routes = {
    '': pageOverview,
    may: pageDevices,
    'canh-bao': pageAlerts,
    'cho-duyet': pagePending,
    'he-thong': pageSystem,
    rule: () => pageSoon('Rule quét log', 'Soạn, ký và gán bộ rule cho từng nhóm máy. Sẽ làm ở mốc M7.'),
    'cap-nhat': pageAgent,
  };

  function route() {
    clearInterval(refreshTimer);
    closeDrawer();
    $('#app').classList.remove('menu-open');
    const raw = location.hash.replace(/^#\/?/, '');
    const [path, query] = raw.split('?');
    const page = routes[path] ? path : '';
    $$('[data-route]').forEach((a) => a.classList.toggle('active', a.dataset.route === page));
    const fresh = view.cloneNode(false);
    view.replaceWith(fresh);
    view = fresh;
    routes[page](new URLSearchParams(query || ''));
    view.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  }

  // ---------------------------------------------------------------- đăng nhập
  function showLogin(message) {
    $('#app').hidden = true;
    $('#login').hidden = false;
    const err = $('#login-error');
    err.hidden = !message;
    err.textContent = message || '';
    $('#login-code').value = '';
    const name = $('#login-name');
    if (!name.value) name.value = local.get(NAME_KEY) || '';
    $('#login-code').focus();
  }

  // Hỏi server phiên hiện tại; null nếu chưa đăng nhập hoặc phiên đã hết.
  async function fetchMe() {
    try {
      const r = await api('/auth/me');
      return r.ok ? await r.json() : null;
    } catch {
      return null;
    }
  }

  async function showApp() {
    me = await fetchMe();
    if (!me) { showLogin('Phiên đăng nhập đã hết, hãy đăng nhập lại.'); return; }
    $('#login').hidden = true;
    $('#app').hidden = false;
    $('#me-name').textContent = me.name;
    $('#me-role').textContent = isAdmin() ? 'Admin' : 'Chỉ xem';
    $('#me-role').className = `pill no-dot ${isAdmin() ? 'pill-ok' : 'pill-info'}`;
    refreshPendingCount();
    refreshAlertCount();
    try {
      const h = await (await api('/healthz')).json();
      $('#server-version').textContent = `SCS-Server ${h.version}`;
    } catch { /* bỏ qua */ }
    try { setHealthDot((await api('/readyz')).status === 200); } catch { setHealthDot(false); }
    route();
  }

  // Gửi mã đăng nhập; trả '' nếu thành công, ngược lại là câu báo lỗi để hiện ra.
  async function submitCode(code, name) {
    const r = await api('/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code, name }),
    });
    if (r.ok) return '';
    let msg = `Server lỗi (${r.status}).`;
    try {
      const j = await r.json();
      if (j.error) msg = j.error.charAt(0).toUpperCase() + j.error.slice(1) + '.';
      if (r.status === 401 && typeof j.left === 'number') msg += ` Còn ${j.left} lần thử.`;
    } catch { /* body không phải JSON */ }
    return msg;
  }

  $('#login-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = e.submitter || $('#login-form button');
    const code = $('#login-code').value.replace(/\D/g, '');
    const name = $('#login-name').value.trim();
    if (!code || !name) return;
    btn.disabled = true;
    try {
      const problem = await submitCode(code, name);
      if (problem) { showLogin(problem); return; }
      local.set(NAME_KEY, name);
      showApp();
    } catch {
      showLogin('Không kết nối được server.');
    } finally {
      btn.disabled = false;
    }
  });

  // ---------------------------------------------------------------- nút chung
  document.addEventListener('click', (e) => {
    const a = e.target.closest('[data-action]'); if (!a) return;
    switch (a.dataset.action) {
      case 'menu': $('#app').classList.toggle('menu-open'); break;
      case 'close-drawer': closeDrawer(); break;
      case 'logout':
        api('/auth/logout', { method: 'POST' }).catch(() => {}).finally(() => { me = null; showLogin(''); });
        break;
      case 'theme': {
        const dark = document.documentElement.dataset.theme
          ? document.documentElement.dataset.theme === 'dark'
          : matchMedia('(prefers-color-scheme: dark)').matches;
        document.documentElement.dataset.theme = dark ? 'light' : 'dark';
        local.set(THEME_KEY, document.documentElement.dataset.theme);
        break;
      }
    }
  });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeDrawer(); });
  window.addEventListener('hashchange', () => { if (!$('#app').hidden) route(); });

  // ---------------------------------------------------------------- khởi động
  const savedTheme = local.get(THEME_KEY);
  if (savedTheme === 'light' || savedTheme === 'dark') document.documentElement.dataset.theme = savedTheme;

  fetchMe().then((m) => { if (m) showApp(); else showLogin(''); });
})();
