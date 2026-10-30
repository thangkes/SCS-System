// SCS-System · giao diện quản trị.
// Không dùng thư viện ngoài, không có script/style inline (CSP chặn). Mọi giá trị
// đưa vào HTML đều đi qua esc() để không bị chèn mã.
'use strict';

(() => {
  const THEME_KEY = 'scs-theme';
  const NAME_KEY = 'scs-login-name';

  // Người đang đăng nhập, lấy từ /auth/me: { id, name, role }.
  let me = null;
  let pendingCount = 0; // số máy chờ duyệt (dữ liệu thật)
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
  const demo = (() => {
    let seed = 20261029;
    const rnd = () => { seed |= 0; seed = (seed + 0x6D2B79F5) | 0; let t = Math.imul(seed ^ (seed >>> 15), 1 | seed); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
    const pick = (a) => a[Math.floor(rnd() * a.length)];
    const hex = (n) => Array.from({ length: n }, () => Math.floor(rnd() * 16).toString(16)).join('');
    const now = Date.now();
    const regions = ['HCM', 'HCM', 'HCM', 'Đồng Nai', 'Bình Dương', 'Long An', 'Cần Thơ', 'Tây Ninh'];
    const roles = ['pos', 'pos', 'pos', 'kho', 'quay'];

    const stores = Array.from({ length: 28 }, (_, i) => ({ code: `${10001 + i}`, region: pick(regions) }));
    const devices = [];
    stores.forEach((st) => {
      const n = 2 + Math.floor(rnd() * 3);
      for (let j = 1; j <= n; j++) {
        const r = rnd();
        const status = r < 0.86 ? 'online' : r < 0.97 ? 'offline' : 'locked';
        const lastMin = status === 'online' ? rnd() * 5 : 20 + rnd() * 600;
        devices.push({
          id: `${st.code}-${String(j).padStart(2, '0')}`,
          store: st.code, region: st.region,
          hostname: `${st.code.toLowerCase()}-${pick(roles)}${j}`,
          status, last: new Date(now - lastMin * 60000),
          version: rnd() < 0.8 ? '1.0.3' : '1.0.2',
          rules: rnd() < 0.9 ? '2026.10.27-03' : '2026.10.20-01',
          ip: `113.161.${Math.floor(rnd() * 255)}.${Math.floor(rnd() * 255)}`,
          fp: hex(64), machineId: hex(32),
          board: `PF${hex(6).toUpperCase()}`, serial: rnd() < 0.15 ? '' : `SN${hex(8).toUpperCase()}`,
          mac: Array.from({ length: 6 }, () => hex(2)).join(':'),
          enrolled: new Date(now - (30 + rnd() * 200) * 86400000),
        });
      }
    });

    const rules = [
      { id: 'SSH-FAIL-01', sev: 'medium', text: 'Failed password for root from 10.20.3.15 port 52114 ssh2' },
      { id: 'RAID-DEGRADED-01', sev: 'critical', text: 'md0: DegradedArray event detected' },
      { id: 'POS-SVC-DOWN-01', sev: 'high', text: 'pos.service: Failed with result \'exit-code\'' },
      { id: 'USB-ADD-01', sev: 'low', text: 'usb 1-2: New USB device found, idVendor=0781' },
      { id: 'DISK-FULL-01', sev: 'high', text: '/var: 96% used' },
    ];
    const alerts = Array.from({ length: 46 }, () => {
      const d = pick(devices); const r = pick(rules);
      return { device: d.id, store: d.store, rule: r.id, sev: r.sev, text: r.text, count: 1 + Math.floor(rnd() * 9), time: new Date(now - rnd() * 86400000) };
    }).sort((a, b) => b.time - a.time);


    // Số máy online theo từng giờ trong 24 giờ qua.
    const total = devices.length;
    const hourly = Array.from({ length: 24 }, (_, i) => {
      const h = (new Date(now).getHours() - 23 + i + 24) % 24;
      const closed = h >= 23 || h < 6; // cửa hàng đóng cửa ban đêm
      const v = closed ? total * (0.35 + rnd() * 0.1) : total * (0.9 + rnd() * 0.08);
      return { h, v: Math.round(v) };
    });

    return { stores, devices, alerts, hourly, hex };
  })();

  const SEV = {
    critical: ['pill-bad', 'Nghiêm trọng'],
    high: ['pill-warn', 'Cao'],
    medium: ['pill-info', 'Trung bình'],
    low: ['', 'Thấp'],
  };
  const STATUS = {
    online: ['pill-ok', 'Online'],
    offline: ['pill-bad', 'Offline'],
    locked: ['pill-warn', 'Bị khóa'],
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
  function setDemo(on) { $('#demo-banner').hidden = !on; }

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

  // Thao tác ghi chưa có API: báo rõ, không giả vờ đã làm.
  const notYet = (what) => toast(`${what}: chưa làm (mốc M6), dữ liệu không thay đổi.`);

  // Thuộc tính cho nút ghi: quyền Chỉ xem thì khóa nút (server cũng chặn bằng 403).
  const adminOnly = () => (isAdmin() ? '' : ' disabled title="Cần quyền Admin"');

  // ---------------------------------------------------------------- trang: Tổng quan
  function pageOverview() {
    setTitle('Tổng quan'); setDemo(true);
    const d = demo.devices;
    const online = d.filter((x) => x.status === 'online').length;
    const offline = d.filter((x) => x.status === 'offline').length;
    const crit = demo.alerts.filter((a) => a.sev === 'critical').length;

    const byStore = {};
    d.forEach((x) => { (byStore[x.store] ||= { store: x.store, region: x.region, total: 0, off: 0, crit: 0 }).total++; if (x.status !== 'online') byStore[x.store].off++; });
    demo.alerts.forEach((a) => { if (a.sev === 'critical' && byStore[a.store]) byStore[a.store].crit++; });
    const attention = Object.values(byStore).filter((s) => s.off || s.crit).sort((a, b) => (b.crit * 3 + b.off) - (a.crit * 3 + a.off)).slice(0, 6);

    view.innerHTML = `
      <div class="grid-kpi">
        ${kpi('Tổng số máy', fmtNum(d.length), `${demo.stores.length} cửa hàng`)}
        ${kpi('Online', fmtNum(online), `${Math.round(online / d.length * 100)}% heartbeat trong 5 phút`, 'is-ok')}
        ${kpi('Offline', fmtNum(offline), 'không heartbeat quá 15 phút', offline ? 'is-bad' : '')}
        ${kpi('Chờ duyệt', fmtNum(pendingCount), 'yêu cầu đăng ký mới (số thật)', pendingCount ? 'is-warn' : '')}
        ${kpi('Cảnh báo nghiêm trọng', fmtNum(crit), 'trong 24 giờ', crit ? 'is-bad' : '')}
      </div>

      <div class="grid-2">
        <div class="card">
          <div class="card-head"><h2>Máy online 24 giờ qua</h2>
            <div class="right legend"><span><i></i>Giờ mở cửa</span><span><i class="low"></i>Ban đêm</span></div></div>
          <div class="card-body"><svg class="chart" id="chart" role="img" aria-label="Số máy online theo giờ"></svg></div>
        </div>
        <div class="card">
          <div class="card-head"><h2>Cảnh báo mới</h2><div class="right"><a href="#/canh-bao" class="btn btn-sm">Xem tất cả</a></div></div>
          <ul class="list">${demo.alerts.slice(0, 6).map(alertItem).join('')}</ul>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><h2>Cửa hàng cần chú ý</h2><span class="sub">Có máy offline hoặc cảnh báo nghiêm trọng</span></div>
        <div class="table-wrap"><table>
          <thead><tr><th>Cửa hàng</th><th class="hide-sm">Khu vực</th><th class="num">Số máy</th><th class="num">Offline / khóa</th><th class="num">Nghiêm trọng</th></tr></thead>
          <tbody>${attention.map((s) => `
            <tr class="clickable" data-store="${esc(s.store)}">
              <td class="strong">${esc(s.store)}</td><td class="hide-sm">${esc(s.region)}</td>
              <td class="num">${s.total}</td>
              <td class="num">${s.off ? `<span class="pill pill-bad no-dot">${s.off}</span>` : '0'}</td>
              <td class="num">${s.crit ? `<span class="pill pill-bad no-dot">${s.crit}</span>` : '0'}</td>
            </tr>`).join('') || `<tr><td colspan="5" class="empty">Không có cửa hàng nào cần chú ý.</td></tr>`}
          </tbody></table></div>
      </div>`;

    drawChart($('#chart'), demo.hourly, d.length);
    $$('tr[data-store]', view).forEach((tr) => tr.addEventListener('click', () => {
      location.hash = `#/may?q=${encodeURIComponent(tr.dataset.store)}`;
    }));
  }

  function kpi(label, value, meta, cls = '') {
    return `<div class="card kpi ${cls}"><span class="label">${esc(label)}</span><span class="value">${esc(value)}</span><span class="meta">${esc(meta)}</span></div>`;
  }

  function alertItem(a) {
    return `<li>${pill(SEV[a.sev])}<div class="grow"><div class="title">${esc(a.rule)} · ${esc(a.device)}</div><div class="desc mono">${esc(a.text)}</div></div><span class="time">${esc(ago(a.time))}</span></li>`;
  }

  function drawChart(svg, data, max) {
    const NS = 'http://www.w3.org/2000/svg';
    const W = 600, H = 190, padL = 30, padB = 22, padT = 8;
    svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
    svg.setAttribute('preserveAspectRatio', 'none');
    const el = (tag, attrs, text) => { const n = document.createElementNS(NS, tag); for (const k in attrs) n.setAttribute(k, attrs[k]); if (text != null) n.textContent = text; svg.append(n); return n; };
    const plotH = H - padB - padT, plotW = W - padL;
    [0, 0.5, 1].forEach((f) => {
      const y = padT + plotH * (1 - f);
      el('line', { x1: padL, x2: W, y1: y, y2: y, class: 'grid-line' });
      el('text', { x: 0, y: y + 3 }, Math.round(max * f));
    });
    const bw = plotW / data.length;
    data.forEach((p, i) => {
      const h = Math.max(2, plotH * (p.v / max));
      const night = p.h >= 23 || p.h < 6;
      el('rect', { x: padL + i * bw + 2, y: padT + plotH - h, width: bw - 4, height: h, rx: 2, class: night ? 'bar low' : 'bar' })
        .append(Object.assign(document.createElementNS(NS, 'title'), { textContent: `${p.h}:00 · ${p.v} máy online` }));
      if (i % 3 === 0) el('text', { x: padL + i * bw + bw / 2, y: H - 6, 'text-anchor': 'middle' }, `${p.h}h`);
    });
  }

  // ---------------------------------------------------------------- trang: Máy
  function pageDevices(params) {
    setTitle('Máy'); setDemo(true);
    const state = { q: params.get('q') || '', status: params.get('status') || '', page: 0 };
    const PAGE = 15;

    view.innerHTML = `
      <div class="card">
        <div class="toolbar">
          <input type="search" id="dev-q" placeholder="Tìm mã máy, cửa hàng, hostname, IP…" value="${esc(state.q)}" aria-label="Tìm máy">
          <select id="dev-status" aria-label="Lọc trạng thái">
            <option value="">Mọi trạng thái</option><option value="online">Online</option>
            <option value="offline">Offline</option><option value="locked">Bị khóa</option>
          </select>
          <span class="count" id="dev-count"></span>
        </div>
        <div class="table-wrap"><table>
          <thead><tr><th>Máy</th><th>Trạng thái</th><th>Heartbeat cuối</th><th class="hide-sm">Agent</th><th class="hide-sm">Bộ rule</th><th class="hide-sm">IP WAN</th></tr></thead>
          <tbody id="dev-rows"></tbody>
        </table></div>
        <div class="pager"><span id="dev-range"></span><div class="btns">
          <button class="btn btn-sm" id="dev-prev">Trước</button><button class="btn btn-sm" id="dev-next">Sau</button></div></div>
      </div>`;
    $('#dev-status').value = state.status;

    const render = () => {
      const q = state.q.trim().toLowerCase();
      const rows = demo.devices.filter((d) =>
        (!state.status || d.status === state.status) &&
        (!q || [d.id, d.store, d.hostname, d.ip].some((v) => v.toLowerCase().includes(q))));
      const pages = Math.max(1, Math.ceil(rows.length / PAGE));
      state.page = Math.min(state.page, pages - 1);
      const slice = rows.slice(state.page * PAGE, state.page * PAGE + PAGE);
      $('#dev-count').textContent = `${fmtNum(rows.length)} máy`;
      $('#dev-range').textContent = rows.length ? `Trang ${state.page + 1} / ${pages}` : '';
      $('#dev-prev').disabled = state.page === 0;
      $('#dev-next').disabled = state.page >= pages - 1;
      $('#dev-rows').innerHTML = slice.map((d) => `
        <tr class="clickable" data-id="${esc(d.id)}">
          <td><span class="strong">${esc(d.id)}</span><span class="sub">${esc(d.hostname)}</span></td>
          <td>${pill(STATUS[d.status])}</td>
          <td>${esc(ago(d.last))}</td>
          <td class="hide-sm mono">${esc(d.version)}</td>
          <td class="hide-sm mono">${esc(d.rules)}</td>
          <td class="hide-sm mono">${esc(d.ip)}</td>
        </tr>`).join('') || `<tr><td colspan="6" class="empty">Không có máy nào khớp bộ lọc.</td></tr>`;
    };

    $('#dev-q').addEventListener('input', (e) => { state.q = e.target.value; state.page = 0; render(); });
    $('#dev-status').addEventListener('change', (e) => { state.status = e.target.value; state.page = 0; render(); });
    $('#dev-prev').addEventListener('click', () => { state.page--; render(); });
    $('#dev-next').addEventListener('click', () => { state.page++; render(); });
    $('#dev-rows').addEventListener('click', (e) => {
      const tr = e.target.closest('tr[data-id]');
      if (tr) showDevice(demo.devices.find((d) => d.id === tr.dataset.id));
    });
    render();
  }

  function showDevice(d) {
    if (!d) return;
    const events = [
      { t: d.last, text: 'Heartbeat gần nhất' },
      { t: new Date(d.last - 2 * 86400000), text: `Cập nhật agent lên ${d.version} (vòng 2)` },
      { t: new Date(d.last - 9 * 86400000), text: `Nhận bộ rule ${d.rules}` },
      { t: d.enrolled, text: 'Đăng ký, cấp chứng chỉ (hạn 90 ngày)' },
    ];
    openDrawer(d.id, `
      <div class="drawer-actions">${pill(STATUS[d.status])}<span class="pill no-dot">${esc(d.store)} · ${esc(d.region)}</span></div>
      <section><h3>Thông tin</h3><dl class="kv">
        <dt>Hostname</dt><dd>${esc(d.hostname)}</dd>
        <dt>Heartbeat cuối</dt><dd>${esc(fmtTime(d.last))} (${esc(ago(d.last))})</dd>
        <dt>Phiên bản agent</dt><dd class="mono">${esc(d.version)}</dd>
        <dt>Bộ rule</dt><dd class="mono">${esc(d.rules)}</dd>
        <dt>Ngày đăng ký</dt><dd>${esc(d.enrolled.toLocaleDateString('vi-VN'))}</dd>
      </dl></section>
      <section><h3>Fingerprint</h3><dl class="kv">
        <dt>Mã băm</dt><dd class="mono">${esc(d.fp)}</dd>
        <dt>machine-id</dt><dd class="mono">${esc(d.machineId)}</dd>
        <dt>Serial mainboard</dt><dd class="mono">${esc(d.board)}</dd>
        <dt>Serial hệ thống</dt><dd class="mono">${d.serial ? esc(d.serial) : '<span class="pill pill-warn no-dot">Không có (serial mặc định của hãng)</span>'}</dd>
        <dt>MAC chính</dt><dd class="mono">${esc(d.mac)}</dd>
      </dl></section>
      <section><h3>IP WAN đã duyệt</h3><dl class="kv">
        <dt>Line chính</dt><dd class="mono">${esc(d.ip)}</dd>
      </dl></section>
      <section><h3>Lịch sử</h3><ul class="timeline">${events.map((e) => `<li><div>${esc(e.text)}<div class="time">${esc(fmtTime(e.t))}</div></div></li>`).join('')}</ul></section>
      <section><h3>Thao tác</h3><div class="drawer-actions">
        <button class="btn btn-sm" data-dev-action="lock"${adminOnly()}>${d.status === 'locked' ? 'Mở khóa máy' : 'Khóa máy'}</button>
        <button class="btn btn-sm btn-danger" data-dev-action="revoke"${adminOnly()}>Thu hồi chứng chỉ</button>
      </div></section>`);

    $('#drawer-body').querySelectorAll('[data-dev-action]').forEach((b) => b.addEventListener('click', async () => {
      const revoke = b.dataset.devAction === 'revoke';
      const ok = await confirmBox(
        revoke ? 'Thu hồi chứng chỉ?' : (d.status === 'locked' ? 'Mở khóa máy?' : 'Khóa máy?'),
        `<p>${revoke
          ? `Máy <strong>${esc(d.id)}</strong> sẽ không gửi được dữ liệu nữa; muốn dùng lại thì máy đăng ký lại và chờ admin duyệt.`
          : `Máy <strong>${esc(d.id)}</strong> ${d.status === 'locked' ? 'sẽ được nhận dữ liệu trở lại.' : 'sẽ bị từ chối dữ liệu cho tới khi mở khóa.'}`}</p>`,
        revoke ? 'Thu hồi' : 'Xác nhận', revoke);
      if (ok) notYet(revoke ? 'Thu hồi chứng chỉ' : 'Khóa/mở khóa máy');
    }));
  }

  // ---------------------------------------------------------------- trang: Cảnh báo
  function pageAlerts() {
    setTitle('Cảnh báo'); setDemo(true);
    let sev = '';
    view.innerHTML = `
      <div class="card">
        <div class="toolbar">
          <div class="chips" id="sev-chips">
            <button class="chip active" data-sev="">Tất cả</button>
            ${Object.entries(SEV).map(([k, [, l]]) => `<button class="chip" data-sev="${k}">${esc(l)}</button>`).join('')}
          </div>
          <span class="count" id="al-count"></span>
        </div>
        <div class="table-wrap"><table>
          <thead><tr><th>Mức</th><th>Rule</th><th>Máy</th><th class="hide-sm">Nội dung</th><th class="num">Số lần</th><th>Thời gian</th></tr></thead>
          <tbody id="al-rows"></tbody></table></div>
      </div>`;
    const render = () => {
      const rows = demo.alerts.filter((a) => !sev || a.sev === sev);
      $('#al-count').textContent = `${rows.length} cảnh báo trong 24 giờ`;
      $('#al-rows').innerHTML = rows.map((a) => `
        <tr class="clickable" data-id="${esc(a.device)}">
          <td>${pill(SEV[a.sev])}</td><td class="mono">${esc(a.rule)}</td>
          <td><span class="strong">${esc(a.device)}</span></td>
          <td class="hide-sm mono">${esc(a.text)}</td><td class="num">${a.count}</td>
          <td>${esc(ago(a.time))}</td>
        </tr>`).join('') || `<tr><td colspan="6" class="empty">Không có cảnh báo.</td></tr>`;
    };
    $('#sev-chips').addEventListener('click', (e) => {
      const b = e.target.closest('[data-sev]'); if (!b) return;
      sev = b.dataset.sev;
      $$('.chip', $('#sev-chips')).forEach((c) => c.classList.toggle('active', c === b));
      render();
    });
    $('#al-rows').addEventListener('click', (e) => {
      const tr = e.target.closest('tr[data-id]');
      if (tr) showDevice(demo.devices.find((d) => d.id === tr.dataset.id));
    });
    render();
  }

  // ---------------------------------------------------------------- trang: Chờ duyệt (dữ liệu thật)
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
    setTitle('Chờ duyệt'); setDemo(false);
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

  // ---------------------------------------------------------------- trang: Hệ thống (dữ liệu thật)
  async function pageSystem() {
    setTitle('Hệ thống'); setDemo(false);
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

  // ---------------------------------------------------------------- trang: sắp có
  function pageSoon(title, text) {
    setTitle(title); setDemo(false);
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
    'cap-nhat': () => pageSoon('Cập nhật agent', 'Phát hành bản agent mới theo vòng (lab → 1% → 10% → toàn bộ) và theo dõi tỷ lệ lỗi. Sẽ làm ở mốc M7.'),
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
    $('#nav-alerts').textContent = demo.alerts.filter((a) => a.sev === 'critical').length || '';
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
