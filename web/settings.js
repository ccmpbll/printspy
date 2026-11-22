// Settings pages: a full page with side tabs, plus an edit page for each
// printer, smart plug, camera and slicer target. Routes (hash based):
//   #/settings/<tab>                     tab = printers | devices | general | notifications | integrations | account
//   #/settings/<kind>/<id|new>[?from=card]   kind = printer | plug | camera | target

const SET_TABS = [
    ['printers', 'Printers'],
    ['devices', 'Plugs & cameras'],
    ['general', 'General'],
    ['notifications', 'Notifications'],
    ['integrations', 'Integrations'],
    ['account', 'Account & backup'],
];
const EDIT_KINDS = {
    printer: {tab: 'printers', tabLabel: 'Printers', add: 'Add printer'},
    plug: {tab: 'devices', tabLabel: 'Plugs & cameras', add: 'Add smart plug'},
    camera: {tab: 'devices', tabLabel: 'Plugs & cameras', add: 'Add camera'},
    target: {tab: 'devices', tabLabel: 'Plugs & cameras', add: 'Add slicer target'},
};

const NOTIFY_TYPES = ['start', 'complete', 'failed', 'error', 'checkpoint1', 'checkpoint2'];
const PUSHOVER_SOUNDS = ['pushover', 'bike', 'bugle', 'cashregister', 'classical', 'cosmic', 'falling', 'gamelan',
    'incoming', 'intermission', 'magic', 'mechanical', 'pianobar', 'siren', 'spacealarm', 'tugboat', 'alien',
    'climb', 'persistent', 'echo', 'updown', 'vibrate', 'none'];

// Per-event defaults shown as placeholders and in the preview. 'start' is the
// only event that defaults to a thumbnail-only image (see poller.go's
// sendNotification) - everything else defaults to camera-with-fallback.
const NOTIFY_META = {
    start: {name: 'Print started', titleDef: 'Print started', msgDef: '{printer}: {file}', ph: ['printer', 'file'], imgDef: 'thumbnail'},
    complete: {name: 'Print complete', titleDef: 'Print complete', msgDef: '{printer}: {file} ({material}, {filament_g}g) - {duration}', ph: ['printer', 'file', 'material', 'filament_g', 'duration'], imgDef: 'camera'},
    failed: {name: 'Print failed', titleDef: 'Print failed', msgDef: '{printer}: {file} ({material}, {filament_g}g) - {duration}', ph: ['printer', 'file', 'material', 'filament_g', 'duration'], imgDef: 'camera'},
    error: {name: 'Printer error', sub: 'Thermal runaway, attention, etc.', titleDef: '{printer}: error', msgDef: '{message}', ph: ['printer', 'message'], imgDef: 'camera'},
    checkpoint1: {name: 'Checkpoint 1', titleDef: 'Print checkpoint', msgDef: '{printer}: {file} reached {percent}%', ph: ['printer', 'file', 'percent'], imgDef: 'camera', pct: true},
    checkpoint2: {name: 'Checkpoint 2', titleDef: 'Print checkpoint', msgDef: '{printer}: {file} reached {percent}%', ph: ['printer', 'file', 'percent'], imgDef: 'camera', pct: true},
};
const NOTIFY_SAMPLE = {printer: 'Core One', file: 'allparts_m_joints.bgcode', material: 'PETG', filament_g: '87', duration: '1h14m', message: 'Thermal runaway: bed above limit', percent: '50'};
const IMAGE_LABEL = {camera: 'Camera snapshot', thumbnail: 'Plate thumbnail', none: 'No image'};

const CAMERA_RESOLUTIONS = [
    ['10', 'VGA (640x480, fastest stream)'], ['11', 'SVGA (800x600)'], ['12', 'XGA (1024x768)'],
    ['13', 'HD (1280x720, default)'], ['14', 'SXGA (1280x1024)'], ['15', 'UXGA (1600x1200, slowest stream)'],
];

let pageToken = 0;

// ---------- small helpers ----------

function $(id) { return document.getElementById(id); }
function val(id) { return $(id).value; }
function isOn(id) { return $(id).classList.contains('on'); }

async function getJSON(url) {
    const resp = await fetch(url);
    if (!resp.ok) throw new Error(`${url}: ${resp.status}`);
    return resp.json();
}

// A button briefly says "Saved" instead of closing a dialog.
function savedFlash(btn, text) {
    if (!btn) return;
    const original = btn.dataset.flashOriginal || btn.innerHTML;
    btn.dataset.flashOriginal = original;
    btn.textContent = text || 'Saved ✓';
    clearTimeout(btn._flash);
    btn._flash = setTimeout(() => { btn.innerHTML = original; }, 1600);
}

function toggleSwitch(el) {
    el.classList.toggle('on');
    el.setAttribute('aria-checked', el.classList.contains('on'));
    if (el.dataset.change) window[el.dataset.change](el);
}

const fld = (label, id, value, o = {}) => `
    <div class="fld ${o.wide ? 'wide' : ''}" ${o.attrs || ''}>
        <label for="${id}">${label}</label>
        <div class="inp ${o.unit ? 'has-unit' : ''}">
            <input id="${id}" type="${o.type || 'text'}" value="${esc(String(value === undefined || value === null ? '' : value))}" placeholder="${esc(o.ph || '')}"${o.step ? ` step="${o.step}"` : ''}${o.min !== undefined ? ` min="${o.min}"` : ''}${o.max !== undefined ? ` max="${o.max}"` : ''}${o.on ? ` oninput="${o.on}"` : ''}${o.req ? ' required' : ''}${o.minlen ? ` minlength="${o.minlen}"` : ''}>
            ${o.unit ? `<span class="unit">${o.unit}</span>` : ''}
        </div>
        ${o.hint ? `<small>${o.hint}</small>` : ''}
    </div>`;

const swrow = (id, on, title, hint, change) => `
    <div class="swrow"><span class="swtext"><b>${title}</b>${hint ? `<small>${hint}</small>` : ''}</span>
        <span class="sw ${on ? 'on' : ''}" id="${id}" role="switch" tabindex="0" aria-checked="${!!on}"${change ? ` data-change="${change}"` : ''} onclick="toggleSwitch(this)" onkeydown="if(event.key===' '||event.key==='Enter'){event.preventDefault();toggleSwitch(this)}"></span></div>`;

const setCard = (title, sub, body, foot, extra) => `
    <div class="set-card ${extra || ''}">
        <div class="set-card-head"><h3>${title}</h3>${sub ? `<p>${sub}</p>` : ''}</div>
        ${body}
        ${foot ? `<div class="set-foot">${foot}</div>` : ''}
    </div>`;

const selectFld = (label, id, options, current, o = {}) => `
    <div class="fld ${o.wide ? 'wide' : ''}"><label for="${id}">${label}</label>
        <div class="inp sel"><select id="${id}"${o.on ? ` onchange="${o.on}"` : ''}>${options.map(([v, l]) => `<option value="${esc(v)}" ${String(v) === String(current) ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select></div>
        ${o.hint ? `<small>${o.hint}</small>` : ''}</div>`;

const printerOptions = (withNone, only) => (withNone ? [['', 'Unassigned']] : [])
    .concat(printers.filter(p => !only || p.config.type === only).map(p => [p.config.id, p.config.name]));

const resultLine = (id) => `<div class="test-line" id="${id}" role="status"></div>`;

// ---------- router ----------

let dashScroll = 0;

function showPage(on) {
    if (on && !$('dashboard').hidden) dashScroll = window.scrollY; // remember where the dashboard was
    if ($('dashboard').hidden !== on) setDashboardActive(!on);
    $('dashboard').hidden = on;
    $('page').hidden = !on;
}

function route() {
    const h = location.hash.replace(/^#\/?/, '');
    const [path, qs] = h.split('?');
    const parts = path ? path.split('/') : [];
    const q = new URLSearchParams(qs || '');
    closeRawApi();
    if (parts[0] === 'settings') {
        showPage(true);
        renderSettingsRoute(parts.slice(1), q);
        return;
    }
    if (parts[0] === 'files' || parts[0] === 'history') {
        showPage(true);
        const id = parseInt(parts[1], 10);
        if (parts[0] === 'files') renderFilesRoute(id); else renderHistoryRoute(id);
        return;
    }
    showPage(false);
    pageToken++;
}

window.addEventListener('hashchange', () => {
    const wasDash = !$('dashboard').hidden;
    route();
    window.scrollTo(0, !wasDash && !$('dashboard').hidden ? dashScroll : 0);
});

function openSettings() { location.hash = '#/settings/printers'; }
// After deleting from an edit page the record is gone: go to the list, don't re-render it.
function afterDelete(editPrefix, list) {
    if (location.hash.startsWith(editPrefix)) location.hash = list;
    else route();
}

function goBack(q, fallback) { location.hash = q && q.get('from') === 'card' ? '#/' : fallback; }

function setShell(tab, crumbs, body) {
    $('page').innerHTML = `
        <div class="set-title"><h2>${crumbs}</h2></div>
        <div class="set-wrap">
            <nav class="set-nav">${SET_TABS.map(([k, l]) => `<a class="set-tab ${k === tab ? 'on' : ''}" href="#/settings/${k}">${l}</a>`).join('')}</nav>
            <div class="set-body" id="set-body">${body}</div>
        </div>`;
}

async function renderSettingsRoute(parts, q) {
    const token = ++pageToken;
    const kind = parts[0] || 'printers';
    if (EDIT_KINDS[kind]) return renderEditPage(kind, parts[1], q, token);
    const tab = SET_TABS.some(t => t[0] === kind) ? kind : 'printers';
    setShell(tab, '<a class="crumb" href="#/">Dashboard</a> <span class="sep">&rsaquo;</span> Settings', '<div class="set-empty">Loading…</div>');
    try {
        const html = await TAB_RENDERERS[tab]();
        if (token !== pageToken) return;
        $('set-body').innerHTML = html;
        if (tab === 'notifications') initNotifications();
    } catch (e) {
        if (token === pageToken) $('set-body').innerHTML = '<div class="set-empty">Failed to load.</div>';
    }
}

// ---------- tab: printers ----------

async function printersTab() {
    if (!printers.length) await fetchPrinters();
    const rows = printers.map((p, i) => {
        const cfg = p.config, state = cfg.maintenance ? 'maintenance' : (p.status ? p.status.state : 'offline');
        const kind = esc(p.display_name || cfg.type);
        return `<div class="set-row">
            <span class="reorder"><button ${i === 0 ? 'disabled' : ''} onclick="movePrinter(${cfg.id},-1)" title="Move up">&#9650;</button><button ${i === printers.length - 1 ? 'disabled' : ''} onclick="movePrinter(${cfg.id},1)" title="Move down">&#9660;</button></span>
            <span class="dotst ${esc(state)}"></span>
            <div class="set-row-info"><span class="set-row-name">${esc(cfg.name)}<span class="chip">${kind}</span></span><span class="set-row-meta">${cfg.model ? esc(cfg.model) + ' &middot; ' : ''}${esc(cfg.url)}</span></div>
            <div class="set-row-actions">
                <a class="btn" href="#/settings/printer/${cfg.id}">&#9998; Edit</a>
                ${cfg.type === 'prusalink' ? `<button class="btn" onclick="openRawApi(${cfg.id})" title="Raw API debug view">Raw API</button>` : '<button class="btn raw-ph" tabindex="-1" aria-hidden="true">Raw API</button>'}
                <a class="btn link-accent" href="${esc(safeUrl(cfg.url))}" target="_blank" rel="noopener" title="Open ${kind}">${kind} &#8599;</a>
                <button class="btn btn-maintenance ${cfg.maintenance ? 'active' : ''}" onclick="toggleMaintenance(${cfg.id},${!cfg.maintenance})" title="${cfg.maintenance ? 'End maintenance' : 'Mark as in maintenance'}">Maintenance</button>
                <button class="btn btn-danger" onclick="confirmAction(this, () => deletePrinter(${cfg.id}))">Delete</button>
            </div>
        </div>`;
    }).join('');
    return setCard('Printers', 'Order here is the order on the dashboard.',
        printers.length ? `<div class="set-list">${rows}</div>` : '<div class="set-empty">No printers configured yet.</div>',
        '<a class="btn btn-primary" href="#/settings/printer/new">+ Add printer</a>');
}

async function toggleMaintenance(id, maintenance) {
    try {
        const resp = await fetch(`/api/printers/${id}/maintenance`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({maintenance}),
        });
        await checkOk(resp, 'Failed to change maintenance mode.');
        await fetchPrinters();
        route();
    } catch (e) { netFail('Failed to change maintenance mode.'); }
}

async function movePrinter(id, direction) {
    const idx = printers.findIndex(p => p.config.id === id);
    if (idx < 0) return;
    const newIdx = idx + direction;
    if (newIdx < 0 || newIdx >= printers.length) return;

    [printers[idx], printers[newIdx]] = [printers[newIdx], printers[idx]];
    const ids = printers.map(p => p.config.id);

    prevPrinterIDs = [];
    updateDashboard();
    route();

    // The optimistic swap above is only true if the server agrees - on any
    // failure, reload the real order so the UI never shows an unsaved one.
    try {
        const resp = await fetch('/api/printers/reorder', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({ids}),
        });
        if (await checkOk(resp, 'Failed to reorder printers.')) return;
    } catch (e) { netFail('Failed to reorder printers.'); }
    await fetchPrinters();
    route();
}

async function deletePrinter(id) {
    try {
        const resp = await fetch(`/api/printers/${id}`, {method: 'DELETE'});
        if (await checkOk(resp, 'Failed to delete printer.')) {
            await fetchPrinters();
            if (location.hash.startsWith('#/settings/printer/')) location.hash = '#/settings/printers';
            else route();
        }
    } catch (e) { netFail('Failed to delete printer.'); }
}

// ---------- tab: plugs & cameras ----------

async function devicesTab() {
    if (!printers.length) await fetchPrinters();
    const [plugs, cams, targets] = await Promise.all([
        getJSON('/api/smartplugs'), getJSON('/api/cameras'), getJSON('/api/ingest-keys'),
    ]);
    const row = (name, meta, kind, id, del, fn) => `<div class="set-row">
        <div class="set-row-info"><span class="set-row-name">${name}</span><span class="set-row-meta">${meta}</span></div>
        <div class="set-row-actions"><a class="btn" href="#/settings/${kind}/${id}">&#9998; Edit</a><button class="btn btn-danger" onclick="confirmAction(this, () => ${fn}(${id}))">Delete</button></div></div>`;
    const empty = (t) => `<div class="set-empty">${t}</div>`;
    const plugRows = (plugs || []).map(p => row(esc(p.label || p.ip || p.mqtt_topic),
        `${p.mqtt_topic ? `MQTT: ${esc(p.mqtt_topic)}:${esc(p.idx)}` : `${esc(p.ip)}:${esc(p.idx)}`} — ${p.printer_name ? esc(p.printer_name) : 'Unassigned'}${p.hide_label ? ' — label hidden' : ''}`, 'plug', p.id, true, 'deleteSmartPlug')).join('');
    const camRows = (cams || []).map(c => row(esc(c.name || c.url), `${esc(c.url)} — ${c.printer_name ? esc(c.printer_name) : 'Unassigned'}`, 'camera', c.id, true, 'deleteCamera')).join('');
    const targetRows = (targets || []).map(t => row(esc(t.label || printerName(t.printer_id) || 'Untitled'),
        `Printer: ${esc(printerName(t.printer_id) || `#${t.printer_id}`)} — Host: ${esc(location.origin)}/ingest/${esc(t.label || t.id)}`, 'target', t.id, true, 'deleteIngestKey')).join('');
    return setCard('Smart plugs (Tasmota)', 'Talk directly to a Tasmota device, independent of any auto-detected plugins. Add a plug, then assign it to any printer.',
            plugRows ? `<div class="set-list">${plugRows}</div>` : empty('No smart plugs configured yet.'), '<a class="btn btn-primary" href="#/settings/plug/new">+ Add plug</a>')
        + setCard('Cameras (printspy-cam)', 'Standalone printspy-cam device, independent of printer type. Add one, then assign it to any printer.',
            camRows ? `<div class="set-list">${camRows}</div>` : empty('No cameras configured yet.'), '<a class="btn btn-primary" href="#/settings/camera/new">+ Add camera</a>')
        + setCard('Slicer targets', 'Lets PrusaSlicer/OrcaSlicer\'s "Send to printer" (Physical Printer, PrusaLink mode) stage a file here for a model bucket. Dispatch a staged job to a specific printer from its dashboard card.',
            targetRows ? `<div class="set-list">${targetRows}</div>` : empty('No slicer targets configured yet.'), '<a class="btn btn-primary" href="#/settings/target/new">+ Add slicer target</a>');
}

async function deleteSmartPlug(id) {
    try {
        const resp = await fetch(`/api/smartplugs/${id}`, {method: 'DELETE'});
        if (await checkOk(resp, 'Failed to delete smart plug.')) afterDelete('#/settings/plug/', '#/settings/devices');
    } catch (e) { netFail('Failed to delete smart plug.'); }
}

async function deleteCamera(id) {
    try {
        const resp = await fetch(`/api/cameras/${id}`, {method: 'DELETE'});
        if (await checkOk(resp, 'Failed to delete camera.')) afterDelete('#/settings/camera/', '#/settings/devices');
    } catch (e) { netFail('Failed to delete camera.'); }
}

async function deleteIngestKey(id) {
    try {
        const resp = await fetch(`/api/ingest-keys/${id}`, {method: 'DELETE'});
        if (await checkOk(resp, 'Failed to delete slicer target.')) {
            loadIngestJobs();
            afterDelete('#/settings/target/', '#/settings/devices');
        }
    } catch (e) { netFail('Failed to delete slicer target.'); }
}

function printerName(printerID) {
    const p = printers.find(p => p.config.id === printerID);
    return p ? p.config.name : '';
}

// ---------- tab: general ----------

async function generalTab() {
    const s = await getJSON('/api/settings');
    const saveBtn = (fn) => `<button class="btn btn-primary" onclick="${fn}(this)">Save</button>`;
    return setCard('Dashboard', '', `<div class="grid2">
            ${fld('Snapshot refresh interval (seconds)', 's-snapshot-interval', s.snapshot_interval || '10', {type: 'number', min: 3, max: 300, unit: 'seconds', hint: 'How often webcam snapshots refresh. Lower values use more bandwidth.'})}
        </div>
        ${swrow('s-hide-webcam', s.hide_webcam_when_unreachable === '1', 'Hide camera/thumbnail section when a printer is unreachable', 'Removes the image box and SNAP/LIVE/PLATE buttons from a card only while that printer is offline/disconnected - normal otherwise. Applies to all printers.')}`, saveBtn('saveDashboardSettings'))
        + setCard('Polling & control', '', `<div class="grid2">
            ${fld('Poll interval (seconds)', 's-poll-interval', s.poll_interval || '', {type: 'number', min: 3, max: 60, ph: 'Per-printer default', unit: 'seconds', hint: 'Global override for how often printers are polled. Leave empty to use per-printer settings.'})}
            ${fld('Print control timeout (seconds)', 's-print-control-timeout', s.print_control_timeout_secs || '15', {type: 'number', min: 5, max: 60, unit: 'seconds', hint: 'After clicking Print/Pause/Resume/Cancel, how long to wait for the printer to actually confirm the new state before giving up and falling back to the next regular poll. Longer covers a slow reheat-before-resume; shorter keeps the button responsive.'})}
            ${fld('PrusaLink keepalive ping interval (seconds)', 's-ping-interval', s.prusalink_ping_interval || '', {type: 'number', min: 0, max: 60, ph: '0 = disabled', unit: 'seconds', hint: 'Pings each PrusaLink printer\'s IP over ICMP on this interval, to stop its wifi interface from dropping off the network when idle. 0 disables.'})}
        </div>`, saveBtn('savePollingSettings'))
        + setCard('Power safety', 'Applies to printers with a directly-assigned smart plug.', `<div class="grid2">
            ${fld('Auto-off after idle (minutes)', 's-auto-off-idle', s.auto_off_idle_minutes || '', {type: 'number', min: 0, max: 1440, ph: '0 = disabled, per-printer default', unit: 'minutes', hint: 'Global override for how long a printer with an assigned smart plug can sit idle before its power is cut. Leave empty to use per-printer settings. Only applies to printers with a directly-assigned smart plug.'})}
            ${fld('Auto-off cooldown temp (°C)', 's-auto-off-cooldown', s.auto_off_cooldown_temp || '40', {type: 'number', min: 10, max: 100, unit: '&deg;C', hint: 'Power isn\'t cut until hotend and bed both drop below this temperature, even after the idle timeout elapses.'})}
            ${fld('Thermal runaway: max bed temp (°C)', 's-thermal-bed', s.thermal_max_bed_temp || '', {type: 'number', min: 0, max: 150, ph: '0 = disabled, per-printer default', unit: '&deg;C', hint: 'Cuts power to a printer\'s assigned smart plug if actual bed temp exceeds this. Second layer on top of firmware protection, not a replacement for it. Only applies to printers with a directly-assigned smart plug.'})}
            ${fld('Thermal runaway: max extruder temp (°C)', 's-thermal-extruder', s.thermal_max_extruder_temp || '', {type: 'number', min: 0, max: 350, ph: '0 = disabled, per-printer default', unit: '&deg;C', hint: 'Same as above, for the hotend.'})}
        </div>`, saveBtn('savePowerSettings'))
        + setCard('History & logging', '', `<div class="grid2">
            ${fld('Print history retention (days)', 's-history-retention', s.history_retention_days || '', {type: 'number', min: 0, max: 3650, ph: '0 = keep forever', unit: 'days', hint: 'Entries older than this are pruned the next time a print completes. 0 keeps everything.'})}
        </div>
        ${swrow('s-debug-logging', s.debug_logging === '1', 'Enable debug logging', 'Verbose tracing for polling, MQTT, smart plugs, and API requests - view with <code>docker logs -f printspy</code>. Takes effect immediately, no restart needed. Leave off unless actively troubleshooting.')}`, saveBtn('saveHistorySettings'));
}

async function putSettings(settings) {
    try {
        const resp = await fetch('/api/settings', {
            method: 'PUT',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(settings),
        });
        return await checkOk(resp, 'Failed to save settings.');
    } catch (e) {
        netFail('Failed to save settings.');
        return false;
    }
}

async function saveDashboardSettings(btn) {
    const settings = {
        snapshot_interval: val('s-snapshot-interval') || '10',
        hide_webcam_when_unreachable: isOn('s-hide-webcam') ? '1' : '0',
    };
    if (!await putSettings(settings)) return;
    snapshotInterval = parseInt(settings.snapshot_interval) || 10;
    restartSnapshotTimer();
    hideWebcamWhenUnreachable = settings.hide_webcam_when_unreachable === '1';
    prevPrinterIDs = [];
    updateDashboard();
    savedFlash(btn);
}

async function savePollingSettings(btn) {
    const settings = {
        print_control_timeout_secs: val('s-print-control-timeout') || '15',
        prusalink_ping_interval: val('s-ping-interval') || '0',
    };
    const pollVal = val('s-poll-interval');
    if (pollVal) settings.poll_interval = pollVal;
    if (await putSettings(settings)) savedFlash(btn);
}

async function savePowerSettings(btn) {
    const settings = {
        auto_off_idle_minutes: val('s-auto-off-idle') || '0',
        auto_off_cooldown_temp: val('s-auto-off-cooldown') || '40',
        thermal_max_bed_temp: val('s-thermal-bed') || '0',
        thermal_max_extruder_temp: val('s-thermal-extruder') || '0',
    };
    if (await putSettings(settings)) savedFlash(btn);
}

async function saveHistorySettings(btn) {
    const settings = {
        history_retention_days: val('s-history-retention') || '0',
        debug_logging: isOn('s-debug-logging') ? '1' : '0',
    };
    if (await putSettings(settings)) savedFlash(btn);
}

// ---------- tab: notifications ----------

let NS = {};            // per-event editable state
let nOpen = new Set();  // which Customize panels are open

async function notificationsTab() {
    const s = await getJSON('/api/settings');
    NS = {};
    NOTIFY_TYPES.forEach(t => {
        const meta = NOTIFY_META[t];
        NS[t] = {
            on: meta.pct ? s[`notify_${t}_enabled`] === '1' : s[`notify_on_${t}`] === '1',
            pct: s[`notify_${t}_percent`] || (t === 'checkpoint1' ? '5' : '50'),
            title: s[`notify_${t}_title`] || '', msg: s[`notify_${t}_message`] || '',
            sound: s[`notify_${t}_sound`] || '', image: s[`notify_${t}_image`] || '',
            high: s[`notify_${t}_high_priority`] === '1',
        };
    });
    nOpen = new Set();
    return setCard('Pushover', 'Notifications are sent through your Pushover account.', `<div class="grid2">
            ${fld('Pushover user key', 'n-user-key', s.pushover_user_key || '', {ph: 'e.g. u1a2b3c4d5e6f7g8h9'})}
            ${fld('Pushover app token', 'n-app-token', s.pushover_app_token || '', {ph: 'e.g. a1b2c3d4e5f6g7h8i9'})}
        </div>`, '<button class="btn btn-primary" onclick="saveNotificationSettings(this)">Save</button><button class="btn" onclick="sendTestNotification()">Send test notification</button>')
        + resultLine('notify-test-result')
        + setCard('Events', 'Turn each notification on or off and customize how it looks and sounds. Leave Title and Message empty to use the defaults shown in grey. Checkpoint and print-complete/failed notifications include a live webcam snapshot when a camera\'s assigned, otherwise the plate thumbnail.',
            '<div class="ev-list" id="ev-list"></div>', '<button class="btn btn-primary" onclick="saveNotificationSettings(this)">Save</button>');
}

function initNotifications() { redrawEvents(); }

function fillPlaceholders(t, pct) {
    return t.replace(/\{(\w+)\}/g, (m, k) => (k === 'percent' && pct) ? pct : (NOTIFY_SAMPLE[k] !== undefined ? NOTIFY_SAMPLE[k] : m));
}

function notifyImage(t) { return NS[t].image || NOTIFY_META[t].imgDef; }

function notifySummary(t) {
    const st = NS[t], bits = [];
    if (st.title || st.msg) bits.push('<span class="chip acc">Custom text</span>');
    bits.push(`<span class="chip">&#9835; ${st.sound ? esc(st.sound) : 'default'}</span>`);
    const img = notifyImage(t);
    bits.push(`<span class="chip">${img === 'none' ? 'No image' : img === 'camera' ? 'Camera' : 'Thumbnail'}</span>`);
    if (st.high) bits.push('<span class="chip warn">High priority</span>');
    return bits.join('');
}

function notifyPreview(t) {
    const meta = NOTIFY_META[t], st = NS[t], img = notifyImage(t), pct = meta.pct ? st.pct : null;
    return `<div class="pv">
        <div class="pv-top"><img src="/logo.png" alt=""><span>PRINTSPY</span><span class="pv-now">now</span></div>
        <div class="pv-title" id="pv-${t}-t">${esc(fillPlaceholders(st.title || meta.titleDef, pct))}</div>
        <div class="pv-msg" id="pv-${t}-m">${esc(fillPlaceholders(st.msg || meta.msgDef, pct))}</div>
        ${img === 'none' ? '' : `<div class="pv-img"><span>${IMAGE_LABEL[img]}</span></div>`}
        <div class="pv-meta">${st.high ? '<b>High priority</b> · bypasses quiet hours · ' : ''}Sound: ${st.sound ? esc(st.sound) : 'app default'}</div>
    </div>`;
}

function notifyPanel(t) {
    const meta = NOTIFY_META[t], st = NS[t];
    const imgBtn = (v, l) => `<button class="seg-btn ${st.image === v ? 'active' : ''}" onclick="notifySet('${t}','image','${v}',true)">${l}</button>`;
    return `<div class="ev-panel">
        <div class="ev-form">
            <div class="fld"><label>Title</label><div class="inp"><input type="text" value="${esc(st.title)}" placeholder="${esc(meta.titleDef)}" oninput="notifySet('${t}','title',this.value)"></div></div>
            <div class="fld"><label>Message</label><div class="inp"><input type="text" id="msg-${t}" value="${esc(st.msg)}" placeholder="${esc(meta.msgDef)}" oninput="notifySet('${t}','msg',this.value)"></div>
                <div class="phrow"><small>Placeholders:</small>${meta.ph.map(p => `<button class="phchip" onclick="notifyInsert('${t}','${p}')">{${p}}</button>`).join('')}</div></div>
            <div class="ev-stack">
                <div class="fld"><label>Sound</label><div class="inp sel"><select onchange="notifySet('${t}','sound',this.value,true)"><option value="">App default</option>${PUSHOVER_SOUNDS.map(x => `<option ${st.sound === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div></div>
                <div class="fld"><label>Image</label><div class="seg">${imgBtn('', 'Default')}${imgBtn('camera', 'Camera')}${imgBtn('thumbnail', 'Thumbnail')}${imgBtn('none', 'None')}</div>
                    <small>Default is ${meta.imgDef === 'camera' ? 'the camera, falling back to the plate thumbnail' : 'the plate thumbnail'}.</small></div>
            </div>
            <div class="swrow flat"><span class="swtext"><b>High priority</b><small>Bypasses your phone's quiet hours / Do Not Disturb.</small></span><span class="sw ${st.high ? 'on' : ''}" role="switch" tabindex="0" aria-checked="${st.high}" onclick="notifySet('${t}','high',!NS['${t}'].high,true)"></span></div>
        </div>
        <div class="ev-preview"><div class="ev-prev-label">Preview <small>(sample values)</small></div>${notifyPreview(t)}</div>
    </div>`;
}

function redrawEvents() {
    const list = $('ev-list');
    if (!list) return;
    list.innerHTML = NOTIFY_TYPES.map(t => {
        const meta = NOTIFY_META[t], st = NS[t], open = nOpen.has(t);
        const pct = meta.pct ? `<span class="inp has-unit tiny"><input type="number" min="1" max="99" value="${esc(st.pct)}" oninput="notifySet('${t}','pct',this.value)" aria-label="${meta.name} percent"><span class="unit">%</span></span>` : '';
        return `<div class="ev ${open ? 'open' : ''} ${st.on ? '' : 'off'}">
            <div class="ev-row">
                <span class="sw ${st.on ? 'on' : ''}" role="switch" tabindex="0" aria-checked="${st.on}" aria-label="${meta.name}" onclick="notifySet('${t}','on',!NS['${t}'].on,true)"></span>
                <div class="ev-name"><b>${meta.name}${meta.pct ? ' at' : ''}</b>${pct}${meta.sub ? `<small>${meta.sub}</small>` : ''}</div>
                <div class="ev-sum">${st.on ? notifySummary(t) : '<span class="chip">Off</span>'}</div>
                <button class="btn" onclick="notifyToggle('${t}')">${open ? 'Done' : 'Customize'}</button>
            </div>
            ${open ? notifyPanel(t) : ''}
        </div>`;
    }).join('');
}

function notifySet(t, field, value, redraw) {
    NS[t][field] = value;
    if (redraw) { redrawEvents(); return; }
    const meta = NOTIFY_META[t], st = NS[t], pct = meta.pct ? st.pct : null;
    const title = $(`pv-${t}-t`), msg = $(`pv-${t}-m`);
    if (title) title.textContent = fillPlaceholders(st.title || meta.titleDef, pct);
    if (msg) msg.textContent = fillPlaceholders(st.msg || meta.msgDef, pct);
}

function notifyInsert(t, name) {
    const el = $(`msg-${t}`);
    el.value = (el.value ? el.value + ' ' : '') + '{' + name + '}';
    notifySet(t, 'msg', el.value);
    el.focus();
}

function notifyToggle(t) {
    if (nOpen.has(t)) nOpen.delete(t); else nOpen.add(t);
    redrawEvents();
}

async function saveNotificationSettings(btn) {
    const settings = {
        pushover_user_key: val('n-user-key'),
        pushover_app_token: val('n-app-token'),
        notify_on_start: NS.start.on ? '1' : '0',
        notify_on_complete: NS.complete.on ? '1' : '0',
        notify_on_failed: NS.failed.on ? '1' : '0',
        notify_on_error: NS.error.on ? '1' : '0',
        notify_checkpoint1_enabled: NS.checkpoint1.on ? '1' : '0',
        notify_checkpoint1_percent: NS.checkpoint1.pct || '5',
        notify_checkpoint2_enabled: NS.checkpoint2.on ? '1' : '0',
        notify_checkpoint2_percent: NS.checkpoint2.pct || '50',
    };
    NOTIFY_TYPES.forEach(t => {
        settings[`notify_${t}_title`] = NS[t].title;
        settings[`notify_${t}_message`] = NS[t].msg;
        settings[`notify_${t}_sound`] = NS[t].sound;
        settings[`notify_${t}_image`] = NS[t].image;
        settings[`notify_${t}_high_priority`] = NS[t].high ? '1' : '0';
    });
    if (await putSettings(settings)) savedFlash(btn);
}

async function sendTestNotification() {
    const result = $('notify-test-result');
    result.textContent = 'Sending...';
    try {
        const resp = await fetch('/api/notify-test', {method: 'POST'});
        const data = await resp.json();
        result.textContent = data.success ? 'Test notification sent - check your device.' : `Failed: ${data.error}`;
    } catch (e) {
        result.textContent = `Failed: ${e.message}`;
    }
}

// ---------- tab: integrations ----------

async function integrationsTab() {
    const s = await getJSON('/api/settings');
    const mqttURL = s.mqtt_broker_url || '';
    return setCard('MQTT broker', '', `<div class="grid2">
            ${fld('Broker host:port', 'm-broker', mqttURL.replace(/^(tcp|ssl):\/\//, ''), {ph: '192.168.1.10:1883', hint: 'Only needed if you assign a smart plug to MQTT mode below.'})}
            <div class="fld"><label>&nbsp;</label>${swrow('m-tls', mqttURL.startsWith('ssl://'), 'Use TLS')}</div>
            ${fld('Username', 'm-user', s.mqtt_username || '', {ph: 'optional'})}
            ${fld('Password', 'm-pass', s.mqtt_password || '', {type: 'password', ph: 'optional'})}
        </div>
        ${swrow('m-publish', s.mqtt_publish_enabled === '1', 'Publish printer status', 'Publishes each printer\'s full status (state, progress, temps, job) as a retained MQTT message on every poll tick.')}
        <div class="grid2">${fld('Publish topic', 'm-topic', s.mqtt_publish_topic || '', {ph: 'printspy/printer/{id}/state', wide: true, hint: 'Use <code>{id}</code> and <code>{name}</code> as placeholders.'})}</div>`,
        '<button class="btn btn-primary" onclick="saveMQTTSettings(this)">Save</button><button class="btn" onclick="sendMQTTTest()">Test connection</button><button class="btn" onclick="saveAndTestMQTT()">Save and test</button>')
        + resultLine('mqtt-test-result')
        + setCard('Dashboard status API', '', `<div class="fld"><label for="s-status-key">API key</label>
            <div class="inline"><div class="inp"><input id="s-status-key" type="text" value="${esc(s.status_api_key || '')}" placeholder="leave blank to disable"></div><button class="btn" onclick="generateStatusAPIKey()">Generate</button></div>
            <small>Used by external dashboards (e.g. Homepage) to read <code>GET /api/status</code>. Send it as the <code>X-Api-Key</code> header.</small></div>`,
        '<button class="btn btn-primary" onclick="saveStatusAPISettings(this)">Save</button>');
}

async function putMQTTSettings() {
    const hostPort = val('m-broker');
    return putSettings({
        mqtt_broker_url: hostPort ? (isOn('m-tls') ? 'ssl://' : 'tcp://') + hostPort : '',
        mqtt_username: val('m-user'),
        mqtt_password: val('m-pass'),
        mqtt_publish_enabled: isOn('m-publish') ? '1' : '0',
        mqtt_publish_topic: val('m-topic'),
    });
}

async function saveMQTTSettings(btn) {
    if (await putMQTTSettings()) savedFlash(btn);
}

async function sendMQTTTest() {
    const result = $('mqtt-test-result');
    result.textContent = 'Connecting...';
    try {
        const resp = await fetch('/api/mqtt-test', {method: 'POST'});
        const data = await resp.json();
        result.textContent = data.success ? 'Connected to broker successfully.' : `Failed: ${data.error}`;
    } catch (e) {
        result.textContent = `Failed: ${e.message}`;
    }
}

async function saveAndTestMQTT() {
    const result = $('mqtt-test-result');
    result.textContent = 'Saving...';
    if (!await putMQTTSettings()) {
        result.textContent = 'Failed to save settings.';
        return;
    }
    await sendMQTTTest();
}

function generateStatusAPIKey() {
    $('s-status-key').value = Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join('');
}

async function saveStatusAPISettings(btn) {
    if (await putSettings({status_api_key: val('s-status-key').trim()})) savedFlash(btn);
}

// ---------- tab: account & backup ----------

async function accountTab() {
    const [users, me] = await Promise.all([getJSON('/api/users'), fetch('/api/me').then(r => r.ok ? r.json() : null).catch(() => null)]);
    const userRows = (users || []).map(u => `<div class="set-row"><div class="set-row-info"><span class="set-row-name">${esc(u.username)}${me && me.username === u.username ? '<span class="chip">you</span>' : ''}</span></div>
        <div class="set-row-actions"><button class="btn btn-danger" onclick="confirmAction(this, () => deleteUser(${u.id}))">Delete</button></div></div>`).join('');
    return setCard('Your account', '', `<div class="grid2">
            ${fld('Current password', 'a-current', '', {type: 'password'})}
            ${fld('New password', 'a-new', '', {type: 'password'})}
        </div>`, '<button class="btn btn-primary" onclick="changePassword(this)">Change password</button>')
        + setCard('Users', '', `<div class="set-list">${userRows}</div>
            <div class="grid2" style="margin-top:1rem">${fld('Username', 'a-new-user', '', {req: true})}${fld('Password', 'a-new-pass', '', {type: 'password', req: true, minlen: 8})}</div>`,
            '<button class="btn btn-primary" onclick="addUser(this)">+ Add</button>')
        + setCard('Backup', 'Export or import your printer configuration and settings as YAML.', '',
            `<button class="btn" onclick="exportConfig()">Export YAML</button><button class="btn" onclick="$('import-file').click()">Import YAML</button><input type="file" id="import-file" accept=".yaml,.yml" hidden onchange="importConfig(this)">`);
}

async function changePassword(btn) {
    try {
        const resp = await fetch('/api/account/password', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({current_password: val('a-current'), new_password: val('a-new')}),
        });
        if (await checkOk(resp, 'Failed to change password.')) {
            $('a-current').value = '';
            $('a-new').value = '';
            savedFlash(btn, 'Password changed ✓');
        }
    } catch (e) { netFail('Failed to change password.'); }
}

async function addUser(btn) {
    if (!editValid(btn.closest('.set-card') || undefined)) return;
    try {
        const resp = await fetch('/api/users', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({username: val('a-new-user'), password: val('a-new-pass')}),
        });
        if (await checkOk(resp, 'Failed to add user.')) route();
    } catch (e) { netFail('Failed to add user.'); }
}

async function deleteUser(id) {
    try {
        const resp = await fetch(`/api/users/${id}`, {method: 'DELETE'});
        if (await checkOk(resp, 'Failed to delete user.')) route();
    } catch (e) { netFail('Failed to delete user.'); }
}

const TAB_RENDERERS = {
    printers: printersTab, devices: devicesTab, general: generalTab,
    notifications: notificationsTab, integrations: integrationsTab, account: accountTab,
};

// ---------- edit pages ----------

async function renderEditPage(kind, idParam, q, token) {
    const info = EDIT_KINDS[kind];
    const isNew = !idParam || idParam === 'new';
    const id = isNew ? null : parseInt(idParam, 10);
    setShell(info.tab, '', '<div class="set-empty">Loading…</div>');
    try {
        if (!printers.length) await fetchPrinters();
        const page = await EDIT_BUILDERS[kind](id, q);
        if (token !== pageToken) return;
        const back = `#/settings/${info.tab}`;
        const crumbs = q.get('from') === 'card'
            ? `<a class="crumb" href="#/">Dashboard</a> <span class="sep">&rsaquo;</span> ${esc(page.title)}`
            : `<a class="crumb" href="${back}">Settings &rsaquo; ${info.tabLabel}</a> <span class="sep">&rsaquo;</span> ${esc(page.title)}`;
        document.querySelector('.set-title h2').innerHTML = crumbs;
        $('set-body').classList.add('edit');
        $('set-body').innerHTML = page.html + `<div class="edit-bar"><button class="btn btn-quiet" onclick="goBack(editQuery,'${back}')">Cancel</button>${page.bar || ''}<button class="btn btn-primary" id="edit-save" onclick="if(editValid())saveBusy(this,()=>${page.save})">Save</button></div>`;
        editQuery = q;
        if (page.after) page.after();
    } catch (e) {
        if (token === pageToken) $('set-body').innerHTML = '<div class="set-empty">Failed to load.</div>';
    }
}
let editQuery = new URLSearchParams();

// Native validation (required / type=url / minlength) for the visible inputs
// of an edit page: the pages are not <form>s, so check by hand.
function editValid(root) {
    for (const inp of (root || $('set-body')).querySelectorAll('input')) {
        if (inp.closest('[hidden]')) continue;
        // minlength is only enforced by browsers for user-typed values; check it by hand.
        if (inp.minLength > 0 && inp.value && inp.value.length < inp.minLength) {
            inp.setCustomValidity(`Use at least ${inp.minLength} characters.`);
        }
        if (!inp.checkValidity()) {
            inp.reportValidity();
            inp.addEventListener('input', () => inp.setCustomValidity(''), {once: true});
            return false;
        }
    }
    return true;
}

// Disable Save while the request runs so a double click or repeated Enter can't create duplicates.
async function saveBusy(btn, fn) {
    btn.disabled = true;
    try { await fn(); } finally { btn.disabled = false; }
}

// Enter in a text field saves the edit page, like the old modal forms did.
document.addEventListener('keydown', e => {
    if (e.key !== 'Enter' || e.repeat || e.isComposing || e.target.tagName !== 'INPUT' || e.target.type === 'file') return;
    const save = $('edit-save');
    if (save && !save.disabled && save.closest('#set-body') && e.target.closest('#set-body')) { e.preventDefault(); save.click(); }
});

const dangerCard = (what, fn) => setCard(`Delete ${what}`, 'This can\'t be undone.', '', `<button class="btn btn-danger" onclick="confirmAction(this, () => ${fn})">Delete ${what}</button>`, 'danger');

function segField(label, id, options, current, onchange) {
    return `<div class="fld"><label>${label}</label><div class="seg" id="${id}">${options.map(([v, l]) => `<button class="seg-btn ${current === v ? 'active' : ''}" data-value="${v}" onclick="segPick('${id}','${v}'${onchange ? `,${onchange}` : ''})">${l}</button>`).join('')}</div></div>`;
}
function segPick(id, value, cb) {
    document.querySelectorAll(`#${id} .seg-btn`).forEach(b => b.classList.toggle('active', b.dataset.value === value));
    if (cb) cb(value);
}
function segValue(id) { const b = document.querySelector(`#${id} .seg-btn.active`); return b ? b.dataset.value : ''; }

// --- printer ---

async function buildPrinterEdit(id) {
    let cfg = {name: '', model: '', hide_model: false, type: 'octoprint', url: '', username: 'maker', poll_interval: 10, idle_timeout_minutes: 0, max_bed_temp: 0, max_extruder_temp: 0};
    let apiKey = '';
    if (id) {
        const p = printers.find(p => p.config.id === id);
        if (!p) throw new Error('missing');
        cfg = p.config;
        try {
            const data = await getJSON(`/api/printers/${id}`);
            apiKey = data.api_key || '';
            if (data.username) cfg = {...cfg, username: data.username};
        } catch (e) {}
    }
    const pl = cfg.type === 'prusalink';
    return {
        title: id ? cfg.name : 'Add printer',
        save: `savePrinter(${id || 'null'})`,
        bar: '',
        html: `<input type="hidden" id="p-id" value="${id || ''}">`
            + setCard('Printer', '', `<div class="grid2">
                ${fld('Name', 'p-name', cfg.name, {ph: 'My Printer', req: true})}
                ${fld('Model', 'p-model', cfg.model || '', {ph: 'e.g. Core One, MK4S (optional)'})}
            </div>${swrow('p-hide-model', !!cfg.hide_model, 'Hide model on dashboard card')}`)
            + setCard('Connection', '', `${segField('Type', 'p-type', [['octoprint', 'OctoPrint'], ['prusalink', 'PrusaLink']], cfg.type, 'printerTypeChanged')}
                <div class="grid2" style="margin-top:1rem">
                    ${fld('URL', 'p-url', cfg.url, {ph: 'http://192.168.1.40', wide: true, req: true})}
                    <div class="fld" id="p-user-wrap" ${pl ? '' : 'hidden'}><label for="p-user">Username</label><div class="inp"><input id="p-user" type="text" value="${esc(cfg.username || 'maker')}" placeholder="maker"></div></div>
                    <div class="fld"><label for="p-key" id="p-key-label">${pl ? 'Password' : 'API Key'}</label><div class="inp"><input id="p-key" type="text" required value="${esc(apiKey)}" placeholder="${pl ? 'Your PrusaLink password' : 'Your OctoPrint API key'}"></div></div>
                    ${fld('Poll interval (seconds)', 'p-poll', cfg.poll_interval || 10, {type: 'number', min: 1, max: 300, unit: 'seconds'})}
                </div>
                <div class="test-row"><button class="btn" onclick="testPrinterConnection()">Test connection</button><span id="p-test" class="test-line" role="status"></span></div>`)
            + setCard('Power & safety', 'Only applies if a smart plug is assigned to this printer. Overridden by the global setting if set.', `<div class="grid2">
                ${fld('Auto-off after idle (minutes)', 'p-idle', cfg.idle_timeout_minutes || 0, {type: 'number', min: 0, max: 1440, ph: '0 = disabled', unit: 'minutes'})}
                <span></span>
                ${fld('Thermal runaway: max bed temp (°C)', 'p-max-bed', cfg.max_bed_temp || 0, {type: 'number', step: 'any', min: 0, max: 150, ph: '0 = disabled', unit: '&deg;C'})}
                ${fld('Thermal runaway: max extruder temp (°C)', 'p-max-ext', cfg.max_extruder_temp || 0, {type: 'number', step: 'any', min: 0, max: 350, ph: '0 = disabled', unit: '&deg;C'})}
            </div>`)
            + (id ? dangerCard('printer', `deletePrinter(${id})`) : ''),
    };
}

function printerTypeChanged(type) {
    const pl = type === 'prusalink';
    $('p-user-wrap').hidden = !pl;
    $('p-key-label').textContent = pl ? 'Password' : 'API Key';
    $('p-key').placeholder = pl ? 'Your PrusaLink password' : 'Your OctoPrint API key';
    if (pl && !val('p-user')) $('p-user').value = 'maker';
}

async function testPrinterConnection() {
    const el = $('p-test');
    const url = val('p-url').replace(/\/+$/, ''), key = val('p-key'), type = segValue('p-type');
    el.className = 'test-line';
    if (!url || !key) { el.className = 'test-line err'; el.textContent = 'URL and API key are required'; return; }
    el.textContent = 'Testing connection...';
    try {
        const body = {type, url, api_key: key};
        if (type === 'prusalink') body.username = val('p-user');
        const resp = await fetch('/api/test', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
        const data = await resp.json();
        if (data.success) {
            el.className = 'test-line ok';
            el.textContent = 'Connection successful!';
            if (!val('p-name') && data.name) $('p-name').value = data.name;
        } else {
            el.className = 'test-line err';
            el.textContent = `Connection failed: ${data.error}`;
        }
    } catch (e) {
        el.className = 'test-line err';
        el.textContent = 'Connection test failed';
    }
}

async function savePrinter(id) {
    const type = segValue('p-type');
    const data = {
        name: val('p-name'), type, model: val('p-model'), hide_model: isOn('p-hide-model'),
        url: val('p-url').replace(/\/+$/, ''), api_key: val('p-key'),
        username: type === 'prusalink' ? val('p-user') : '',
        poll_interval: parseInt(val('p-poll')) || 10,
        idle_timeout_minutes: parseInt(val('p-idle')) || 0,
        max_bed_temp: parseFloat(val('p-max-bed')) || 0,
        max_extruder_temp: parseFloat(val('p-max-ext')) || 0,
        enabled: true,
    };
    try {
        const resp = await fetch(id ? `/api/printers/${id}` : '/api/printers', {
            method: id ? 'PUT' : 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data),
        });
        if (await checkOk(resp, 'Failed to save printer.')) {
            await fetchPrinters();
            goBack(editQuery, '#/settings/printers');
        }
    } catch (e) { netFail('Failed to save printer.'); }
}

// --- smart plug ---

async function buildPlugEdit(id) {
    let plug = {ip: '', mqtt_topic: '', idx: '1', label: '', hide_label: false, printer_id: null};
    if (id) {
        plug = (await getJSON('/api/smartplugs')).find(p => p.id === id);
        if (!plug) throw new Error('missing');
    }
    const mqtt = !!plug.mqtt_topic;
    return {
        title: id ? (plug.label || plug.ip || plug.mqtt_topic) : 'Add smart plug',
        save: `saveSmartPlug(${id || 'null'})`,
        html: setCard('Smart plug (Tasmota)', '', `${segField('Connection', 'pl-mode', [['http', 'Direct HTTP'], ['mqtt', 'MQTT']], mqtt ? 'mqtt' : 'http', 'plugModeChanged')}
                <div class="grid2" style="margin-top:1rem">
                    ${fld('IP', 'pl-ip', plug.ip, {ph: '192.168.1.60', wide: true, req: true, attrs: `id="pl-ip-wrap" ${mqtt ? 'hidden' : ''}`})}
                    ${fld('MQTT topic', 'pl-topic', plug.mqtt_topic || '', {ph: 'tasmota_ABC123', wide: true, req: true, attrs: `id="pl-topic-wrap" ${mqtt ? '' : 'hidden'}`, hint: 'The device\'s Topic, as configured in its own Tasmota console (Configuration → MQTT).'})}
                    ${fld('Relay index', 'pl-idx', plug.idx || '1', {hint: 'Which relay to control on multi-relay Tasmota devices. Leave as 1 for a normal single-outlet plug.'})}
                    ${fld('Label', 'pl-label', plug.label || '', {ph: 'Printer', on: 'plugPreview()'})}
                </div>${swrow('pl-hide-label', !!plug.hide_label, 'Hide label on dashboard power button', '', 'plugPreview')}`)
            + setCard('Assignment', '', `<div class="grid2">${selectFld('Assigned printer', 'pl-printer', printerOptions(true), plug.printer_id || '')}
                <div class="fld"><label>On the dashboard</label><div class="pvbox" id="pl-preview"></div></div></div>`)
            + (id ? dangerCard('plug', `deleteSmartPlug(${id})`) : ''),
        after: plugPreview,
    };
}

function plugModeChanged(mode) {
    $('pl-ip-wrap').hidden = mode === 'mqtt';
    $('pl-topic-wrap').hidden = mode !== 'mqtt';
}

function plugPreview() {
    const el = $('pl-preview');
    if (!el) return;
    const hide = isOn('pl-hide-label');
    el.innerHTML = `<span class="plug on"><span class="knob"></span>${hide ? '' : esc(val('pl-label') || 'Printer')}</span><small>${hide ? 'Label hidden: just the switch.' : 'Label shown next to the switch.'}</small>`;
}

async function saveSmartPlug(id) {
    const mqtt = segValue('pl-mode') === 'mqtt';
    const printerIdStr = val('pl-printer');
    const data = {
        ip: mqtt ? '' : val('pl-ip'), mqtt_topic: mqtt ? val('pl-topic') : '',
        idx: val('pl-idx') || '1', label: val('pl-label'), hide_label: isOn('pl-hide-label'),
        printer_id: printerIdStr ? parseInt(printerIdStr) : null,
    };
    try {
        const resp = await fetch(id ? `/api/smartplugs/${id}` : '/api/smartplugs', {
            method: id ? 'PUT' : 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data),
        });
        if (await checkOk(resp, 'Failed to save smart plug.')) goBack(editQuery, '#/settings/devices');
    } catch (e) { netFail('Failed to save smart plug.'); }
}

// --- camera ---

let editCamSettings = null;

async function buildCameraEdit(id) {
    let cam = {url: '', name: '', printer_id: null};
    editCamSettings = null;
    if (id) {
        cam = (await getJSON('/api/cameras')).find(c => c.id === id);
        if (!cam) throw new Error('missing');
        editCamSettings = await fetch(`/api/cameras/${id}/settings`).then(r => r.ok ? r.json() : null).catch(() => null);
    }
    const cs = editCamSettings || {};
    const res = cs.resolution !== undefined ? String(cs.resolution) : '13';
    const q = cs.quality !== undefined ? cs.quality : 12;
    return {
        title: id ? (cam.name || cam.url) : 'Add camera',
        save: `saveCamera(${id || 'null'})`,
        html: setCard('Camera (printspy-cam)', '', `<div class="grid2">
                ${fld('URL', 'c-url', cam.url, {type: 'url', ph: 'http://printspy-cam-abcdef.local', wide: true, req: true})}
                ${fld('Name', 'c-name', cam.name || '', {ph: 'Printer'})}
                ${selectFld('Assigned printer', 'c-printer', printerOptions(true), cam.printer_id || '', {hint: 'Overrides that printer\'s own webcam, if any.'})}
            </div>`)
            + (id ? setCard('Camera settings', 'Applied live on the camera itself.', `<div class="cam-set">
                <div class="cam-form">
                    <div><a class="printer-link" href="${esc(safeUrl(cam.url))}" target="_blank" rel="noopener">Open camera web UI &#8599;</a></div>
                    ${selectFld('Resolution', 'c-res', CAMERA_RESOLUTIONS, res, {on: 'saveCameraOrientation()'})}
                    <div class="fld"><label for="c-quality">JPEG quality <b id="c-quality-val">${q}</b> <small class="inl">(lower = better/slower)</small></label>
                        <input class="range" id="c-quality" type="range" min="4" max="40" step="1" value="${q}" oninput="$('c-quality-val').textContent=this.value" onchange="saveCameraOrientation()"></div>
                    ${swrow('c-hmirror', !!cs.hmirror, 'Mirror horizontally', '', 'camTransformChanged')}
                    ${swrow('c-vflip', !!cs.vflip, 'Flip vertically', 'Applied live on the camera itself.', 'camTransformChanged')}
                </div>
                ${cam.printer_id ? `<div class="cam-prev"><div class="ev-prev-label">Preview</div><div class="cam-img"><img id="c-preview" src="/api/snapshot/${cam.printer_id}?t=${Date.now()}" alt=""></div></div>` : ''}
            </div>`) : '')
            + (id ? dangerCard('camera', `deleteCamera(${id})`) : ''),
        after: () => { if (id) camPreviewTransform(); },
    };
}

function camTransformChanged() {
    saveCameraOrientation();
    camPreviewTransform();
}

function camPreviewTransform() {
    const img = $('c-preview');
    if (img) img.style.transform = `${isOn('c-hmirror') ? 'scaleX(-1) ' : ''}${isOn('c-vflip') ? 'scaleY(-1)' : ''}` || 'none';
}

async function saveCameraOrientation() {
    const m = location.hash.match(/\/camera\/(\d+)/);
    if (!m || !$('c-res')) return;
    const data = {
        hmirror: isOn('c-hmirror'), vflip: isOn('c-vflip'),
        resolution: parseInt(val('c-res'), 10), quality: parseInt(val('c-quality'), 10),
    };
    try {
        const resp = await fetch(`/api/cameras/${m[1]}/settings`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)});
        await checkOk(resp, 'Failed to save camera settings.');
    } catch (e) { netFail('Failed to save camera settings.'); }
}

async function saveCamera(id) {
    const printerIdStr = val('c-printer');
    const data = {url: val('c-url'), name: val('c-name'), printer_id: printerIdStr ? parseInt(printerIdStr) : null};
    try {
        const resp = await fetch(id ? `/api/cameras/${id}` : '/api/cameras', {
            method: id ? 'PUT' : 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data),
        });
        if (await checkOk(resp, 'Failed to save camera.')) goBack(editQuery, '#/settings/devices');
    } catch (e) { netFail('Failed to save camera.'); }
}

// --- slicer target ---

function targetHostBox(label, apiKey) {
    const host = `${location.origin}/ingest/${label}`;
    return `<div class="fld" style="margin-top:1rem"><label>Host and API key for the slicer</label>
        <div class="copybox"><code>Host: ${esc(host)}</code></div>
        <div class="copybox"><code>API key: ${esc(apiKey)}</code></div></div>`;
}

async function buildTargetEdit(id) {
    let target = {label: '', printer_id: null, api_key: ''};
    if (id) {
        target = (await getJSON('/api/ingest-keys')).find(t => t.id === id);
        if (!target) throw new Error('missing');
    }
    const options = printerOptions(false, 'prusalink');
    return {
        title: id ? (target.label || printerName(target.printer_id) || 'Slicer target') : 'Add slicer target',
        save: `saveIngestKey(${id || 'null'})`,
        html: setCard('Slicer target', '', `<div class="grid2">
                ${selectFld('Printer', 't-printer', options, target.printer_id || (options[0] ? options[0][0] : ''), {hint: 'Files the slicer sends here relay to this printer automatically - no manual step. "Upload" relays once it\'s online; "Upload and Print" also powers it on if it\'s off.'})}
                ${fld('Label', 't-label', target.label || '', {ph: 'Optional, e.g. coreone', hint: 'Lowercase letters, numbers, and hyphens only - also becomes the ingest URL: /ingest/&lt;label&gt; instead of /ingest/&lt;id&gt;.'})}
            </div><div id="t-result" role="status">${id ? targetHostBox(target.label || id, target.api_key) : ''}</div>`)
            + (id ? dangerCard('slicer target', `deleteIngestKey(${id})`) : ''),
    };
}

async function saveIngestKey(id) {
    const result = $('t-result');
    const data = {
        printer_id: parseInt(val('t-printer')),
        label: val('t-label').trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''),
    };
    try {
        const resp = await fetch(id ? `/api/ingest-keys/${id}` : '/api/ingest-keys', {
            method: id ? 'PUT' : 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data),
        });
        if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            result.innerHTML = `<div class="test-line err">${esc(err.error || 'Failed to save.')}</div>`;
            return;
        }
        if (!id) {
            const created = await resp.json();
            result.innerHTML = targetHostBox(data.label || created.id, created.api_key) + '<small>Paste these into the slicer\'s Physical Printer settings, then save.</small>';
            $('edit-save').remove();
            document.querySelector('.edit-bar .btn-quiet').textContent = 'Done';
            return;
        }
        goBack(editQuery, '#/settings/devices');
    } catch (e) { netFail('Failed to save slicer target.'); }
}

const EDIT_BUILDERS = {printer: buildPrinterEdit, plug: buildPlugEdit, camera: buildCameraEdit, target: buildTargetEdit};
