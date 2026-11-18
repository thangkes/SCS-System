// SCS-System · giao diện quản trị.
// Không dùng thư viện ngoài, không có script/style inline (CSP chặn). Mọi giá trị
// đưa vào HTML đều đi qua esc() để không bị chèn mã.
'use strict';

(() => {
  const THEME_KEY = 'scs-theme';
  const NAME_KEY = 'scs-login-name';

  // Người đang đăng nhập, lấy từ /auth/me: { id, name, role, level, level_name, account }.
  let me = null;
  let pendingCount = 0; // số máy chờ duyệt
  // Mức quyền: số nhỏ = quyền cao, mức trên có mọi quyền của mức dưới (server cũng kiểm bằng 403).
  const LV = { root: 0, cluster: 1, rules: 2, agents: 3, servers: 4, incidents: 5, agentInfo: 6, view: 7 };
  const LEVEL_NAMES = ['Quản trị gốc', 'Duyệt cụm server', 'Duyệt và phát hành rule', 'Duyệt agent, phát hành bản cập nhật',
    'Địa chỉ server gửi cho agent', 'Xác nhận sự cố', 'Chỉnh thông tin agent', 'Chỉ xem'];
  const myLevel = () => (me && Number.isInteger(me.level) ? me.level : LV.view);
  const can = (need) => !!me && myLevel() <= need;
  const isAdmin = () => can(LV.root); // quản trị gốc

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


  // Thuộc tính cho nút ghi: chưa đủ mức quyền thì khóa nút (server cũng chặn bằng 403).
  const adminOnly = (need = LV.root) => (can(need) ? '' : ` disabled title="Cần quyền: ${esc(LEVEL_NAMES[need])}"`);

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
      case 'event':
        if (a.reasons && a.reasons.length) {
          const r = a.reasons[0];
          return `${r.title || r.rule} ×${r.count}${r.sample ? ` — ${r.sample}` : ''}${a.reasons.length > 1 ? ` (+${a.reasons.length - 1} rule khác)` : ''}`;
        }
        return (d.rules || []).slice(0, 4).map((r) => `${r.rule} ×${r.count}`).join(', ');
      case 'offline': return 'Không nghe máy gửi gì; tự đóng khi máy gửi lại';
      default: return Object.entries(d).map(([k, v]) => `${k}: ${v}`).join(', ');
    }
  }

  // reasonsHTML: lý do sự cố hiện ngay trong danh sách (tiêu đề rule, mã rule, số lần, dòng log mẫu),
  // không phải mở máy mới xem được. Cảnh báo không phải sự cố thì một dòng diễn giải.
  function reasonsHTML(a, max) {
    const rs = (a.reasons || []).slice(0, max);
    if (!rs.length) {
      const t = alertText(a);
      return t ? `<div class="reason-line">${esc(t)}</div>` : '';
    }
    return rs.map((r) => `<div class="reason">
        <div class="reason-head">${pill(SEV[r.severity] || SEV.low)}<span class="strong">${esc(r.title || r.rule)}</span>
          ${r.title ? `<span class="mono sub-inline">${esc(r.rule)}</span>` : ''}<span class="sub-inline">×${fmtNum(r.count)}${r.last ? ` · lần cuối ${esc(ago(new Date(r.last)))}` : ''}</span></div>
        ${r.sample ? `<code class="reason-sample" title="${esc(r.sample)}">${esc(r.sample)}</code>` : ''}
      </div>`).join('') + ((a.reasons || []).length > max ? `<div class="sub-inline">+${(a.reasons || []).length - max} rule khác</div>` : '');
  }

  // openDeviceById: bấm vào cảnh báo → mở chi tiết máy (tình trạng, dòng thời gian, nút xử lý).
  async function openDeviceById(id, reload) {
    if (!id) return;
    try {
      for (const st of ['approved', 'pending', 'revoked']) {
        const d = (await getJSON(`/admin/v1/devices?status=${st}`)).devices.find((x) => String(x.id) === String(id));
        if (d) { showDevice(d, reload); return; }
      }
      toast('Không tìm thấy máy này (có thể đã bị xóa).');
    } catch (err) {
      toast(`Không mở được máy: ${err.message}`);
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
    // Số siêu thị có máy đang hoạt động (máy chưa có mã siêu thị không tính).
    const stores = [...new Set(approved.map((d) => d.store_code).filter(Boolean))];

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
        <div class="card srv" id="srv-card">${serverCard(lastStats, approved)}</div>
      </div>`;
    overviewDevices = approved;
    sizeMeters(page);
    const reloadOverview = () => pageOverview(true);
    $$('li[data-dev]', page).forEach((li) => {
      li.addEventListener('click', () => openDeviceById(li.dataset.dev, reloadOverview));
      li.addEventListener('keydown', (e) => { if (e.key === 'Enter') openDeviceById(li.dataset.dev, reloadOverview); });
    });
    clearInterval(refreshTimer);
    refreshStats();
    refreshTimer = setInterval(refreshStats, 10000);
  }

  // ---------------------------------------------------------------- khung Server (chỉ xem)
  let lastStats = null;     // số liệu server lần gần nhất (vẽ lại trang không bị trống)
  let overviewDevices = []; // máy đang hoạt động, để đếm máy gọi về trong 15 phút

  async function refreshStats() {
    const card = $('#srv-card'); if (!card) return;
    try {
      lastStats = await getJSON('/admin/v1/server/stats');
    } catch (err) {
      lastStats = { error: err.message };
    }
    const c = $('#srv-card'); if (c) { c.innerHTML = serverCard(lastStats, overviewDevices); sizeMeters(c); }
  }

  // Độ rộng thanh: đặt qua CSSOM (CSP chặn thuộc tính style viết trong HTML).
  function sizeMeters(el) {
    $$('.meter-bar span[data-p]', el).forEach((b) => { b.style.width = `${b.dataset.p}%`; });
  }

  const fmtBytes = (n) => {
    if (!(n >= 0)) return '—';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${u[i]}`;
  };
  const fmtRate = (bps) => {
    if (!(bps >= 0)) return '—';
    const bits = bps * 8;
    if (bits < 1000) return `${Math.round(bits)} bit/s`;
    if (bits < 1e6) return `${(bits / 1e3).toFixed(1)} Kbit/s`;
    if (bits < 1e9) return `${(bits / 1e6).toFixed(1)} Mbit/s`;
    return `${(bits / 1e9).toFixed(2)} Gbit/s`;
  };
  const fmtUptime = (sec) => {
    if (!(sec > 0)) return '—';
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    return d ? `${d} ngày ${h} giờ` : h ? `${h} giờ ${m} phút` : `${m} phút`;
  };
  const perMin = (v) => (v >= 10 ? fmtNum(Math.round(v)) : (Math.round(v * 10) / 10).toLocaleString('vi-VN'));

  // meter: thanh phần trăm, đổi màu từ 70% / 90%.
  function meter(label, pct, value, note) {
    const has = pct >= 0;
    const p = has ? Math.min(100, Math.max(0, pct)) : 0;
    const cls = !has ? '' : p >= 90 ? 'is-bad' : p >= 70 ? 'is-warn' : 'is-ok';
    return `<div class="meter ${cls}">
      <div class="meter-top"><span class="meter-label">${esc(label)}</span><span class="meter-val">${esc(value)}</span></div>
      <div class="meter-bar" role="img" aria-label="${esc(label)} ${has ? Math.round(p) + '%' : 'không rõ'}"><span data-p="${p.toFixed(1)}"></span></div>
      ${note ? `<div class="meter-note">${esc(note)}</div>` : ''}</div>`;
  }

  function stat(label, value, note) {
    return `<div class="srv-stat"><span class="label">${esc(label)}</span><span class="value">${esc(value)}</span>${note ? `<span class="note">${esc(note)}</span>` : ''}</div>`;
  }

  function serverCard(j, devices) {
    const head = (sub) => `<div class="card-head"><h2>Server</h2><span class="sub">${sub}</span><div class="right"><a href="#/bao-cao" class="btn btn-sm">Báo cáo</a></div></div>`;
    if (!j) return `${head('Đang tải…')}<p class="hint srv-pad">Đang lấy số liệu…</p>`;
    if (j.error) return `${head('Chỉ xem')}<p class="hint srv-pad">Không lấy được số liệu server: ${esc(j.error)}</p>`;
    const st = j.stats, h = st.host, n = st.net, c = st.scs;
    const memUsed = h.mem_total ? h.mem_total - h.mem_avail : -1;
    const diskUsed = h.disk_total ? h.disk_total - h.disk_free : -1;
    const now = Date.now();
    const recent = (devices || []).filter((d) => d.last_seen && now - new Date(d.last_seen).getTime() < 15 * 60000).length;
    const since = n.today_since ? new Date(n.today_since) : null;
    const todayLbl = since && (since.getHours() || since.getMinutes()) ? `từ ${since.toLocaleTimeString('vi-VN', { hour: '2-digit', minute: '2-digit' })}` : 'hôm nay';
    const ifaces = (n.ifaces || []).map((i) => `
      <div class="srv-if">
        <div class="srv-if-head"><span class="mono strong">${esc(i.name)}</span><span class="mono sub">${esc((i.addrs || []).join(', ') || 'không có IPv4')}</span></div>
        <div class="srv-if-rates">
          <span title="Đang nhận">↓ ${esc(fmtRate(i.rx_bps))}</span><span title="Đang gửi">↑ ${esc(fmtRate(i.tx_bps))}</span>
        </div>
        <div class="srv-if-today">${esc(todayLbl)}: nhận ${esc(fmtBytes(i.rx_today))} · gửi ${esc(fmtBytes(i.tx_today))}</div>
      </div>`).join('') || `<p class="hint">${h.linux ? 'Không thấy card mạng.' : 'Chỉ đọc được trên Linux.'}</p>`;
    const load = h.load ? `tải ${h.load.map((x) => x.toFixed(2)).join(' · ')} · ${h.cpus} lõi` : `${h.cpus} lõi`;
    return `${head(`${esc(h.name || '')} · máy chạy ${esc(fmtUptime(h.uptime_s))} · tự cập nhật 10 giây · chỉ xem`)}
      <div class="srv-body">
        <section class="srv-sec">
          <h3>Mạng</h3>
          ${ifaces}
          <div class="srv-row">${stat('Kết nối agent đang mở', c.ingest_here ? fmtNum(n.ingest_conns) : '—', 'cổng 6222 · mỗi agent chỉ mở vài giây khi gọi về, hay là 0')}</div>
        </section>
        <section class="srv-sec">
          <h3>Tài nguyên máy</h3>
          ${meter('CPU', h.cpu_pct ?? -1, h.cpu_pct != null ? `${Math.round(h.cpu_pct)}%` : '—', load)}
          ${meter('RAM', memUsed >= 0 ? (100 * memUsed) / h.mem_total : -1, memUsed >= 0 ? `${fmtBytes(memUsed)} / ${fmtBytes(h.mem_total)}` : '—')}
          ${meter('Ổ đĩa dữ liệu', diskUsed >= 0 ? (100 * diskUsed) / h.disk_total : -1, diskUsed >= 0 ? `${fmtBytes(diskUsed)} / ${fmtBytes(h.disk_total)}` : '—', h.disk_path ? `còn trống ${fmtBytes(h.disk_free)} · ${h.disk_path}` : '')}
        </section>
        <section class="srv-sec">
          <h3>Hoạt động SCS</h3>
          <div class="srv-grid">
            ${stat('Heartbeat / phút', c.ingest_here ? perMin(c.heartbeats_min) : '—')}
            ${stat('Sự kiện log / phút', c.ingest_here ? perMin(c.log_events_min) : '—')}
            ${stat('Máy gọi về 15 phút', `${fmtNum(recent)} / ${fmtNum((devices || []).length)}`)}
            ${stat('RAM scs-server', fmtBytes(c.rss || -1))}
            ${stat('Dung lượng DB', j.db_size >= 0 ? fmtBytes(j.db_size) : '—')}
            ${stat('scs-server chạy', fmtUptime((now - new Date(c.started).getTime()) / 1000))}
          </div>
          ${c.ingest_here ? '' : '<p class="hint">Cổng agent chạy ở tiến trình khác: không có số heartbeat / kết nối.</p>'}
        </section>
      </div>`;
  }

  function kpi(label, value, meta, cls = '') {
    return `<div class="card kpi ${cls}"><span class="label">${esc(label)}</span><span class="value">${esc(value)}</span><span class="meta">${esc(meta)}</span></div>`;
  }

  function alertItem(a) {
    const click = a.device_id ? ` class="clickable" data-dev="${esc(a.device_id)}" tabindex="0" title="Bấm để xem tình trạng máy"` : '';
    return `<li${click}>${pill(SEV[a.severity] || SEV.low)}<div class="grow"><div class="title">${esc(KIND[a.kind] || a.kind)} · <span class="mono">${esc(a.hostname || a.store_code || '—')}</span></div><div class="reasons">${reasonsHTML(a, 2)}</div></div><span class="time">${esc(ago(new Date(a.last_at)))}</span></li>`;
  }

  // ---------------------------------------------------------------- trang: Báo cáo server
  // Lịch sử thông số server (ghi mỗi phút): CPU, RAM, ổ đĩa, băng thông mạng, heartbeat. Xuất CSV, in / lưu PDF.
  const STEP_NAME = { 60: 'phút', 600: '10 phút', 3600: 'giờ', 86400: 'ngày' };
  const dayStr = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  const pctTxt = (v) => (v == null ? '—' : `${(Math.round(v * 10) / 10).toLocaleString('vi-VN')}%`);

  // lineChart: biểu đồ đường SVG đơn giản (không thư viện ngoài). series: [{label, cls, pts: [[ms, v|null]]}].
  function lineChart(series, opt) {
    const W = 760, H = 210, L = 52, R = 12, T = 12, B = 28;
    const x0 = opt.from, x1 = opt.to;
    let yMax = opt.yMax || 0;
    series.forEach((s) => s.pts.forEach(([, v]) => { if (v != null && v > yMax) yMax = v; }));
    if (!opt.yMax) yMax = yMax > 0 ? yMax * 1.15 : 1;
    const X = (t) => L + ((t - x0) / Math.max(1, x1 - x0)) * (W - L - R);
    const Y = (v) => T + (1 - v / yMax) * (H - T - B);
    const grid = [0, 0.25, 0.5, 0.75, 1].map((f) => {
      const v = yMax * f, y = Y(v).toFixed(1);
      return `<line class="ch-grid" x1="${L}" x2="${W - R}" y1="${y}" y2="${y}"/><text class="ch-ax" x="${L - 6}" y="${(+y + 3.5).toFixed(1)}" text-anchor="end">${esc(opt.fmt(v))}</text>`;
    }).join('');
    const span = x1 - x0, nT = 6;
    const ticks = Array.from({ length: nT + 1 }, (_, i) => x0 + (span * i) / nT).map((t) => {
      const d = new Date(t);
      const lbl = span > 3 * 86400000 ? `${d.getDate()}/${d.getMonth() + 1}` : `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
      return `<text class="ch-ax" x="${X(t).toFixed(1)}" y="${H - 8}" text-anchor="middle">${esc(lbl)}</text>`;
    }).join('');
    const lines = series.map((s) => {
      // ngắt đường khi thiếu số liệu (server tắt)
      let d = '', pen = false;
      s.pts.forEach(([t, v]) => {
        if (v == null) { pen = false; return; }
        d += `${pen ? 'L' : 'M'}${X(t).toFixed(1)} ${Y(v).toFixed(1)}`;
        pen = true;
      });
      return `<path class="ch-line ${s.cls}" d="${d}"/>`;
    }).join('');
    const legend = series.map((s) => `<span class="ch-key ${s.cls}">${esc(s.label)}</span>`).join('');
    return `<div class="chart"><div class="ch-head"><h3>${esc(opt.title)}</h3><div class="ch-legend">${legend}</div></div>
      <svg viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc(opt.title)}">${grid}${ticks}${lines}</svg></div>`;
  }

  async function pageReport() {
    setTitle('Báo cáo server');
    const page = view;
    const now = new Date();
    const st = { preset: '24h', from: '', to: '', step: '' };
    page.innerHTML = `
      <div class="card report-ctl no-print">
        <div class="toolbar">
          <div class="seg" id="rp-preset">
            <button type="button" data-p="24h" class="on">24 giờ</button><button type="button" data-p="7d">7 ngày</button>
            <button type="button" data-p="30d">30 ngày</button><button type="button" data-p="90d">90 ngày</button></div>
          <label class="inline">Từ <input type="date" id="rp-from"></label>
          <label class="inline">Đến <input type="date" id="rp-to" value="${dayStr(now)}"></label>
          <label class="inline">Gom theo <select id="rp-step"><option value="">Tự động</option><option value="10min">10 phút</option>
            <option value="hour">Giờ</option><option value="day">Ngày</option></select></label>
          <div class="right">
            <a class="btn btn-sm" id="rp-xlsx" download>Xuất Excel (.xlsx)</a>
            <a class="btn btn-sm" id="rp-csv" download>Xuất CSV</a>
            <button type="button" class="btn btn-sm btn-primary" id="rp-print">In / Lưu PDF</button></div>
        </div>
        <p class="hint rp-pad">Server ghi số liệu mỗi phút và giữ 90 ngày (<code>SCS_METRICS_KEEP</code>). Giờ theo giờ của máy server.</p>
      </div>
      <div id="rp-body"><p class="hint">Đang tải…</p></div>`;

    const query = () => {
      const p = new URLSearchParams();
      if (st.preset) {
        const h = { '24h': 24, '7d': 168, '30d': 720, '90d': 2160 }[st.preset];
        const to = new Date(), from = new Date(to.getTime() - h * 3600000);
        p.set('from', from.toISOString()); p.set('to', to.toISOString());
        p.set('step', st.step || (h <= 24 ? '10min' : h <= 720 ? 'hour' : 'day'));
      } else {
        if (st.from) p.set('from', st.from);
        if (st.to) p.set('to', st.to);
        p.set('step', st.step || 'hour');
      }
      return p.toString();
    };

    const load = async () => {
      const q = query();
      $('#rp-csv').href = `/admin/v1/server/report.csv?${q}`;
      $('#rp-xlsx').href = `/admin/v1/server/report.xlsx?${q}`;
      let j;
      try { j = await getJSON(`/admin/v1/server/report?${q}`); } catch (err) { if (page === view) showLoadError($('#rp-body'), err); return; }
      if (page !== view) return;
      const sm = j.summary, rows = j.rows, from = new Date(j.from), to = new Date(j.to);
      const cover = j.summary.expected_minutes ? Math.min(100, (100 * sm.minutes) / sm.expected_minutes) : 0;
      const pts = (f) => rows.map((r) => [new Date(r.at).getTime(), f(r)]);
      const rate = (b, r) => (r.minutes ? (b * 8) / (r.minutes * 60) / 1e6 : null); // Mbit/s trung bình
      const fmtD = (d) => d.toLocaleString('vi-VN', { hour: '2-digit', minute: '2-digit', day: '2-digit', month: '2-digit', year: 'numeric' });
      const mbps = (bps) => `${((bps * 8) / 1e6).toLocaleString('vi-VN', { maximumFractionDigits: 2 })} Mbit/s`;
      const diskGrow = sm.disk_end && sm.disk_start ? sm.disk_end - sm.disk_start : null;
      $('#rp-body').innerHTML = `
        <div class="card report-head">
          <h2>Báo cáo vận hành server ${esc(j.host || '')}</h2>
          <p>Từ <b>${esc(fmtD(from))}</b> đến <b>${esc(fmtD(to))}</b> · gom theo ${esc(STEP_NAME[j.step_s] || '')} ·
            có số liệu ${esc(pctTxt(cover))} thời gian${cover < 95 && sm.expected_minutes ? ' (phần thiếu = server tắt / khởi động lại / chưa bật ghi)' : ''}</p>
        </div>
        ${rows.length ? '' : '<div class="card soon"><p>Chưa có số liệu trong khoảng này. Server ghi mỗi phút kể từ bản cập nhật có trang này.</p></div>'}
        <div class="grid-kpi rp-kpi">
          ${kpi('CPU', pctTxt(sm.cpu_avg), `trung bình · đỉnh ${pctTxt(sm.cpu_max)}`, sm.cpu_max >= 90 ? 'is-bad' : sm.cpu_max >= 70 ? 'is-warn' : '')}
          ${kpi('RAM', pctTxt(sm.mem_pct_avg), `trung bình · đỉnh ${pctTxt(sm.mem_pct_max)} của ${fmtBytes(sm.mem_total)}`, sm.mem_pct_max >= 90 ? 'is-bad' : sm.mem_pct_max >= 70 ? 'is-warn' : '')}
          ${kpi('Ổ đĩa dữ liệu', sm.disk_end ? `${fmtBytes(sm.disk_end)} / ${fmtBytes(sm.disk_total)}` : '—', diskGrow != null ? `tăng ${fmtBytes(Math.max(0, diskGrow))} trong kỳ` : '', sm.disk_total && sm.disk_end / sm.disk_total >= 0.9 ? 'is-bad' : '')}
          ${kpi('Mạng nhận', fmtBytes(sm.rx_bytes), `TB ${mbps(sm.rx_avg_bps)} · đỉnh ${mbps(sm.rx_peak_bps)}`)}
          ${kpi('Mạng gửi', fmtBytes(sm.tx_bytes), `TB ${mbps(sm.tx_avg_bps)} · đỉnh ${mbps(sm.tx_peak_bps)}`)}
          ${kpi('Heartbeat', fmtNum(sm.heartbeats), `${fmtNum(sm.log_events)} sự kiện log · tối đa ${fmtNum(sm.conns_max)} kết nối agent`)}
          ${kpi('Dung lượng DB', sm.db_end ? fmtBytes(sm.db_end) : '—', sm.db_start && sm.db_end ? `đầu kỳ ${fmtBytes(sm.db_start)}` : '')}
          ${kpi('RAM scs-server', sm.rss_max ? fmtBytes(sm.rss_max) : '—', 'cao nhất trong kỳ')}
        </div>
        ${rows.length ? `<div class="card rp-charts">
          ${lineChart([{ label: 'CPU trung bình', cls: 's1', pts: pts((r) => r.cpu_avg) }, { label: 'CPU đỉnh', cls: 's2', pts: pts((r) => r.cpu_max) }],
            { title: 'CPU (%)', from: from.getTime(), to: to.getTime(), yMax: 100, fmt: (v) => `${Math.round(v)}%` })}
          ${lineChart([{ label: 'RAM trung bình', cls: 's1', pts: pts((r) => r.mem_pct_avg) }, { label: 'RAM đỉnh', cls: 's2', pts: pts((r) => r.mem_pct_max) }],
            { title: 'RAM (%)', from: from.getTime(), to: to.getTime(), yMax: 100, fmt: (v) => `${Math.round(v)}%` })}
          ${lineChart([{ label: 'Nhận (TB)', cls: 's1', pts: pts((r) => rate(r.rx_bytes, r)) }, { label: 'Gửi (TB)', cls: 's3', pts: pts((r) => rate(r.tx_bytes, r)) },
            { label: 'Nhận (đỉnh)', cls: 's2', pts: pts((r) => (r.minutes ? (r.rx_peak_bps * 8) / 1e6 : null)) }],
            { title: 'Băng thông mạng (Mbit/s)', from: from.getTime(), to: to.getTime(), fmt: (v) => (v >= 10 ? Math.round(v) : (Math.round(v * 100) / 100)).toLocaleString('vi-VN') })}
          ${lineChart([{ label: 'Heartbeat', cls: 's1', pts: pts((r) => r.heartbeats) }],
            { title: `Heartbeat mỗi ${STEP_NAME[j.step_s] || 'khoảng'}`, from: from.getTime(), to: to.getTime(), fmt: (v) => fmtNum(Math.round(v)) })}
        </div>
        <div class="card">
          <div class="card-head"><h2>Chi tiết</h2><span class="sub">${rows.length > 200 ? `200 / ${fmtNum(rows.length)} dòng mới nhất — đủ trong file CSV` : `${fmtNum(rows.length)} dòng`}</span></div>
          <div class="table-wrap"><table class="rp-table">
            <thead><tr><th>Thời điểm</th><th class="num">CPU TB / đỉnh</th><th class="num">RAM TB / đỉnh</th><th class="num">Ổ đĩa</th>
              <th class="num">Nhận</th><th class="num">Gửi</th><th class="num">Đỉnh nhận</th><th class="num">Heartbeat</th><th class="num hide-sm">Phút có số liệu</th></tr></thead>
            <tbody>${rows.slice(-200).reverse().map((r) => `<tr>
              <td class="mono">${esc(j.step_s >= 86400 ? new Date(r.at).toLocaleDateString('vi-VN') : fmtD(new Date(r.at)))}</td>
              <td class="num">${esc(pctTxt(r.cpu_avg))} / ${esc(pctTxt(r.cpu_max))}</td>
              <td class="num">${esc(pctTxt(r.mem_pct_avg))} / ${esc(pctTxt(r.mem_pct_max))}</td>
              <td class="num">${r.disk_used ? esc(fmtBytes(r.disk_used)) : '—'}</td>
              <td class="num">${esc(fmtBytes(r.rx_bytes))}</td><td class="num">${esc(fmtBytes(r.tx_bytes))}</td>
              <td class="num">${esc(mbps(r.rx_peak_bps))}</td><td class="num">${fmtNum(r.heartbeats)}</td>
              <td class="num hide-sm">${fmtNum(r.minutes)}</td></tr>`).join('')}</tbody></table></div>
        </div>` : ''}
        <p class="hint print-only">In lúc ${esc(fmtD(new Date()))} từ web quản trị SCS-System.</p>`;
    };

    $('#rp-preset').addEventListener('click', (e) => {
      const b = e.target.closest('[data-p]'); if (!b) return;
      st.preset = b.dataset.p;
      $$('#rp-preset button').forEach((x) => x.classList.toggle('on', x === b));
      load();
    });
    const custom = () => {
      st.preset = '';
      st.from = $('#rp-from').value; st.to = $('#rp-to').value;
      $$('#rp-preset button').forEach((x) => x.classList.remove('on'));
      if (st.from && st.to) load();
    };
    $('#rp-from').addEventListener('change', custom);
    $('#rp-to').addEventListener('change', custom);
    $('#rp-step').addEventListener('change', (e) => { st.step = e.target.value; load(); });
    $('#rp-print').addEventListener('click', () => window.print());
    await load();
    refreshTimer = setInterval(() => { if (st.preset === '24h') load(); }, 60000); // 24 giờ gần nhất: tự cập nhật mỗi phút
  }

  // ---------------------------------------------------------------- trang: Máy
  // Gom theo mã siêu thị; viền màu theo máy tệ nhất trong siêu thị (cảnh báo đang mở):
  //   đỏ  = cảnh báo nghiêm trọng (critical) hoặc mất liên lạc: cần người xử lý
  //   vàng = cảnh báo khác đang mở (mức cao tự đóng sau 24 giờ, mức thấp, IP lạ...) hoặc có máy chờ duyệt
  //   xanh = mọi máy OK
  const RANK = { ok: 0, warn: 1, bad: 2 };
  const worst = (a, b) => (RANK[b] > RANK[a] ? b : a);
  const alertLevel = (a) => (a.severity === 'critical' || a.kind === 'offline' ? 'bad' : 'warn');
  const LEVEL_TXT = { ok: 'OK', warn: 'Đang chờ xử lý', bad: 'Cần xử lý' };

  async function pageDevices(params) {
    setTitle('Máy');
    const page = view;
    const state = { q: params.get('q') || '', status: params.get('status') || 'approved', page: 0, list: [],
      alerts: [], pending: [], stores: {}, open: new Set() };
    const PAGE = 20; // siêu thị mỗi trang

    page.innerHTML = `
      <div class="card">
        <div class="toolbar">
          <input type="search" id="dev-q" placeholder="Tìm mã siêu thị, hostname, IP, mã khóa, bộ rule…" value="${esc(state.q)}" aria-label="Tìm máy">
          <select id="dev-status" aria-label="Lọc trạng thái">
            <option value="approved">Đang hoạt động</option><option value="revoked">Đã thu hồi</option>
            <option value="rejected">Đã từ chối</option>
          </select>
          <span class="count" id="dev-count"></span>
          <button type="button" class="btn btn-sm" id="dev-import" title="Nạp thông tin siêu thị từ file .xlsx / .csv">Nạp thông tin từ file</button>
          <div class="right store-legend" aria-hidden="true">
            <span class="lg lg-bad">Cần xử lý</span><span class="lg lg-warn">Đang chờ xử lý</span><span class="lg lg-ok">OK</span>
          </div>
        </div>
        <div id="dev-groups" class="store-groups"><p class="empty">Đang tải…</p></div>
        <div class="pager"><span id="dev-range"></span><div class="btns">
          <button class="btn btn-sm" id="dev-prev">Trước</button><button class="btn btn-sm" id="dev-next">Sau</button></div></div>
      </div>`;
    $('#dev-status').value = state.status;

    const devRow = (d, lvl) => `
      <tr class="clickable" data-id="${d.id}">
        <td><span class="dot-lvl lvl-${lvl}" title="${esc(LEVEL_TXT[lvl])}"></span><span class="strong mono">${esc(d.hostname)}</span><span class="sub mono">${esc(d.key_id)}</span></td>
        <td class="mono">${esc(d.tn ? `TN${d.tn}${d.role ? ' · ' + d.role : ''}` : '—')}</td>
        <td>${d.last_seen ? esc(ago(new Date(d.last_seen))) : '—'}</td>
        <td class="hide-sm mono">${esc(d.last_ip || d.enroll_ip)}</td>
        <td class="hide-sm mono">${esc(d.agent_version || '—')}</td>
        <td class="hide-sm">${ruleCell(d.rules)}</td>
      </tr>`;

    const render = () => {
      const colored = state.status === 'approved';
      const byDev = {}, byStore = {};
      if (colored) {
        state.alerts.forEach((a) => {
          const l = alertLevel(a);
          if (a.device_id) byDev[a.device_id] = worst(byDev[a.device_id] || 'ok', l);
          else if (a.store_code && a.kind !== 'enroll_request') byStore[a.store_code] = worst(byStore[a.store_code] || 'ok', l);
        });
      }
      const pendingBy = {};
      if (colored) state.pending.forEach((d) => { const k = d.store_code || ''; pendingBy[k] = (pendingBy[k] || 0) + 1; });

      const q = state.q.trim().toLowerCase();
      const hit = (d) => [d.hostname, d.store_code, d.last_ip, d.enroll_ip, d.key_id, d.rules && d.rules.name && `${d.rules.name}@${d.rules.version}`]
        .some((v) => (v || '').toLowerCase().includes(q));
      const groups = {};
      state.list.forEach((d) => {
        const k = d.store_code || '';
        (groups[k] ||= { code: k, devs: [] }).devs.push(d);
      });
      let list = Object.values(groups).map((g) => {
        g.devs.sort((x, y) => (x.tn || 99) - (y.tn || 99) || x.hostname.localeCompare(y.hostname));
        g.lv = g.devs.map((d) => (colored ? byDev[d.id] || 'ok' : 'none'));
        g.level = !colored ? 'none' : g.lv.reduce(worst, byStore[g.code] || 'ok');
        g.pending = pendingBy[g.code] || 0;
        if (g.pending) g.level = worst(g.level, 'warn');
        g.bad = g.lv.filter((l) => l === 'bad').length;
        g.warn = g.lv.filter((l) => l === 'warn').length;
        g.last = g.devs.reduce((m, d) => (d.last_seen && d.last_seen > m ? d.last_seen : m), '');
        g.info = state.stores[g.code] || null;
        const infoHit = g.info && [g.info.name, g.info.organization, g.info.manager, g.info.deputy, g.info.leader,
          g.info.deputy_department, ...(g.info.ips || [])].some((v) => (v || '').toLowerCase().includes(q));
        g.show = !q || g.code.toLowerCase().includes(q) || infoHit ? g.devs : g.devs.filter(hit);
        return g;
      }).filter((g) => g.show.length);
      // Đỏ trước, rồi vàng, rồi xanh; cùng màu thì theo mã, máy chưa có mã siêu thị xếp cuối.
      list.sort((x, y) => (RANK[y.level] ?? 0) - (RANK[x.level] ?? 0) || (!x.code - !y.code) || x.code.localeCompare(y.code));

      const nDev = list.reduce((n, g) => n + g.show.length, 0);
      const pages = Math.max(1, Math.ceil(list.length / PAGE));
      state.page = Math.min(state.page, pages - 1);
      const slice = list.slice(state.page * PAGE, state.page * PAGE + PAGE);
      const cnt = colored ? { bad: list.filter((g) => g.level === 'bad').length, warn: list.filter((g) => g.level === 'warn').length } : null;
      $('#dev-count').textContent = `${fmtNum(list.length)} siêu thị · ${fmtNum(nDev)} máy` +
        (cnt && (cnt.bad || cnt.warn) ? ` · ${fmtNum(cnt.bad)} cần xử lý, ${fmtNum(cnt.warn)} đang chờ` : '');
      $('#dev-range').textContent = list.length > PAGE ? `Trang ${state.page + 1} / ${pages}` : '';
      $('#dev-prev').disabled = state.page === 0;
      $('#dev-next').disabled = state.page >= pages - 1;
      const exact = q && list.length === 1;
      $('#dev-groups').innerHTML = slice.map((g) => {
        const open = state.open.has(g.code) || exact || (q && !g.code.toLowerCase().includes(q));
        const pos = g.devs.map((d) => (d.tn ? `TN${d.tn}` : '')).filter(Boolean).join(' · ');
        const badges = [
          g.bad ? `<span class="pill pill-bad no-dot">${fmtNum(g.bad)} cần xử lý</span>` : '',
          g.warn ? `<span class="pill pill-warn no-dot">${fmtNum(g.warn)} đang chờ</span>` : '',
          g.pending ? `<span class="pill pill-warn no-dot">${fmtNum(g.pending)} chờ duyệt</span>` : '',
        ].join('');
        return `<div class="store-group lvl-${g.level}${open ? ' open' : ''}">
          <button type="button" class="store-head" data-store="${esc(g.code)}" aria-expanded="${open}">
            <span class="chev" aria-hidden="true"></span>
            <span class="store-code"><span class="mono">${esc(g.code || 'Chưa có mã siêu thị')}</span>${g.info && g.info.name ? ` - ${esc(g.info.name)}` : ''}</span>
            <span class="store-meta">${fmtNum(g.devs.length)} máy${pos ? ` · ${esc(pos)}` : ''}${g.info && g.info.manager ? ` · QL: ${esc(g.info.manager)}` : ''}</span>
            <span class="store-badges">${badges}${g.level !== 'none' && !badges ? `<span class="pill pill-ok no-dot">OK</span>` : ''}</span>
            <span class="store-last hide-sm">${g.last ? esc(ago(new Date(g.last))) : '—'}</span>
          </button>
          ${open ? `${g.code ? storeSummary(g.code, g.info) : ''}<div class="table-wrap"><table class="store-devs">
            <thead><tr><th>Máy</th><th>Vị trí</th><th>Liên lạc lần cuối</th><th class="hide-sm">IP</th><th class="hide-sm">Agent</th><th class="hide-sm">Bộ rule</th></tr></thead>
            <tbody>${g.show.map((d) => devRow(d, g.lv[g.devs.indexOf(d)])).join('')}</tbody></table></div>` : ''}
        </div>`;
      }).join('') || `<p class="empty">${state.list.length ? 'Không có máy nào khớp tìm kiếm.' : 'Không có máy nào.'}</p>`;
    };

    const load = async () => {
      try {
        const approved = state.status === 'approved';
        const [list, alerts, pending, stores] = await Promise.all([
          getJSON(`/admin/v1/devices?status=${encodeURIComponent(state.status)}`).then((j) => j.devices),
          approved ? getJSON('/admin/v1/alerts').then((j) => j.alerts) : [],
          approved ? getJSON('/admin/v1/devices?status=pending').then((j) => j.devices) : [],
          getJSON('/admin/v1/stores').then((j) => j.stores).catch(() => []),
        ]);
        const sig = state.status + JSON.stringify([list, alerts.map((a) => [a.id, a.device_id, a.severity, a.kind]), pending.map((d) => d.id), stores]);
        if (sig === state.sig) return; // không đổi: không vẽ lại
        const byCode = {};
        stores.forEach((x) => { byCode[x.code] = x; });
        storeCache = byCode;
        Object.assign(state, { sig, list, alerts, pending, stores: byCode });
      } catch (err) {
        if (page === view) showLoadError(page, err);
        return;
      }
      if (page === view) render();
    };

    $('#dev-import').addEventListener('click', () => importStores(() => { state.sig = ''; load(); }));
    $('#dev-q').addEventListener('input', (e) => { state.q = e.target.value; state.page = 0; render(); });
    $('#dev-status').addEventListener('change', (e) => { state.status = e.target.value; state.page = 0; state.open.clear(); load(); });
    $('#dev-prev').addEventListener('click', () => { state.page--; render(); });
    $('#dev-next').addEventListener('click', () => { state.page++; render(); });
    $('#dev-groups').addEventListener('click', (e) => {
      const edit = e.target.closest('[data-store-edit]');
      if (edit) { editStore(edit.dataset.storeEdit, state.stores, () => { state.sig = ''; load(); }); return; }
      const h = e.target.closest('.store-head');
      if (h) {
        const k = h.dataset.store;
        if (state.open.has(k)) state.open.delete(k); else state.open.add(k);
        render();
        return;
      }
      const tr = e.target.closest('tr[data-id]');
      if (tr) showDevice(state.list.find((d) => String(d.id) === tr.dataset.id), load);
    });
    load();
    liveReload = load;
  }

  // ---------------------------------------------------------------- thông tin siêu thị
  let storeCache = {}; // mã siêu thị → thông tin (trang Máy tải; trang Cảnh báo dùng để hiện tên)
  const STORE_PEOPLE = [['manager', 'Quản lý'], ['deputy', 'Phó quản lý'], ['leader', 'Trưởng nhóm'], ['deputy_department', 'Phó phòng']];
  const storeLabel = (code) => {
    const n = storeCache[code] && storeCache[code].name;
    return n ? `${code} - ${n}` : code;
  };

  function storeSummary(code, info) {
    const i = info || {};
    const row = (k, v) => (v ? `<div><dt>${esc(k)}</dt><dd>${esc(v)}</dd></div>` : '');
    const body = [row('Tổ chức', i.organization), row('IP', (i.ips || []).join(', ')),
      ...STORE_PEOPLE.map(([k, l]) => row(l, i[k]))].join('');
    return `<div class="store-info">
      <dl class="store-kv">${body || '<div class="sub-inline">Chưa có thông tin siêu thị.</div>'}</dl>
      ${i.note ? `<p class="store-note">${esc(i.note)}</p>` : ''}
      <div class="store-info-actions"><button type="button" class="btn btn-sm" data-store-edit="${esc(code)}">${can(LV.agentInfo) ? 'Sửa thông tin siêu thị' : 'Xem thông tin siêu thị'}</button>
      ${i.updated_by ? `<span class="sub-inline">sửa lần cuối bởi ${esc(i.updated_by)}</span>` : ''}</div></div>`;
  }

  // editStore: form thông tin siêu thị (giống form quản lý cửa hàng: tên, tổ chức, IP, người phụ trách, ghi chú).
  function editStore(code, stores, done) {
    const i = stores[code] || { code, ips: [] };
    const ro = !can(LV.agentInfo);
    const dis = ro ? ' disabled' : '';
    // Gợi ý từ giá trị đã nhập ở các siêu thị khác (gõ tự do vẫn được).
    const all = Object.values(stores);
    const opts = (keys) => [...new Set(all.flatMap((x) => keys.map((k) => x[k])).filter(Boolean))].sort()
      .map((v) => `<option value="${esc(v)}"></option>`).join('');
    const ipRow = (v) => `<div class="ip-row"><input class="mono" data-ip value="${esc(v)}" placeholder="203.0.113.10" spellcheck="false" autocomplete="off"${dis}>
      ${ro ? '' : '<button type="button" class="btn btn-sm" data-ip-del aria-label="Bỏ IP">×</button>'}</div>`;
    const field = (id, label, val, list) => `<label class="field"><span>${esc(label)}</span>
      <input id="sf-${id}" value="${esc(val || '')}" maxlength="120" autocomplete="off"${list ? ` list="sl-${list}"` : ''}${dis}></label>`;
    openDrawer(`${code}${i.name ? ` - ${i.name}` : ''}`, `
      <form id="store-form" class="store-form" autocomplete="off">
        ${field('name', 'Tên siêu thị', i.name)}
        ${field('organization', 'Tổ chức', i.organization, 'org')}
        <div class="field"><span>IP WAN <small>(IP đầu là IP chính; máy gửi từ các IP này không bị báo "IP lạ")</small></span>
          <div id="sf-ips">${((i.ips || []).length ? i.ips : ['']).map(ipRow).join('')}</div>
          ${ro ? '' : '<button type="button" class="btn btn-sm" id="sf-ip-add">+ Thêm IP</button>'}</div>
        ${STORE_PEOPLE.map(([k, l]) => field(k, l, i[k], 'people')).join('')}
        <label class="field"><span>Ghi chú</span><textarea id="sf-note" rows="4" maxlength="2000"${dis}>${esc(i.note || '')}</textarea></label>
        <datalist id="sl-org">${opts(['organization'])}</datalist>
        <datalist id="sl-people">${opts(STORE_PEOPLE.map(([k]) => k))}</datalist>
        <p class="form-error" id="sf-err" hidden></p>
        ${ro ? `<p class="hint">Cần quyền: ${esc(LEVEL_NAMES[LV.agentInfo])} để sửa.</p>` : `<div class="form-actions">
          <button type="button" class="btn" data-action="close-drawer">Hủy</button>
          <button type="submit" class="btn btn-primary">Lưu</button></div>`}
      </form>`);
    if (ro) return;
    const ips = $('#sf-ips');
    $('#sf-ip-add').addEventListener('click', () => {
      if ($$('[data-ip]', ips).length >= 9) { toast('Tối đa 9 IP mỗi siêu thị'); return; }
      ips.insertAdjacentHTML('beforeend', ipRow(''));
      $$('[data-ip]', ips).pop().focus();
    });
    ips.addEventListener('click', (e) => {
      const b = e.target.closest('[data-ip-del]'); if (!b) return;
      if ($$('[data-ip]', ips).length > 1) b.parentElement.remove(); else $('[data-ip]', ips).value = '';
    });
    $('#store-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const btn = e.submitter; btn.disabled = true;
      const body = { name: $('#sf-name').value, organization: $('#sf-organization').value, note: $('#sf-note').value,
        ips: $$('[data-ip]', ips).map((x) => x.value.trim()).filter(Boolean) };
      STORE_PEOPLE.forEach(([k]) => { body[k] = $(`#sf-${k}`).value; });
      try {
        await postJSON(`/admin/v1/stores/${encodeURIComponent(code)}`, body);
        toast('Đã lưu thông tin siêu thị');
        closeDrawer();
        done();
      } catch (err) {
        const el = $('#sf-err'); el.hidden = false; el.textContent = err.message;
        btn.disabled = false;
      }
    });
  }

  // importStores: nạp thông tin siêu thị từ file .xlsx / .csv — chọn file → xem trước → Nạp.
  function importStores(done) {
    const ro = !can(LV.agentInfo);
    openDrawer('Nạp thông tin siêu thị từ file', `
      <section class="import-help">
        <p>File <b>.xlsx</b> (Excel) hoặc <b>.csv</b> (UTF-8), tối đa 5 MB. Dòng đầu là tên cột; bắt buộc có cột <b>Mã siêu thị</b>.
          Các cột nhận được: Mã siêu thị, Tên siêu thị, Tổ chức, IP, Quản lý, Phó quản lý, Trưởng nhóm, Phó phòng, Ghi chú
          (tiếng Việt có dấu, không dấu hoặc tiếng Anh như Store Code, Store Name, Organization, Ip, Manager, Deputy, Leader,
          Deputy Department, Note). Cột khác bị bỏ qua.</p>
        <p><b>Ô trống = giữ nguyên</b> giá trị đang có. Cột IP có giá trị thì thay cả danh sách IP (nhiều IP cách nhau dấu phẩy, chấm phẩy hoặc xuống dòng).</p>
        <p><a class="btn btn-sm" href="/admin/v1/stores/template.csv" download>Tải file mẫu (.csv)</a></p>
      </section>
      ${ro ? `<p class="hint">Cần quyền: ${esc(LEVEL_NAMES[LV.agentInfo])} để nạp.</p>` : `
      <label class="field"><span>Chọn file</span><input type="file" id="imp-file" accept=".xlsx,.csv,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"></label>
      <p class="form-error" id="imp-err" hidden></p>
      <div id="imp-preview"></div>`}`);
    if (ro) return;
    let file = null;
    const send = async (apply) => {
      const buf = await file.arrayBuffer();
      const r = await api(`/admin/v1/stores/import?file=${encodeURIComponent(file.name)}${apply ? '&apply=1' : ''}`,
        { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: buf });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || `lỗi ${r.status}`);
      return j;
    };
    const ST = { new: ['pill-ok', 'Thêm mới'], changed: ['pill-info', 'Thay đổi'], same: ['', 'Không đổi'], error: ['pill-bad', 'Lỗi'] };
    const showPlan = (pl) => {
      const n = pl.new + pl.changed;
      $('#imp-preview').innerHTML = `
        <div class="import-sum">
          <span><b>${fmtNum(pl.total)}</b> dòng</span><span class="pill pill-ok no-dot">${fmtNum(pl.new)} thêm mới</span>
          <span class="pill pill-info no-dot">${fmtNum(pl.changed)} thay đổi</span><span class="pill no-dot">${fmtNum(pl.same)} không đổi</span>
          ${pl.errors ? `<span class="pill pill-bad no-dot">${fmtNum(pl.errors)} lỗi</span>` : ''}</div>
        <p class="hint">Cột nhận ra: ${esc(pl.columns.join(', '))}${pl.unknown.length ? ` · bỏ qua: ${esc(pl.unknown.join(', '))}` : ''}</p>
        <div class="table-wrap"><table>
          <thead><tr><th class="num">Dòng</th><th>Mã</th><th>Tên</th><th>Kết quả</th></tr></thead>
          <tbody>${pl.rows.map((r) => `<tr><td class="num">${r.row}</td><td class="mono">${esc(r.code || '—')}</td><td>${esc(r.name || '')}</td>
            <td>${pill(ST[r.status])} ${r.error ? `<span class="sub">${esc(r.error)}</span>` : r.changes && r.changes.length ? `<span class="sub">${esc(r.changes.join(', '))}</span>` : ''}</td></tr>`).join('')}</tbody>
        </table></div>
        ${pl.total > pl.rows.length ? `<p class="hint">Hiện ${fmtNum(pl.rows.length)} dòng đầu (lỗi và thay đổi trước).</p>` : ''}
        <div class="form-actions">
          ${pl.errors ? '<span class="form-error">Sửa các dòng lỗi trong file rồi chọn lại file.</span>'
            : `<button type="button" class="btn btn-primary" id="imp-apply"${n ? '' : ' disabled'}>${n ? `Nạp ${fmtNum(n)} siêu thị` : 'Không có gì để nạp'}</button>`}
        </div>`;
      const b = $('#imp-apply');
      if (b) b.addEventListener('click', async () => {
        b.disabled = true;
        try {
          const j = await send(true);
          toast(`Đã nạp ${fmtNum(j.plan.new + j.plan.changed)} siêu thị`);
          closeDrawer();
          done();
        } catch (err) {
          b.disabled = false;
          const e = $('#imp-err'); e.hidden = false; e.textContent = err.message;
        }
      });
    };
    $('#imp-file').addEventListener('change', async (e) => {
      file = e.target.files[0];
      const errEl = $('#imp-err');
      errEl.hidden = true;
      $('#imp-preview').innerHTML = '';
      if (!file) return;
      if (file.size > 5 * 1024 * 1024) { errEl.hidden = false; errEl.textContent = 'File quá lớn (tối đa 5 MB).'; return; }
      $('#imp-preview').innerHTML = '<p class="hint">Đang đọc file…</p>';
      try {
        showPlan((await send(false)).plan);
      } catch (err) {
        $('#imp-preview').innerHTML = '';
        errEl.hidden = false; errEl.textContent = err.message;
      }
    });
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

  // Giới hạn hiển thị trong chi tiết máy (tránh tràn khi máy có nhiều giai đoạn / dòng log dài).
  const TL_PAGE = 10;       // giai đoạn mỗi lần "Xem thêm"
  const TL_RULES = 3;       // rule hiện sẵn mỗi giai đoạn (còn lại gập)
  const TL_SAMPLE = 160;    // ký tự dòng log mẫu hiện sẵn (dài hơn thì bấm xem cả dòng)
  const TL_CHANGES = 5;     // lần đổi trạng thái hiện sẵn

  function sampleHTML(text) {
    if (!text) return '';
    if (text.length <= TL_SAMPLE) return `<div class="mono hint break-all sample clamp-3">${esc(text)}</div>`;
    return `<details class="sample-more"><summary class="mono hint break-all">${esc(text.slice(0, TL_SAMPLE))}… <span class="sub">(xem cả dòng)</span></summary>
      <div class="mono hint break-all sample-full">${esc(text)}</div></details>`;
  }

  async function loadTimeline(d, shown = TL_PAGE, shownChanges = TL_CHANGES) {
    const box = $('#dev-timeline');
    if (!box) return;
    let tl;
    let policy = { autoclose_min: 1440, offline_min: 15 };
    try {
      const j = await getJSON(`/admin/v1/devices/${d.id}/periods?limit=${Math.min(shown + 1, 200)}`);
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
      const all = Object.entries(p.rules || {}).sort((a, b) => b[1].count - a[1].count);
      const ruleLi = ([id, r]) => `<li><span class="mono">${esc(id)}</span> ${pill(SEV[r.severity] || SEV.low)} ×${fmtNum(r.count)}${sampleHTML(r.sample)}</li>`;
      const rest = all.slice(TL_RULES);
      return `<li class="period period-${esc(p.kind)}">
        <div class="period-head">${pill(PERIOD_KIND[p.kind] || ['', p.kind])}
          ${p.severity && p.kind === 'alert' ? pill(SEV[p.severity] || SEV.low) : ''}
          <span class="sub">${esc(fmtTime(start))} → ${open ? '<strong>đang mở</strong>' : esc(fmtTime(end))} · ${esc(fmtDur(end - start))}</span>
          ${p.events ? `<span class="sub">${fmtNum(p.events)} sự kiện</span>` : ''}
          ${p.offline_since ? '<span class="pill pill-warn no-dot">đang mất liên lạc</span>' : ''}
          ${!open && p.closed_by ? `<span class="sub">${esc(closedText(p.closed_by))}</span>` : ''}
          ${open && p.kind === 'alert' ? `<button class="btn btn-sm btn-primary" data-resolve="${p.id}"${adminOnly(LV.incidents)}>Đã xử lý</button>
            <span class="sub">${p.severity === 'critical' ? 'chỉ admin đóng' : `tự đóng khi yên ${esc(fmtDur(policy.autoclose_min * 60000))}`}</span>` : ''}
        </div>
        ${all.length ? `<ul class="period-rules">${all.slice(0, TL_RULES).map(ruleLi).join('')}</ul>` : ''}
        ${rest.length ? `<details class="period-rest"><summary class="sub">và ${fmtNum(rest.length)} rule khác</summary>
          <ul class="period-rules">${rest.map(ruleLi).join('')}</ul></details>` : ''}
        ${p.note ? `<p class="hint">Ghi chú: ${esc(p.note)}</p>` : ''}
      </li>`;
    };
    const changes = tl.status_changes || []; // server trả tối đa 50 lần gần nhất
    const periods = tl.periods.slice(0, shown);
    const morePeriods = tl.periods.length > shown;
    const shownCh = changes.slice(0, shownChanges);
    box.innerHTML = `<h3>Dòng thời gian</h3>
      ${periods.length ? `<ul class="periods">${periods.map(item).join('')}</ul>` : '<p class="hint">Chưa có (máy chưa gửi heartbeat nào).</p>'}
      ${morePeriods ? `<div class="more-row"><button class="btn btn-sm" data-tl-more>Xem thêm ${TL_PAGE} giai đoạn cũ hơn</button></div>` : ''}
      ${shownCh.length ? `<h3>Trạng thái thay đổi</h3><ul class="periods">${shownCh.map((c, i) => `<li><span class="sub">${esc(fmtTime(new Date(c.at)))}</span>
        <div class="hint break-all clamp-3">${esc(statusDiff(c.changes, changes[i + 1] && changes[i + 1].changes))}</div></li>`).join('')}</ul>
        ${changes.length > shownChanges ? `<div class="more-row"><button class="btn btn-sm" data-ch-more>Xem thêm ${Math.min(10, changes.length - shownChanges)} lần đổi</button></div>` : ''}` : ''}`;
    const tlMore = box.querySelector('[data-tl-more]');
    if (tlMore) tlMore.addEventListener('click', () => loadTimeline(d, shown + TL_PAGE, shownChanges));
    const chMore = box.querySelector('[data-ch-more]');
    if (chMore) chMore.addEventListener('click', () => loadTimeline(d, shown, shownChanges + 10));
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
        loadTimeline(d, shown, shownChanges);
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
        <button class="btn btn-sm" data-dev-action="test-ring"${adminOnly(LV.agentInfo)}>${d.test_ring ? 'Bỏ khỏi nhóm thử' : 'Thêm vào nhóm thử'}</button>
        <button class="btn btn-sm btn-danger" data-dev-action="revoke"${adminOnly(LV.agents)}>Thu hồi máy</button>
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
    const state = { sev: '', all: false, list: [], page: 0, policy: { autoclose_min: 1440, offline_min: 15 } };
    const AL_PAGE = 30;
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
          <thead><tr><th>Mức</th><th>Máy</th><th>Lý do</th><th class="num hide-sm">Số lần</th><th>Lần cuối</th><th></th></tr></thead>
          <tbody id="al-rows"><tr><td colspan="6" class="empty">Đang tải…</td></tr></tbody></table></div>
        <div class="pager"><span id="al-range"></span><div class="btns">
          <button class="btn btn-sm" id="al-prev">Trước</button><button class="btn btn-sm" id="al-next">Sau</button></div></div>
      </div>`;

    const render = () => {
      const all = state.list.filter((a) => !state.sev || a.severity === state.sev);
      const pages = Math.max(1, Math.ceil(all.length / AL_PAGE));
      state.page = Math.min(state.page, pages - 1);
      const rows = all.slice(state.page * AL_PAGE, state.page * AL_PAGE + AL_PAGE);
      $('#al-count').textContent = `${fmtNum(all.length)} cảnh báo${state.all ? '' : ' đang mở'}`;
      $('#al-range').textContent = all.length > AL_PAGE ? `Trang ${state.page + 1} / ${pages}` : '';
      $('#al-prev').disabled = state.page === 0;
      $('#al-next').disabled = state.page >= pages - 1;
      $('#al-rows').innerHTML = rows.map((a) => `
        <tr${a.device_id ? ` class="clickable" data-dev="${esc(a.device_id)}" title="Bấm để xem tình trạng máy"` : ''}>
          <td>${pill(SEV[a.severity] || SEV.low)}</td>
          <td><span class="strong mono">${esc(a.hostname || '—')}</span>${a.store_code ? `<span class="sub">Siêu thị ${esc(storeLabel(a.store_code))}</span>` : ''}</td>
          <td class="reasons-cell"><div class="reason-kind">${esc(KIND[a.kind] || a.kind)}</div>${reasonsHTML(a, 3)}</td>
          <td class="num hide-sm">${a.occurrences}</td>
          <td>${esc(ago(new Date(a.last_at)))}</td>
          <td>${a.acked_at
            ? `<span class="sub">${a.kind === 'event' ? 'Đã đóng' : 'Đã xác nhận'} · ${esc(a.acked_by)}</span>`
            : `<button class="btn btn-sm${a.kind === 'event' ? ' btn-primary' : ''}" data-ack="${a.id}" data-kind="${esc(a.kind)}"${adminOnly(LV.incidents)}>${a.kind === 'event' ? 'Đã xử lý' : 'Xác nhận'}</button>
               ${a.kind === 'event' ? `<span class="sub">${a.severity === 'critical' ? 'Chỉ admin đóng' : `Tự đóng khi yên ${esc(fmtDur(state.policy.autoclose_min * 60000))}`}</span>` : ''}`}</td>
        </tr>`).join('') || `<tr><td colspan="6" class="empty">Không có cảnh báo${state.all ? '' : ' đang mở'}.</td></tr>`;
    };

    const load = async () => {
      try {
        const [j, stores] = await Promise.all([getJSON(`/admin/v1/alerts${state.all ? '?state=all' : ''}`),
          getJSON('/admin/v1/stores').then((x) => x.stores).catch(() => null)]);
        if (stores) { storeCache = {}; stores.forEach((x) => { storeCache[x.code] = x; }); }
        const sig = String(state.all) + JSON.stringify(j.alerts) + JSON.stringify(stores);
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
      state.page = 0;
      $$('.chip', $('#sev-chips')).forEach((c) => c.classList.toggle('active', c === b));
      render();
    });
    $('#al-all').addEventListener('change', (e) => { state.all = e.target.checked; state.page = 0; load(); });
    $('#al-prev').addEventListener('click', () => { state.page--; render(); });
    $('#al-next').addEventListener('click', () => { state.page++; render(); });
    $('#al-rows').addEventListener('click', async (e) => {
      const b = e.target.closest('[data-ack]');
      if (!b) { // bấm vào dòng (không phải nút): mở tình trạng máy
        const tr = e.target.closest('tr[data-dev]');
        if (tr && !getSelection().toString()) openDeviceById(tr.dataset.dev, load);
        return;
      }
      if (b.disabled) return;
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
            <input class="store-input" id="store-${d.id}" data-store-for="${d.id}" inputmode="numeric" maxlength="10" placeholder="12345" autocomplete="off"${adminOnly(LV.agents)}></label>`}
          <div class="req-actions">
            <button class="btn btn-danger" data-req="reject" data-id="${d.id}"${adminOnly(LV.agents)}>Từ chối</button>
            <button class="btn btn-primary" data-req="approve" data-id="${d.id}"${adminOnly(LV.agents)}>Duyệt</button>
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
        <td>${levelPill(s.level)}${s.account ? '<span class="sub">tài khoản email</span>' : ''}</td>
        <td class="hide-sm mono">${esc(s.ip)}</td>
        <td class="hide-sm">${esc(fmtTime(new Date(s.created)))}</td>
        <td>${esc(ago(new Date(s.last_seen)))}</td>
        <td>${s.current ? '' : `<button class="btn btn-sm btn-danger" data-revoke="${esc(s.id)}" data-name="${esc(s.name)}">Thu hồi</button>`}</td>
      </tr>`).join('') || '<tr><td colspan="6" class="empty">Không có phiên nào.</td></tr>';
  }

  function levelPill(l) {
    const cls = l === LV.root ? 'pill-ok' : l === LV.view ? 'pill-info' : 'pill-warn';
    return `<span class="pill ${cls} no-dot">${esc(l)} · ${esc(LEVEL_NAMES[l] ?? '?')}</span>`;
  }

  // ---------------------------------------------------------------- tài khoản (chỉ quản trị gốc)
  async function refreshAccountCount() {
    const nav = $('#nav-accounts');
    nav.hidden = !isAdmin();
    if (!isAdmin()) return;
    try {
      const j = await getJSON('/admin/v1/accounts');
      const n = (j.accounts || []).filter((a) => a.status === 'pending').length;
      const b = $('#nav-acct-pending');
      b.textContent = n ? String(n) : '';
      b.title = n ? `${n} tài khoản chờ duyệt` : '';
    } catch { /* bỏ qua */ }
  }

  function levelSelect(id, cur) {
    return `<select id="${esc(id)}" aria-label="Mức quyền">${LEVEL_NAMES.map((n, i) => (i === 0 ? '' :
      `<option value="${i}"${i === (cur || LV.view) ? ' selected' : ''}>${i} · ${esc(n)}</option>`)).join('')}</select>`;
  }

  async function pageAccounts(soft) {
    setTitle('Tài khoản');
    soft = soft === true;
    if (!isAdmin()) {
      view.innerHTML = `<div class="card soon">${ICON.warn}<h2>Cần quyền quản trị gốc</h2><p>Chỉ người đăng nhập bằng mã lấy qua SSH trên máy server mới duyệt và đặt quyền tài khoản.</p></div>`;
      return;
    }
    if (!soft) view.innerHTML = '<p class="hint">Đang tải…</p>';
    let j;
    try { j = await getJSON('/admin/v1/accounts'); } catch (err) { showLoadError(view, err); return; }
    const list = j.accounts || [];
    const pending = list.filter((a) => a.status === 'pending');
    const others = list.filter((a) => a.status !== 'pending');
    const t = (v) => (v ? esc(fmtTime(new Date(v))) : '—');
    view.innerHTML = `
      ${j.email ? '' : `<div class="card banner-warn"><p><strong>Server chưa cấu hình gửi mail</strong> (SCS_SMTP_* trong server.env): chưa ai đăng ký hay đăng nhập bằng email được.</p></div>`}
      <div class="card">
        <div class="card-head"><h2>Chờ duyệt</h2><span class="sub">Đã xác minh email; chọn mức quyền rồi duyệt</span></div>
        <div class="table-wrap"><table>
          <thead><tr><th>Tên đăng nhập</th><th>Email</th><th class="hide-sm">Đăng ký lúc</th><th>Mức quyền</th><th></th></tr></thead>
          <tbody id="acct-pending">${pending.map((a) => `<tr>
            <td class="strong">${esc(a.username)}</td><td class="mono">${esc(a.email)}</td><td class="hide-sm">${t(a.created_at)}</td>
            <td>${levelSelect(`lv-${a.username}`, LV.view)}</td>
            <td class="actions"><button class="btn btn-sm btn-danger" data-acct="delete" data-user="${esc(a.username)}">Từ chối</button>
              <button class="btn btn-sm btn-primary" data-acct="approve" data-user="${esc(a.username)}">Duyệt</button></td></tr>`).join('')
              || '<tr><td colspan="5" class="empty">Không có tài khoản chờ duyệt.</td></tr>'}</tbody>
        </table></div>
      </div>
      <div class="card">
        <div class="card-head"><h2>Tài khoản</h2><span class="sub">Đổi quyền, khóa hay xóa đều cắt ngay các phiên đang mở của tài khoản đó</span></div>
        <div class="table-wrap"><table>
          <thead><tr><th>Tên đăng nhập</th><th class="hide-sm">Email</th><th>Trạng thái</th><th>Mức quyền</th><th class="hide-sm">Đăng nhập cuối</th><th></th></tr></thead>
          <tbody>${others.map((a) => `<tr>
            <td class="strong">${esc(a.username)}<span class="sub">duyệt bởi ${esc(a.approved_by || '—')}</span></td>
            <td class="hide-sm mono">${esc(a.email)}</td>
            <td>${a.status === 'active' ? '<span class="pill pill-ok no-dot">Hoạt động</span>' : '<span class="pill pill-bad no-dot">Đã khóa</span>'}</td>
            <td>${levelSelect(`lv-${a.username}`, a.level)} <button class="btn btn-sm" data-acct="level" data-user="${esc(a.username)}">Đổi</button></td>
            <td class="hide-sm">${t(a.last_login)}</td>
            <td class="actions">${a.status === 'active'
              ? `<button class="btn btn-sm" data-acct="disable" data-user="${esc(a.username)}">Khóa</button>`
              : `<button class="btn btn-sm" data-acct="enable" data-user="${esc(a.username)}">Mở khóa</button>`}
              <button class="btn btn-sm btn-danger" data-acct="delete" data-user="${esc(a.username)}">Xóa</button></td></tr>`).join('')
              || '<tr><td colspan="6" class="empty">Chưa có tài khoản nào.</td></tr>'}</tbody>
        </table></div>
      </div>
      <div class="card"><div class="card-head"><h2>Các mức quyền</h2><span class="sub">Số nhỏ = quyền cao; mức trên làm được mọi việc của mức dưới</span></div>
        <ol class="level-list" start="0">${LEVEL_NAMES.map((n, i) => `<li><strong>${esc(n)}</strong>${i === 0 ? ' — chỉ khi đăng nhập bằng mã SSH; duyệt tài khoản, đặt quyền, thu hồi phiên' : ''}</li>`).join('')}</ol>
        <p class="hint">Tài khoản không cấp quyền cho tài khoản khác được: chỉ quản trị gốc duyệt và đặt quyền.</p></div>`;
    view.addEventListener('click', onAccountClick);
    refreshAccountCount();
  }

  async function onAccountClick(e) {
    const b = e.target.closest('[data-acct]'); if (!b) return;
    const user = b.dataset.user, act = b.dataset.acct;
    const sel = document.getElementById(`lv-${user}`);
    const level = sel ? Number(sel.value) : 0;
    const titles = {
      approve: ['Duyệt tài khoản?', `<p>Tài khoản <strong>${esc(user)}</strong> sẽ đăng nhập được với quyền <strong>${esc(level)} · ${esc(LEVEL_NAMES[level])}</strong>.</p>`, 'Duyệt', false],
      level: ['Đổi mức quyền?', `<p><strong>${esc(user)}</strong> → <strong>${esc(level)} · ${esc(LEVEL_NAMES[level])}</strong>. Phiên đang mở của tài khoản bị đăng xuất.</p>`, 'Đổi', false],
      disable: ['Khóa tài khoản?', `<p><strong>${esc(user)}</strong> bị đăng xuất ngay và không đăng nhập được tới khi mở khóa.</p>`, 'Khóa', true],
      enable: ['Mở khóa tài khoản?', `<p><strong>${esc(user)}</strong> đăng nhập lại được với mức quyền cũ.</p>`, 'Mở khóa', false],
      delete: ['Xóa tài khoản?', `<p>Xóa hẳn <strong>${esc(user)}</strong>. Muốn dùng lại thì phải đăng ký lại.</p>`, 'Xóa', true],
    }[act];
    if (!titles || !(await confirmBox(...titles))) return;
    b.disabled = true;
    try {
      await postJSON(`/admin/v1/accounts/${encodeURIComponent(user)}`, { action: act, level });
      toast('Đã lưu.');
    } catch (err) {
      toast(`Không lưu được: ${err.message}`);
    }
    pageAccounts(true);
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
          <dt>Nhóm thử (${fmtNum(testers.length)} máy)</dt><dd>${ringPill(ro.test)} ${ro.test ? `<button class="btn btn-sm" data-ring-stop="test"${adminOnly(LV.agents)}>Dừng</button>` : ''}</dd>
          <dt>Tất cả máy</dt><dd>${ringPill(ro.all)} ${ro.all ? `<button class="btn btn-sm" data-ring-stop="all"${adminOnly(LV.agents)}>Dừng</button>` : ''}</dd>
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
                <button class="btn btn-sm" data-rollout="test" data-version="${esc(r.version)}"${adminOnly(LV.agents)}${ro.test === r.version ? ' disabled' : ''}>Phát hành nhóm thử</button>
                <button class="btn btn-sm btn-primary" data-rollout="all" data-version="${esc(r.version)}"${adminOnly(LV.agents)}${ro.all === r.version ? ' disabled' : ''}>Phát hành tất cả</button>
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
            <input id="cfg-global" placeholder="pos.service, printer.service" value="${esc((globalCfg?.watch_services || []).join(', '))}"${adminOnly(LV.agentInfo)}>
            <small>Tên dịch vụ systemd, cách nhau dấu phẩy. Để trống và lưu = không theo dõi dịch vụ nào.</small></label>
        </div>
        <div class="form-actions"><button class="btn btn-primary btn-sm" id="cfg-global-save"${adminOnly(LV.agentInfo)}>Lưu cấu hình chung</button></div>
        <h3>Ghi đè theo siêu thị / máy</h3>
        <div class="table-wrap"><table>
          <thead><tr><th>Áp cho</th><th>Dịch vụ</th><th class="hide-sm">Sửa lúc</th><th></th></tr></thead>
          <tbody>${overrides.map((c) => `
            <tr><td>${c.scope === 'store' ? `Siêu thị <span class="mono">${esc(c.key)}</span>` : `Máy <span class="mono">${esc(hostOf(c.key))}</span>`}</td>
              <td class="mono">${esc((c.watch_services || []).join(', ') || '(không theo dõi)')}</td>
              <td class="hide-sm">${esc(c.updated_at ? fmtTime(new Date(c.updated_at)) : '')}${c.updated_by ? ` · ${esc(c.updated_by)}` : ''}</td>
              <td><button class="btn btn-sm btn-danger" data-cfg-del="${esc(c.scope)}" data-key="${esc(c.key)}"${adminOnly(LV.agentInfo)}>Bỏ ghi đè</button></td></tr>`).join('') || '<tr><td colspan="4" class="empty">Chưa có ghi đè nào.</td></tr>'}
          </tbody></table></div>
        <div class="form-grid">
          <label class="field">Áp cho
            <select id="ov-scope"${adminOnly(LV.agentInfo)}><option value="store">Siêu thị (mã)</option><option value="device">Máy (hostname)</option></select></label>
          <label class="field">Mã siêu thị / hostname
            <input id="ov-key" placeholder="12345 hoặc 12345-TN1-PRI"${adminOnly(LV.agentInfo)}></label>
          <label class="field full">Dịch vụ
            <input id="ov-watch" placeholder="pos.service"${adminOnly(LV.agentInfo)}></label>
        </div>
        <div class="form-actions"><button class="btn btn-sm" id="ov-save"${adminOnly(LV.agentInfo)}>Thêm / sửa ghi đè</button></div>
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
              <td><button class="btn btn-sm btn-danger" data-lb-del="${esc(c.scope)}" data-key="${esc(c.key)}"${adminOnly(LV.agentInfo)}>Bỏ nhãn</button></td></tr>`).join('') || '<tr><td colspan="4" class="empty">Chưa đặt nhãn nào: agent dùng role / store / os tự suy ra.</td></tr>'}
          </tbody></table></div>
        <div class="form-grid">
          <label class="field">Áp cho
            <select id="lb-scope"${adminOnly(LV.agentInfo)}><option value="global">Mọi máy</option><option value="store">Siêu thị (mã)</option><option value="device">Máy (hostname)</option></select></label>
          <label class="field">Mã siêu thị / hostname
            <input id="lb-key" placeholder="bỏ trống khi áp cho mọi máy"${adminOnly(LV.agentInfo)}></label>
          <label class="field full">Nhãn
            <input id="lb-labels" placeholder="role=pri, vung=mien-nam" autocomplete="off" spellcheck="false"${adminOnly(LV.agentInfo)}>
            <small>khoa=gia_tri, cách nhau dấu phẩy. Chữ thường, số, . _ -. Lưu sẽ thay toàn bộ nhãn ở cấp này.</small></label>
        </div>
        <div class="form-actions"><button class="btn btn-sm" id="lb-save"${adminOnly(LV.agentInfo)}>Lưu nhãn</button></div>
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
        ? `<button class="btn btn-sm" data-unapprove="${type}" data-id="${esc(m.id)}" data-name="${esc(m.name)}"${adminOnly(LV.cluster)}>Rút xác nhận ${esc(CLUSTER_ACT[type])}</button>`
        : `<button class="btn btn-sm ${cls || ''}" data-approve="${type}" data-id="${esc(m.id)}" data-name="${esc(m.name)}" data-fp="${esc(m.fingerprint)}"${adminOnly(LV.cluster)}>${esc(label)}</button>`);
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
                  <button class="btn btn-sm" data-local="${m.isolated_local ? 'off' : 'on'}" data-id="${esc(m.id)}" data-name="${esc(m.name)}"${adminOnly(LV.cluster)}>${m.isolated_local ? 'Nối lại tại đây' : 'Cô lập tại đây'}</button>
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
    : `<button class="btn btn-sm btn-primary" data-approve="member.add" data-id="${esc(j.member.id)}" data-name="${esc(j.member.name)}" data-fp="${esc(j.fingerprint)}"${adminOnly(LV.cluster)}>Duyệt</button>`}
                  <button class="btn btn-sm" data-reject="${esc(j.member.id)}" data-name="${esc(j.member.name)}"${adminOnly(LV.cluster)}>Từ chối</button>
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
            <button class="btn btn-sm" data-act="up" data-url="${esc(a.url)}"${i === 0 ? ' disabled' : adminOnly(LV.servers)} aria-label="Lên">↑</button>
            <button class="btn btn-sm" data-act="down" data-url="${esc(a.url)}"${i === inList.length - 1 ? ' disabled' : adminOnly(LV.servers)} aria-label="Xuống">↓</button>
            <button class="btn btn-sm btn-danger" data-act="remove" data-url="${esc(a.url)}" data-using="${a.using}"${inList.length === 1 ? ' disabled title="Phải còn ít nhất một địa chỉ"' : adminOnly(LV.servers)}>Gỡ</button>
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
                <input id="sv-url" placeholder="https://ten.ddns.net:16222" autocomplete="off" spellcheck="false"${adminOnly(LV.servers)}>
                <small>Dạng https://&lt;IP hoặc tên miền&gt;:&lt;cổng&gt;. IP WAN / tên miền dùng cổng chuyển tiếp trên router về 6222.</small></label>
              <label class="field">Ghi chú
                <input id="sv-note" maxlength="80" placeholder="Địa điểm mới - mạng VNPT"${adminOnly(LV.servers)}></label>
            </div>
            <div class="form-actions">
              <label class="check"><input type="checkbox" id="sv-first"${adminOnly(LV.servers)}> Đặt lên đầu</label>
              <button class="btn btn-primary btn-sm" id="sv-add"${adminOnly(LV.servers)}>Thêm địa chỉ</button>
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
        const signAttr = j.signing ? adminOnly(LV.rules) : ' disabled title="Server chưa có khóa phát hành"';
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
            <button class="btn btn-sm btn-danger" data-act="remove" data-file="${esc(e.file)}" data-name="${esc(e.name)}" data-version="${esc(e.version)}" data-bundle="${e.bundle ? 1 : 0}"${adminOnly(LV.rules)}>Xóa</button>
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
                <input type="file" id="ru-file" accept=".yaml,.yml"${adminOnly(LV.rules)}>
                <small>Server kiểm cú pháp, giới hạn và chạy tests của từng rule. Hợp lệ thì vào nhóm "Chưa ký".
                  Bộ đã ký mà muốn sửa: tăng <span class="mono">version</span> rồi thêm lại.</small></label>
            </div>
            <div class="form-actions"><button class="btn btn-primary btn-sm" id="ru-add"${adminOnly(LV.rules)}>Kiểm tra và thêm</button></div>
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
        <td><button class="btn btn-sm btn-danger" data-unassign="${esc(a.scope)}" data-key="${esc(a.key)}"${adminOnly(LV.rules)}>Bỏ gán</button></td></tr>`).join('');
      return `<div class="card">
        <div class="card-head"><h2>Áp dụng bộ rule cho máy</h2><span class="sub">Máy &gt; siêu thị &gt; tất cả máy. Agent nhận trong vòng 10 phút</span></div>
        <div class="table-wrap"><table><thead><tr><th>Cấp</th><th>Bộ rule</th><th class="hide-sm">Gán lúc · bởi</th><th></th></tr></thead>
          <tbody>${rows || '<tr><td colspan="4" class="empty">Chưa gán bộ nào: agent giữ bộ đang cài (nếu có).</td></tr>'}</tbody></table></div>
        <div class="card-body">
          ${signed.length ? `<div class="form-grid">
            <label class="field">Bộ rule (đã ký)<select id="as-set"${adminOnly(LV.rules)}>${signed.map((k) => `<option value="${esc(k)}">${esc(k)}</option>`).join('')}</select></label>
            <label class="field">Áp dụng cho<select id="as-scope"${adminOnly(LV.rules)}>
              <option value="global">Tất cả máy</option><option value="store">Một siêu thị</option><option value="device">Một máy</option></select></label>
            <label class="field" id="as-store-f" hidden>Mã siêu thị<input id="as-store" inputmode="numeric" placeholder="12345"${adminOnly(LV.rules)}></label>
            <label class="field" id="as-dev-f" hidden>Máy<select id="as-dev"${adminOnly(LV.rules)}>${(state.devices || []).map((d) => `<option value="${esc(d.id)}">${esc(d.hostname)}</option>`).join('')}</select></label>
          </div>
          <div class="form-actions"><button class="btn btn-primary btn-sm" id="as-save"${adminOnly(LV.rules)}>Áp dụng</button></div>`
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
    'bao-cao': pageReport,
    'tai-khoan': pageAccounts,
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
  const USER_KEY = 'scs-login-user';
  let emailOn = false; // server có gửi mail không (GET /auth/options)
  let loginTab = 'ssh';

  function loginMsg(err, ok) {
    const e = $('#login-error'), o = $('#login-ok');
    e.hidden = !err; e.textContent = err || '';
    o.hidden = !ok; o.textContent = ok || '';
  }

  function setLoginTab(tab) {
    if (!emailOn) tab = 'ssh';
    loginTab = tab;
    $$('[data-login-tab]').forEach((b) => b.setAttribute('aria-selected', String(b.dataset.loginTab === tab)));
    $$('[data-pane]').forEach((f) => { f.hidden = f.dataset.pane !== tab; });
    const focus = { email: '#email-user', register: '#reg-user', ssh: '#login-code' }[tab];
    $(focus).focus();
  }

  function resetEmailForms() {
    $('#email-code-f').hidden = true; $('#email-code').value = '';
    $('#email-btn').textContent = 'Gửi mã về email'; $('#email-resend').hidden = true;
    $('#email-user').readOnly = false;
    $('#reg-code-f').hidden = true; $('#reg-code').value = '';
    $('#reg-btn').textContent = 'Gửi mã xác minh';
    $('#reg-user').readOnly = false; $('#reg-email').readOnly = false;
  }

  function showLogin(message) {
    stopLive();
    $('#app').hidden = true;
    $('#login').hidden = false;
    loginMsg(message, '');
    $('#login-code').value = '';
    const name = $('#login-name');
    if (!name.value) name.value = local.get(NAME_KEY) || '';
    const user = $('#email-user');
    if (!user.value) user.value = local.get(USER_KEY) || '';
    resetEmailForms();
    setLoginTab(loginTab);
  }

  async function loadLoginOptions() {
    try {
      const j = await (await api('/auth/options')).json();
      emailOn = !!j.email;
      $('#reg-domains').textContent = j.domains && j.domains.length ? `Chỉ nhận email @${j.domains.join(', @')}.` : '';
    } catch { emailOn = false; }
    $('#login-tabs').hidden = !emailOn;
    if (emailOn && !local.get(NAME_KEY)) loginTab = 'email';
    if (emailOn && local.get(USER_KEY)) loginTab = 'email';
  }

  // POST JSON cho trang đăng nhập: trả { ok, j } với câu báo lỗi của server.
  async function loginPost(path, body) {
    const r = await api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    let j = {};
    try { j = await r.json(); } catch { /* bỏ qua */ }
    const cap = (t) => t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?)]$/.test(t) ? '' : '.');
    return { ok: r.ok, j, err: r.ok ? '' : cap(j.error || `Server lỗi (${r.status})`) };
  }

  $$('[data-login-tab]').forEach((b) => b.addEventListener('click', () => { loginMsg('', ''); setLoginTab(b.dataset.loginTab); }));

  $('#email-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('#email-btn');
    const username = $('#email-user').value.trim().toLowerCase();
    if (!username) return;
    btn.disabled = true;
    try {
      if ($('#email-code-f').hidden) {
        const r = await loginPost('/auth/email/code', { username });
        if (!r.ok) { loginMsg(r.err, ''); return; }
        local.set(USER_KEY, username);
        loginMsg('', r.j.message);
        $('#email-user').readOnly = true;
        $('#email-code-f').hidden = false; $('#email-resend').hidden = false;
        btn.textContent = 'Đăng nhập';
        $('#email-code').focus();
        return;
      }
      const code = $('#email-code').value.replace(/\D/g, '');
      if (code.length !== 6) { loginMsg('Mã gồm 6 chữ số.', ''); return; }
      const r = await loginPost('/auth/email/login', { username, code });
      if (!r.ok) { resetEmailForms(); loginMsg(r.err, ''); return; }
      showApp();
    } catch {
      loginMsg('Không kết nối được server.', '');
    } finally {
      btn.disabled = false;
    }
  });
  $('#email-resend').addEventListener('click', () => { resetEmailForms(); loginMsg('', ''); $('#email-btn').click(); });

  $('#reg-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('#reg-btn');
    const username = $('#reg-user').value.trim().toLowerCase();
    const email = $('#reg-email').value.trim().toLowerCase();
    if (!username || !email) return;
    btn.disabled = true;
    try {
      if ($('#reg-code-f').hidden) {
        const r = await loginPost('/auth/register', { username, email });
        if (!r.ok) { loginMsg(r.err, ''); return; }
        loginMsg('', r.j.message);
        $('#reg-user').readOnly = true; $('#reg-email').readOnly = true;
        $('#reg-code-f').hidden = false;
        btn.textContent = 'Xác minh';
        $('#reg-code').focus();
        return;
      }
      const code = $('#reg-code').value.replace(/\D/g, '');
      if (code.length !== 6) { loginMsg('Mã gồm 6 chữ số.', ''); return; }
      const r = await loginPost('/auth/register/verify', { username, code });
      resetEmailForms();
      if (!r.ok) { loginMsg(r.err, ''); return; }
      local.set(USER_KEY, username);
      $('#email-user').value = username;
      $('#reg-form').reset();
      loginMsg('', r.j.message);
    } catch {
      loginMsg('Không kết nối được server.', '');
    } finally {
      btn.disabled = false;
    }
  });

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
    $('#me-role').textContent = me.level_name || LEVEL_NAMES[myLevel()];
    $('#me-role').title = `Mức quyền ${myLevel()}${me.account ? ' · tài khoản email' : ' · mã SSH'}`;
    $('#me-role').className = `pill no-dot ${isAdmin() ? 'pill-ok' : myLevel() === LV.view ? 'pill-info' : 'pill-warn'}`;
    refreshAccountCount();
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

  loadLoginOptions().then(fetchMe).then((m) => { if (m) showApp(); else showLogin(''); });
})();
