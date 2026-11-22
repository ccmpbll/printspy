let printers = [];
let pollCounter = 0;
let prevPrinterIDs = [];
let eventSource = null;
let statusCache = {};

// State → card border / idle-message styling and default text, keyed once
// instead of re-hand-written at every call site (render, incremental update).
const STATE_META = {
    error:        {cardClass: 'card-error', msgClass: 'msg-error', defaultMsg: 'Printer reported an error'},
    attention:    {cardClass: 'card-attention', msgClass: 'msg-attention', defaultMsg: 'Printer needs attention'},
    offline:      {cardClass: 'card-offline', msgClass: 'msg-offline', defaultMsg: 'Unable to reach printer'},
    disconnected: {cardClass: 'card-disconnected', msgClass: 'msg-disconnected', defaultMsg: 'Printer disconnected', ignoreDetail: true},
    busy:         {cardClass: 'card-busy', msgClass: 'msg-busy', defaultMsg: 'Printer busy', ignoreDetail: true},
};

function stateCardClass(state) {
    return `printer-card ${STATE_META[state] ? STATE_META[state].cardClass : ''}`;
}

function stateIdleMsgClass(state) {
    return STATE_META[state] ? `idle-message ${STATE_META[state].msgClass}` : 'idle-message';
}

function stateIdleMsgText(state, detailMsg) {
    const meta = STATE_META[state];
    if (!meta) return 'Ready for next job';
    return meta.ignoreDetail ? meta.defaultMsg : (detailMsg || meta.defaultMsg);
}

// Failed-request helpers. checkOk alerts the server's own error (or
// fallback) for a non-2xx response and returns whether resp was ok;
// netFail is the catch-all for a request that never got a response.
async function checkOk(resp, fallback) {
    if (resp.ok) return true;
    const data = await resp.json().catch(() => ({}));
    alert(data.error || fallback);
    return false;
}

function netFail(fallback) {
    alert(`${fallback} (network error)`);
}

// localStorage throws in Safari private browsing and when storage is
// blocked/full - a preference must never take the dashboard down with it.
function lsGet(key) {
    try { return localStorage.getItem(key); } catch (e) { return null; }
}

function lsSet(key, value) {
    try { localStorage.setItem(key, value); } catch (e) {}
}

// SSE connection

let sseRetryDelay = 2000;

// EventSource retries on its own for network drops, but a non-2xx or
// wrong-content-type response (expired session, proxy error page) closes it
// for good. Find out why, then either go sign in again or retry with backoff.
async function recoverSSE() {
    try {
        const resp = await fetch('/api/printers', {redirect: 'manual'});
        if (resp.status === 401 || resp.type === 'opaqueredirect') {
            location.href = '/login';
            return;
        }
    } catch (e) {}
    setTimeout(connectSSE, sseRetryDelay);
    sseRetryDelay = Math.min(sseRetryDelay * 2, 30000);
}

function connectSSE() {
    if (eventSource) eventSource.close();

    eventSource = new EventSource('/api/events');

    eventSource.addEventListener('init', (e) => {
        printers = JSON.parse(e.data);
        printers.forEach(p => { if (p.status) statusCache[p.config.id] = p.status; });
        printersSig = JSON.stringify(printers.map(p => ({...p, status: null})));
        prevPrinterIDs = [];
        updateDashboard();
        loadIngestJobs();
        showConnectionBanner(false);
        sseRetryDelay = 2000;
    });

    eventSource.addEventListener('status', (e) => {
        const msg = JSON.parse(e.data);
        applyStatusUpdate(msg.printer_id, msg.status);
        showConnectionBanner(false);
        sseRetryDelay = 2000;
    });

    eventSource.addEventListener('refresh', () => {
        fetchPrinters();
        loadIngestJobs();
    });

    eventSource.addEventListener('error', (e) => {
        console.warn(`[sse] connection error at ${new Date().toISOString()}, readyState=${eventSource.readyState}`, e);
        showConnectionBanner(true);
        if (eventSource.readyState === EventSource.CLOSED) recoverSSE();
    });
}

// A phone suspends a backgrounded page and silently kills its SSE connection,
// and EventSource may still report OPEN when the app comes back. Whenever the
// page returns to the foreground (or the network comes back), reconnect: the
// 'init' event re-sends the full state, so nothing needs a manual refresh.
let hiddenAt = 0;
function resumeConnection() {
    sseRetryDelay = 2000;
    connectSSE();
    refreshSnapshots();
}
document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') { hiddenAt = Date.now(); return; }
    if (hiddenAt && Date.now() - hiddenAt > 3000) resumeConnection();
    hiddenAt = 0;
});
// Restored from the back/forward cache (iOS does this for PWAs too).
window.addEventListener('pageshow', e => { if (e.persisted) resumeConnection(); });
window.addEventListener('online', resumeConnection);

// Applies a fresh PrinterStatus to printers[]/statusCache and patches the
// card - shared by the SSE 'status' listener and any action (print/pause/
// cancel/resume) whose own response already carries the post-repoll status,
// so those don't need to wait on a separate, unordered SSE round-trip to
// reflect what just happened.
function applyStatusUpdate(printerId, status) {
    // An action's own response can arrive after a newer SSE update - applying
    // it would roll the card back (e.g. flip to "Printing" after a pause).
    const cur = statusCache[printerId];
    if (cur && cur.last_updated && status.last_updated && Date.parse(status.last_updated) < Date.parse(cur.last_updated)) return;
    statusCache[printerId] = status;
    const printer = printers.find(p => p.config.id === printerId);
    if (printer) {
        printer.status = status;
        const card = document.querySelector(`[data-printer-id="${printerId}"]`);
        if (card) updateCard(card, printer);
    }
    updateHeaderCount();
}

function showConnectionBanner(show) {
    const banner = document.getElementById('connection-banner');
    if (banner) banner.style.display = show ? 'flex' : 'none';
}

function updateHeaderCount() {
    updateFleet();
    const control = document.getElementById('power-controls');
    if (!printers || printers.length === 0) {
        control.style.display = 'none';
        return;
    }
    const hasPower = printers.some(p => p.status && p.status.power && p.status.power.length > 0);
    control.style.display = hasPower ? 'inline-flex' : 'none';
}

function formatHistoryDate(isoStr) {
    if (!isoStr) return '';
    const d = new Date(isoStr);
    return d.toLocaleDateString(undefined, {year: 'numeric', month: 'short', day: 'numeric'});
}

function formatDuration(secs) {
    if (!secs) return '0m';
    const h = Math.floor(secs / 3600);
    const m = Math.round((secs % 3600) / 60);
    return h > 0 ? `${h}h${m}m` : `${m}m`;
}

function totalWatts(power) {
    if (!power || !power.length) return 0;
    return power.reduce((sum, p) => sum + (p.watts || 0), 0);
}

function formatDate(unixTs) {
    if (!unixTs) return '';
    const d = new Date(unixTs * 1000);
    const now = new Date();
    const diff = now - d;
    if (diff < 86400000) return 'today';
    if (diff < 172800000) return 'yesterday';
    if (diff < 604800000) return `${Math.floor(diff / 86400000)}d ago`;
    return d.toLocaleDateString();
}

// Print control

async function controlPrint(printerId, action) {
    try {
        const resp = await fetch(`/api/printers/${printerId}/control`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({action}),
        });
        const data = await resp.json().catch(() => ({}));
        if (resp.ok) {
            if (data.status) applyStatusUpdate(printerId, data.status);
        } else {
            alert(data.error || 'Print command failed.');
        }
    } catch (e) { netFail('Print command failed.'); }
}

// Power control

async function setPower(printerId, action, plugId) {
    try {
        const resp = await fetch(`/api/printers/${printerId}/power`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({action, plug_id: plugId}),
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            alert(data.error || 'Failed to change power state.');
        }
    } catch (e) {
        alert('Failed to change power state.');
    }
}

async function bulkPower(action) {
    try {
        const resp = await fetch('/api/printers/power', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({action}),
        });
        await checkOk(resp, 'Failed to change power state.');
    } catch (e) { netFail('Failed to change power state.'); }
}

// The dashboard stays in the DOM under Settings/Files/History: stop pulling
// camera frames while it is hidden, and close live streams (reopened on return).
function setDashboardActive(active) {
    document.querySelectorAll('.webcam-img').forEach(img => {
        if (!active && img.src.includes('/api/webcam/')) {
            img.dataset.liveSrc = img.getAttribute('src');
            img.removeAttribute('src');
        } else if (active && img.dataset.liveSrc) {
            img.src = img.dataset.liveSrc;
            delete img.dataset.liveSrc;
        }
    });
}

function refreshSnapshots() {
    if (document.getElementById('dashboard').hidden) return;
    pollCounter++;
    document.querySelectorAll('.webcam-img').forEach(img => {
        const id = img.closest('[data-printer-id]')?.dataset.printerId;
        if (!id) return;
        const mode = getWebcamMode(parseInt(id));
        if (mode === 'live') return;
        // A hidden (errored) img only gets retried when a real camera is
        // assigned - a camera-less printer's img has nothing to reconnect
        // to, it's just sitting there permanently failed by design. Plate
        // mode is the exception: thumbnail availability can change the
        // moment a print starts regardless of camera assignment, so it's
        // always worth retrying.
        if (mode === 'plate' || img.style.display !== 'none' || img.dataset.hasCamera === 'true') {
            img.src = mode === 'plate' ? `/api/thumbnail/${id}?t=${pollCounter}` : `/api/snapshot/${id}?t=${pollCounter}`;
        }
    });
    // The print-thumbnail fallback (see webcamError()) is a one-shot attempt
    // that can lose the race against PrusaLink exposing the thumbnail ref
    // moments after a print starts - retry it here too, same as the webcam
    // img above, as long as it's still sitting in its failed (hidden) state.
    // Reassigning .src re-fires the onload/onerror handlers webcamError()
    // already attached to this element, no need to re-set them. Skipped
    // entirely for plate mode - the main img above already retries the
    // thumbnail directly, this fallback element never gets shown there.
    document.querySelectorAll('.webcam-print-thumb').forEach(thumb => {
        const placeholder = thumb.closest('.webcam-placeholder');
        const printerId = thumb.closest('[data-printer-id]')?.dataset.printerId;
        if (!printerId || getWebcamMode(parseInt(printerId)) === 'plate') return;
        if (placeholder && placeholder.style.display !== 'none' && thumb.style.display === 'none') {
            thumb.src = `/api/thumbnail/${printerId}?t=${pollCounter}`;
        }
    });
}

// Webcam mode - snapshot/live/plate, client-side only (localStorage),
// same as the previous snapshot/live toggle just extended to a third
// option. Buttons live under the image rather than on it (see
// renderPrinterCard) so a full card re-render on click is simplest and
// correct - it naturally handles LIVE being disabled, the corner
// thumb-beside box appearing/disappearing for plate mode, etc, all through
// the same render path the regular poll tick already uses.

function getWebcamMode(printerId) {
    const mode = lsGet(`webcam-mode-${printerId}`);
    return mode === 'snapshot' || mode === 'live' ? mode : 'plate';
}

function setWebcamMode(printerId, mode) {
    lsSet(`webcam-mode-${printerId}`, mode);
    const printer = printers.find(p => p.config.id === printerId);
    const card = document.querySelector(`[data-printer-id="${printerId}"]`);
    if (!printer || !card) return;
    card.outerHTML = renderPrinterCard(printer);
    renderIngestBanners();
}

function webcamError(img, isPrinting, tryThumb, hasCamera, mode) {
    const container = img.parentElement;
    img.style.display = 'none';
    container.querySelector('.webcam-placeholder').style.display = 'flex';

    const text = container.querySelector('.webcam-placeholder-text');
    if (mode === 'plate') {
        // Plate mode only ever attempts the thumbnail - nothing left to
        // fall back to if that itself fails. This only fires when tryThumb
        // was already true (idle or printing - see camAttempt), so the two
        // real cases are "idle, nothing's ever been loaded" and the rarer
        // "printing, but this file has no embedded thumbnail". Never
        // collapses - keeps the box at its normal size instead of the
        // layout jumping around every time the current job/thumbnail
        // changes.
        text.textContent = isPrinting ? 'No plate thumbnail available' : 'No job running';
        text.style.display = 'block';
        container.classList.remove('webcam-collapsed');
        return;
    }

    // No camera assigned - fall back to the print thumbnail (current job, or
    // last loaded file) rather than collapsing the space immediately, but
    // only when idle/printing - a decorative plate render doesn't belong
    // next to an error/attention/offline card, it just competes with the
    // actual message for space. Only collapse (when idle) once the
    // thumbnail also fails to load. Collapsing the container (not the
    // mode-button row below it) keeps SNAP/LIVE/PLATE always reachable
    // regardless of whether the current attempt succeeded.
    //
    // A printer WITH an assigned camera never auto-collapses on failure,
    // even when idle - the "Camera unreachable" text is a real diagnostic
    // signal about hardware you deliberately configured, not a decorative
    // fallback, and collapsing right after showing it made it effectively
    // unseeable outside of active prints.
    const thumb = container.querySelector('.webcam-print-thumb');
    const card = container.closest('[data-printer-id]');
    const printerId = card?.dataset.printerId;
    if (tryThumb && printerId) {
        thumb.onload = () => {
            thumb.style.display = 'block';
            text.style.display = 'none';
            container.classList.remove('webcam-collapsed');
            // The corner thumb-beside box (printer-stats) shows the same
            // plate render this fallback just filled the main webcam slot
            // with - would otherwise duplicate it whenever an assigned
            // camera goes unreachable mid-print.
            const beside = card?.querySelector('.thumb-beside');
            if (beside) beside.style.display = 'none';
        };
        thumb.onerror = () => {
            thumb.style.display = 'none';
            text.style.display = 'block';
            if (!isPrinting && !hasCamera) container.classList.add('webcam-collapsed');
        };
        thumb.src = `/api/thumbnail/${printerId}?t=${pollCounter}`;
    } else if (!isPrinting && !hasCamera) {
        container.classList.add('webcam-collapsed');
    }
}

// Reverses webcamError()'s fallback state when a retried camera load
// actually succeeds - fires on every successful load, including the normal
// non-error case, where it's a harmless no-op (everything's already in the
// state it's setting).
function webcamRecovered(img) {
    const container = img.parentElement;
    container.querySelector('.webcam-placeholder').style.display = 'none';
    container.classList.remove('webcam-collapsed');
    const beside = container.closest('[data-printer-id]')?.querySelector('.thumb-beside');
    if (beside) beside.style.display = '';
}

function webcamSrc(printerId, mode) {
    if (mode === 'plate') return `/api/thumbnail/${printerId}?t=${pollCounter}`;
    if (mode === 'live') return `/api/webcam/${printerId}`;
    return `/api/snapshot/${printerId}?t=${pollCounter}`;
}

// Fetch full printer list
let fetchPrintersReq = 0;
let printersSig = '';

async function fetchPrinters() {
    const req = ++fetchPrintersReq;
    try {
        const resp = await fetch('/api/printers');
        if (!resp.ok) return;
        const data = await resp.json();
        if (req !== fetchPrintersReq) return; // a newer fetch superseded this one
        printers = data;
        // Rebuild the cards only when their static data changed. A forced
        // rebuild on every call (SSE 'refresh' fires on any plug/camera/
        // settings change) restarts every webcam stream for nothing; live
        // status travels separately via SSE, so it's excluded from the compare.
        const sig = JSON.stringify(printers.map(p => ({...p, status: null})));
        if (sig !== printersSig) {
            printersSig = sig;
            prevPrinterIDs = [];
        }
        updateDashboard();
    } catch (e) {}
}

function updateDashboard() {
    const list = document.getElementById('printer-list');

    if (!printers || printers.length === 0) {
        updateHeaderCount();
        list.innerHTML = `
            <div class="empty-state">
                <h2>No printers configured</h2>
                <p>Open settings to add your first printer.</p>
                <button class="btn btn-primary" onclick="openSettings()">Open settings</button>
            </div>`;
        prevPrinterIDs = [];
        return;
    }

    updateHeaderCount();

    const currentIDs = printers.map(p => `${p.config.id}:${p.has_camera}:${p.config.maintenance}`);
    const structureChanged = JSON.stringify(currentIDs) !== JSON.stringify(prevPrinterIDs);

    if (structureChanged) {
        list.innerHTML = printers.map(p => renderPrinterCard(p)).join('');
        prevPrinterIDs = currentIDs;
        renderIngestBanners();
        applyFleetFilter();
        if (document.getElementById('dashboard').hidden) setDashboardActive(false); // rebuilt while hidden: no live streams
    }
}

const ICON_PAUSE = '<svg class="ic-pause" width="11" height="11" viewBox="0 0 12 12" fill="currentColor" aria-hidden="true"><rect x="1.4" y="0.8" width="3.3" height="10.4" rx="1.1"/><rect x="7.3" y="0.8" width="3.3" height="10.4" rx="1.1"/></svg>';
const ICON_MORE = '<svg width="22" height="22" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.8"/><circle cx="12" cy="12" r="1.8"/><circle cx="19" cy="12" r="1.8"/></svg>';

function renderMaintenanceCard(printer) {
    const cfg = printer.config;
    return `
        <div class="printer-card card-maintenance" data-printer-id="${cfg.id}" data-state="maintenance">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                ${cfg.model && !cfg.hide_model ? `<span class="printer-model">${esc(cfg.model)}</span>` : ''}
                <span class="printer-state state-maintenance">Maintenance</span>
            </div>
            <div class="printer-body">
                <div class="idle-message" data-field="idle-msg">In maintenance — polling paused</div>
            </div>
        </div>`;
}

// One plug as a switch. The printer's own plug is locked on while a print is
// running (turning the printer off mid-print is never what you want).
function plugInfo(ps, state, singlePlug) {
    const isBusy = state === 'printing' || state === 'paused' || state === 'busy';
    const isPrinterPlug = singlePlug || (ps.label && ps.label.toLowerCase().includes('printer'));
    const locked = !!(ps.on && isBusy && isPrinterPlug);
    const title = locked ? 'Cannot turn off printer while printing' : autoOffTooltip(ps.source);
    const label = ps.label && !ps.hide_label ? ps.label : '';
    return {locked, title, label, sig: `${ps.on}|${locked}|${title}|${label}`};
}

function plugHTML(cfg, ps, state, singlePlug) {
    const {locked, title, label, sig} = plugInfo(ps, state, singlePlug);
    return `<button type="button" role="switch" aria-checked="${!!ps.on}"${locked ? ' aria-disabled="true"' : ''} class="plug ${ps.on ? 'on' : ''} ${locked ? 'locked' : ''}" data-field="power" data-plug-id="${esc(ps.id)}" data-sig="${esc(sig)}"${title ? ` title="${esc(title)}"` : ''} onclick="event.stopPropagation();togglePlug(${cfg.id}, this)"><span class="knob"></span>${esc(label)}${locked ? '<span class="lock">&#128274;</span>' : ''}</button>`;
}

function togglePlug(printerId, el) {
    if (el.classList.contains('locked')) return;
    setPower(printerId, el.classList.contains('on') ? 'off' : 'on', el.dataset.plugId);
}

function powerFootHTML(cfg, status, state) {
    if (!status || !status.power || status.power.length === 0) return '';
    const singlePlug = status.power.length === 1;
    const watts = Math.round(totalWatts(status.power));
    return `<div class="printer-foot" data-field="foot">
        ${status.power.map(ps => plugHTML(cfg, ps, state, singlePlug)).join('')}
        <span class="power-pill" data-field="watts-pill"${watts > 0 ? '' : ' hidden'}>Power <b data-field="watts">${watts}</b> W</span>
    </div>`;
}

function renderPrinterCard(printer) {
    const cfg = printer.config;
    if (cfg.maintenance) return renderMaintenanceCard(printer);
    const status = printer.status;
    const state = status ? status.state : 'offline';
    let stateLabel;
    if (state === 'error' && status && status.state_message) {
        stateLabel = `Error: ${status.state_message}`;
    } else if (state === 'disconnected') {
        stateLabel = 'Printer disconnected';
    } else {
        stateLabel = state.charAt(0).toUpperCase() + state.slice(1);
    }
    // !! coerces to a real boolean - status.job is an object, and && returns
    // the last truthy operand rather than true, which broke the onerror
    // attribute string built below (${isPrinting} interpolated an object as
    // literal "[object Object]", invalid JS, silently killing the handler).
    const isPrinting = !!((state === 'printing' || state === 'paused') && status && status.job);
    // A print-thumbnail fallback only makes sense on idle/printing (a
    // decorative plate render doesn't belong on an error/attention/offline/
    // disconnected card - see webcamError()).
    const tryThumb = state === 'idle' || isPrinting;
    // Live streaming needs either a plugin-reported webcam stream URL or an
    // assigned printspy-cam (snapshot-only plugins like PrusaLink report no
    // webcam URL of their own).
    const supportsLive = printer.has_webcam || printer.has_camera;
    let wcMode = getWebcamMode(cfg.id);
    if (wcMode === 'live' && !supportsLive) wcMode = 'snapshot';
    const isPlate = wcMode === 'plate';
    // An assigned printspy-cam is a separate device, independent of the
    // printer's own connectivity - still worth attempting even when the
    // printer itself is offline. Without one, this column's own
    // print-thumbnail fallback (see webcamError()) is what fills the camera
    // spot on idle/printing - only skip the attempt entirely (collapse up
    // front) when there's no camera assigned *and* the state wouldn't show
    // a thumbnail anyway. Plate mode never attempts the camera at all, so
    // it's gated on tryThumb alone.
    const camAttempt = isPlate ? tryThumb : (tryThumb || printer.has_camera);
    const cardClass = stateCardClass(state);
    // hideWebcamWhenUnreachable only kicks in for a genuinely unreachable
    // printer - not a blanket hide for every card regardless of state.
    const sectionHidden = hideWebcamWhenUnreachable && (state === 'offline' || state === 'disconnected');

    let controlHTML = '';
    if (state === 'printing') {
        controlHTML = `<span class="print-controls" data-field="print-controls"><button class="btn btn-pause" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'pause'))">${ICON_PAUSE} Pause</button><button class="btn btn-danger" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'cancel'))"><span class="icon-stop"></span> Cancel</button></span>`;
    } else if (state === 'paused') {
        controlHTML = `<span class="print-controls" data-field="print-controls"><button class="btn btn-primary" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'resume'))"><span class="icon-play"></span> Resume</button><button class="btn btn-danger" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'cancel'))"><span class="icon-stop"></span> Cancel</button></span>`;
    }

    const filesHTML = `<button class="btn" onclick="event.stopPropagation();location.hash='#/files/${cfg.id}'">Files</button>`;
    const historyHTML = `<button class="btn" onclick="event.stopPropagation();location.hash='#/history/${cfg.id}'">History</button>`;
    const menuHTML = `<span class="menu-wrap"><button class="btn btn-more" onclick="toggleMenu(event,'card-menu-${cfg.id}')" title="More" aria-haspopup="true">${ICON_MORE}</button><div class="menu" id="card-menu-${cfg.id}"><a href="#/settings/printer/${cfg.id}?from=card">&#9998; Edit printer</a><a href="${esc(safeUrl(cfg.url))}" target="_blank" rel="noopener">&#8599; Open ${esc(printer.display_name || 'printer')}</a></div></span>`;

    return `
        <div class="${cardClass}" data-printer-id="${cfg.id}" data-state="${state}">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                ${cfg.model && !cfg.hide_model ? `<span class="printer-model">${esc(cfg.model)}</span>` : ''}
                <span class="printer-state state-${state}" data-field="state">${esc(stateLabel)}</span>
                <span class="spacer"></span>
                <span class="printer-actions">${controlHTML}${filesHTML}${historyHTML}${menuHTML}</span>
            </div>
            <div class="printer-body">
                ${sectionHidden ? '' : `
                <div class="webcam-wrapper">
                    <div class="webcam-container ${camAttempt ? (isPlate || isPrinting || printer.has_camera ? '' : 'webcam-idle') : 'webcam-collapsed'}">
                        <img class="webcam-img" data-has-camera="${!!printer.has_camera}" ${camAttempt ? `src="${webcamSrc(cfg.id, wcMode)}"` : ''} alt="Webcam" onerror="webcamError(this,${isPrinting},${tryThumb},${!!printer.has_camera},'${wcMode}')" onload="webcamRecovered(this)">
                        <div class="webcam-placeholder" style="display:none">
                            <img class="webcam-print-thumb" style="display:none" alt="">
                            <span class="webcam-placeholder-text" data-field="webcam-placeholder-text">${(state === 'offline' && !printer.has_camera) ? 'No camera' : 'Camera Unreachable'}</span>
                        </div>
                    </div>
                    <div class="webcam-mode-row">
                        <button class="webcam-mode-btn ${isPlate ? 'active' : ''}" ${!tryThumb ? 'disabled' : ''} onclick="event.stopPropagation();setWebcamMode(${cfg.id},'plate')">PLATE</button>
                        <button class="webcam-mode-btn ${wcMode === 'snapshot' ? 'active' : ''}" onclick="event.stopPropagation();setWebcamMode(${cfg.id},'snapshot')">SNAP</button>
                        <button class="webcam-mode-btn ${wcMode === 'live' ? 'active' : ''}" ${!supportsLive ? 'disabled' : ''} onclick="event.stopPropagation();setWebcamMode(${cfg.id},'live')">LIVE</button>
                    </div>
                </div>`}
                <div class="printer-stats">
                    ${isPrinting ? renderPrintingStats(cfg, status, !sectionHidden && !isPlate && !!(printer.has_camera || printer.has_webcam)) : renderIdleStats(status, state)}
                </div>
            </div>
            ${powerFootHTML(cfg, status, state)}
        </div>`;
}

// Temperature cell: label, actual / target, and a bar filling toward target
// (green once it's there, amber while heating).
const TEMP_MAX = {hotend: 300, bed: 120, chamber: 60};

function tempFill(field, actual, target) {
    const t = Math.round(target);
    const atTarget = t > 0 && actual >= target - 3;
    const pct = Math.min(100, (t > 0 ? actual / target : (actual / TEMP_MAX[field]) * 0.5) * 100);
    return {atTarget, pct};
}

function tempCellHTML(label, field, actual, target) {
    const {atTarget, pct} = tempFill(field, actual, target);
    const t = Math.round(target);
    return `<div class="stat-item ${atTarget ? 'at-target' : ''}" data-cell="${field}">
        <div class="stat-label">${label}</div>
        <div class="stat-value" data-field="${field}">${Math.round(actual)}<span class="stat-unit">°C / ${t > 0 ? t + '°C' : 'off'}</span></div>
        <span class="temp-bar"><i data-field="${field}-bar" style="width:${pct}%"></i></span>
    </div>`;
}

function tempCellsHTML(temps) {
    const cells = [tempCellHTML('Hotend', 'hotend', temps.hotend_actual, temps.hotend_target),
                   tempCellHTML('Bed', 'bed', temps.bed_actual, temps.bed_target)];
    if (temps.has_chamber) cells.push(tempCellHTML('Chamber', 'chamber', temps.chamber_actual, temps.chamber_target));
    return `<div class="stat-grid">${cells.join('')}</div>`;
}

function renderPrintingStats(cfg, status, hasRealCam) {
    const job = status.job;
    const progress = Math.round(job.progress || 0);
    const layerCell = job.total_layers > 0
        ? `<div class="stat-item"><div class="stat-label">Layer</div><div class="stat-value" data-field="layer">${job.current_layer} <span class="stat-unit">/ ${job.total_layers}</span></div></div>`
        : '';

    return `
        <div class="file-and-thumb">
            <div class="file-and-progress">
                <div class="print-filename" data-field="filename" title="${esc(job.file_name)}">${esc(job.file_name)}</div>
                <div class="progress-row">
                    <div class="progress-bar"><div class="progress-fill" data-field="progress-bar" style="width: ${progress}%"></div></div>
                    <span class="progress-value" data-field="progress-text">${progress}%</span>
                </div>
            </div>
            ${hasRealCam ? `<div class="thumb-beside">
                <img src="/api/thumbnail/${cfg.id}?t=${pollCounter}" alt="Thumbnail" onerror="this.parentElement.style.display='none'">
            </div>` : ''}
        </div>
        <div class="stat-grid">
            <div class="stat-item"><div class="stat-label">ETA</div><div class="stat-value" data-field="eta">${computeETA(job.remaining_secs)}</div></div>
            <div class="stat-item"><div class="stat-label">Remaining</div><div class="stat-value" data-field="remaining">${formatTime(job.remaining_secs)}</div></div>
            <div class="stat-item"><div class="stat-label">Elapsed</div><div class="stat-value" data-field="elapsed">${formatTime(job.elapsed_secs)}</div></div>
            ${layerCell}
        </div>
        ${tempCellsHTML(status.temps)}`;
}

function renderIdleStats(status, state) {
    const temps = status ? status.temps : null;
    const detailMsg = status && status.state_message ? status.state_message : '';
    const stateMsg = stateIdleMsgText(state, detailMsg);

    const tempsHTML = temps && state !== 'offline' && state !== 'disconnected' ? tempCellsHTML(temps) : '';

    if (state === 'disconnected') {
        return '';
    }

    return `<div class="${stateIdleMsgClass(state)}" data-field="idle-msg">${esc(stateMsg)}</div>${tempsHTML}`;
}

function updateCard(card, printer) {
    const cfg = printer.config;
    const status = printer.status;
    const state = status ? status.state : 'offline';
    const prevState = card.dataset.state;
    // !! coerces to a real boolean - status.job is an object, and && returns
    // the last truthy operand rather than true, which broke the onerror
    // attribute string built below (${isPrinting} interpolated an object as
    // literal "[object Object]", invalid JS, silently killing the handler).
    const isPrinting = !!((state === 'printing' || state === 'paused') && status && status.job);
    const wasPrinting = (prevState === 'printing' || prevState === 'paused');

    const wasDown = prevState === 'offline' || prevState === 'disconnected';
    const isDown = state === 'offline' || state === 'disconnected';

    const hadPower = !!card.querySelector('[data-field="power"]');
    const hasPower = status && status.power && status.power.length > 0;

    // Whether the webcam-failure fallback tries a print thumbnail is baked
    // into the onerror handler at render time (see renderPrinterCard) - it
    // needs a full re-render whenever that eligibility flips, or a stale
    // thumbnail attempt from a prior idle/printing state keeps firing (e.g.
    // idle -> error still showing the old thumbnail instead of the error
    // placeholder).
    const wasThumbEligible = prevState === 'idle' || wasPrinting;
    const isThumbEligible = state === 'idle' || isPrinting;

    // An assigned printspy-cam's camAttempt is always true regardless of
    // printer state (see renderPrinterCard), so the offline/online boundary
    // never actually changes anything about its webcam element - forcing a
    // full rebuild here would only tear down and reconnect an already-fine
    // camera feed, showing a blank gap while it reloads for no reason.
    // Camera-less printers still need it, to toggle the collapsed
    // placeholder and its "No camera"/"Camera unreachable" text. That
    // optimization only holds when hideWebcamWhenUnreachable is off though -
    // with it on, the whole section's presence depends on this exact
    // boundary even for a camera-equipped printer.
    const downTransitionNeedsRebuild = (wasDown !== isDown) && (!printer.has_camera || hideWebcamWhenUnreachable);

    // Pause/Resume swaps controlHTML's button set (Pause/Cancel <->
    // Resume/Cancel), but isPrinting is true for both printing and paused -
    // that transition alone wouldn't otherwise cross any of the rebuild
    // triggers above, leaving stale buttons after a pause or resume.
    const pausedChanged = (prevState === 'paused') !== (state === 'paused');

    if ((isPrinting && !wasPrinting) || (!isPrinting && wasPrinting) || pausedChanged || downTransitionNeedsRebuild || (hasPower && !hadPower) || (wasThumbEligible !== isThumbEligible)) {
        card.outerHTML = renderPrinterCard(printer);
        renderIngestBanners();
        applyFleetFilter();
        return;
    }

    card.dataset.state = state;

    // Update card class (rail colour comes from data-state) on state change
    if (prevState !== state) {
        card.className = stateCardClass(state);
        const idleMsg = card.querySelector('[data-field="idle-msg"]');
        if (idleMsg) {
            const detailMsg = status && status.state_message ? status.state_message : '';
            idleMsg.className = stateIdleMsgClass(state);
            idleMsg.textContent = stateIdleMsgText(state, detailMsg);
        }
        setText(card, 'webcam-placeholder-text', (state === 'offline' && !printer.has_camera) ? 'No camera' : 'Camera Unreachable');
        applyFleetFilter();
    }

    const stateEl = card.querySelector('[data-field="state"]');
    if (stateEl) {
        let stateLabel;
        if (state === 'error' && status && status.state_message) {
            stateLabel = `Error: ${status.state_message}`;
        } else if (state === 'disconnected') {
            stateLabel = 'Printer disconnected';
        } else {
            stateLabel = state.charAt(0).toUpperCase() + state.slice(1);
        }
        stateEl.textContent = stateLabel;
        stateEl.className = `printer-state state-${state}`;
    }

    // Update plug switches
    if (hasPower) {
        const singlePlug = status.power.length === 1;
        status.power.forEach(ps => {
            // Same physical plug can appear more than once (e.g. auto-detected
            // by a plugin AND separately assigned as a direct smart plug) --
            // both share the same data-plug-id, so update every match.
            card.querySelectorAll(`[data-field="power"][data-plug-id="${CSS.escape(ps.id)}"]`).forEach(el => {
                if (el.getAttribute('data-sig') !== plugInfo(ps, state, singlePlug).sig) el.outerHTML = plugHTML(cfg, ps, state, singlePlug);
            });
        });
    }

    if (isPrinting && status.job) {
        const job = status.job;
        const progress = Math.round(job.progress || 0);
        const temps = status.temps;

        setText(card, 'filename', job.file_name);
        setText(card, 'progress-text', `${progress}%`);
        const bar = card.querySelector('[data-field="progress-bar"]');
        if (bar) bar.style.width = `${progress}%`;
        setText(card, 'elapsed', formatTime(job.elapsed_secs));
        setText(card, 'remaining', formatTime(job.remaining_secs));
        setText(card, 'eta', computeETA(job.remaining_secs));
        setTemp(card, 'hotend', temps.hotend_actual, temps.hotend_target);
        setTemp(card, 'bed', temps.bed_actual, temps.bed_target);
        if (temps.has_chamber) setTemp(card, 'chamber', temps.chamber_actual, temps.chamber_target);
        if (job.total_layers > 0) {
            setStatValue(card, 'layer', job.current_layer, `/ ${job.total_layers}`);
        }
    } else if (status && status.temps) {
        const temps = status.temps;
        setTemp(card, 'hotend', temps.hotend_actual, temps.hotend_target);
        setTemp(card, 'bed', temps.bed_actual, temps.bed_target);
        if (temps.has_chamber) setTemp(card, 'chamber', temps.chamber_actual, temps.chamber_target);
    }
    setWatts(card, status);
}

function setWatts(card, status) {
    const pill = card.querySelector('[data-field="watts-pill"]');
    if (!pill) return;
    const watts = Math.round(totalWatts(status && status.power));
    pill.hidden = !(watts > 0);
    setText(card, 'watts', String(watts));
}

function setTemp(card, field, actual, target) {
    const t = Math.round(target);
    setStatValue(card, field, Math.round(actual), `°C / ${t > 0 ? t + '°C' : 'off'}`);
    const {atTarget, pct} = tempFill(field, actual, target);
    const bar = card.querySelector(`[data-field="${field}-bar"]`);
    if (bar) bar.style.width = `${pct}%`;
    const cell = card.querySelector(`[data-cell="${field}"]`);
    if (cell) cell.classList.toggle('at-target', atTarget);
}

function setText(card, field, value) {
    const el = card.querySelector(`[data-field="${field}"]`);
    if (el) el.textContent = value;
}

function setStatValue(card, field, main, unit) {
    const el = card.querySelector(`[data-field="${field}"]`);
    if (!el) return;
    el.textContent = '';
    el.appendChild(document.createTextNode(main));
    const span = document.createElement('span');
    span.className = 'stat-unit';
    span.textContent = (field === 'layer' ? ' ' : '') + unit;
    el.appendChild(span);
}

// Dropdown menus (Power, user, card "more"): one open at a time, any outside
// click closes them.
function toggleMenu(e, id) {
    e.stopPropagation();
    const menu = document.getElementById(id);
    const wasOpen = menu.classList.contains('open');
    document.querySelectorAll('.menu.open').forEach(m => m.classList.remove('open'));
    if (!wasOpen) menu.classList.add('open');
}
document.addEventListener('click', () => document.querySelectorAll('.menu.open').forEach(m => m.classList.remove('open')));

// Fleet strip: state chips (click to filter the cards) and the next finish.
let fleetFilter = 'all';
let fleetSig = '';
const FLEET_ORDER = ['printing', 'paused', 'idle', 'busy', 'attention', 'offline', 'maintenance', 'error'];

function printerCardState(p) {
    return p.config.maintenance ? 'maintenance' : (p.status ? p.status.state : 'offline');
}

function fleetGroup(state) {
    return state === 'disconnected' ? 'offline' : state;
}

function updateFleet() {
    const el = document.getElementById('fleet');
    if (!el) return;
    if (!printers || printers.length === 0) {
        el.innerHTML = '';
        fleetSig = '';
        return;
    }
    const counts = {};
    printers.forEach(p => {
        const g = fleetGroup(printerCardState(p));
        counts[g] = (counts[g] || 0) + 1;
    });
    if (fleetFilter !== 'all' && !counts[fleetFilter]) fleetFilter = 'all';

    let next = null;
    printers.forEach(p => {
        const s = p.status;
        if (!p.config.maintenance && s && s.state === 'printing' && s.job && s.job.remaining_secs > 0) {
            if (!next || s.job.remaining_secs < next.job.remaining_secs) next = {name: p.config.name, job: s.job};
        }
    });

    const known = FLEET_ORDER.filter(s => counts[s]);
    const extra = Object.keys(counts).filter(s => !FLEET_ORDER.includes(s));
    const chip = (key, label, n) => `<button class="fleet-chip ${key} ${fleetFilter === key ? 'on' : ''}" onclick="setFleetFilter('${key}')"><span class="dot"></span>${n} ${label}</button>`;
    const html = `<button class="fleet-chip all ${fleetFilter === 'all' ? 'on' : ''}" onclick="setFleetFilter('all')">All ${printers.length}</button>`
        + known.concat(extra).map(s => chip(s, s, counts[s])).join('')
        + (next ? `<span class="fleet-next">Next finish: <b>${esc(next.name)}</b> at <b>${computeETA(next.job.remaining_secs)}</b></span>` : '');
    if (html !== fleetSig) {
        fleetSig = html;
        el.innerHTML = html;
    }
    applyFleetFilter();
}

function setFleetFilter(f) {
    fleetFilter = f;
    fleetSig = '';
    updateFleet();
}

function applyFleetFilter() {
    document.querySelectorAll('.printer-card[data-printer-id]').forEach(card => {
        card.classList.toggle('card-hidden', fleetFilter !== 'all' && fleetGroup(card.dataset.state) !== fleetFilter);
    });
}

// Dialogs: in-page replacements for the browser's confirm()/alert() popups.

function appDialog(title, bodyHTML, buttons) {
    return new Promise(resolve => {
        const root = document.createElement('div');
        root.className = 'dialog-overlay';
        root.innerHTML = `<div class="dialog" role="alertdialog" aria-modal="true" aria-label="${esc(title)}">
            <h2>${esc(title)}</h2>${bodyHTML}
            <div class="dialog-foot">${buttons.map((b, i) => `<button class="btn ${b.cls || ''}" data-i="${i}">${esc(b.label)}</button>`).join('')}</div></div>`;
        const opener = document.activeElement;
        const done = v => {
            root.remove(); document.removeEventListener('keydown', onKey); resolve(v);
            if (opener && opener.isConnected && opener.focus) opener.focus();
        };
        const onKey = e => {
            if (e.key === 'Escape') { done(null); return; }
            if (e.key !== 'Tab') return;
            const f = [...root.querySelectorAll('button, [href], input, select, textarea')].filter(x => !x.disabled);
            if (!f.length) return;
            const i = f.indexOf(document.activeElement);
            if (e.shiftKey && i <= 0) { e.preventDefault(); f[f.length - 1].focus(); }
            else if (!e.shiftKey && (i === -1 || i === f.length - 1)) { e.preventDefault(); f[0].focus(); }
        };
        root.querySelectorAll('button').forEach(btn => { btn.onclick = () => done(buttons[btn.dataset.i].value); });
        root.addEventListener('click', e => { if (e.target === root) done(null); });
        document.addEventListener('keydown', onKey);
        document.body.appendChild(root);
        const primary = root.querySelector('.btn-primary');
        if (primary) primary.focus();
    });
}

async function appConfirm(title, message, okLabel) {
    const v = await appDialog(title, `<p class="dialog-text">${esc(message)}</p>`,
        [{label: 'Cancel', cls: 'btn-quiet', value: false}, {label: okLabel || 'OK', cls: 'btn-primary', value: true}]);
    return v === true;
}

function appAlert(title, message, list) {
    return appDialog(title, `<p class="dialog-text">${esc(message)}</p>${list && list.length ? `<ul class="dialog-list">${list.map(i => `<li>${esc(i)}</li>`).join('')}</ul>` : ''}`,
        [{label: 'OK', cls: 'btn-primary', value: true}]);
}

// Click once to arm, click again within the window to actually run - avoids
// native confirm() popups for any action that benefits from a confirm step
// (delete, print, pause/cancel/resume, discard).
function confirmAction(btn, action) {
    if (btn.dataset.armed === '1') {
        clearTimeout(btn._confirmTimer);
        const original = btn.dataset.confirmOriginal;
        btn.disabled = true;
        btn.textContent = 'Working…';
        const restore = () => {
            // Most callers replace this button entirely once the action's
            // effect shows up (a reload, a card rebuild), in which case
            // this is a no-op on an already-detached element. This clears
            // it for callers that don't (or on failure) - as soon as our
            // request actually completes, not a guessed delay. For an
            // action like print-cancel, that's well before the real printer
            // finishes physically stopping - the button going back to
            // normal only means "the command was sent", the card's own
            // fields catching up moments later is what reflects the rest.
            if (btn.isConnected && btn.textContent === 'Working…') {
                btn.disabled = false;
                btn.textContent = original;
                btn.dataset.armed = '0';
            }
        };
        const result = action();
        if (result && typeof result.finally === 'function') {
            result.finally(restore);
        } else {
            restore();
        }
        return;
    }
    btn.dataset.armed = '1';
    btn.dataset.confirmOriginal = btn.textContent;
    btn.textContent = 'Sure?';
    btn._confirmTimer = setTimeout(() => {
        btn.textContent = btn.dataset.confirmOriginal;
        btn.dataset.armed = '0';
    }, 3000);
}

let snapshotTimer = null;
let snapshotInterval = 10;
let hideWebcamWhenUnreachable = false;

function restartSnapshotTimer() {
    if (snapshotTimer) clearInterval(snapshotTimer);
    snapshotTimer = setInterval(refreshSnapshots, snapshotInterval * 1000);
}

// Utilities

function formatTime(totalSecs) {
    if (!totalSecs || totalSecs <= 0) return '--';
    const h = Math.floor(totalSecs / 3600);
    const m = Math.floor((totalSecs % 3600) / 60);
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
}

function computeETA(remainingSecs) {
    if (!remainingSecs || remainingSecs <= 0) return '--';
    const eta = new Date(Date.now() + remainingSecs * 1000);
    return eta.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'});
}

function esc(str) {
    if (!str) return '';
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

// Only http(s) URLs may become an href; stored config could hold javascript: etc.
function safeUrl(u) {
    return /^https?:\/\//i.test(u) ? u : '#';
}

function plugLabel(ps) {
    return ps.label && !ps.hide_label ? ps.label + ' ' : '';
}

// Distinguishes an automatic power-off from a manual toggle or an
// unexplained outage - only lives for the one broadcast where it fires
// (the next real poll overwrites Source with the plug's normal value).
function autoOffTooltip(source) {
    if (source === 'auto-idle') return 'Turned off automatically after sitting idle';
    if (source === 'auto-thermal') return 'Turned off automatically: thermal runaway protection';
    if (source === 'mqtt-offline') return 'Device unreachable (MQTT offline) - showing as off, last known state unconfirmed';
    return '';
}

// Config export/import

async function exportConfig() {
    try {
        const resp = await fetch('/api/config/export');
        if (!resp.ok) return;
        const blob = await resp.blob();
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'printspy-config.yaml';
        a.click();
        URL.revokeObjectURL(url);
    } catch (e) {}
}

async function importConfig(input) {
    const file = input.files[0];
    if (!file) return;
    if (!await appConfirm('Import settings', 'Import will add printers and overwrite settings from this file. Continue?', 'Import')) {
        input.value = '';
        return;
    }
    try {
        const text = await file.text();
        const resp = await fetch('/api/config/import', {
            method: 'POST',
            headers: {'Content-Type': 'application/x-yaml'},
            body: text,
        });
        if (resp.ok) {
            const data = await resp.json().catch(() => ({}));
            const msg = `Import complete: ${data.printers_added} printer(s), ${data.plugs_added} smart plug(s), ${data.cameras_added} camera(s), ${data.ingest_keys_added} ingest key(s) added.`;
            const skipped = data.skipped && data.skipped.length ? data.skipped : [];
            await appAlert('Import complete', skipped.length ? `${msg} Skipped ${skipped.length} item(s):` : msg, skipped);
            await fetchPrinters();
            route();
        } else {
            const data = await resp.json().catch(() => ({}));
            await appAlert('Import failed', data.error || 'Import failed.');
        }
    } catch (e) { netFail('Import failed.'); }
    input.value = '';
}

// Slicer ingest jobs — files staged by a slicer, awaiting dispatch to a printer

let ingestJobs = [];

async function loadIngestJobs() {
    try {
        const resp = await fetch('/api/ingest-jobs');
        if (!resp.ok) return;
        ingestJobs = (await resp.json()) || [];
        renderIngestBanners();
    } catch (e) {}
}

// Ingest jobs relay automatically (see poller.checkIngestOnline /
// ingest.Handler.upload) - this banner is just visibility into a job that's
// still waiting (printer off, no print_after so no proactive power-on) or
// that failed. Retry only shows on a failed job - a still-waiting job needs
// no action, it'll relay itself once the printer's seen online.
function renderIngestBanners() {
    document.querySelectorAll('.ingest-banner').forEach(b => b.remove());
    if (!ingestJobs.length) return;

    for (const printer of printers) {
        const cfg = printer.config;
        const card = document.querySelector(`[data-printer-id="${cfg.id}"]`);
        if (!card) continue;
        const jobs = ingestJobs.filter(j => j.status !== 'dispatching' && j.pinned_printer_id === cfg.id);
        if (!jobs.length) continue;

        for (const job of jobs) {
            const banner = document.createElement('div');
            banner.className = job.error ? 'ingest-banner failed' : 'ingest-banner';
            banner.innerHTML = `
                <span>&#128196; ${esc(job.filename)} ${job.error ? `failed to relay — <span class="msg-error">${esc(job.error)}</span>` : `waiting for ${esc(cfg.name)} to come online`}</span>
                <div class="ingest-banner-actions">
                    ${job.error ? `<button class="btn btn-sm btn-primary" onclick="retryIngestJob(${job.id})">Retry</button>` : ''}
                    <button class="btn btn-sm btn-danger" onclick="confirmAction(this, () => discardIngestJob(${job.id}))">Discard</button>
                </div>`;
            card.insertBefore(banner, card.querySelector('.printer-body'));
        }
    }
}

async function retryIngestJob(jobID) {
    try {
        const resp = await fetch(`/api/ingest-jobs/${jobID}/retry`, {method: 'POST'});
        await checkOk(resp, 'Failed to retry job.');
        loadIngestJobs();
    } catch (e) { netFail('Failed to retry job.'); }
}

async function discardIngestJob(jobID) {
    try {
        const resp = await fetch(`/api/ingest-jobs/${jobID}`, {method: 'DELETE'});
        await checkOk(resp, 'Failed to discard job.');
        loadIngestJobs();
    } catch (e) { netFail('Failed to discard job.'); }
}

// Initialize
fetch('/api/settings').then(r => r.json()).then(settings => {
    snapshotInterval = parseInt(settings.snapshot_interval) || 10;
    hideWebcamWhenUnreachable = settings.hide_webcam_when_unreachable === '1';
    restartSnapshotTimer();
    // This races connectSSE() below, which triggers its own fetchPrinters()
    // as soon as the connection opens - real network, that often resolves
    // before this fetch does, rendering cards with hideWebcamWhenUnreachable
    // still at its default (false). Force a re-render now that the real
    // value is in, so a card built with the wrong default doesn't just sit
    // there until some unrelated state change happens to rebuild it.
    prevPrinterIDs = [];
    updateDashboard();
}).catch(() => {
    restartSnapshotTimer();
});

fetch('/api/version').then(r => r.json()).then(data => {
    const el = document.getElementById('app-version');
    if (el && data.version) el.textContent = 'PrintSpy v' + data.version;
}).catch(() => {});

fetch('/api/me').then(r => r.ok ? r.json() : null).then(me => {
    if (!me || !me.username) return;
    document.getElementById('user-avatar').textContent = (Array.from(me.username)[0] || '?').toUpperCase();
    document.getElementById('user-name').textContent = me.username;
}).catch(() => {});

connectSSE();
