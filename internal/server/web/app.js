/**
 * HomeGuard 桌面窗口：守护、流量、体检、分流脚本、设置五个页面，布局与配色对齐微信桌面版。
 * 状态由守护服务每 400 毫秒推送一次，页面只在内容变化时重绘。
 */
(function () {
  'use strict';

  var app = document.getElementById('app');
  var modalRoot = document.getElementById('modal-root');
  var S = { template: '', snap: null, events: [], config: null, version: '', dataDir: '', report: null, checkupRunning: false, lastSeq: 0 };

  /* ---------- 工具 ---------- */
  // 节点名等外部文字里的 emoji（房子、国旗等带绿色）显示时去掉，界面中不出现绿色
  var EMOJI = /[\u{1F000}-\u{1FAFF}\u{2600}-\u{27BF}\u{FE0F}\u{200D}\u{E0020}-\u{E007F}]/gu;
  function esc(s) {
    return String(s == null ? '' : s).replace(EMOJI, '').replace(/^\s+/, '').replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; });
  }
  function api(method, path, body) {
    return fetch(path, { method: method, headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined, credentials: 'same-origin' })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          if (!r.ok) { throw new Error(j.error || ('HTTP ' + r.status)); }
          return j;
        });
      });
  }
  var toastTimer = 0;
  function toast(msg) {
    var el = document.getElementById('toast');
    el.textContent = msg;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { el.classList.remove('show'); }, 1800);
  }
  function copyText(text) {
    var done = function () { toast('已复制'); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text); done(); });
    } else { fallbackCopy(text); done(); }
  }
  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); } catch (e) { /* 忽略 */ }
    ta.remove();
  }
  function fmtBytes(n) {
    if (!n) { return '0'; }
    var u = ['B', 'KB', 'MB', 'GB'], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(1) : n) + ' ' + u[i];
  }
  function fmtAge(iso) {
    var s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (s < 60) { return Math.floor(s) + ' 秒'; }
    if (s < 3600) { return Math.floor(s / 60) + ' 分'; }
    return Math.floor(s / 3600) + ' 时';
  }
  function fmtTime(iso) {
    var d = new Date(iso), p = function (n) { return (n < 10 ? '0' : '') + n; };
    var today = new Date().toDateString() === d.toDateString();
    return (today ? '' : (d.getMonth() + 1) + '/' + d.getDate() + ' ') + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
  }
  /** setHTML：内容不变时不重绘，保留滚动位置与悬停状态 */
  function setHTML(el, html) {
    if (el && el.__html !== html) { el.innerHTML = html; el.__html = html; }
  }

  /* ---------- 程序头像 ---------- */
  var AV = {
    'claude': { bg: '#D97757', t: 'C' },
    'claude-code': { bg: '#3A3A3A', t: '&gt;_' },
    'codex': { bg: '#111111', t: 'Cx' },
    'chatgpt': { bg: '#3A3A3C', t: 'G' }
  };
  function avatar(id, idle, small) {
    var a = AV[id] || { bg: '#8A94A6', t: icon('globe', 18) };
    return '<div class="pd-avatar cc-av' + (idle ? ' idle' : '') + (small ? ' pd-avatar-s' : '') + '" style="background:' + a.bg + '">' + a.t + '</div>';
  }
  function appState(a) {
    if (a.paused && a.paused.length) { return '已暂停 ' + a.paused.length + ' 个进程'; }
    if (!a.procs || !a.procs.length) { return '未运行'; }
    return '运行中' + (a.conns ? ' · ' + a.conns + ' 个连接' : '');
  }

  /* ---------- 路由：#guard/overview、#traffic/all、#check/all、#settings/guard ---------- */
  function route() {
    var h = location.hash.replace(/^#/, '').split('/');
    var page = ['guard', 'traffic', 'check', 'script', 'settings'].indexOf(h[0]) >= 0 ? h[0] : 'guard';
    var def = { guard: 'overview', traffic: 'all', check: 'all', script: '', settings: 'guard' }[page];
    return { page: page, sub: decodeURIComponent(h[1] || def) };
  }

  /* ---------- 框架 ---------- */
  function shell() {
    if (document.getElementById('rail')) { return; }
    app.innerHTML = '<div class="pd-shell"><nav class="pd-rail" id="rail"></nav><aside class="pd-list" id="list"></aside><main class="pd-main" id="main"></main></div>';
  }

  function renderRail() {
    var r = route(), s = S.snap;
    var bad = s && s.enabled && !s.safe;
    var btn = function (page, ic, label, dot) {
      return '<button class="pd-rail-btn' + (r.page === page ? ' active' : '') + '" data-go="' + page + '" title="' + label + '" aria-label="' + label + '">' + icon(ic, 22) + (dot ? '<i class="pd-dot" style="position:absolute;top:6px;right:6px"></i>' : '') + '</button>';
    };
    setHTML(document.getElementById('rail'),
      '<div class="pd-me" style="background:' + (bad ? 'var(--cc-bad)' : 'var(--pd-accent)') + '" title="HomeGuard">' + icon(bad ? 'shield-alert' : 'shield-check', 20) + '</div>' +
      btn('guard', 'shield', '守护', bad) + btn('traffic', 'arrow-up-down', '流量', false) + btn('check', 'activity', '体检', false) +
      btn('script', 'file-text', '分流脚本', S.snap.script && S.snap.script !== 'active') +
      '<div class="pd-rail-gap"></div>' + btn('settings', 'menu', '设置', false));
  }

  function render() {
    if (!S.snap) { return; }
    shell();
    renderRail();
    var r = route();
    var list = document.getElementById('list');
    list.classList.toggle('narrow', r.page === 'settings');
    list.style.display = r.page === 'script' ? 'none' : '';
    if (r.page === 'guard') { setHTML(list, guardList(r)); setHTML(document.getElementById('main'), guardMain(r)); }
    else if (r.page === 'traffic') { setHTML(list, trafficList(r)); setHTML(document.getElementById('main'), trafficMain(r)); }
    else if (r.page === 'check') { setHTML(list, checkList(r)); setHTML(document.getElementById('main'), checkMain(r)); }
    else if (r.page === 'script') { scriptMain(); }
    else { setHTML(list, settingsList(r)); settingsMain(r); }
  }

  function listTop(title, extra) {
    return '<div class="pd-list-top"><div class="pd-list-title">' + title + '</div>' + (extra || '') + '</div>';
  }
  function row(go, active, av, title, sub, right) {
    return '<button class="pd-row' + (active ? ' active' : '') + '" data-go="' + go + '"><div class="pd-row-avatar">' + av + '</div>' +
      '<div class="pd-row-main"><div class="pd-row-line"><span class="pd-row-title">' + title + '</span>' + (right || '') + '</div><div class="pd-row-sub">' + sub + '</div></div></button>';
  }
  function head(title, sub, acts) {
    return '<header class="pd-head"><div class="pd-head-main"><div class="pd-head-title">' + title + '</div>' + (sub ? '<div class="pd-head-sub">' + sub + '</div>' : '') + '</div><div class="pd-head-acts">' + (acts || '') + '</div></header>';
  }
  function switchHtml(act, on, label) {
    return '<label class="pd-switch" title="' + label + '"><input type="checkbox" data-act="' + act + '"' + (on ? ' checked' : '') + ' aria-label="' + label + '"><span></span></label>';
  }

  /* ---------- 守护 ---------- */
  function stateText(s) {
    if (!s.enabled) { return { cls: 'off', title: '守护已关闭', ic: 'shield' }; }
    if (!s.safe) { return { cls: 'bad', title: '已拦截', ic: 'shield-alert' }; }
    return { cls: '', title: '线路安全 · 守护中', ic: 'shield-check' };
  }

  function guardList(r) {
    var s = S.snap, st = stateText(s);
    var out = listTop('守护') + '<div class="pd-list-body">';
    out += row('guard/overview', r.sub === 'overview', '<div class="pd-avatar" style="background:' + (st.cls === 'bad' ? 'var(--cc-bad)' : st.cls === 'off' ? 'var(--pd-text-4)' : 'var(--pd-accent)') + '">' + icon(st.ic, 22) + '</div>',
      '总览', esc(st.title));
    out += '<div class="pd-list-sec">受保护的程序</div>';
    (s.apps || []).forEach(function (a) {
      var paused = a.paused && a.paused.length;
      out += row('guard/' + a.id, r.sub === a.id, avatar(a.id, !a.procs || !a.procs.length), esc(a.name), esc(appState(a)), paused ? '<span class="pd-tag-auto">暂停</span>' : '');
    });
    return out + '</div>';
  }

  function checkRow(c) {
    var cls = c.ok ? 'cc-ok' : (c.critical ? 'cc-bad' : 'cc-warn');
    var ic = c.ok ? 'circle-check' : (c.critical ? 'circle-x' : 'alert-circle');
    return '<div class="cc-check"><span class="' + cls + '">' + icon(ic, 18) + '</span><div class="cc-check-main"><div class="cc-check-name">' + esc(c.name) +
      (c.critical ? '' : ' <span class="pd-tag" style="font-size:11px;line-height:16px">参考</span>') + '</div><div class="cc-check-detail">' + esc(c.detail) + '</div>' +
      (!c.ok && c.fix ? '<div class="cc-check-fix">' + esc(c.fix) + '</div>' : '') + '</div></div>';
  }

  function eventsHtml(limit, kinds) {
    var list = S.events.filter(function (e) { return !kinds || kinds.indexOf(e.kind) >= 0; }).slice(-limit).reverse();
    if (!list.length) { return '<div class="pd-card"><div class="pd-empty">还没有记录</div></div>'; }
    return '<div class="cc-events">' + list.map(function (e) {
      return '<div class="cc-event ' + esc(e.kind) + '"><time>' + fmtTime(e.at) + '</time><span>' + esc(e.text) + '</span></div>';
    }).join('') + '</div>';
  }

  function guardMain(r) {
    var s = S.snap;
    if (r.sub !== 'overview') { return appMain(r.sub); }
    var st = stateText(s), x = s.exit || {};
    var exitLine = x.ip ? '出口 ' + esc(x.ip) + ' · ' + esc(x.country || '') + ' ' + esc(x.city || '') : '出口还没核验';
    if (s.current) { exitLine += ' · ' + esc(s.current); }
    var nConns = (s.conns || []).length;
    var out = head('守护', s.version ? 'Clash ' + esc(s.version) + ' · 每 0.1 秒核对一次连接' : '每 0.1 秒核对一次连接', switchHtml('toggle-guard', s.enabled, '守护开关'));
    out += '<div class="pd-page"><div class="pd-page-inner">';
    out += '<div class="cc-hero ' + st.cls + '"><div class="cc-hero-icon">' + icon(st.ic, 28) + '</div><div class="cc-hero-main"><div class="cc-hero-title">' + st.title + '</div>' +
      (s.enabled && !s.safe ? '<div class="cc-hero-reason">' + esc(s.reason) + '</div>' : '') +
      '<div class="cc-hero-sub">' + (s.probing ? '正在核验出口…' : exitLine) + '</div>' +
      '<div class="cc-acts">' +
      (s.latch ? '<button class="pd-btn pd-btn-primary" data-act="clear">' + icon('check', 16) + '已处理，恢复放行</button>' : '') +
      '<button class="pd-btn" data-act="probe">' + icon('refresh-cw', 16) + '立即核验出口</button></div></div>' +
      '<div class="pd-stats pd-hide-s"><div><b>' + nConns + '</b><span>受保护连接</span></div><div><b>' + (s.closed || 0) + '</b><span>已断开</span></div><div><b>' + (s.blocks || 0) + '</b><span>拦截次数</span></div></div></div>';
    out += consistencyCard(s);
    if (!browserDone()) {
      out += '<div class="pd-card" style="margin-top:12px"><div class="pd-setting">' + icon('globe', 20) + '<div class="pd-setting-text"><div>浏览器侧还没检测</div><div class="pd-setting-desc">WebRTC、浏览器时区与语言只能在浏览器里测，首次使用检测一次</div></div>' +
        '<button class="pd-btn pd-btn-primary" data-act="browser-check">浏览器检测</button></div></div>';
    }
    if (s.enabled && !s.safe) {
      out += '<div class="pd-warn">' + icon('alert-triangle', 16) + '<div>受保护程序的全部连接已断开' + (s.pauseApps ? '，程序已暂停（不收发任何数据）' : '') + '。问题解决后自动恢复' + (s.latch ? '；因发现连接走错出口，需要你确认处理后点「恢复放行」' : '') + '。</div></div>';
    }
    out += '<div class="pd-h2">守护条件</div><div class="pd-card">' + (s.checks || []).map(checkRow).join('') + '</div>';
    out += '<div class="pd-h2">受保护的程序</div><div class="cc-apps">' + (s.apps || []).map(function (a) {
      return '<button class="cc-app" data-go="guard/' + a.id + '">' + avatar(a.id, !a.procs || !a.procs.length) + '<div style="min-width:0"><div class="cc-app-name">' + esc(a.name) + '</div><div class="cc-app-sub">' + esc(appState(a)) + '</div></div></button>';
    }).join('') + '</div>';
    out += '<div class="pd-h2">最近记录</div>' + eventsHtml(30);
    return out + '</div></div>';
  }

  /** browserDone：做过浏览器检测（没做过时服务端返回零值时间） */
  function browserDone() { return !!S.browserAt && new Date(S.browserAt).getFullYear() > 2000; }

  /** consistencyCard：系统时区、出口时区、三个视角的出口 IP 放一起对比 */
  function consistencyCard(s) {
    var v = s.views || {}, x = s.exit || {};
    var views = [['国内视角 IP', v.cn], ['国外视角 IP', v.intl], ['谷歌侧视角 IP', v.gfw]];
    var ref = x.ip || v.intl || v.gfw || v.cn || '';
    var tzOK = s.tzMatch === 'same' || s.tzMatch === 'offset';
    var cells = [
      ['系统时区', v.sysTZ, !s.tzMatch ? '' : tzOK],
      ['出口时区', x.timezone, !s.tzMatch ? '' : tzOK]
    ].concat(views.map(function (i) { return [i[0], i[1], i[1] ? i[1] === ref : false]; }));
    var measured = !!v.at;
    var allOK = measured && cells.every(function (c) { return c[2] === true; });
    var tag = !measured ? '<span class="pd-tag">核验中</span>' : allOK ? '<span class="pd-tag pd-tag-ok">一致</span>' : '<span class="pd-tag cc-tag-bad">不一致</span>';
    var note = s.tzMatch === 'offset' ? ' · 时区名不同但时差相同' : '';
    return '<div class="pd-h2">出口一致性 ' + tag + '<span style="font-weight:400"> · ' + (measured ? '核验于 ' + fmtTime(v.at) : '随出口核验一起刷新') + note + '</span></div>' +
      '<div class="pd-card cc-cmp">' + cells.map(function (c) {
        var st = !measured || c[2] === '' ? '' : c[2] ? '<span class="cc-ok">' + icon('circle-check', 14) + '</span>' : '<span class="cc-bad">' + icon('circle-x', 14) + '</span>';
        return '<div class="cc-cmp-cell"><div class="cc-cmp-label">' + st + c[0] + '</div><div class="cc-cmp-value pd-mono" title="' + esc(c[1] || '') + '">' + esc(c[1] || (measured ? '测不到' : '—')) + '</div></div>';
      }).join('') + '</div>';
  }

  function connTable(conns, withApp) {
    if (!conns.length) { return '<div class="pd-card"><div class="pd-empty">当前没有连接</div></div>'; }
    return '<div class="pd-card"><table class="pd-table cc-table"><tr>' + (withApp ? '<th>程序</th>' : '') + '<th>目标</th><th>出口</th><th class="cc-rule">规则</th><th>流量</th><th>时长</th><th></th></tr>' +
      conns.map(function (c) {
        var ex = c.residential ? '<span class="pd-tag pd-tag-ok" title="' + esc(c.exit) + '">' + esc(c.exit) + '</span>' : '<span class="pd-tag cc-tag-bad" title="' + esc(c.exit || 'DIRECT') + '">' + esc(c.exit || 'DIRECT') + '</span>';
        return '<tr>' + (withApp ? '<td class="pd-nowrap">' + avatarInline(c) + '</td>' : '') + '<td class="cc-target">' + esc(c.target) + '</td><td>' + ex + '</td><td class="cc-rule pd-path">' + esc(c.rule) + '</td>' +
          '<td class="pd-nowrap pd-muted">↑' + fmtBytes(c.up) + ' ↓' + fmtBytes(c.down) + '</td><td class="pd-nowrap pd-muted">' + fmtAge(c.start) + '</td>' +
          '<td><button class="pd-link pd-link-danger" data-act="close-conn" data-id="' + esc(c.id) + '">断开</button></td></tr>';
      }).join('') + '</table></div>';
  }
  function avatarInline(c) {
    return '<span style="display:inline-flex;align-items:center;gap:8px">' + '<span style="transform:scale(.7);margin:-6px">' + avatar(c.app || '', false, true) + '</span>' + esc(c.appName || c.process || '未知') + '</span>';
  }

  function appMain(id) {
    var s = S.snap, a = (s.apps || []).filter(function (x) { return x.id === id; })[0];
    if (!a) { return '<div class="pd-empty-main">没有这个程序</div>'; }
    var conns = (s.conns || []).filter(function (c) { return c.app === id; });
    var out = head(esc(a.name), esc(appState(a)));
    out += '<div class="pd-page"><div class="pd-page-inner">';
    out += '<div class="pd-h2">进程</div><div class="pd-card">';
    out += (a.procs && a.procs.length) ? a.procs.map(function (p) {
      var paused = (a.paused || []).indexOf(p.pid) >= 0;
      return '<div class="pd-setting"><div class="pd-setting-text"><div class="pd-mono" style="font-size:13px">PID ' + p.pid + (paused ? ' <span class="pd-tag cc-tag-warn">已暂停</span>' : '') + '</div><div class="pd-setting-desc pd-path">' + esc(p.path) + '</div></div></div>';
    }).join('') : '<div class="pd-empty">没有运行</div>';
    out += '</div><div class="pd-h2"><span class="cc-live"></span>实时连接 · ' + conns.length + '</div>' + connTable(conns, false);
    return out + '</div></div>';
  }

  /* ---------- 流量 ---------- */
  function trafficFilters() {
    var s = S.snap, conns = s.conns || [];
    var f = [{ id: 'all', name: '全部连接', n: conns.length, av: '<div class="pd-avatar" style="background:var(--pd-tile-blue)">' + icon('arrow-up-down', 20) + '</div>' }];
    (s.apps || []).forEach(function (a) { f.push({ id: a.id, name: a.name, n: a.conns, av: avatar(a.id, !a.conns) }); });
    var other = conns.filter(function (c) { return !c.app; }).length;
    f.push({ id: 'other', name: '其他程序', n: other, av: avatar('', !other), sub: '访问受保护网站的浏览器等' });
    f.push({ id: 'log', name: '拦截记录', n: -1, av: '<div class="pd-avatar" style="background:var(--cc-bad)">' + icon('shield-alert', 20) + '</div>', sub: '断开、拦截与恢复' });
    return f;
  }
  function trafficList(r) {
    return listTop('流量') + '<div class="pd-list-body">' + trafficFilters().map(function (f) {
      return row('traffic/' + f.id, r.sub === f.id, f.av, esc(f.name), f.sub || (f.n + ' 个连接'));
    }).join('') + '</div>';
  }
  function trafficMain(r) {
    var conns = S.snap.conns || [];
    if (r.sub === 'log') {
      return head('拦截记录', '断开、拦截与恢复') + '<div class="pd-page"><div class="pd-page-inner">' + eventsHtml(300, ['block', 'close', 'resume', 'warn']) + '</div></div>';
    }
    var f = trafficFilters().filter(function (x) { return x.id === r.sub; })[0] || trafficFilters()[0];
    var list = conns.filter(function (c) { return r.sub === 'all' || (r.sub === 'other' ? !c.app : c.app === r.sub); });
    var up = 0, down = 0;
    list.forEach(function (c) { up += c.up; down += c.down; });
    return head(esc(f.name), list.length + ' 个连接 · ↑' + fmtBytes(up) + ' ↓' + fmtBytes(down)) +
      '<div class="pd-page"><div class="pd-page-inner"><div class="pd-h2"><span class="cc-live"></span>实时 · 蓝色为住宅出口，红色会被立即断开</div>' + connTable(list, true) + '</div></div>';
  }

  /* ---------- 体检 ---------- */
  var CATS = ['守护', '出口', 'DNS', '本机', '浏览器', '稳定'];
  function catScore(items) {
    var sum = 0, w = 0;
    items.forEach(function (i) { sum += i.weight * i.score; w += i.weight; });
    return w ? Math.round(sum / w) : 0;
  }
  function bar(score) {
    var cls = score >= 90 ? '' : score >= 60 ? ' mid' : ' low';
    return '<span class="cc-bar' + cls + '"><i style="width:' + score + '%"></i></span>';
  }
  function checkList(r) {
    var rep = S.report;
    var out = listTop('体检') + '<div class="pd-list-body">';
    out += row('check/all', r.sub === 'all', '<div class="pd-avatar" style="background:var(--pd-accent)">' + icon('activity', 20) + '</div>', '全部', rep ? '总分 ' + rep.score : (S.checkupRunning ? '体检中…' : '还没体检'));
    if (rep) {
      CATS.forEach(function (c) {
        var items = rep.items.filter(function (i) { return i.cat === c; });
        if (!items.length) { return; }
        var sc = catScore(items), bad = items.filter(function (i) { return i.score < 60; }).length;
        out += row('check/' + c, r.sub === c, '<div class="pd-avatar" style="background:' + (sc >= 90 ? 'var(--pd-accent)' : sc >= 60 ? 'var(--cc-warn)' : 'var(--cc-bad)') + '">' + sc + '</div>', c, items.length + ' 项' + (bad ? ' · ' + bad + ' 项有问题' : ''));
      });
    }
    return out + '</div>';
  }
  function checkMain(r) {
    var rep = S.report, running = S.checkupRunning;
    var bbtn = '<button class="pd-btn" data-act="browser-check" title="在默认浏览器里检测 WebRTC、时区、语言等">' + icon('globe', 16) + '浏览器检测</button>';
    var btn = bbtn + '<button class="pd-btn pd-btn-primary" data-act="checkup"' + (running ? ' disabled' : '') + '>' + (running ? '<span class="cc-spin">' + icon('loader', 16) + '</span>体检中' : icon('refresh-cw', 16) + '重新体检') + '</button>';
    var out = head('体检', rep ? '上次 ' + fmtTime(rep.at) + ' · 仅供参考，放行由守护实时判断' : '仅供参考，放行由守护实时判断', btn);
    out += '<div class="pd-page"><div class="pd-page-inner">';
    if (!rep) { return out + '<div class="pd-card"><div class="pd-empty">' + (running ? '正在体检，大约需要 10 秒' : '点右上角「重新体检」开始') + '</div></div></div></div>'; }
    var color = rep.score >= 90 ? 'var(--pd-accent)' : rep.score >= 60 ? 'var(--cc-warn)' : 'var(--cc-bad)';
    var bad = rep.items.filter(function (i) { return i.score < 60; });
    out += '<div class="cc-hero' + (rep.score < 60 ? ' bad' : '') + '"><div class="cc-ring" style="background:conic-gradient(' + color + ' ' + rep.score * 3.6 + 'deg, var(--pd-input) 0)"><b>' + rep.score + '</b></div>' +
      '<div class="cc-hero-main"><div class="cc-hero-title">' + (rep.score >= 90 ? '环境良好' : rep.score >= 60 ? '有待改进' : '风险较高') + '</div><div class="cc-hero-sub">' + (bad.length ? bad.length + ' 项需要处理' : '没有明显问题') + '</div></div></div>';
    CATS.forEach(function (c) {
      if (r.sub !== 'all' && r.sub !== c) { return; }
      var items = rep.items.filter(function (i) { return i.cat === c; });
      if (!items.length) { return; }
      out += '<div class="pd-h2">' + c + ' · ' + catScore(items) + ' 分</div><div class="pd-card">' + items.map(function (i) {
        return '<div class="cc-check"><div class="cc-check-main"><div class="cc-check-name">' + esc(i.name) + '</div><div class="cc-check-detail">' + esc(i.value) + '</div>' +
          (i.score < 100 && i.tip ? '<div class="cc-check-fix">' + esc(i.tip) + '</div>' : '') + '</div>' + bar(i.score) + '<span class="cc-score">' + i.score + '</span></div>';
      }).join('') + '</div>';
    });
    return out + '</div></div>';
  }

  /* ---------- 分流脚本：只提供复制，不在页面上展示脚本 ---------- */
  function scriptMain() {
    var st = S.snap.script, ok = st === 'active';
    var sub = ok ? 'Clash 正在使用' : st === 'stale' ? '脚本已更新，Clash 还没刷新：去订阅页点一下刷新' : st === 'overridden' ? 'Clash 用的不是这份脚本：检查订阅自己的扩展脚本是否为空，再刷新订阅' : '还没用上';
    setHTML(document.getElementById('main'), head('分流脚本') + '<div class="pd-page"><div class="pd-page-inner" style="max-width:560px">' +
      '<div class="cc-hero' + (ok ? '' : ' bad') + '"><div class="cc-hero-icon">' + icon(ok ? 'circle-check' : 'alert-triangle', 28) + '</div>' +
      '<div class="cc-hero-main"><div class="cc-hero-title">' + (ok ? '已生效' : '未生效') + '</div><div class="cc-hero-sub">' + sub + '</div></div>' +
      '<button class="pd-btn pd-btn-primary" style="height:36px;padding:0 18px" data-act="copy-script">' + icon('copy', 16) + '复制脚本</button></div>' +
      '<div class="cc-steps"><div>1　粘贴到 Clash Verge 的「全局扩展脚本」，改开头的住宅代理</div><div>2　订阅自己的「扩展脚本」保持为空</div><div>3　刷新订阅</div></div>' +
      '</div></div>');
  }

  /* ---------- 设置 ---------- */
  var SECTIONS = [['guard', '守护', 'shield'], ['apps', '受保护程序', 'bot'], ['clash', 'Clash', 'wifi'], ['about', '关于', 'info']];
  function settingsList(r) {
    return listTop('设置') + '<div class="pd-list-body">' + SECTIONS.map(function (x) {
      return '<button class="pd-row' + (r.sub === x[0] ? ' active' : '') + '" style="height:48px" data-go="settings/' + x[0] + '"><span class="pd-row-title">' + x[1] + '</span></button>';
    }).join('') + '</div>';
  }

  var settingsKey = '';
  /** settingsMain：设置页含输入框，只在切换分区时重绘，守护开关等单独更新 */
  function settingsMain(r) {
    var main = document.getElementById('main');
    var c = S.config;
    if (settingsKey === r.sub && main.__html) {
      var g = main.querySelector('[data-act="toggle-guard"]');
      if (g) { g.checked = S.snap.enabled; }
      var p = main.querySelector('[data-act="toggle-pause"]');
      if (p) { p.checked = S.snap.pauseApps; }
      return;
    }
    settingsKey = r.sub;
    var title = (SECTIONS.filter(function (x) { return x[0] === r.sub; })[0] || SECTIONS[0])[1];
    var body = '';
    if (r.sub === 'guard') {
      body = '<div class="pd-card">' +
        setting('守护开关', '关闭后只显示状态，不断开连接、不暂停程序', switchHtml('toggle-guard', S.snap.enabled, '守护开关')) +
        setting('拦截时暂停程序', '线路不安全时暂停 Claude、Codex、ChatGPT 等程序（不收发任何数据），恢复后自动继续；关闭后只断开连接', switchHtml('toggle-pause', S.snap.pauseApps, '暂停程序')) +
        setting('出口核验间隔', '切换住宅代理时会立即核验', '<select class="pd-input" data-act="probe-interval">' + [30, 60, 120, 300].map(function (n) {
          return '<option value="' + n + '"' + (c.probeInterval === n ? ' selected' : '') + '>' + (n < 60 ? n + ' 秒' : n / 60 + ' 分钟') + '</option>';
        }).join('') + '</select>') + '</div>' +
        '<div class="pd-h2">工作方式</div><div class="pd-card"><div class="pd-setting"><div class="pd-setting-text" style="line-height:1.8;font-size:13px">' +
        '1、Clash 分流规则让受保护的程序与网站只能从住宅出口出去（零延迟）<br>' +
        '2、本应用每 0.1 秒核对全部连接，路由一变化立即检查：TUN、IPv6、住宅出口选择、出口国家<br>' +
        '3、任何一项不满足：立即断开全部受保护的连接，暂停受保护的程序，恢复后自动继续<br>' +
        '4、发现有连接没走住宅出口：断开并锁定，确认处理后手动恢复</div></div></div>';
    } else if (r.sub === 'apps') {
      body = '<div class="pd-card">' + c.apps.map(function (a, i) {
        return '<div class="pd-setting" style="align-items:flex-start">' + avatar(a.id, false, true) + '<div class="pd-setting-text"><div>' + esc(a.name) + '</div>' +
          '<div class="pd-setting-desc">程序路径规则（正则，每行一个）</div><textarea class="pd-input cc-textarea" data-app="' + i + '">' + esc(a.patterns.join('\n')) + '</textarea></div></div>';
      }).join('') + '</div>' +
        '<div class="pd-h2">受保护的网站</div><div class="pd-card"><div class="pd-setting"><div class="pd-setting-text"><div class="pd-setting-desc">任何程序访问这些网站（含子域名），都必须走住宅出口，否则立即断开；每行一个</div>' +
        '<textarea class="pd-input cc-textarea" id="domains" style="min-height:150px">' + esc(c.domains.join('\n')) + '</textarea></div></div></div>' +
        '<div class="pd-savebar"><button class="pd-btn" data-act="reset-apps">恢复默认程序</button><button class="pd-btn pd-btn-primary" data-act="save-apps">保存</button></div>';
    } else if (r.sub === 'clash') {
      body = '<div class="pd-card">' + setting('当前使用', esc(S.snap.controller || '未连接') + (S.snap.controllerSource ? ' · ' + esc(S.snap.controllerSource) : ''), '') + '</div>' +
        '<div class="pd-h2">手动指定 · 下面的地址连不上时，会自动查找 Clash Verge 配置、运行中的内核和常见位置</div>' +
        '<div class="pd-card"><div class="pd-form-grid"><div class="pd-field"><label>控制接口</label><input class="pd-input pd-mono" id="controller" value="' + esc(c.controller) + '"></div>' +
        '<div class="pd-field"><label>密钥（没有可留空）</label><input class="pd-input" id="secret" type="password" value="' + esc(c.secret) + '"></div>' +
        '<div class="pd-field"><label>住宅出口分组名</label><input class="pd-input" id="group" value="' + esc(c.residentialGroup) + '"></div></div>' +
        '<div class="pd-setting"><div class="pd-setting-text pd-setting-desc">Clash Verge 默认是 unix:/tmp/verge/verge-mihomo.sock；也可以填 http://127.0.0.1:9097 这类地址。住宅出口分组里的单个代理都算住宅代理（类型不限），分组、直连不算。</div></div></div>' +
        '<div class="pd-savebar"><button class="pd-btn" data-act="test-clash">测试连接</button><button class="pd-btn pd-btn-primary" data-act="save-clash">保存</button></div>';
    } else {
      body = '<div class="pd-card"><dl class="pd-kv"><dt>版本</dt><dd>' + esc(S.version) + '</dd><dt>Clash</dt><dd>' + esc(S.snap.version || '未连接') + '</dd><dt>数据目录</dt><dd class="pd-path">' + esc(S.dataDir) + '</dd></dl></div>' +
        '<div class="pd-h2">退出</div><div class="pd-card">' + setting('退出守护', '关闭窗口后守护仍在后台运行；退出后不再保护，并继续被暂停的程序', '<button class="pd-btn pd-btn-danger" data-act="quit">' + icon('power', 16) + '退出守护</button>') + '</div>';
    }
    setHTML(main, head(title) + '<div class="pd-page"><div class="pd-page-inner">' + body + '</div></div>');
  }
  function setting(name, desc, ctl) {
    return '<div class="pd-setting"><div class="pd-setting-text"><div>' + name + '</div><div class="pd-setting-desc">' + desc + '</div></div>' + ctl + '</div>';
  }

  /* ---------- 弹窗 ---------- */
  function confirmBox(title, text, okLabel, onOk) {
    modalRoot.innerHTML = '<div class="pd-scrim"><div class="pd-dialog" role="dialog" aria-modal="true"><div class="pd-dialog-head"><div class="pd-dialog-title">' + esc(title) + '</div></div>' +
      '<div class="pd-modal-body"><div>' + text + '</div></div><div class="pd-dialog-foot"><button class="pd-btn" data-act="modal-close">取消</button><button class="pd-btn pd-btn-danger" id="confirm-ok">' + esc(okLabel) + '</button></div></div></div>';
    document.getElementById('confirm-ok').onclick = function () { modalRoot.innerHTML = ''; onOk(); };
  }

  /* ---------- 事件 ---------- */
  function refreshConfig() {
    return api('GET', '/api/config').then(function (c) { S.config = c; settingsKey = ''; render(); });
  }

  document.addEventListener('click', function (e) {
    var go = e.target.closest('[data-go]');
    if (go) { location.hash = go.getAttribute('data-go'); return; }
    var a = e.target.closest('[data-act]');
    if (!a || a.tagName === 'INPUT' || a.tagName === 'SELECT') { return; }
    var act = a.getAttribute('data-act');
    switch (act) {
      case 'clear': api('POST', '/api/clear').then(function () { toast('已解除，正在重新检查'); }); break;
      case 'probe': api('POST', '/api/probe').then(function () { toast('正在核验出口'); }); break;
      case 'close-conn': api('POST', '/api/conn/close', { id: a.getAttribute('data-id') }).then(function () { toast('已断开'); }).catch(function (er) { toast(er.message); }); break;
      case 'checkup': S.checkupRunning = true; render(); api('POST', '/api/checkup'); break;
      case 'browser-check':
        api('POST', '/api/browser/start').then(function () { toast('已在默认浏览器打开检测页，完成后自动重新体检'); }).catch(function (er) { toast(er.message); });
        break;
      case 'copy': copyText(a.getAttribute('data-text')); break;
      case 'modal-close': modalRoot.innerHTML = ''; break;
      case 'copy-script':
        (S.template ? Promise.resolve() : api('GET', '/api/clash-script').then(function (j) { S.template = j.script; }))
          .then(function () { copyText(S.template); }).catch(function (er) { toast(er.message); });
        break;
      case 'test-clash':
        api('POST', '/api/clash/test', { controller: val('controller'), secret: val('secret') }).then(function (j) { toast('连接成功 · ' + j.version); }).catch(function (er) { toast(er.message); });
        break;
      case 'save-clash':
        api('POST', '/api/config', { controller: val('controller'), secret: val('secret'), residentialGroup: val('group') }).then(function () { toast('已保存'); refreshConfig(); }).catch(function (er) { toast(er.message); });
        break;
      case 'save-apps':
        var apps = S.config.apps.map(function (x, i) {
          var ta = document.querySelector('[data-app="' + i + '"]');
          return { id: x.id, name: x.name, patterns: ta.value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean) };
        });
        api('POST', '/api/config', { apps: apps, domains: val('domains').split('\n') }).then(function () { toast('已保存'); refreshConfig(); }).catch(function (er) { toast(er.message); });
        break;
      case 'reset-apps':
        api('POST', '/api/config', { resetApps: true }).then(function () { toast('已恢复默认'); refreshConfig(); });
        break;
      case 'quit':
        confirmBox('退出守护', '退出后 Claude、Codex、ChatGPT 不再受保护，被暂停的程序会继续运行。', '退出', function () {
          api('POST', '/api/quit').then(function () { app.innerHTML = '<div class="pd-center pd-muted">守护已退出，可以关闭窗口</div>'; });
        });
        break;
    }
  });
  function val(id) { var el = document.getElementById(id); return el ? el.value : ''; }

  document.addEventListener('change', function (e) {
    var act = e.target.getAttribute && e.target.getAttribute('data-act');
    if (act === 'toggle-guard') {
      var on = e.target.checked;
      if (!on) {
        e.target.checked = true;
        confirmBox('关闭守护', '关闭后流量走错出口也不会被拦截。确定关闭？', '关闭守护', function () { api('POST', '/api/guard', { enabled: false }).then(function () { toast('守护已关闭'); }); });
      } else {
        api('POST', '/api/guard', { enabled: true }).then(function () { toast('守护已开启'); });
      }
    } else if (act === 'toggle-pause') {
      api('POST', '/api/guard', { pauseApps: e.target.checked });
    } else if (act === 'probe-interval') {
      api('POST', '/api/config', { probeInterval: +e.target.value }).then(function (c) { S.config = c; toast('已保存'); });
    }
  });

  window.addEventListener('hashchange', function () { settingsKey = ''; render(); });

  /* ---------- 状态推送 ---------- */
  function connect() {
    var es = new EventSource('/api/stream?since=' + S.lastSeq);
    es.onmessage = function (m) {
      var p = JSON.parse(m.data);
      var wasRunning = S.checkupRunning;
      S.snap = p.snap;
      S.checkupRunning = p.checkupRunning;
      S.browserAt = p.browserAt;
      (p.events || []).forEach(function (ev) { if (ev.seq > S.lastSeq) { S.events.push(ev); S.lastSeq = ev.seq; } });
      if (S.events.length > 500) { S.events = S.events.slice(-500); }
      if (wasRunning && !p.checkupRunning) {
        api('GET', '/api/checkup').then(function (j) { S.report = j.report; render(); });
      }
      render();
    };
    es.onerror = function () {
      es.close();
      if (S.snap) { S.snap = Object.assign({}, S.snap, { enabled: true, safe: false, reason: '与守护服务的连接断开，正在重连' }); render(); }
      setTimeout(connect, 1500);
    };
  }

  api('GET', '/api/state').then(function (j) {
    S.browserAt = j.browser ? j.browser.at : '';
    S.config = j.config; S.version = j.version; S.dataDir = j.dataDir; S.report = j.report; S.checkupRunning = j.checkupRunning;
    S.snap = j.snap;
    (j.events || []).forEach(function (ev) { S.events.push(ev); S.lastSeq = Math.max(S.lastSeq, ev.seq); });
    render();
    connect();
    if (!S.report && !S.checkupRunning) { api('POST', '/api/checkup'); S.checkupRunning = true; }
  }).catch(function (er) {
    app.innerHTML = '<div class="pd-center pd-muted">无法连接守护服务：' + esc(er.message) + '</div>';
  });
})();
