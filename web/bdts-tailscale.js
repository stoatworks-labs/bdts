// The Tailscale panel on birdUI's System page.
//
// Everything the panel does lives here rather than in the template, because
// settings.html is a Go template that also carries the firmware upload form: a
// bad edit there takes the recovery path with it, so the template edit is one
// empty div and this script tag, and never changes again.
//
// The API is a separate process on its own port (see serve.go). Requests are
// sent with credentials so the browser attaches the birdUI session cookie —
// cookies are scoped by host and not by port, which is what lets this reuse the
// login the user has already done rather than asking for a second password.

(function () {
  'use strict';

  var API_PORT = __BDTS_API_PORT__;
  var API = location.protocol + '//' + location.hostname + ':' + API_PORT;

  var IDLE_POLL = 5000;   // steady state
  var ACTIVE_POLL = 2000; // while waiting for a login to complete

  var mount = document.getElementById('bdts_section');
  if (!mount) return;

  var state = null;
  var busy = false;
  var notice = null;      // transient message shown under the buttons
  var timer = null;
  var editing = {};       // fields the user has typed in, so polling cannot stamp on them

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = 'include';
    if (opts.body) {
      opts.headers = { 'Content-Type': 'application/json' };
    }
    return fetch(API + path, opts).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (body) {
        if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
        return body;
      });
    });
  }

  function poll() {
    return api('/api/status')
      .then(function (s) {
        state = s;
        state.unreachable = null;
      })
      .catch(function (e) {
        state = state || {};
        // A failed status call is nearly always the daemon being down, and
        // saying "check bd-tailscale-ui" is more use than the raw fetch error.
        state.unreachable = e.message || 'no response';
      })
      .then(render)
      .then(schedule);
  }

  function schedule() {
    clearTimeout(timer);
    var wait = (state && (state.login_url || state.backend_state === 'Starting')) ? ACTIVE_POLL : IDLE_POLL;
    timer = setTimeout(poll, wait);
  }

  function act(path, body, okMessage) {
    if (busy) return;
    busy = true;
    notice = null;
    render();
    api(path, { method: 'POST', body: body ? JSON.stringify(body) : '{}' })
      .then(function () {
        notice = { kind: 'ok', text: okMessage };
        editing = {};
      })
      .catch(function (e) {
        notice = { kind: 'err', text: e.message };
      })
      .then(function () {
        busy = false;
        return poll();
      });
  }

  // ---------------------------------------------------------------- rendering

  function box(title, inner) {
    return '' +
      '<div class="col-12 m-0 p-2 cm_h_lg_100">' +
        '<div class="div_content_box back_gray_color">' +
          '<div class="div_box_title border_bottom pl-3 pr-1">' +
            '<div class="div_subtitle" style="height: 100%; display: inline-block;">' +
              '<span class="font_gray_color">' + esc(title) + '</span>' +
            '</div>' +
          '</div>' +
          '<div class="div_box_content"><div class="p-3">' + inner + '</div></div>' +
        '</div>' +
      '</div>';
  }

  function row(label, value) {
    return '' +
      '<div class="row m-0 p-1">' +
        '<div class="col-xl-3 m-0 p-0 my-auto">' + esc(label) + '</div>' +
        '<div class="col-xl-9 m-0 p-0 my-auto">' + value + '</div>' +
      '</div>';
  }

  function button(id, text, disabled) {
    return '<button class="setting_submit" id="' + id + '"' + (disabled ? ' disabled' : '') +
      ' onmousedown="if(window.mouseDown)mouseDown(this)" onmouseup="if(window.mouseUp)mouseUp(this)">' +
      esc(text) + '</button>';
  }

  function checkbox(id, label, checked, disabled) {
    return '<label style="font-weight:normal;margin:0 1.5em 0 0;">' +
      '<input type="checkbox" id="' + id + '"' + (checked ? ' checked' : '') +
      (disabled ? ' disabled' : '') + ' style="margin-right:.4em;vertical-align:middle;">' +
      esc(label) + '</label>';
  }

  function statusPill(s) {
    var text, colour;
    if (s.unreachable) { text = 'Panel service not responding'; colour = '#c0392b'; }
    else if (!s.installed) { text = 'Not installed'; colour = '#7f8c8d'; }
    else if (!s.service_active) { text = 'Service stopped'; colour = '#c0392b'; }
    else if (s.connected) { text = 'Connected'; colour = '#22b24c'; }
    else if (s.needs_login) { text = 'Not signed in'; colour = '#e67e22'; }
    else { text = s.backend_state || 'Unknown'; colour = '#e67e22'; }
    return '<span style="display:inline-block;width:.6em;height:.6em;border-radius:50%;' +
      'background:' + colour + ';margin-right:.5em;vertical-align:middle;"></span>' + esc(text);
  }

  function authBanner(s) {
    var a = s.auth || {};
    if (a.authenticated || !a.required) return '';
    var msg = a.reason || 'Sign in to birdUI to change Tailscale settings.';
    return '<div class="p-2 mb-2" style="border-left:3px solid #e67e22;background:rgba(230,126,34,.08);">' +
      esc(msg) + '</div>';
  }

  function noticeHTML() {
    if (!notice) return '';
    var colour = notice.kind === 'ok' ? '#22b24c' : '#c0392b';
    return '<div class="p-2 mt-2" style="border-left:3px solid ' + colour + ';">' + esc(notice.text) + '</div>';
  }

  function connectSection(s) {
    var locked = !(s.auth && (s.auth.authenticated || !s.auth.required)) || busy;

    if (s.login_url) {
      return '' +
        row('Sign in', '<a href="' + esc(s.login_url) + '" target="_blank" rel="noopener noreferrer">' +
          esc(s.login_url) + '</a>') +
        '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
          'Open that link on any device and approve this machine. This page updates by itself once it is done.' +
        '</div></div>' +
        '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="text-align:right;">' +
          button('bdts_cancel', 'Cancel', locked) + '</div></div>';
    }

    if (s.needs_login) {
      var key = editing.authkey != null ? editing.authkey : '';
      return '' +
        row('Auth key <span style="opacity:.7">(optional)</span>',
            '<input type="password" id="bdts_authkey" autocomplete="off" placeholder="tskey-auth-…" ' +
            'style="width:100%;max-width:28em;" value="' + esc(key) + '"' + (locked ? ' disabled' : '') + '>') +
        '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
          'Leave it empty to sign in through a browser instead — a link appears here. ' +
          'A key is only worth using for unattended setup, because it is reusable until it expires.' +
        '</div></div>' +
        '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="text-align:right;">' +
          button('bdts_connect', busy ? 'Working…' : 'CONNECT', locked) + '</div></div>';
    }

    // Disconnected but still authenticated. Reconnecting needs no key, so this
    // must not fall through to the connected view and offer DISCONNECT again.
    if (s.backend_state === 'Stopped') {
      return '' +
        (s.dns_name ? row('Machine name', esc(s.dns_name)) : '') +
        '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
          'Still signed in to the tailnet, but not connected. Reconnecting needs no key.' +
        '</div></div>' +
        '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="text-align:right;">' +
          button('bdts_reconnect', busy ? 'Working…' : 'CONNECT', locked) + ' ' +
          button('bdts_logout', 'LOG OUT', locked) +
        '</div></div>';
    }

    var ips = (s.ips || []).join(', ');
    return '' +
      (s.dns_name ? row('Machine name', esc(s.dns_name)) : '') +
      (ips ? row('Tailnet address', esc(ips)) : '') +
      (s.tailnet ? row('Tailnet', esc(s.tailnet)) : '') +
      '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="text-align:right;">' +
        button('bdts_down', 'DISCONNECT', locked) + ' ' +
        button('bdts_logout', 'LOG OUT', locked) +
      '</div></div>';
  }

  function settingsSection(s) {
    var locked = !(s.auth && (s.auth.authenticated || !s.auth.required)) || busy;
    var p = s.prefs || {};
    var host = editing.hostname != null ? editing.hostname : (p.Hostname || s.hostname || '');

    var exitOptions = '<option value="">None</option>';
    (s.exit_nodes || []).forEach(function (n) {
      var label = (n.hostname || n.name) + (n.online ? '' : ' (offline)');
      exitOptions += '<option value="' + esc(n.name || n.ip) + '"' + (n.in_use ? ' selected' : '') + '>' +
        esc(label) + '</option>';
    });

    var unknown = s.prefs_error
      ? '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
        'Current values could not be read from Tailscale, so the boxes below start unticked. ' +
        'Saving still applies exactly what you set here.</div></div>'
      : '';

    var routing = s.exit_node_capable
      ? ''
      : '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
        'This device cannot advertise routes or act as an exit node: its kernel has no TUN driver, ' +
        'so Tailscale runs in userspace networking mode. Using someone else\'s exit node still works.' +
        '</div></div>';

    return '' +
      unknown +
      row('Machine name',
          '<input type="text" id="bdts_hostname" style="width:100%;max-width:28em;" value="' + esc(host) + '"' +
          (locked ? ' disabled' : '') + '>') +
      row('Options',
          checkbox('bdts_dns', 'Use tailnet DNS (MagicDNS)', !!p.CorpDNS, locked) +
          checkbox('bdts_routes', 'Accept subnet routes', !!p.RouteAll, locked) +
          checkbox('bdts_ssh', 'Tailscale SSH', !!p.RunSSH, locked)) +
      row('Exit node',
          '<select id="bdts_exit" style="max-width:28em;"' + (locked ? ' disabled' : '') + '>' +
          exitOptions + '</select>') +
      routing +
      '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="text-align:right;">' +
        button('bdts_save', busy ? 'Working…' : 'APPLY', locked) + '</div></div>';
  }

  function render() {
    var s = state || {};
    var header = row('Status', statusPill(s));
    var body;

    if (s.unreachable) {
      body = header + '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
        'The Tailscale panel service is not answering on port ' + API_PORT + '. ' +
        'On the device: <code>systemctl status bd-tailscale-ui</code>.</div></div>';
    } else if (!s.installed) {
      body = header + '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
        esc(s.error || 'Tailscale is not installed on this device.') + '</div></div>';
    } else if (s.error) {
      body = header + '<div class="row m-0 p-1"><div class="col-12 m-0 p-0" style="opacity:.75;">' +
        esc(s.error) + '</div></div>';
    } else {
      body = authBanner(s) + header + connectSection(s);
    }

    var html = box('Tailscale', body + noticeHTML());
    if (s.installed && !s.error && !s.unreachable && !s.needs_login && !s.login_url) {
      html += box('Tailscale Settings', settingsSection(s));
    }
    mount.innerHTML = html;
    wire(s);
  }

  // Track typing so a poll landing mid-edit does not wipe the field.
  function track(id, key) {
    var el = document.getElementById(id);
    if (el) el.addEventListener('input', function () { editing[key] = el.value; });
  }

  function on(id, fn) {
    var el = document.getElementById(id);
    if (el) el.addEventListener('click', fn);
  }

  function wire(s) {
    track('bdts_authkey', 'authkey');
    track('bdts_hostname', 'hostname');

    on('bdts_connect', function () {
      var key = (document.getElementById('bdts_authkey') || {}).value || '';
      act('/api/up', key ? { authkey: key } : {},
          key ? 'Connected.' : 'Starting sign-in — a link will appear here shortly.');
    });
    on('bdts_cancel', function () { act('/api/down', {}, 'Sign-in cancelled.'); });
    on('bdts_reconnect', function () { act('/api/up', {}, 'Reconnected.'); });
    on('bdts_down', function () { act('/api/down', {}, 'Disconnected. Sign in again without a key to reconnect.'); });
    on('bdts_logout', function () {
      if (!window.confirm('Log this device out of the tailnet? Reconnecting will need a new sign-in.')) return;
      act('/api/logout', {}, 'Logged out.');
    });
    on('bdts_save', function () {
      var exit = document.getElementById('bdts_exit');
      act('/api/settings', {
        hostname: (document.getElementById('bdts_hostname') || {}).value || '',
        accept_dns: !!(document.getElementById('bdts_dns') || {}).checked,
        accept_routes: !!(document.getElementById('bdts_routes') || {}).checked,
        ssh: !!(document.getElementById('bdts_ssh') || {}).checked,
        exit_node: exit ? exit.value : ''
      }, 'Settings applied.');
    });
  }

  poll();
})();
