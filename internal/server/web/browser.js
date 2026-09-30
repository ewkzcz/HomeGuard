/**
 * 浏览器检测页：采集只有浏览器里才拿得到的信号，发回 HomeGuard 体检。
 * WebRTC 通过 STUN 查询本机在外网看到的地址，用来发现不走代理的 UDP 出口。
 */
(function () {
  'use strict';
  var token = new URLSearchParams(location.search).get('t') || '';
  var out = { tz: '', locale: '', langs: [], rtcSrflx: [], rtcHost: [], webgl: '', uaPlatform: '' };
  document.getElementById('hero-icon').innerHTML = icon('loader', 28);

  // 1、时区、区域、语言
  try {
    var o = Intl.DateTimeFormat().resolvedOptions();
    out.tz = o.timeZone || '';
    out.locale = o.locale || '';
  } catch (e) { /* 忽略 */ }
  out.langs = (navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language]).filter(Boolean);

  // 2、WebGL 渲染器
  try {
    var gl = document.createElement('canvas').getContext('webgl');
    if (gl) {
      var ext = gl.getExtension('WEBGL_debug_renderer_info');
      out.webgl = String(gl.getParameter(ext ? ext.UNMASKED_RENDERER_WEBGL : gl.RENDERER) || '');
    }
  } catch (e) { /* 忽略 */ }

  // 3、Client Hints 平台
  if (navigator.userAgentData) { out.uaPlatform = navigator.userAgentData.platform || ''; }

  // 4、WebRTC 候选地址，最多等 6 秒
  function webrtc() {
    return new Promise(function (resolve) {
      var pc, done = false;
      function finish() { if (done) { return; } done = true; try { pc.close(); } catch (e) { /* 忽略 */ } resolve(); }
      try {
        pc = new RTCPeerConnection({ iceServers: [{ urls: 'stun:stun.cloudflare.com:3478' }, { urls: 'stun:stun.l.google.com:19302' }] });
        pc.createDataChannel('probe');
        pc.onicecandidate = function (e) {
          if (!e.candidate) { finish(); return; }
          var m = / (\S+) \d+ typ (host|srflx|relay)/.exec(e.candidate.candidate);
          if (!m) { return; }
          var list = m[2] === 'srflx' ? out.rtcSrflx : out.rtcHost;
          if (list.indexOf(m[1]) < 0) { list.push(m[1]); }
        };
        pc.createOffer().then(function (d) { return pc.setLocalDescription(d); }).catch(finish);
        setTimeout(finish, 6000);
      } catch (e) { finish(); }
    });
  }

  function row(k, v) { return '<dt>' + k + '</dt><dd class="pd-mono" style="font-size:13px">' + (v || '—').replace(/</g, '&lt;') + '</dd>'; }

  webrtc().then(function () {
    document.getElementById('list').innerHTML = row('WebRTC 公网', out.rtcSrflx.join('、') || '没拿到（无泄露）') + row('WebRTC 本地', out.rtcHost.join('、')) +
      row('浏览器时区', out.tz) + row('语言', out.langs.join(', ')) + row('区域', out.locale) + row('渲染器', out.webgl) + row('平台', out.uaPlatform || '浏览器不支持');
    return fetch('/browser/report?t=' + encodeURIComponent(token), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(out) });
  }).then(function (r) {
    if (!r.ok) { throw new Error(r.status === 403 ? '检测链接已失效，请回到 HomeGuard 重新点「浏览器检测」' : 'HTTP ' + r.status); }
    document.getElementById('hero-icon').innerHTML = icon('circle-check', 28);
    document.getElementById('title').textContent = '检测完成';
    document.getElementById('sub').textContent = '结果已发回 HomeGuard，正在重新体检，可以关闭这个页面';
  }).catch(function (e) {
    document.getElementById('hero').classList.add('bad');
    document.getElementById('hero-icon').innerHTML = icon('alert-triangle', 28);
    document.getElementById('title').textContent = '没能发回结果';
    document.getElementById('sub').textContent = e.message;
  });
})();
