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
  // liveReload: trang hiện tại tự tải lại khi luồng thông báo báo có thay đổi (null = không).
  let liveReload = null;

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
    event: 'Sự cố (log mức cao)',
  };

  // alertText: diễn giải phần chi tiết của cảnh báo thành một câu.
  function alertText(a) {
    const d = a.detail || {};
    switch (a.kind) {
      case 'enroll_request': return [d.ip && `Gửi từ IP ${d.ip}`, d.key_id && `mã khóa ${d.key_id}`].filter(Boolean).join(', ');
      case 'hostname_invalid': return d.error || '';
      case 'wan_ip_mismatch': return d.got ? `Gửi từ ${d.got}, IP WAN của siêu thị là ${d.expected}` : '';
      case 'event': return (d.rules || []).slice(0, 4).map((r) => `${r.rule} ×${r.count}`).join(', ');
      case 'offline': return 'Không nghe máy gửi gì; tự đóng khi máy gửi lại';
      default: return Object.entries(d).map(([k, v]) => `${k}: ${v}`).join(', ');
    }
  }

  function setAlertCount(alerts) {
    $('#nav-alerts').textContent = alerts.length || '';
  }

  async function refreshAlertCount() {
    try { setAlertCount((await getJSON('/admin/v1/alerts')).alerts); } catch { /* bỏ qua */ }
  }

  // ---------------------------------------------------------------- thông báo tức thời
  // Luồng Server-Sent Events /admin/v1/stream (cùng cổng, cùng phiên đăng nhập, không dịch vụ ngoài).
  // Server đẩy số liệu khi có thay đổi: số trên thanh bên cập nhật ngay, trang đang xem tự tải lại,
  // cảnh báo mới hiện ở góc màn hình. Bật "Thông báo" thì thêm thông báo của hệ điều hành và tiếng báo
  // cho cảnh báo mức cao / nghiêm trọng / mất liên lạc (cài đặt lưu trong trình duyệt này).
  const NOTIFY_KEY = 'scs-notify';
  const SEV_RANK = { info: 1, low: 2, medium: 3, high: 4, critical: 5 };
  let live = null;
  let liveSeen = null; // id cảnh báo → mức đã báo (null = chưa nhận gói đầu)
  let liveRetry = null;
  let audioCtx = null;
  const notifyOn = () => local.get(NOTIFY_KEY) === '1';

  function setNotifyButton() {
    const b = $('#notify-btn');
    if (!b) return;
    const on = notifyOn();
    b.classList.toggle('on', on);
    b.title = on ? 'Thông báo đang bật (bấm để tắt)' : 'Bật thông báo và tiếng báo cho cảnh báo mới';
    b.setAttribute('aria-pressed', on ? 'true' : 'false');
  }

  function beep(serious) {
    try {
      audioCtx ||= new AudioContext();
      const t = audioCtx.currentTime;
      (serious ? [880, 660, 880] : [660]).forEach((f, i) => {
        const o = audioCtx.createOscillator();
        const g = audioCtx.createGain();
        o.frequency.value = f;
        g.gain.setValueAtTime(0.0001, t + i * 0.22);
        g.gain.exponentialRampToValueAtTime(0.2, t + i * 0.22 + 0.02);
        g.gain.exponentialRampToValueAtTime(0.0001, t + i * 0.22 + 0.2);
        o.connect(g).connect(audioCtx.destination);
        o.start(t + i * 0.22);
        o.stop(t + i * 0.22 + 0.21);
      });
    } catch { /* trình duyệt chặn âm thanh: bỏ qua */ }
  }

  function notifyAlert(a) {
    const host = a.hostname || a.store_code || '';
    const title = `${(SEV[a.severity] || SEV.low)[1]} · ${KIND[a.kind] || a.kind}`;
    const body = [host, alertText(a)].filter(Boolean).join(' — ');
    toast(`${title}${body ? `: ${body}` : ''}`);
    const loud = a.kind === 'offline' || SEV_RANK[a.severity] >= SEV_RANK.high;
    if (!notifyOn() || !loud) return;
    beep(a.severity === 'critical');
    if ('Notification' in window && Notification.permission === 'granted') {
      try {
        const n = new Notification(title, { body, tag: `scs-alert-${a.id}` });
        n.onclick = () => { window.focus(); location.hash = '#/canh-bao'; n.close(); };
      } catch { /* bỏ qua */ }
    }
  }

  function onPulse(p) {
    $('#nav-alerts').textContent = p.open_alerts || '';
    setPendingCount(p.pending);
    $('#nav-rules').textContent = p.rules_pending || '';
    const changed = p.changed || [];
    if (liveSeen === null) {
      liveSeen = new Map(changed.map((a) => [a.id, SEV_RANK[a.severity] || 0]));
    } else {
      for (const a of changed) {
        const rank = SEV_RANK[a.severity] || 0;
        const prev = liveSeen.get(a.id);
        liveSeen.set(a.id, Math.max(rank, prev || 0));
        // Chỉ báo cảnh báo mới hoặc vừa lên mức cao hơn (cảnh báo cũ lặp lại thì không báo nữa).
        if (prev === undefined || rank > prev) notifyAlert(a);
      }
    }
    scheduleLive();
  }

  // scheduleLive: tải lại trang đang xem khi có thay đổi, gom nhiều pulse liền nhau (tối đa 1 lần / 3 giây),
  // bỏ qua lúc đang mở hộp thoại / chi tiết máy / đang gõ (lần pulse sau sẽ tải).
  let liveTimer = null;
  let liveLast = 0;
  function scheduleLive() {
    if (!liveReload || liveTimer) return;
    const wait = Math.max(0, 3000 - (Date.now() - liveLast));
    liveTimer = setTimeout(() => {
      liveTimer = null;
      const busy = $('#dialog').open || $('#drawer').classList.contains('open') ||
        (document.activeElement && ['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement.tagName));
      if (!liveReload || busy || document.hidden) return;
      liveLast = Date.now();
      liveReload();
    }, wait);
  }

  // swapView: thay vùng nội dung bằng phần tử mới (bỏ listener cũ) ngay trước khi vẽ lại, giữ vị trí cuộn.
  // Gọi rồi gán innerHTML trong cùng một lượt nên không nháy trang trắng.
  function swapView() {
    const y = window.scrollY;
    const fresh = view.cloneNode(false);
    view.replaceWith(fresh);
    view = fresh;
    requestAnimationFrame(() => window.scrollTo(0, y));
    return fresh;
  }
  const softSig = {}; // trang → dấu dữ liệu lần vẽ gần nhất (giống thì không vẽ lại)

  function startLive() {
    stopLive();
    if (!('EventSource' in window)) return;
    live = new EventSource('/admin/v1/stream');
    live.addEventListener('pulse', (e) => {
      try { onPulse(JSON.parse(e.data)); } catch { /* gói hỏng: bỏ qua */ }
    });
    live.addEventListener('logout', () => { stopLive(); showLogin('Phiên đăng nhập đã hết, hãy đăng nhập lại.'); });
    live.onerror = () => {
      // Trình duyệt tự nối lại khi mất mạng. Bị từ chối hẳn (hết phiên, server tắt luồng) thì thử lại sau 30 giây.
      if (live && live.readyState === EventSource.CLOSED) {
        stopLive();
        liveRetry = setTimeout(() => { fetchMe().then((m) => { if (m && !$('#app').hidden) startLive(); }); }, 30000);
      }
    };
  }

  function stopLive() {
    clearTimeout(liveRetry);
    if (live) live.close();
    live = null;
  }

  async function toggleNotify() {
    if (notifyOn()) {
      local.set(NOTIFY_KEY, '0');
      setNotifyButton();
      toast('Đã tắt thông báo và tiếng báo');
      return;
    }
    local.set(NOTIFY_KEY, '1');
    setNotifyButton();
    beep(false); // bấm nút là cho phép trình duyệt phát âm thanh
    if ('Notification' in window && Notification.permission === 'default') {
      try { await Notification.requestPermission(); } catch { /* bỏ qua */ }
    }
    const granted = 'Notification' in window && Notification.permission === 'granted';
    toast(granted ? 'Đã bật thông báo và tiếng báo cho cảnh báo mức cao, nghiêm trọng, mất liên lạc'
      : 'Đã bật tiếng báo; trình duyệt chưa cho hiện thông báo hệ điều hành (xem quyền của trang)');
  }

  // ---------------------------------------------------------------- trang: Tổng quan
  async function pageOverview(soft) {
    setTitle('Tổng quan');
    liveReload = () => pageOverview(true);
    let page = view;
    soft = soft === true; // route() truyền tham số URL: không phải tải lại mềm
    if (!soft) page.innerHTML = '<p class="hint">Đang tải…</p>';
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
    // Bộ rule cần ký: không tải được (kho chưa mở) thì bỏ qua, trang vẫn hiện.
    const ruleList = await getJSON('/admin/v1/rules').then((j) => j.entries).catch(() => []);
    if (page !== view) return;
    setPendingCount(pending.length);
    setAlertCount(alerts);
    setRuleCount(ruleList);
    const sig = JSON.stringify([approved.map((d) => [d.id, d.store_code, d.tn]), pending.length,
      alerts.map((a) => [a.id, a.severity, a.occurrences]), ruleList.map((e) => [e.name, e.version, e.status])]);
    if (soft && sig === softSig.overview) return;
    softSig.overview = sig;
    if (soft) page = swapView();
    const toSign = ruleList.filter((e) => e.status === 'chua-ky' || e.status === 'can-ky-lai');

    const serious = alerts.filter((a) => a.severity === 'high' || a.severity === 'critical').length;
    const byStore = {};
    approved.forEach((d) => {
      const s = (byStore[d.store_code] ||= { store: d.store_code, count: 0, pos: [] });
      s.count++;
      if (d.tn) s.pos.push(`TN${d.tn}`);
    });
    const stores = Object.values(byStore).sort((a, b) => a.store.localeCompare(b.store));

    page.innerHTML = `
      ${toSign.length ? `<div class="callout rule-notice">
        <strong>${fmtNum(toSign.length)} bộ rule cần duyệt và ký:</strong>
        ${toSign.slice(0, 5).map((e) => `<span class="mono">${esc(ruleKey(e))}</span>${e.status === 'can-ky-lai' ? ' (cần ký lại)' : ''}`).join(', ')}${toSign.length > 5 ? ', …' : ''}
        <a href="#/rule" class="btn btn-sm">Xem và duyệt</a></div>` : ''}
      <div class="grid-kpi">
        ${kpi('Máy đang hoạt động', fmtNum(approved.length), `${fmtNum(stores.length)} siêu thị`)}
        ${kpi('Chờ duyệt', fmtNum(pending.length), 'yêu cầu đăng ký mới', pending.length ? 'is-warn' : '')}
        ${kpi('Cảnh báo đang mở', fmtNum(alerts.length), `${fmtNum(serious)} mức cao / nghiêm trọng`, serious ? 'is-bad' : (alerts.length ? 'is-warn' : ''))}
        ${kpi('Mất liên lạc', fmtNum(alerts.filter((a) => a.kind === 'offline').length), 'máy quá hạn không gửi gì', alerts.some((a) => a.kind === 'offline') ? 'is-warn' : '')}
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
          <input type="search" id="dev-q" placeholder="Tìm hostname, siêu thị, IP, mã khóa, bộ rule…" value="${esc(state.q)}" aria-label="Tìm máy">
          <select id="dev-status" aria-label="Lọc trạng thái">
            <option value="approved">Đang hoạt động</option><option value="revoked">Đã thu hồi</option>
            <option value="rejected">Đã từ chối</option>
          </select>
          <span class="count" id="dev-count"></span>
        </div>
        <div class="table-wrap"><table>
          <thead><tr><th>Máy</th><th>Siêu thị</th><th>Vị trí</th><th>Liên lạc lần cuối</th><th class="hide-sm">IP</th><th class="hide-sm">Agent</th><th class="hide-sm">Bộ rule</th></tr></thead>
          <tbody id="dev-rows"><tr><td colspan="7" class="empty">Đang tải…</td></tr></tbody>
        </table></div>
        <div class="pager"><span id="dev-range"></span><div class="btns">
          <button class="btn btn-sm" id="dev-prev">Trước</button><button class="btn btn-sm" id="dev-next">Sau</button></div></div>
      </div>`;
    $('#dev-status').value = state.status;

    const render = () => {
      const q = state.q.trim().toLowerCase();
      const rows = state.list.filter((d) => !q ||
        [d.hostname, d.store_code, d.last_ip, d.enroll_ip, d.key_id, d.rules && d.rules.name && `${d.rules.name}@${d.rules.version}`]
          .some((v) => (v || '').toLowerCase().includes(q)));
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
          <td class="hide-sm">${ruleCell(d.rules)}</td>
        </tr>`).join('') || `<tr><td colspan="7" class="empty">${state.list.length ? 'Không có máy nào khớp tìm kiếm.' : 'Không có máy nào.'}</td></tr>`;
    };

    const load = async () => {
      try {
        const list = (await getJSON(`/admin/v1/devices?status=${encodeURIComponent(state.status)}`)).devices;
        const sig = state.status + JSON.stringify(list);
        if (sig === state.sig) return; // không đổi: không vẽ lại
        state.sig = sig;
        state.list = list;
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
    liveReload = load;
  }

  // ruleCell: bộ rule máy báo đang cài (heartbeat gần nhất).
  function ruleCell(r) {
    if (!r) return '<span class="sub">Agent chưa báo</span>';
    if (r.error) return `<span class="pill pill-bad no-dot">Lỗi bộ rule</span>`;
    if (!r.installed) return '<span class="pill no-dot">Chưa cài</span>';
    return `<span class="mono">${esc(r.name)}@${esc(r.version)}</span>
      <span class="sub">${fmtNum(r.active)}/${fmtNum(r.total)} rule chạy trên máy · ${r.scanning ? 'đang quét log' : 'chưa quét'}</span>`;
  }

  const DEV_STATUS = {
    approved: ['pill-ok', 'Đang hoạt động'],
    pending: ['pill-warn', 'Chờ duyệt'],
    rejected: ['', 'Đã từ chối'],
    revoked: ['pill-bad', 'Đã thu hồi'],
  };

  // Dòng thời gian của máy: mỗi giai đoạn OK / cảnh báo / mất liên lạc là một dòng (lưu theo giai đoạn).
  const PERIOD_KIND = {
    ok: ['pill-ok', 'OK'],
    alert: ['pill-bad', 'Cảnh báo'],
    offline: ['pill-warn', 'Mất liên lạc'],
  };
  function fmtDur(ms) {
    const m = Math.max(0, Math.round(ms / 60000));
    if (m < 60) return `${m} phút`;
    const h = Math.floor(m / 60);
    if (h < 48) return `${h} giờ${m % 60 ? ` ${m % 60} phút` : ''}`;
    return `${Math.floor(h / 24)} ngày${h % 24 ? ` ${h % 24} giờ` : ''}`;
  }
  function closedText(by) {
    if (!by) return '';
    if (by === 'auto') return 'tự đóng';
    if (by === 'system') return 'tự chuyển';
    return `${by.replace(/^admin:/, '')} xử lý`;
  }
  // statusDiff: những gì đổi so với lần trước (lần đầu: in đủ), dạng dễ đọc.
  const STATUS_LABEL = { os: 'Hệ điều hành', kernel: 'Kernel', agent: 'Agent', rules: 'Bộ rule', ip: 'IP', time_synced: 'Đồng bộ giờ' };
  function flatStatus(c) {
    const out = {};
    if (!c) return out;
    for (const [k, v] of Object.entries(c)) {
      if (k === 'services') for (const [n, st] of Object.entries(v || {})) out[`Dịch vụ ${n}`] = st;
      else if (k === 'raid') for (const [n, st] of Object.entries(v || {})) out[`RAID ${n}`] = st;
      else if (k === 'disks_full') for (const [m, lv] of Object.entries(v || {})) out[`Ổ ${m}`] = `đầy ≥ ${lv}%`;
      else if (k === 'time_synced') out[STATUS_LABEL[k]] = v ? 'có' : 'không';
      else if (v !== '') out[STATUS_LABEL[k] || k] = String(v);
    }
    return out;
  }
  function statusDiff(cur, prev) {
    const a = flatStatus(cur);
    const b = prev ? flatStatus(prev) : null;
    const parts = [];
    for (const [k, v] of Object.entries(a)) if (!b || b[k] !== v) parts.push(`${k}: ${b && b[k] !== undefined ? `${b[k]} → ` : ''}${v}`);
    if (b) for (const k of Object.keys(b)) if (!(k in a)) parts.push(`${k}: bỏ`);
    return parts.join(' · ') || 'không đổi';
  }

  async function loadTimeline(d) {
    const box = $('#dev-timeline');
    if (!box) return;
    let tl;
    let policy = { autoclose_min: 1440, offline_min: 15 };
    try {
      const j = await getJSON(`/admin/v1/devices/${d.id}/periods?limit=50`);
      tl = j.timeline;
      policy = j.policy || policy;
    } catch (err) {
      box.innerHTML = `<h3>Dòng thời gian</h3><p class="hint">Không tải được: ${esc(err.message)}</p>`;
      return;
    }
    if (!$('#dev-timeline') || box !== $('#dev-timeline')) return;
    const lastSeen = tl.last_seen ? new Date(tl.last_seen) : null;
    const item = (p) => {
      const start = new Date(p.started_at);
      const open = !p.ended_at;
      const end = open ? (lastSeen && lastSeen > start ? lastSeen : new Date()) : new Date(p.ended_at);
      const rules = Object.entries(p.rules || {}).sort((a, b) => b[1].count - a[1].count).slice(0, 6);
      return `<li class="period period-${esc(p.kind)}">
        <div class="period-head">${pill(PERIOD_KIND[p.kind] || ['', p.kind])}
          ${p.severity && p.kind === 'alert' ? pill(SEV[p.severity] || SEV.low) : ''}
          <span class="sub">${esc(fmtTime(start))} → ${open ? '<strong>đang mở</strong>' : esc(fmtTime(end))} · ${esc(fmtDur(end - start))}</span>
          ${p.events ? `<span class="sub">${fmtNum(p.events)} sự kiện</span>` : ''}
          ${p.offline_since ? '<span class="pill pill-warn no-dot">đang mất liên lạc</span>' : ''}
          ${!open && p.closed_by ? `<span class="sub">${esc(closedText(p.closed_by))}</span>` : ''}
          ${open && p.kind === 'alert' ? `<button class="btn btn-sm btn-primary" data-resolve="${p.id}"${adminOnly()}>Đã xử lý</button>
            <span class="sub">${p.severity === 'critical' ? 'chỉ admin đóng' : `tự đóng khi yên ${esc(fmtDur(policy.autoclose_min * 60000))}`}</span>` : ''}
        </div>
        ${rules.length ? `<ul class="period-rules">${rules.map(([id, r]) => `<li><span class="mono">${esc(id)}</span>
          ${pill(SEV[r.severity] || SEV.low)} ×${fmtNum(r.count)}${r.sample ? `<div class="mono hint break-all">${esc(r.sample)}</div>` : ''}</li>`).join('')}</ul>` : ''}
        ${p.note ? `<p class="hint">Ghi chú: ${esc(p.note)}</p>` : ''}
      </li>`;
    };
    const changes = (tl.status_changes || []).slice(0, 6);
    box.innerHTML = `<h3>Dòng thời gian</h3>
      ${tl.periods.length ? `<ul class="periods">${tl.periods.map(item).join('')}</ul>` : '<p class="hint">Chưa có (máy chưa gửi heartbeat nào).</p>'}
      ${changes.length ? `<h3>Trạng thái thay đổi</h3><ul class="periods">${changes.slice(0, 5).map((c, i) => `<li><span class="sub">${esc(fmtTime(new Date(c.at)))}</span>
        <div class="hint break-all">${esc(statusDiff(c.changes, changes[i + 1] && changes[i + 1].changes))}</div></li>`).join('')}</ul>` : ''}`;
    box.querySelectorAll('[data-resolve]').forEach((b) => b.addEventListener('click', async () => {
      if (!(await confirmBox('Đã xử lý sự cố này?',
        '<p>Đóng giai đoạn cảnh báo và bắt đầu giai đoạn OK mới.</p><label class="field">Ghi chú (tùy chọn)<input id="resolve-note" maxlength="500"></label>',
        'Đã xử lý'))) return;
      const note = ($('#resolve-note') && $('#resolve-note').value) || '';
      b.disabled = true;
      try {
        await postJSON(`/admin/v1/periods/${b.dataset.resolve}/resolve`, { note });
        toast('Đã đóng sự cố');
        refreshAlertCount();
        loadTimeline(d);
      } catch (err) {
        b.disabled = false;
        toast(`Không đóng được: ${err.message}`);
      }
    }));
  }

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
      <section><h3>Bộ rule</h3>${!d.rules ? '<p class="hint">Agent chưa báo (máy chưa gửi heartbeat hoặc agent bản cũ: cập nhật agent).</p>'
        : `<dl class="kv">
        <dt>Trạng thái</dt><dd>${d.rules.error ? `<span class="pill pill-bad no-dot">Lỗi</span> ${esc(d.rules.error)}` : d.rules.installed ? '<span class="pill pill-ok no-dot">Đã cài, chữ ký đúng</span>' : 'Chưa cài bộ nào'}</dd>
        ${d.rules.name ? `<dt>Bộ</dt><dd class="mono">${esc(d.rules.name)}@${esc(d.rules.version)}</dd>` : ''}
        ${d.rules.sha256 ? `<dt>SHA-256</dt><dd class="mono break-all">${esc(d.rules.sha256)}</dd>` : ''}
        ${d.rules.installed ? `<dt>Rule chạy trên máy</dt><dd>${fmtNum(d.rules.active)} / ${fmtNum(d.rules.total)} (rule không khớp applies_to của máy thì bỏ qua)</dd>
        <dt>Quét log</dt><dd>${d.rules.scanning ? 'Đang quét liên tục, sự kiện gửi về server' : 'Chưa quét (agent bản cũ, hoặc không có rule nào chạy trên máy này)'}</dd>` : ''}
      </dl>`}</section>
      <section id="dev-timeline"><h3>Dòng thời gian</h3><p class="hint">Đang tải…</p></section>
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

    loadTimeline(d);

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
    const state = { sev: '', all: false, list: [], policy: { autoclose_min: 1440, offline_min: 15 } };
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
            ? `<span class="sub">${a.kind === 'event' ? 'Đã đóng' : 'Đã xác nhận'} · ${esc(a.acked_by)}</span>`
            : `<button class="btn btn-sm${a.kind === 'event' ? ' btn-primary' : ''}" data-ack="${a.id}" data-kind="${esc(a.kind)}"${adminOnly()}>${a.kind === 'event' ? 'Đã xử lý' : 'Xác nhận'}</button>
               ${a.kind === 'event' ? `<span class="sub">${a.severity === 'critical' ? 'Chỉ admin đóng' : `Tự đóng khi yên ${esc(fmtDur(state.policy.autoclose_min * 60000))}`}</span>` : ''}`}</td>
        </tr>`).join('') || `<tr><td colspan="7" class="empty">Không có cảnh báo${state.all ? '' : ' đang mở'}.</td></tr>`;
    };

    const load = async () => {
      try {
        const j = await getJSON(`/admin/v1/alerts${state.all ? '?state=all' : ''}`);
        const sig = String(state.all) + JSON.stringify(j.alerts);
        if (sig === state.sig) return; // không đổi: không vẽ lại
        state.sig = sig;
        state.list = j.alerts;
        state.policy = j.policy || state.policy;
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
      if (b.dataset.kind === 'event' && !(await confirmBox('Đã xử lý sự cố này?',
        '<p>Đóng giai đoạn cảnh báo của máy và bắt đầu giai đoạn OK mới. Sự cố quay lại thì sẽ mở cảnh báo mới.</p>',
        'Đã xử lý'))) return;
      b.disabled = true;
      try {
        await postJSON(`/admin/v1/alerts/${b.dataset.ack}/ack`);
        toast(b.dataset.kind === 'event' ? 'Đã đóng sự cố' : 'Đã xác nhận cảnh báo');
        load();
      } catch (err) {
        b.disabled = false;
        toast(`Không xác nhận được: ${err.message}`);
      }
    });
    load();
    liveReload = load;
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

  async function pagePending(soft) {
    setTitle('Chờ duyệt');
    liveReload = () => pagePending(true);
    let page = view;
    soft = soft === true; // route() truyền tham số URL: không phải tải lại mềm
    if (!soft) page.innerHTML = '<p class="hint">Đang tải…</p>';
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
    const sig = JSON.stringify(list);
    if (soft && sig === softSig.pending) return;
    softSig.pending = sig;
    if (soft) page = swapView();

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
        delete softSig.pending;
        pagePending(true);
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
            <tr><td class="mono strong">5222</td><td>Liên lạc giữa các server trong cụm</td><td class="hide-sm">Chỉ mở khi server đã thuộc một cụm. Chỉ nhận server có khóa trong danh sách cụm.</td></tr>
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
  // (chung / siêu thị / máy). Agent nhận cả hai qua heartbeat (5–10 phút).
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
    const globalCfg = cfg.configs.find((c) => c.scope === 'global' && c.watch_services !== null);
    const overrides = cfg.configs.filter((c) => c.scope !== 'global' && c.watch_services !== null);
    const labelRows = cfg.configs.filter((c) => c.labels);
    const scopeOrder = { global: 0, store: 1, device: 2 };
    labelRows.sort((a, b) => scopeOrder[a.scope] - scopeOrder[b.scope] || a.key.localeCompare(b.key));
    const fmtLabels = (m) => Object.keys(m).sort().map((k) => `${k}=${m[k]}`).join(', ');
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
        <div class="card-head"><h2>Cấu hình agent</h2><span class="sub">Máy &gt; siêu thị &gt; chung. Agent nhận trong vòng 10 phút</span></div>
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
      </div>
      <div class="card">
        <div class="card-head"><h2>Nhãn máy (chọn rule)</h2><span class="sub">Gộp theo từng nhãn: chung &lt; siêu thị &lt; máy. Agent nhận trong vòng 10 phút</span></div>
        <p class="hint">Agent tự biết <span class="mono">role</span> (pri / sec / phu) và <span class="mono">store</span> từ hostname, <span class="mono">os</span> (ví dụ ubuntu-22.04) từ /etc/os-release.
          Nhãn ở đây ghi đè các giá trị đó hoặc thêm nhãn tự do; rule có <span class="mono">applies_to</span> chỉ chạy trên máy có nhãn khớp.</p>
        <div class="table-wrap"><table>
          <thead><tr><th>Áp cho</th><th>Nhãn</th><th class="hide-sm">Sửa lúc</th><th></th></tr></thead>
          <tbody>${labelRows.map((c) => `
            <tr><td>${c.scope === 'global' ? 'Mọi máy' : c.scope === 'store' ? `Siêu thị <span class="mono">${esc(c.key)}</span>` : `Máy <span class="mono">${esc(hostOf(c.key))}</span>`}</td>
              <td class="mono">${esc(fmtLabels(c.labels))}</td>
              <td class="hide-sm">${esc(c.updated_at ? fmtTime(new Date(c.updated_at)) : '')}${c.updated_by ? ` · ${esc(c.updated_by)}` : ''}</td>
              <td><button class="btn btn-sm btn-danger" data-lb-del="${esc(c.scope)}" data-key="${esc(c.key)}"${adminOnly()}>Bỏ nhãn</button></td></tr>`).join('') || '<tr><td colspan="4" class="empty">Chưa đặt nhãn nào: agent dùng role / store / os tự suy ra.</td></tr>'}
          </tbody></table></div>
        <div class="form-grid">
          <label class="field">Áp cho
            <select id="lb-scope"${adminOnly()}><option value="global">Mọi máy</option><option value="store">Siêu thị (mã)</option><option value="device">Máy (hostname)</option></select></label>
          <label class="field">Mã siêu thị / hostname
            <input id="lb-key" placeholder="bỏ trống khi áp cho mọi máy"${adminOnly()}></label>
          <label class="field full">Nhãn
            <input id="lb-labels" placeholder="role=pri, vung=mien-nam" autocomplete="off" spellcheck="false"${adminOnly()}>
            <small>khoa=gia_tri, cách nhau dấu phẩy. Chữ thường, số, . _ -. Lưu sẽ thay toàn bộ nhãn ở cấp này.</small></label>
        </div>
        <div class="form-actions"><button class="btn btn-sm" id="lb-save"${adminOnly()}>Lưu nhãn</button></div>
      </div>`;

    const saveCfg = async (scope, key, watch, okMsg) => {
      try {
        await postJSON('/admin/v1/agent/config', { scope, key, watch_services: watch });
        toast(okMsg);
        pageAgent();
      } catch (err) { toast(`Không lưu được: ${err.message}`); }
    };

    const saveLabels = async (scope, key, labels, okMsg) => {
      try {
        await postJSON('/admin/v1/agent/labels', { scope, key, labels });
        toast(okMsg);
        pageAgent();
      } catch (err) { toast(`Không lưu được: ${err.message}`); }
    };

    page.addEventListener('click', async (e) => {
      const ro2 = e.target.closest('[data-rollout]');
      if (ro2 && !ro2.disabled) {
        const ring = ro2.dataset.rollout, v = ro2.dataset.version;
        const ok = await confirmBox(ring === 'test' ? 'Phát hành cho nhóm thử?' : 'Phát hành cho tất cả máy?',
          `<p>Bản <strong class="mono">${esc(v)}</strong> sẽ được ${ring === 'test' ? `${fmtNum(testers.length)} máy nhóm thử` : 'mọi máy đã duyệt'} tải về ở lần heartbeat tới (tối đa khoảng 10 phút), kiểm chữ ký rồi tự khởi động lại.</p>`,
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
      const lbDel = e.target.closest('[data-lb-del]');
      if (lbDel && !lbDel.disabled) { saveLabels(lbDel.dataset.lbDel, lbDel.dataset.key, null, 'Đã bỏ nhãn'); return; }
      if (e.target.closest('#lb-save')) {
        const scope = $('#lb-scope').value;
        let key = $('#lb-key').value.trim();
        if (scope === 'global') key = '';
        else if (scope === 'device') {
          const d = devices.find((x) => x.hostname.toLowerCase() === key.toLowerCase());
          if (!d) { toast('Không có máy đang hoạt động với hostname này'); return; }
          key = String(d.id);
        } else if (!/^[0-9]{1,10}$/.test(key)) { toast('Mã siêu thị chỉ gồm chữ số'); return; }
        const labels = {};
        for (const part of $('#lb-labels').value.split(',').map((x) => x.trim()).filter(Boolean)) {
          const i = part.indexOf('=');
          if (i < 1) { toast(`"${part}" phải có dạng khoa=gia_tri`); return; }
          labels[part.slice(0, i).trim()] = part.slice(i + 1).trim();
        }
        saveLabels(scope, key, Object.keys(labels).length ? labels : null, 'Đã lưu nhãn');
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
  // ---------------------------------------------------------------- trang: Cụm server
  // Mọi nút ở đây chỉ ghi XÁC NHẬN CỦA ADMIN TRÊN NODE NÀY. Quyết định có hiệu lực khi đủ số node
  // cũng được xác nhận (thêm node: mọi node; còn lại: quá nửa) — phải đăng nhập web từng node.
  const CLUSTER_ACT = {
    'member.add': 'thêm node',
    'member.isolate': 'cô lập',
    'member.restore': 'bỏ cô lập',
    'member.remove': 'gỡ khỏi cụm',
  };

  async function pageCluster() {
    setTitle('Cụm server');
    const page = view;
    page.innerHTML = '<p class="hint">Đang tải…</p>';
    async function load() {
      let c;
      try { c = await getJSON('/admin/v1/cluster'); } catch (err) { if (page === view) showLoadError(page, err); return; }
      if (page !== view) return;
      if (!c.enabled || !c.initialized) {
        page.innerHTML = `<div class="card soon">${ICON.clock}<h2>${c.joining ? 'Node đang xin vào cụm' : 'Server này chưa thuộc cụm nào'}</h2>
          <p>${c.joining
    ? `Đã gửi yêu cầu qua <span class="mono">${esc(c.joining.target)}</span>. Admin cần duyệt trên web của mọi node trong cụm, so với mã khóa dưới đây.`
    : 'Tạo cụm mới bằng lệnh <span class="mono">scs-server cluster init</span>, hoặc xin vào cụm đã có bằng <span class="mono">scs-server cluster join</span>.'}</p>
          ${c.enabled ? `<p>Mã node <span class="mono strong">${esc(c.node_id)}</span><br>Mã khóa <span class="mono strong">${esc(c.fingerprint)}</span></p>` : ''}</div>`;
        return;
      }
      const approved = (type, id) => Boolean((c.approvals || {})[`${type}:${id}`]);
      const actBtn = (type, m, label, cls) => (approved(type, m.id)
        ? `<button class="btn btn-sm" data-unapprove="${type}" data-id="${esc(m.id)}" data-name="${esc(m.name)}"${adminOnly()}>Rút xác nhận ${esc(CLUSTER_ACT[type])}</button>`
        : `<button class="btn btn-sm ${cls || ''}" data-approve="${type}" data-id="${esc(m.id)}" data-name="${esc(m.name)}" data-fp="${esc(m.fingerprint)}"${adminOnly()}>${esc(label)}</button>`);
      const waiting = Object.keys(c.approvals || {});
      const equiv = Object.entries(c.equivocators || {});
      page.innerHTML = `
        ${c.fork ? `<div class="card soon">${ICON.warn}<h2>Nhật ký cụm bị tách nhánh</h2><p>${esc(c.fork)}</p>
          <p>Server này đã dừng ra quyết định mới. Có node đã ký hai quyết định trái nhau: cô lập node đó rồi liên hệ người quản trị để chọn nhánh đúng.</p></div>` : ''}
        ${equiv.length ? `<div class="card soon">${ICON.warn}<h2>Có node ký hai đề nghị trái nhau</h2>
          <p>${equiv.map(([id, why]) => `<span class="mono strong">${esc(id)}</span>: ${esc(why)}`).join('<br>')}</p>
          <p>Đây là dấu hiệu node bị chiếm quyền hoặc lỗi nặng. Nên cô lập node đó.</p></div>` : ''}
        <div class="card">
          <div class="card-head"><h2>Cụm ${esc(c.cluster)}</h2><span class="sub">Quyết định cần ${fmtNum(c.majority)}/${fmtNum(c.valid)} node hợp lệ ký; thêm node cần ${fmtNum(c.valid)}/${fmtNum(c.valid)}</span></div>
          <dl class="kv">
            <dt>Mã cụm</dt><dd class="mono">${esc(c.cluster_id)}</dd>
            <dt>Node này</dt><dd><span class="mono strong">${esc(c.node_id)}</span></dd>
            <dt>Mã khóa node này</dt><dd class="mono">${esc(c.fingerprint)}</dd>
            <dt>Số quyết định</dt><dd>${fmtNum(c.seq)} (danh sách cụm đời ${fmtNum(c.epoch)})</dd>
          </dl>
        </div>
        <div class="card">
          <div class="card-head"><h2>Node trong cụm</h2><span class="sub">Dòng chữ nhỏ dưới tên là mã khóa của node</span></div>
          <p class="hint">"Cô lập tại đây" có hiệu lực ngay trên server này. Các nút còn lại là xác nhận của bạn trên server này: phải xác nhận trên đủ số server thì mới có hiệu lực.</p>
          <div class="table-wrap"><table>
            <thead><tr><th>Node</th><th>Trạng thái</th><th class="hide-sm">Địa chỉ cổng cụm</th><th></th></tr></thead>
            <tbody>${c.members.map((m) => `
              <tr><td><span class="strong">${esc(m.name)}</span>${m.self ? ' <span class="pill pill-info no-dot">node này</span>' : ''}<br><span class="mono nowrap hint">${esc(m.fingerprint)}</span></td>
                <td>${m.status === 'active' ? '<span class="pill pill-ok">Hoạt động</span>' : '<span class="pill pill-bad">Bị cô lập</span>'}
                  ${m.isolated_local ? '<br><span class="pill pill-warn no-dot">Cô lập tại node này</span>' : ''}</td>
                <td class="hide-sm mono">${m.addrs.cluster.map(esc).join('<br>')}</td>
                <td>${m.self ? '' : `<div class="btns btns-col">
                  <button class="btn btn-sm" data-local="${m.isolated_local ? 'off' : 'on'}" data-id="${esc(m.id)}" data-name="${esc(m.name)}"${adminOnly()}>${m.isolated_local ? 'Nối lại tại đây' : 'Cô lập tại đây'}</button>
                  ${m.status === 'active' ? actBtn('member.isolate', m, 'Cô lập toàn cụm', 'btn-danger') : actBtn('member.restore', m, 'Bỏ cô lập')}
                  ${actBtn('member.remove', m, 'Gỡ khỏi cụm')}
                </div>`}</td></tr>`).join('')}
            </tbody></table></div>
          ${waiting.length ? `<p class="hint">Đã xác nhận trên node này, đang chờ các node khác: ${waiting.map((k) => {
    const i = k.lastIndexOf(':'); return `${esc(CLUSTER_ACT[k.slice(0, i)] || k)} <span class="mono">${esc(k.slice(i + 1))}</span>`;
  }).join(', ')}. Đăng nhập web của các node còn lại và xác nhận cùng việc đó.</p>` : ''}
        </div>
        <div class="card">
          <div class="card-head"><h2>Server chờ vào cụm</h2><span class="sub">Phải duyệt trên mọi node. Chỉ duyệt khi mã khóa trùng với mã in trên chính server đó</span></div>
          <div class="table-wrap"><table>
            <thead><tr><th>Tên node</th><th>Mã khóa (so với mã in trên server đó)</th><th class="hide-sm">Địa chỉ</th><th class="hide-sm">Nhận lúc</th><th></th></tr></thead>
            <tbody>${c.joins.map((j) => `
              <tr><td><span class="strong">${esc(j.member.name)}</span><br><span class="mono">${esc(j.member.id)}</span></td>
                <td class="mono strong nowrap">${esc(j.fingerprint)}</td>
                <td class="hide-sm mono">${j.member.addrs.cluster.map(esc).join('<br>')}</td>
                <td class="hide-sm">${esc(fmtTime(new Date(j.received_at)))}${j.via ? '<br>qua node khác' : ''}</td>
                <td><div class="btns">
                  ${j.approved ? '<span class="pill pill-ok no-dot">Đã duyệt trên node này</span>'
    : `<button class="btn btn-sm btn-primary" data-approve="member.add" data-id="${esc(j.member.id)}" data-name="${esc(j.member.name)}" data-fp="${esc(j.fingerprint)}"${adminOnly()}>Duyệt</button>`}
                  <button class="btn btn-sm" data-reject="${esc(j.member.id)}" data-name="${esc(j.member.name)}"${adminOnly()}>Từ chối</button>
                </div></td></tr>`).join('') || '<tr><td colspan="5" class="empty">Không có server nào đang xin vào cụm.</td></tr>'}
            </tbody></table></div>
        </div>
        <p class="hint">Tự cập nhật mỗi 10 giây.</p>`;
    }
    page.addEventListener('click', async (e) => {
      const b = e.target.closest('button');
      if (!b || b.disabled) return;
      const { id, name } = b.dataset;
      try {
        if (b.dataset.approve) {
          const type = b.dataset.approve;
          const isAdd = type === 'member.add';
          const ok = await confirmBox(`Xác nhận ${CLUSTER_ACT[type]} ${name}?`,
            `<p>Node <span class="mono strong">${esc(name)}</span> (${esc(id)})</p>
             ${isAdd ? `<p>Mã khóa: <span class="mono strong">${esc(b.dataset.fp)}</span></p><p><strong>Chỉ duyệt khi mã này trùng với mã in trên chính server đó.</strong> Thêm node cần mọi node trong cụm duyệt.</p>`
    : '<p>Đây là xác nhận trên node này. Cần quá nửa số node (không tính node bị nhắm tới) cùng xác nhận thì mới có hiệu lực.</p>'}`,
            isAdd ? 'Duyệt' : 'Xác nhận', !isAdd);
          if (!ok) return;
          const r = await postJSON('/admin/v1/cluster/approve', { type, id });
          toast(r.message);
        } else if (b.dataset.unapprove) {
          await postJSON('/admin/v1/cluster/unapprove', { type: b.dataset.unapprove, id });
          toast(`Đã rút xác nhận ${CLUSTER_ACT[b.dataset.unapprove]} ${name}`);
        } else if (b.dataset.reject) {
          if (!(await confirmBox(`Từ chối ${name}?`, '<p>Yêu cầu bị xóa trên node này. Server đó có thể gửi lại.</p>', 'Từ chối', true))) return;
          await postJSON('/admin/v1/cluster/reject-join', { id: b.dataset.reject });
          toast(`Đã từ chối ${name}`);
        } else if (b.dataset.local) {
          const on = b.dataset.local === 'on';
          if (on && !(await confirmBox(`Cô lập ${name} tại node này?`,
            '<p>Node này ngừng liên lạc với node đó <strong>ngay lập tức</strong>, không cần node khác đồng ý. Dùng khi nghi node đó bị tấn công. Các node khác không bị ảnh hưởng cho tới khi bạn cô lập trên chúng hoặc cô lập toàn cụm.</p>', 'Cô lập ngay', true))) return;
          await postJSON('/admin/v1/cluster/isolate-local', { id, on });
          toast(on ? `Đã cô lập ${name} tại node này` : `Đã nối lại ${name} tại node này`);
        } else {
          return;
        }
        load();
      } catch (err) {
        toast(`Không làm được: ${err.message}`);
      }
    });
    await load();
    refreshTimer = setInterval(() => { if (!$('#dialog').open) load(); }, 10000);
  }

  // Địa chỉ server gửi cho agent: thêm địa chỉ mới trước khi đổi mạng, gỡ địa chỉ cũ khi hết máy dùng.
  async function pageServers() {
    setTitle('Địa chỉ server');
    const page = view;
    page.innerHTML = '<p class="hint">Đang tải…</p>';
    let hosts = new Map();
    async function load() {
      let s;
      try {
        const [j, devs] = await Promise.all([
          getJSON('/admin/v1/agent/servers'),
          getJSON('/admin/v1/devices?status=approved').then((x) => x.devices).catch(() => []),
        ]);
        s = j.stats;
        hosts = new Map(devs.map((d) => [d.id, d.hostname]));
      } catch (err) { if (page === view) showLoadError(page, err); return; }
      if (page !== view) return;
      const pct = s.online ? Math.round((s.synced * 100) / s.online) : 0;
      const inList = s.addrs.filter((a) => a.in_list);
      const outside = s.addrs.filter((a) => !a.in_list);
      const row = (a, i) => `
        <tr><td><span class="mono strong">${esc(a.url)}</span>${a.note ? `<br><span class="hint">${esc(a.note)}</span>` : ''}</td>
          <td class="num">${fmtNum(a.using)}</td>
          <td class="hide-sm">${esc(a.added ? fmtTime(new Date(a.added)) : '')}</td>
          <td><div class="btns">
            <button class="btn btn-sm" data-act="up" data-url="${esc(a.url)}"${i === 0 ? ' disabled' : adminOnly()} aria-label="Lên">↑</button>
            <button class="btn btn-sm" data-act="down" data-url="${esc(a.url)}"${i === inList.length - 1 ? ' disabled' : adminOnly()} aria-label="Xuống">↓</button>
            <button class="btn btn-sm btn-danger" data-act="remove" data-url="${esc(a.url)}" data-using="${a.using}"${inList.length === 1 ? ' disabled title="Phải còn ít nhất một địa chỉ"' : adminOnly()}>Gỡ</button>
          </div></td></tr>`;
      page.innerHTML = `
        ${s.file_error ? `<div class="card soon">${ICON.warn}<h2>File danh sách bị lỗi</h2><p class="mono">${esc(s.file_error)}</p>
          <p>Server đang gửi danh sách cũ. Sửa bằng lệnh <span class="mono">scs-server servers set</span> trên server (menu mục 15).</p></div>` : ''}
        <div class="grid-kpi">
          <div class="card kpi"><span class="sub">Máy đang gọi về (${fmtNum(s.window_s / 60)} phút qua)</span><strong>${fmtNum(s.online)}</strong></div>
          <div class="card kpi"><span class="sub">Đã nhận danh sách hiện tại</span><strong>${fmtNum(s.synced)} <small>(${pct}%)</small></strong></div>
          <div class="card kpi"><span class="sub">Còn giữ danh sách cũ</span><strong>${fmtNum(s.previous)}</strong></div>
          <div class="card kpi"><span class="sub">Agent bản cũ (không báo)</span><strong>${fmtNum(s.unknown)}</strong></div>
        </div>
        <div class="card">
          <div class="card-head"><h2>Danh sách gửi cho agent</h2><span class="sub">${fmtNum(inList.length)}/${fmtNum(s.max)} địa chỉ · thứ tự là thứ tự agent thử</span></div>
          <div class="table-wrap"><table>
            <thead><tr><th>Địa chỉ</th><th class="num">Máy đang dùng</th><th class="hide-sm">Thêm lúc</th><th></th></tr></thead>
            <tbody>${inList.map(row).join('') || '<tr><td colspan="4" class="empty">Chưa có địa chỉ nào: agent chỉ dùng địa chỉ lúc cài.</td></tr>'}</tbody>
          </table></div>
          <div class="card-body">
            <div class="form-grid">
              <label class="field">Địa chỉ mới
                <input id="sv-url" placeholder="https://ten.ddns.net:16222" autocomplete="off" spellcheck="false"${adminOnly()}>
                <small>Dạng https://&lt;IP hoặc tên miền&gt;:&lt;cổng&gt;. IP WAN / tên miền dùng cổng chuyển tiếp trên router về 6222.</small></label>
              <label class="field">Ghi chú
                <input id="sv-note" maxlength="80" placeholder="Địa điểm mới - mạng VNPT"${adminOnly()}></label>
            </div>
            <div class="form-actions">
              <label class="check"><input type="checkbox" id="sv-first"${adminOnly()}> Đặt lên đầu</label>
              <button class="btn btn-primary btn-sm" id="sv-add"${adminOnly()}>Thêm địa chỉ</button>
            </div>
          </div>
        </div>
        ${outside.length ? `<div class="card">
          <div class="card-head"><h2>Máy còn gọi qua địa chỉ ngoài danh sách</h2><span class="sub">Địa chỉ đã gỡ hoặc địa chỉ lúc cài agent</span></div>
          <div class="table-wrap"><table><thead><tr><th>Địa chỉ</th><th class="num">Máy</th></tr></thead>
            <tbody>${outside.map((a) => `<tr><td class="mono">${esc(a.url)}</td><td class="num">${fmtNum(a.using)}</td></tr>`).join('')}</tbody></table></div>
          <div class="card-body"><p class="hint">Agent luôn giữ địa chỉ lúc cài trong danh sách của nó. Khi địa chỉ đó không còn dùng được, agent tự chuyển sang địa chỉ trong danh sách và lần khởi động sau thử địa chỉ gọi được lần cuối trước.</p></div>
        </div>` : ''}
        ${s.pending.length ? `<div class="card">
          <div class="card-head"><h2>Máy chưa nhận danh sách hiện tại</h2><span class="sub">Nhận ở lần heartbeat kế tiếp (≤ 10 phút)</span></div>
          <div class="card-body"><p>${s.pending.map((id) => `<span class="pill no-dot mono">${esc(hosts.get(id) || `#${id}`)}</span>`).join(' ')}</p></div></div>` : ''}
        <div class="card">
          <div class="card-head"><h2>Đổi địa điểm / đổi mạng không mất kết nối</h2></div>
          <div class="card-body">
            <ol class="hint">
              <li>Thêm địa chỉ mới (IP WAN mới hoặc tên miền DDNS) <strong>trước khi</strong> chuyển, lúc agent còn gọi được server.</li>
              <li>Chờ ô "Đã nhận danh sách hiện tại" gần 100% (mỗi máy nhận ở heartbeat kế tiếp, tối đa khoảng 10 phút).</li>
              <li>Chuyển server sang mạng mới. Agent thấy địa chỉ cũ không gọi được thì tự thử địa chỉ kế tiếp.</li>
              <li>Khi cột "Máy đang dùng" của địa chỉ cũ về 0, gỡ địa chỉ cũ.</li>
            </ol>
            <p class="hint">Không biết trước IP mới thì không báo trước cho agent được: nên luôn có một tên miền DDNS trong danh sách.
              Số liệu tính từ lúc server khởi động (${esc(fmtTime(new Date(s.since)))}), mỗi máy cập nhật qua heartbeat. Tự cập nhật mỗi 15 giây.</p>
          </div>
        </div>`;
    }
    async function change(body) {
      const r = await api('/admin/v1/agent/servers', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      });
      const j = await r.json().catch(() => ({}));
      return { status: r.status, j };
    }
    page.addEventListener('click', async (e) => {
      const b = e.target.closest('button');
      if (!b || b.disabled) return;
      try {
        if (b.id === 'sv-add') {
          const url = $('#sv-url').value.trim();
          if (!url) { toast('Nhập địa chỉ trước'); return; }
          const { status, j } = await change({ action: 'add', url, note: $('#sv-note').value.trim(), first: $('#sv-first').checked });
          if (status !== 200) throw new Error(j.error || `lỗi ${status}`);
          toast(`Đã thêm ${url}. Agent nhận trong vòng 10 phút.`);
        } else if (b.dataset.act === 'remove') {
          const url = b.dataset.url;
          const using = Number(b.dataset.using || 0);
          if (!(await confirmBox(`Gỡ ${url}?`,
            `<p>Agent sẽ bỏ địa chỉ <span class="mono strong">${esc(url)}</span> ở lần heartbeat kế tiếp.</p>
             ${using ? `<p><strong>Còn ${fmtNum(using)} máy đang gọi qua địa chỉ này.</strong> Sau khi nhận danh sách mới, các máy đó chỉ còn các địa chỉ khác (và địa chỉ lúc cài).</p>` : ''}`,
            'Gỡ', true))) return;
          let { status, j } = await change({ action: 'remove', url });
          if (status === 409 && j.using) ({ status, j } = await change({ action: 'remove', url, force: true }));
          if (status !== 200) throw new Error(j.error || `lỗi ${status}`);
          toast(`Đã gỡ ${url}`);
        } else if (b.dataset.act === 'up' || b.dataset.act === 'down') {
          const { status, j } = await change({ action: b.dataset.act, url: b.dataset.url });
          if (status !== 200) throw new Error(j.error || `lỗi ${status}`);
        } else {
          return;
        }
        load();
      } catch (err) {
        toast(`Không làm được: ${err.message}`);
      }
    });
    await load();
    refreshTimer = setInterval(() => {
      const typing = document.activeElement && ['sv-url', 'sv-note'].includes(document.activeElement.id);
      if (!$('#dialog').open && !typing) load();
    }, 15000);
  }

  // ---------------------------------------------------------------- trang: Rule quét log
  // Kho bộ rule trên server: tự chia chưa ký / đã ký / cần ký lại / lỗi. Duyệt = ký bằng khóa phát
  // hành của server; server trong cụm thì phải được quá nửa server duyệt (quyết định ruleset.publish).
  const RULE_ST = {
    'chua-ky': ['pill-warn', 'Chưa ký'],
    'da-ky': ['pill-ok', 'Đã ký'],
    'can-ky-lai': ['pill-bad', 'Cần ký lại'],
    loi: ['pill-bad', 'Lỗi'],
  };
  const ruleKey = (e) => `${e.name}@${e.version}`;

  function setRuleCount(entries) {
    const n = entries.filter((e) => e.status === 'chua-ky' || e.status === 'can-ky-lai').length;
    $('#nav-rules').textContent = n || '';
  }

  async function refreshRuleCount() {
    try { setRuleCount((await getJSON('/admin/v1/rules')).entries); } catch { /* bỏ qua */ }
  }

  async function pageRules() {
    setTitle('Rule quét log');
    const page = view;
    const state = { filter: '', data: null, assign: [], devices: [] };
    page.innerHTML = '<p class="hint">Đang tải…</p>';

    const counts = (list) => {
      const c = { '': list.length };
      for (const e of list) c[e.status] = (c[e.status] || 0) + 1;
      return c;
    };

    function render() {
      const j = state.data;
      const list = j.entries;
      const c = counts(list);
      const rows = list.filter((e) => !state.filter || e.status === state.filter);
      const cl = j.cluster;
      const chip = (k, label) => `<button class="chip${state.filter === k ? ' active' : ''}" data-filter="${k}">${esc(label)} (${fmtNum(c[k] || 0)})</button>`;
      const row = (e) => {
        const signedTxt = e.signed ? `${esc(fmtTime(new Date(e.signed.at)))} · ${esc(e.signed.by)}` : '';
        let clTxt = '';
        if (cl && e.name) {
          clTxt = e.cluster ? `<span class="pill pill-ok no-dot">Cụm duyệt #${esc(e.cluster.seq)}</span>` : '<span class="pill no-dot">Cụm chưa duyệt</span>';
        }
        // "Cần ký lại" vì sửa nguồn mà giữ version thì không ký được (phải tăng version); vì đổi khóa thì ký lại được.
        const edited = /đã sửa sau khi ký/.test(e.error || '');
        const canSign = !!e.file && (e.status === 'chua-ky' || (e.status === 'can-ky-lai' && !edited));
        const signAttr = j.signing ? adminOnly() : ' disabled title="Server chưa có khóa phát hành"';
        const bundleURL = `/admin/v1/rules/bundle?name=${encodeURIComponent(e.name)}&version=${encodeURIComponent(e.version)}`;
        return `<tr>
          <td><span class="strong">${esc(e.name || '(không đọc được)')}</span><br><span class="mono hint">${esc(e.version || e.file)}</span></td>
          <td>${pill(RULE_ST[e.status] || ['', e.status])}${clTxt ? `<br>${clTxt}` : ''}</td>
          <td class="num hide-sm">${e.rules ? `${fmtNum(e.enabled)}/${fmtNum(e.rules)}` : ''}</td>
          <td class="num hide-sm">${e.tests ? fmtNum(e.tests) : ''}</td>
          <td class="num">${j.devices ? fmtNum(j.devices[list.indexOf(e)] || 0) : '—'}</td>
          <td class="hide-sm">${signedTxt}</td>
          <td><div class="btns">
            <button class="btn btn-sm" data-act="view" data-file="${esc(e.file)}" data-key="${esc(ruleKey(e))}">Xem</button>
            ${canSign ? `<button class="btn btn-sm btn-primary" data-act="approve" data-file="${esc(e.file)}"${signAttr}>Duyệt &amp; ký</button>` : ''}
            ${e.bundle && e.status === 'da-ky' ? `<a class="btn btn-sm" href="${esc(bundleURL)}" download>Tải bản ký</a>` : ''}
            <button class="btn btn-sm btn-danger" data-act="remove" data-file="${esc(e.file)}" data-name="${esc(e.name)}" data-version="${esc(e.version)}" data-bundle="${e.bundle ? 1 : 0}"${adminOnly()}>Xóa</button>
          </div></td></tr>`;
      };
      const only = j.cluster_only || [];
      page.innerHTML = `
        ${!j.signing ? '<div class="callout">Server chưa có khóa phát hành (SCS_RELEASE_KEY): xem và thêm được, chưa ký được bộ rule.</div>' : ''}
        ${cl ? `<div class="card"><div class="card-body"><p class="hint">Server này thuộc cụm <strong>${esc(cl.name)}</strong>
          (${fmtNum(cl.valid)} server hợp lệ). Duyệt một bộ rule mới sẽ gửi đề nghị cho các server khác ký;
          cần <strong>${fmtNum(cl.majority)}</strong> server đồng ý thì server này mới ký bộ rule cho agent của nó.
          Bộ cụm đã duyệt: server khác chỉ cần thêm đúng file đó rồi bấm duyệt.</p>
          ${cl.error ? `<p class="err-text">${esc(cl.error)}</p>` : ''}</div></div>` : ''}
        <div class="card">
          <div class="toolbar">
            <div class="chips">
              ${chip('', 'Tất cả')}${chip('chua-ky', 'Chưa ký')}${chip('da-ky', 'Đã ký')}${chip('can-ky-lai', 'Cần ký lại')}${chip('loi', 'Lỗi')}
            </div>
          </div>
          <div class="table-wrap"><table>
            <thead><tr><th>Bộ rule</th><th>Trạng thái</th><th class="num hide-sm">Rule bật</th><th class="num hide-sm">Test</th><th class="num">Máy đang dùng</th><th class="hide-sm">Ký lúc · bởi</th><th></th></tr></thead>
            <tbody>${rows.map(row).join('') || `<tr><td colspan="7" class="empty">${list.length ? 'Không có bộ nào ở nhóm này.' : 'Chưa có bộ rule nào. Thêm file .yaml ở dưới.'}</td></tr>`}</tbody>
          </table></div>
        </div>
        ${(j.other_usage && j.other_usage.length) || j.no_rules || j.unknown_rules ? `<div class="card">
          <div class="card-head"><h2>Máy chưa dùng bộ nào trong kho</h2><span class="sub">Theo heartbeat gần nhất của máy đã duyệt</span></div>
          ${j.other_usage && j.other_usage.length ? `<div class="table-wrap"><table><thead><tr><th>Bộ máy đang cài</th><th class="hide-sm">SHA-256</th><th class="num">Máy</th></tr></thead>
            <tbody>${j.other_usage.map((u) => `<tr><td class="mono">${esc(u.name)}@${esc(u.version)}</td><td class="mono hide-sm">${esc(String(u.sha256).slice(0, 16))}…</td><td class="num">${fmtNum(u.devices)}</td></tr>`).join('')}</tbody></table></div>` : ''}
          <div class="card-body"><p class="hint">
            ${j.other_usage && j.other_usage.length ? 'Bộ ở bảng trên không khớp bản ký nào trong kho (bản ký khác, hoặc bộ đã xóa khỏi kho). ' : ''}
            ${j.no_rules ? `${fmtNum(j.no_rules)} máy chưa cài bộ rule nào (hoặc bộ đang cài bị lỗi). ` : ''}
            ${j.unknown_rules ? `${fmtNum(j.unknown_rules)} máy chưa báo (chưa gửi heartbeat hoặc agent bản cũ, cần cập nhật agent).` : ''}</p></div>
        </div>` : ''}
        ${only.length ? `<div class="card">
          <div class="card-head"><h2>Cụm đã duyệt, server này chưa có file</h2></div>
          <div class="table-wrap"><table><thead><tr><th>Bộ rule</th><th>Quyết định</th><th class="hide-sm">SHA-256</th></tr></thead>
            <tbody>${only.map((x) => `<tr><td><span class="strong">${esc(x.name)}</span><br><span class="mono hint">${esc(x.version)}</span></td>
              <td>#${esc(x.seq)}${x.admin ? ` · ${esc(x.admin)}` : ''}</td><td class="mono hide-sm">${esc(String(x.sha256).slice(0, 16))}…</td></tr>`).join('')}</tbody></table></div>
          <div class="card-body"><p class="hint">Thêm đúng file nguồn của bộ đó (cùng nội dung) rồi bấm "Duyệt &amp; ký": ký ngay, các server không phải bỏ phiếu lại.</p></div>
        </div>` : ''}
        ${assignCard(j)}
        <div class="card">
          <div class="card-head"><h2>Thêm bộ rule</h2><span class="sub">File .yaml viết theo docs/huong-dan-viet-rule.md</span></div>
          <div class="card-body">
            <div class="form-grid">
              <label class="field">File rule
                <input type="file" id="ru-file" accept=".yaml,.yml"${adminOnly()}>
                <small>Server kiểm cú pháp, giới hạn và chạy tests của từng rule. Hợp lệ thì vào nhóm "Chưa ký".
                  Bộ đã ký mà muốn sửa: tăng <span class="mono">version</span> rồi thêm lại.</small></label>
            </div>
            <div class="form-actions"><button class="btn btn-primary btn-sm" id="ru-add"${adminOnly()}>Kiểm tra và thêm</button></div>
            <p class="hint">Rule chỉ đọc log và khớp regex, không chạy được lệnh trên máy. Agent hiện chưa tự tải bộ rule:
              tải bản ký, chép sang máy rồi chạy <span class="mono">sudo scs-agent rules -install FILE.json</span> (server tự phát bộ rule là mốc M7).
              Thư mục kho trên server: <span class="mono">${esc(j.dir)}</span>.</p>
          </div>
        </div>`;
    }

    async function load() {
      try {
        const [j, as, devs] = await Promise.all([
          getJSON('/admin/v1/rules'),
          getJSON('/admin/v1/rules/assign').then((x) => x.assignments).catch(() => []),
          getJSON('/admin/v1/devices?status=approved').then((x) => x.devices).catch(() => []),
        ]);
        const sig = JSON.stringify([j, as, devs.map((d) => [d.id, d.hostname])]);
        if (sig === state.sig && page.firstElementChild) return; // không đổi: không vẽ lại
        state.sig = sig;
        state.data = j;
        state.assign = as;
        state.devices = devs;
      } catch (err) {
        if (page === view) showLoadError(page, err);
        return;
      }
      if (page !== view) return;
      setRuleCount(state.data.entries);
      render();
    }

    // Gán bộ rule cho máy: cấp cụ thể hơn thắng (máy > siêu thị > tất cả máy). Agent nhận ở heartbeat kế
    // tiếp (≤ 10 phút), tự tải, kiểm chữ ký, cài rồi quét log theo bộ mới.
    function assignCard(j) {
      const signed = j.entries.filter((e) => e.status === 'da-ky' && e.bundle).map(ruleKey);
      const hostOf = (id) => (state.devices || []).find((d) => String(d.id) === String(id))?.hostname || `máy #${id}`;
      const scopeTxt = (a) => (a.scope === 'global' ? 'Tất cả máy' : a.scope === 'store' ? `Siêu thị ${esc(a.key)}` : `Máy ${esc(hostOf(a.key))}`);
      const rows = (state.assign || []).map((a) => `<tr><td>${scopeTxt(a)}</td><td class="mono">${esc(a.rule_set)}</td>
        <td class="hide-sm">${a.updated_at ? esc(fmtTime(new Date(a.updated_at))) : ''}${a.updated_by ? ` · ${esc(a.updated_by)}` : ''}</td>
        <td><button class="btn btn-sm btn-danger" data-unassign="${esc(a.scope)}" data-key="${esc(a.key)}"${adminOnly()}>Bỏ gán</button></td></tr>`).join('');
      return `<div class="card">
        <div class="card-head"><h2>Áp dụng bộ rule cho máy</h2><span class="sub">Máy &gt; siêu thị &gt; tất cả máy. Agent nhận trong vòng 10 phút</span></div>
        <div class="table-wrap"><table><thead><tr><th>Cấp</th><th>Bộ rule</th><th class="hide-sm">Gán lúc · bởi</th><th></th></tr></thead>
          <tbody>${rows || '<tr><td colspan="4" class="empty">Chưa gán bộ nào: agent giữ bộ đang cài (nếu có).</td></tr>'}</tbody></table></div>
        <div class="card-body">
          ${signed.length ? `<div class="form-grid">
            <label class="field">Bộ rule (đã ký)<select id="as-set"${adminOnly()}>${signed.map((k) => `<option value="${esc(k)}">${esc(k)}</option>`).join('')}</select></label>
            <label class="field">Áp dụng cho<select id="as-scope"${adminOnly()}>
              <option value="global">Tất cả máy</option><option value="store">Một siêu thị</option><option value="device">Một máy</option></select></label>
            <label class="field" id="as-store-f" hidden>Mã siêu thị<input id="as-store" inputmode="numeric" placeholder="12345"${adminOnly()}></label>
            <label class="field" id="as-dev-f" hidden>Máy<select id="as-dev"${adminOnly()}>${(state.devices || []).map((d) => `<option value="${esc(d.id)}">${esc(d.hostname)}</option>`).join('')}</select></label>
          </div>
          <div class="form-actions"><button class="btn btn-primary btn-sm" id="as-save"${adminOnly()}>Áp dụng</button></div>`
          : '<p class="hint">Chưa có bộ nào đã ký: thêm file rule rồi bấm "Duyệt &amp; ký" trước.</p>'}
          <p class="hint">Máy nhận bộ mới ở heartbeat kế tiếp, tự tải từ server, kiểm chữ ký bằng khóa phát hành rồi cài và quét log
            theo bộ đó; cột "Máy đang dùng" và trang Máy cập nhật sau đó. Bỏ gán thì máy giữ bộ đang cài.</p>
        </div></div>`;
    }

    async function viewEntry(file, key) {
      const e = state.data.entries.find((x) => (file ? x.file === file : ruleKey(x) === key));
      if (!e) return;
      let src = '';
      if (file) {
        try {
          const r = await api(`/admin/v1/rules/source?file=${encodeURIComponent(file)}`);
          if (r.ok) src = await r.text();
        } catch { /* bỏ qua */ }
      }
      openDrawer(e.name ? ruleKey(e) : e.file, `
        <dl class="kv">
          <dt>Trạng thái</dt><dd>${pill(RULE_ST[e.status] || ['', e.status])}</dd>
          <dt>File nguồn</dt><dd class="mono">${e.file ? esc(e.file) : 'Không còn (chỉ có bản ký)'}</dd>
          ${e.description ? `<dt>Mô tả</dt><dd>${esc(e.description)}</dd>` : ''}
          ${e.rules ? `<dt>Rule</dt><dd>${fmtNum(e.rules)} (${fmtNum(e.enabled)} đang bật), ${fmtNum(e.tests)} dòng test</dd>` : ''}
          ${e.schema ? `<dt>Schema</dt><dd>${esc(e.schema)}</dd>` : ''}
          ${e.sha256 ? `<dt>SHA-256</dt><dd class="mono">${esc(e.sha256)}</dd>` : ''}
          ${e.signed ? `<dt>Ký</dt><dd>${esc(fmtTime(new Date(e.signed.at)))} · ${esc(e.signed.by)}${e.signed.cluster_seq ? ` · cụm #${esc(e.signed.cluster_seq)}` : ''}</dd>` : ''}
          ${e.cluster ? `<dt>Cụm duyệt</dt><dd>Quyết định #${esc(e.cluster.seq)}${e.cluster.admin ? ` · ${esc(e.cluster.admin)}` : ''}</dd>` : ''}
        </dl>
        ${e.error ? `<h3>Lỗi</h3><pre class="err-text">${esc(e.error)}</pre>` : ''}
        ${src ? `<h3>Nội dung</h3><pre class="src-view">${esc(src)}</pre>` : ''}`);
    }

    page.addEventListener('change', (ev) => {
      if (ev.target.id !== 'as-scope') return;
      $('#as-store-f').hidden = ev.target.value !== 'store';
      $('#as-dev-f').hidden = ev.target.value !== 'device';
    });

    page.addEventListener('click', async (ev) => {
      const chipB = ev.target.closest('[data-filter]');
      if (chipB) {
        state.filter = chipB.dataset.filter;
        render();
        return;
      }
      const b = ev.target.closest('button');
      if (!b || b.disabled) return;
      try {
        if (b.id === 'ru-add') {
          const f = $('#ru-file').files[0];
          if (!f) { toast('Chọn file .yaml trước'); return; }
          if (f.size > 2 * 1024 * 1024) { toast('File rule tối đa 2 MB'); return; }
          const r = await api('/admin/v1/rules', { method: 'POST', headers: { 'Content-Type': 'text/plain; charset=utf-8' }, body: await f.text() });
          const j = await r.json().catch(() => ({}));
          if (!r.ok) {
            infoBox('Không thêm được bộ rule', `<pre class="err-text">${esc(j.error || `lỗi ${r.status}`)}</pre>`);
            return;
          }
          toast(`Đã thêm ${ruleKey(j.entry)}: ${(RULE_ST[j.entry.status] || ['', j.entry.status])[1]}`);
          state.filter = '';
        } else if (b.id === 'as-save') {
          const scope = $('#as-scope').value;
          const key = scope === 'store' ? $('#as-store').value.trim() : scope === 'device' ? $('#as-dev').value : '';
          if (scope === 'store' && !/^[0-9]{1,10}$/.test(key)) { toast('Nhập mã siêu thị (chỉ chữ số)'); return; }
          const set = $('#as-set').value;
          const where = scope === 'global' ? 'mọi máy' : scope === 'store' ? `siêu thị ${key}` : $('#as-dev').selectedOptions[0].textContent;
          if (!(await confirmBox(`Áp dụng ${set}?`,
            `<p>Bộ <span class="mono">${esc(set)}</span> sẽ được cài cho <strong>${esc(where)}</strong> ở heartbeat kế tiếp (≤ 10 phút),
             thay bộ đang cài trên các máy đó (cấp cụ thể hơn vẫn thắng).</p>`, 'Áp dụng'))) return;
          await postJSON('/admin/v1/rules/assign', { scope, key, rule_set: set });
          toast(`Đã gán ${set} cho ${where}`);
        } else if (b.dataset.unassign) {
          if (!(await confirmBox('Bỏ gán bộ rule ở cấp này?', '<p>Máy giữ bộ đang cài; cấp trên (nếu có gán) sẽ áp dụng.</p>', 'Bỏ gán', true))) return;
          await postJSON('/admin/v1/rules/assign', { scope: b.dataset.unassign, key: b.dataset.key, rule_set: '' });
          toast('Đã bỏ gán');
        } else if (b.dataset.act === 'view') {
          await viewEntry(b.dataset.file, b.dataset.key);
          return;
        } else if (b.dataset.act === 'approve') {
          const e = state.data.entries.find((x) => x.file === b.dataset.file);
          if (!e) return;
          const cl = state.data.cluster;
          const ok = await confirmBox(`Duyệt và ký ${ruleKey(e)}?`,
            `<p>${fmtNum(e.enabled)} rule đang bật, ${fmtNum(e.tests)} dòng test đã đạt.</p>
             <p class="mono hint break-all">SHA-256 ${esc(e.sha256)}</p>
             <p>Sau khi ký, bộ rule này được dùng cho agent. Xem lại nội dung (nút "Xem") trước khi duyệt.</p>
             ${cl && !e.cluster ? `<p><strong>Server trong cụm:</strong> gửi đề nghị cho các server khác, cần ${fmtNum(cl.majority)}/${fmtNum(cl.valid)} server ký; có thể mất vài giây.</p>` : ''}`,
            'Duyệt & ký');
          if (!ok) return;
          b.disabled = true;
          const j = await postJSON('/admin/v1/rules/approve', { file: e.file });
          toast(`Đã ký ${ruleKey(j.entry)}${j.cluster_seq ? ` (cụm duyệt #${j.cluster_seq})` : ''}`);
        } else if (b.dataset.act === 'remove') {
          const { file, name, version } = b.dataset;
          const hasBundle = b.dataset.bundle === '1';
          const label = name ? `${name}@${version}` : file;
          const ok = await confirmBox(`Xóa ${label}?`,
            `${file ? `<p>Xóa file nguồn <span class="mono">${esc(file)}</span>.</p>` : '<p>Xóa bản ký (không còn file nguồn).</p>'}
             ${file && hasBundle ? '<label class="check"><input type="checkbox" id="ru-rm-bundle"> Xóa luôn bản đã ký</label>' : ''}
             <p class="hint">Agent đã cài bộ rule này không bị ảnh hưởng.</p>`, 'Xóa', true);
          if (!ok) return;
          const box = $('#ru-rm-bundle');
          await postJSON('/admin/v1/rules/remove', file ? { file, bundle: !!(box && box.checked) } : { name, version });
          toast(`Đã xóa ${label}`);
        } else {
          return;
        }
        load();
      } catch (err) {
        toast(`Không làm được: ${err.message}`);
        load();
      }
    });
    await load();
    liveReload = load;
    refreshTimer = setInterval(() => {
      if (!$('#dialog').open && !$('#drawer').classList.contains('open')) load();
    }, 30000);
  }

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
    rule: pageRules,
    'cap-nhat': pageAgent,
    cum: pageCluster,
    'dia-chi': pageServers,
  };

  function route() {
    clearInterval(refreshTimer);
    liveReload = null;
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
    stopLive();
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
    refreshRuleCount();
    setNotifyButton();
    startLive();
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
      case 'notify': toggleNotify(); break;
      case 'logout':
        stopLive();
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
