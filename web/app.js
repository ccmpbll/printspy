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

// SSE connection

function connectSSE() {
    if (eventSource) eventSource.close();

    eventSource = new EventSource('/api/events');

    eventSource.addEventListener('init', (e) => {
        printers = JSON.parse(e.data);
        printers.forEach(p => { if (p.status) statusCache[p.config.id] = p.status; });
        prevPrinterIDs = [];
        updateDashboard();
        loadIngestJobs();
        showConnectionBanner(false);
    });

    eventSource.addEventListener('status', (e) => {
        const msg = JSON.parse(e.data);
        applyStatusUpdate(msg.printer_id, msg.status);
        showConnectionBanner(false);
    });

    eventSource.addEventListener('refresh', () => {
        fetchPrinters();
        loadIngestJobs();
    });

    eventSource.addEventListener('error', (e) => {
        console.warn(`[sse] connection error at ${new Date().toISOString()}, readyState=${eventSource.readyState}`, e);
        showConnectionBanner(true);
    });
}

// Applies a fresh PrinterStatus to printers[]/statusCache and patches the
// card - shared by the SSE 'status' listener and any action (print/pause/
// cancel/resume) whose own response already carries the post-repoll status,
// so those don't need to wait on a separate, unordered SSE round-trip to
// reflect what just happened.
function applyStatusUpdate(printerId, status) {
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
    const count = document.getElementById('printer-count');
    if (!printers || printers.length === 0) {
        count.textContent = '';
        document.getElementById('power-controls').style.display = 'none';
        return;
    }
    const connected = printers.filter(p => p.status && p.status.state !== 'offline' && p.status.state !== 'disconnected').length;
    count.textContent = `${connected}/${printers.length} connected`;
    const hasPower = printers.some(p => p.status && p.status.power && p.status.power.length > 0);
    document.getElementById('power-controls').style.display = hasPower ? 'inline-flex' : 'none';
}

// Power control

async function startReprint(btn) {
    const printerId = btn.dataset.printer;
    const origin = btn.dataset.origin;
    const path = btn.dataset.path;
    try {
        const resp = await fetch(`/api/printers/${printerId}/print`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({origin, path}),
        });
        const data = await resp.json();
        if (resp.ok) {
            if (data.status) applyStatusUpdate(parseInt(printerId), data.status);
        } else if (data.error) {
            alert(data.error);
        }
    } catch (e) {}
}

// File manager - replaces the old per-card "Recent files" dropdown. Same
// row look (thumbnail, print-history status badge), but every file, and
// reachable regardless of printer state.
let fileManagerPrinterId = null;

async function openFileManager(printerId) {
    fileManagerPrinterId = printerId;
    document.getElementById('filemanager-modal-title').textContent = `Files — ${esc(printerName(printerId))}`;
    document.getElementById('filemanager-modal').classList.add('active');
    await loadFileManagerFiles(printerId);
}

function refreshFileManagerIfShowing(printerId) {
    if (fileManagerPrinterId === printerId && document.getElementById('filemanager-modal').classList.contains('active')) {
        loadFileManagerFiles(printerId);
    }
}

async function loadFileManagerFiles(printerId) {
    const list = document.getElementById('filemanager-list');
    list.innerHTML = '<div class="settings-empty">Loading…</div>';
    try {
        const resp = await fetch(`/api/printers/${printerId}/recent?all=1`);
        const files = await resp.json();
        if (!files || !files.length) {
            list.innerHTML = '<div class="settings-empty">No files found.</div>';
            return;
        }
        list.innerHTML = files.map(f => {
            // Every file gets a thumbnail attempt now, not just ones PrusaLink
            // already renders a live thumb for (f.thumbnail_path) - cached
            // .gcode thumbnails have nothing to show there but still resolve
            // via the cache. Files with genuinely nothing still 404 and hide
            // via onerror, same as before.
            const thumb = `<img class="recent-thumb lazy-thumb" data-src="/api/file-thumbnail/${printerId}?path=${encodeURIComponent(f.path)}&uploaded_at=${f.uploaded_at}${f.thumbnail_path ? '&thumb=' + encodeURIComponent(f.thumbnail_path) : ''}" alt="" onerror="this.style.display='none'">`;
            const toolsHTML = f.tools && f.tools.length > 0 ? ` &middot; <span class="recent-tools">${toolsJoin(f.tools)}</span>` : '';
            let status = 'New';
            let statusClass = 'recent-status-new';
            if (f.success_count > 0 && f.last_success !== false) {
                status = `${f.success_count}x printed`;
                statusClass = 'recent-status-success';
            } else if (f.last_success === false) {
                status = 'Failed';
                statusClass = 'recent-status-failed';
            }
            const btnLabel = f.success_count > 0 ? '&#8634; Reprint' : '<span class="icon-play"></span> Print';
            return `<div class="recent-item">
                ${thumb}
                <div class="recent-item-info">
                    <span class="recent-name" title="${esc(f.file_name)}">${esc(f.file_name)}</span>
                    <span class="recent-meta"><span class="${statusClass}">${status}</span> &middot; ${formatDate(f.uploaded_at)}${toolsHTML}</span>
                </div>
                <button class="btn btn-sm btn-reprint" data-printer="${printerId}" data-origin="${esc(f.origin)}" data-path="${esc(f.path)}" onclick="confirmAction(this, () => startReprint(this))" title="Print">${btnLabel}</button>
                <a class="btn btn-sm" href="/api/printers/${printerId}/download?origin=${encodeURIComponent(f.origin)}&path=${encodeURIComponent(f.path)}&filename=${encodeURIComponent(f.file_name)}" title="Download">&#8595; Download</a>
                <button class="btn btn-sm btn-danger" data-printer="${printerId}" data-origin="${esc(f.origin)}" data-path="${esc(f.path)}" onclick="confirmAction(this, () => deleteManagedFile(this))">Delete</button>
            </div>`;
        }).join('');
        observeLazyThumbs(list, fileManagerThumbObserver);
    } catch (e) {
        list.innerHTML = '<div class="settings-empty">Failed to load files.</div>';
    }
}

// Loads thumbnails only as they scroll into view, instead of firing every
// row's thumbnail request the instant a modal opens - matters once a list
// has more than a handful of entries. root is the list's own scroll
// container (not the viewport/null), since both File Manager and History
// scroll internally within a fixed-height modal - each needs its own
// observer instance since IntersectionObserver's root is fixed at
// construction time.
function makeLazyThumbObserver(rootId) {
    const observer = new IntersectionObserver(entries => {
        for (const entry of entries) {
            if (!entry.isIntersecting) continue;
            const img = entry.target;
            img.src = img.dataset.src;
            observer.unobserve(img);
        }
    }, {root: document.getElementById(rootId), rootMargin: '200px'});
    return observer;
}
const fileManagerThumbObserver = makeLazyThumbObserver('filemanager-list');
const historyThumbObserver = makeLazyThumbObserver('history-list');

function observeLazyThumbs(container, observer) {
    container.querySelectorAll('.lazy-thumb').forEach(img => observer.observe(img));
}

// Print history - separate from the file manager (that's what's on the
// printer now; this is what's ever been printed, one row per completed job).
let historyPrinterId = null;
let historyPage = 0;
let historyHasMore = false;
const HISTORY_PAGE_SIZE = 20;

async function openPrintHistory(printerId) {
    historyPrinterId = printerId;
    historyPage = 0;
    document.getElementById('history-modal-title').textContent = `History — ${esc(printerName(printerId))}`;
    document.getElementById('history-modal').classList.add('active');
    loadHistorySummaryLine(printerId);
    await loadHistoryPage();
}

async function loadHistorySummaryLine(printerId) {
    const line = document.getElementById('history-summary-line');
    line.textContent = '';
    try {
        const resp = await fetch(`/api/printers/${printerId}/history`);
        if (!resp.ok) return;
        const summary = await resp.json();
        if (!summary || summary.count === 0) return;
        line.textContent = `${summary.total_hours.toFixed(1)}h printed · ${summary.success_rate}% success`;
    } catch (e) {}
}

function historyPrevPage() {
    if (historyPage === 0) return;
    historyPage--;
    loadHistoryPage();
}

function historyNextPage() {
    if (!historyHasMore) return;
    historyPage++;
    loadHistoryPage();
}

async function loadHistoryPage() {
    const list = document.getElementById('history-list');
    const prevBtn = document.getElementById('history-prev');
    const nextBtn = document.getElementById('history-next');
    const label = document.getElementById('history-page-label');
    list.innerHTML = '<div class="settings-empty">Loading…</div>';
    prevBtn.disabled = true;
    nextBtn.disabled = true;
    try {
        const offset = historyPage * HISTORY_PAGE_SIZE;
        const resp = await fetch(`/api/printers/${historyPrinterId}/history/list?limit=${HISTORY_PAGE_SIZE}&offset=${offset}`);
        const data = await resp.json();
        const entries = data.entries || [];
        if (!entries.length) {
            list.innerHTML = `<div class="settings-empty">${historyPage === 0 ? 'No print history yet.' : 'No more entries.'}</div>`;
        } else {
            list.innerHTML = entries.map(historyRowHTML).join('');
            observeLazyThumbs(list, historyThumbObserver);
        }
        historyHasMore = !!data.has_more;
        label.textContent = `Page ${historyPage + 1}`;
        prevBtn.disabled = historyPage === 0;
        nextBtn.disabled = !historyHasMore;
    } catch (e) {
        list.innerHTML = '<div class="settings-empty">Failed to load history.</div>';
    }
}

// toolsJoin renders "{material} (T{n})" pairs, comma-separated - shared by
// History (always shown, even for a single tool) and File Manager (only
// shown for 2+ tools, gated by the caller).
function toolsJoin(tools) {
    return tools.map(t => `${esc(t.material)} (T${t.tool_index + 1})`).join(', ');
}

function historyRowHTML(h) {
    let status = 'Completed';
    let statusClass = 'recent-status-success';
    if (h.result === 'failed') {
        status = 'Failed';
        statusClass = 'recent-status-failed';
    } else if (h.result === 'cancelled') {
        status = 'Cancelled';
        statusClass = 'recent-status-cancelled';
    }

    const filament = h.filament_used_g
        ? (h.filament_cost ? `${h.filament_used_g.toFixed(0)}g ($${h.filament_cost.toFixed(2)})` : `${h.filament_used_g.toFixed(0)}g`)
        : '';
    const durationStr = formatDuration(h.duration_secs);
    const estStr = h.estimated_secs ? ` <span class="history-est">(est. ${formatDuration(h.estimated_secs)})</span>` : '';

    // Material always reads "{material} (T{n})" whether it's a single-tool
    // or multi-tool print - h.tools (only present for 2+ tools) just adds
    // more of the same pairing, not a different format.
    const tools = (h.tools && h.tools.length) ? h.tools : (h.material ? [{material: h.material, tool_index: h.tool_index}] : []);
    const materialStr = tools.length ? toolsJoin(tools) : '';

    const details = [
        materialStr ? `Material: ${materialStr}` : '',
        h.layer_height_mm ? `Layer: ${h.layer_height_mm}mm` : '',
        h.fill_density ? `Fill: ${esc(h.fill_density)}` : '',
        filament ? `Filament: ${filament}` : '',
        h.tool_changes ? `${h.tool_changes} tool changes` : '',
    ].filter(Boolean).join(' - ');

    // Cache-only lookup (no live-proxy fallback like File Manager has) -
    // History has no printer-side thumbnail ref to fall back to, only
    // whatever trackPrintHistory cached at completion time. Omitted
    // entirely when path's empty (no MetadataDownloader support, or this
    // row predates the path/uploaded_at columns).
    const thumb = h.path ? `<img class="recent-thumb lazy-thumb" data-src="/api/file-thumbnail/${historyPrinterId}?path=${encodeURIComponent(h.path)}&uploaded_at=${h.uploaded_at}" alt="" onerror="this.style.display='none'">` : '';

    return `<div class="recent-item history-item">
        ${thumb}
        <div class="recent-item-info">
            <span class="recent-name" title="${esc(h.filename)}">${esc(h.filename)}</span>
            <span class="recent-meta"><span class="${statusClass}">${status}</span> - ${formatHistoryDate(h.completed_at)} - Duration: ${durationStr}${estStr}</span>
            ${details ? `<span class="recent-meta">${details}</span>` : ''}
        </div>
    </div>`;
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

async function deleteManagedFile(btn) {
    const printerId = btn.dataset.printer;
    const origin = btn.dataset.origin;
    const path = btn.dataset.path;
    try {
        const resp = await fetch(`/api/printers/${printerId}/recent?origin=${encodeURIComponent(origin)}&path=${encodeURIComponent(path)}`, {
            method: 'DELETE',
        });
        if (resp.ok) {
            loadFileManagerFiles(printerId);
        } else {
            const data = await resp.json();
            if (data.error) alert(data.error);
        }
    } catch (e) {}
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
        const data = await resp.json();
        if (resp.ok) {
            if (data.status) applyStatusUpdate(printerId, data.status);
        } else if (data.error) {
            alert(data.error);
        }
    } catch (e) {}
}

// Power control

async function setPower(printerId, action, plugId) {
    try {
        await fetch(`/api/printers/${printerId}/power`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({action, plug_id: plugId}),
        });
    } catch (e) {}
}

async function bulkPower(action) {
    try {
        await fetch('/api/printers/power', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({action}),
        });
    } catch (e) {}
}

function refreshSnapshots() {
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
    return localStorage.getItem(`webcam-mode-${printerId}`) || 'plate';
}

function setWebcamMode(printerId, mode) {
    localStorage.setItem(`webcam-mode-${printerId}`, mode);
    const printer = printers.find(p => p.config.id === printerId);
    const card = document.querySelector(`[data-printer-id="${printerId}"]`);
    if (!printer || !card) return;
    card.outerHTML = renderPrinterCard(printer);
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
async function fetchPrinters() {
    try {
        const resp = await fetch('/api/printers');
        if (!resp.ok) return;
        printers = await resp.json();
        prevPrinterIDs = [];
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
    }
}

function renderMaintenanceCard(printer) {
    const cfg = printer.config;
    return `
        <div class="printer-card card-maintenance" data-printer-id="${cfg.id}" data-state="maintenance">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                <span class="printer-state state-maintenance">Maintenance</span>
            </div>
            <div class="printer-body">
                <div class="idle-message" data-field="idle-msg">In maintenance — polling paused</div>
            </div>
        </div>`;
}

function renderPrinterCard(printer) {
    const cfg = printer.config;
    if (cfg.maintenance) return renderMaintenanceCard(printer);
    const status = printer.status;
    const state = status ? status.state : 'offline';
    const stateClass = `state-${state}`;
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

    let powerHTML = '';
    if (status && status.power && status.power.length > 0) {
        const isBusy = state === 'printing' || state === 'paused';
        const singlePlug = status.power.length === 1;
        powerHTML = status.power.map(ps => {
            const onClass = ps.on ? 'power-btn-active power-on' : '';
            const offClass = !ps.on ? 'power-btn-active power-off' : '';
            const isPrinterPlug = singlePlug || (ps.label && ps.label.toLowerCase().includes('printer'));
            const autoTitle = autoOffTooltip(ps.source);
            const offDisabled = isBusy && isPrinterPlug ? 'disabled title="Cannot turn off printer while printing"' : (autoTitle ? `title="${esc(autoTitle)}"` : '');
            const onTitle = autoTitle ? ` title="${esc(autoTitle)}"` : '';
            const label = esc(plugLabel(ps));
            return `<span class="power-btn-group" data-field="power" data-plug-id="${esc(ps.id)}"><button class="power-toggle-btn ${onClass}" onclick="event.stopPropagation();setPower(${cfg.id},'on','${esc(ps.id)}')"${onTitle}>${label}&#9889; On</button><button class="power-toggle-btn ${offClass}" onclick="event.stopPropagation();setPower(${cfg.id},'off','${esc(ps.id)}')" ${offDisabled}>Off</button></span>`;
        }).join('');
    }

    let controlHTML = '';
    if (state === 'printing') {
        controlHTML = `<span class="print-controls" data-field="print-controls"><button class="btn btn-sm" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'pause'))">&#10074;&#10074; Pause</button><button class="btn btn-sm btn-danger" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'cancel'))"><span class="icon-stop"></span> Cancel</button></span>`;
    } else if (state === 'paused') {
        controlHTML = `<span class="print-controls" data-field="print-controls"><button class="btn btn-sm btn-primary" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'resume'))"><span class="icon-play"></span> Resume</button><button class="btn btn-sm btn-danger" onclick="event.stopPropagation();confirmAction(this, () => controlPrint(${cfg.id},'cancel'))"><span class="icon-stop"></span> Cancel</button></span>`;
    }

    const filesHTML = `<button class="btn btn-sm btn-files" onclick="event.stopPropagation();openFileManager(${cfg.id})">Files</button>`;
    const historyHTML = `<button class="btn btn-sm btn-history" onclick="event.stopPropagation();openPrintHistory(${cfg.id})">History</button>`;

    return `
        <div class="${cardClass}" data-printer-id="${cfg.id}" data-state="${state}">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                ${cfg.model && !cfg.hide_model ? `<span class="printer-model">${esc(cfg.model)}</span>` : ''}
                <span class="printer-state ${stateClass}" data-field="state">${stateLabel}</span>
                ${powerHTML}
                ${controlHTML}
                ${filesHTML}
                ${historyHTML}
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
        </div>`;
}

function renderPrintingStats(cfg, status, hasRealCam) {
    const job = status.job;
    const progress = Math.round(job.progress || 0);
    const elapsed = formatTime(job.elapsed_secs);
    const remaining = formatTime(job.remaining_secs);
    const eta = computeETA(job.remaining_secs);
    const temps = status.temps;

    const tempCells = [];
    tempCells.push(`<div class="stat-box"><div class="stat-label">Hotend</div><div class="stat-value" data-field="hotend">${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.hotend_target)}&deg;C</span></div></div>`);
    tempCells.push(`<div class="stat-box"><div class="stat-label">Bed</div><div class="stat-value" data-field="bed">${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.bed_target)}&deg;C</span></div></div>`);
    if (temps.has_chamber) {
        tempCells.push(`<div class="stat-box"><div class="stat-label">Chamber</div><div class="stat-value" data-field="chamber">${Math.round(temps.chamber_actual)}<span class="stat-unit">&deg;C / ${temps.chamber_target > 0 ? Math.round(temps.chamber_target) + '&deg;C' : 'off'}</span></div></div>`);
    }

    let layerCell = '';
    if (job.total_layers > 0) {
        layerCell = `<div class="stat-box"><div class="stat-label">Layer</div><div class="stat-value" data-field="layer">${job.current_layer} <span class="stat-unit">/ ${job.total_layers}</span></div></div>`;
    }

    if (totalWatts(status.power) > 0) {
        tempCells.push(`<div class="stat-box"><div class="stat-label">Power</div><div class="stat-value" data-field="watts">${Math.round(totalWatts(status.power))}<span class="stat-unit">W</span></div></div>`);
    }

    return `
        <div class="file-and-thumb">
            <div class="file-and-progress">
                <div class="print-filename" data-field="filename" title="${esc(job.file_name)}">${esc(job.file_name)}</div>
                <div>
                    <div class="progress-row">
                        <span class="progress-label">Progress</span>
                        <span class="progress-value" data-field="progress-text">${progress}%</span>
                    </div>
                    <div class="progress-bar">
                        <div class="progress-fill" data-field="progress-bar" style="width: ${progress}%"></div>
                    </div>
                </div>
            </div>
            ${hasRealCam ? `<div class="thumb-beside">
                <img src="/api/thumbnail/${cfg.id}?t=${pollCounter}" alt="Thumbnail" onerror="this.parentElement.style.display='none'">
            </div>` : ''}
        </div>
        <div class="stat-rows">
            <div class="stat-grid stat-grid-auto">
                <div class="stat-box"><div class="stat-label">Elapsed</div><div class="stat-value" data-field="elapsed">${elapsed}</div></div>
                <div class="stat-box"><div class="stat-label">Remaining</div><div class="stat-value" data-field="remaining">${remaining}</div></div>
                <div class="stat-box"><div class="stat-label">ETA</div><div class="stat-value" data-field="eta">${eta}</div></div>
                ${layerCell}
            </div>
            <div class="stat-grid stat-grid-auto">${tempCells.join('')}</div>
        </div>`;
}

function renderIdleStats(status, state) {
    const temps = status ? status.temps : null;
    const detailMsg = status && status.state_message ? status.state_message : '';
    const stateMsg = stateIdleMsgText(state, detailMsg);

    let tempsHTML = '';
    if (temps && state !== 'offline' && state !== 'disconnected') {
        const cells = [];
        cells.push(`<div class="stat-box"><div class="stat-label">Hotend</div><div class="stat-value" data-field="hotend">${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${temps.hotend_target > 0 ? Math.round(temps.hotend_target) + '&deg;C' : 'off'}</span></div></div>`);
        cells.push(`<div class="stat-box"><div class="stat-label">Bed</div><div class="stat-value" data-field="bed">${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${temps.bed_target > 0 ? Math.round(temps.bed_target) + '&deg;C' : 'off'}</span></div></div>`);
        if (temps.has_chamber) {
            cells.push(`<div class="stat-box"><div class="stat-label">Chamber</div><div class="stat-value" data-field="chamber">${Math.round(temps.chamber_actual)}<span class="stat-unit">&deg;C / ${temps.chamber_target > 0 ? Math.round(temps.chamber_target) + '&deg;C' : 'off'}</span></div></div>`);
        }
        if (totalWatts(status.power) > 0) {
            cells.push(`<div class="stat-box"><div class="stat-label">Power</div><div class="stat-value" data-field="watts">${Math.round(totalWatts(status.power))}<span class="stat-unit">W</span></div></div>`);
        }
        tempsHTML = `<div class="stat-grid stat-grid-auto">${cells.join('')}</div>`;
    }

    if (state === 'disconnected') {
        return '';
    }

    return `<div class="${stateIdleMsgClass(state)}" data-field="idle-msg">${stateMsg}</div>${tempsHTML}`;
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
        return;
    }

    card.dataset.state = state;

    // Update card border class on state change
    if (prevState !== state) {
        card.className = stateCardClass(state);
        const idleMsg = card.querySelector('[data-field="idle-msg"]');
        if (idleMsg) {
            const detailMsg = status && status.state_message ? status.state_message : '';
            idleMsg.className = stateIdleMsgClass(state);
            idleMsg.textContent = stateIdleMsgText(state, detailMsg);
        }
        setText(card, 'webcam-placeholder-text', (state === 'offline' && !printer.has_camera) ? 'No camera' : 'Camera Unreachable');
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

    // Update power buttons
    if (status && status.power && status.power.length > 0) {
        const isBusy = state === 'printing' || state === 'paused';
        const singlePlug = status.power.length === 1;
        status.power.forEach(ps => {
            // Same physical plug can appear more than once (e.g. auto-detected
            // by a plugin AND separately assigned as a direct smart plug) --
            // both share the same data-plug-id, so update every match.
            const groups = card.querySelectorAll(`[data-field="power"][data-plug-id="${ps.id}"]`);
            const isPrinterPlug = singlePlug || (ps.label && ps.label.toLowerCase().includes('printer'));
            const label = plugLabel(ps);
            const autoTitle = autoOffTooltip(ps.source);
            groups.forEach(group => {
                const btns = group.querySelectorAll('.power-toggle-btn');
                if (btns[0]) {
                    btns[0].className = `power-toggle-btn ${ps.on ? 'power-btn-active power-on' : ''}`;
                    btns[0].textContent = `${label}⚡ On`;
                    btns[0].title = autoTitle;
                }
                if (btns[1]) {
                    btns[1].className = `power-toggle-btn ${!ps.on ? 'power-btn-active power-off' : ''}`;
                    btns[1].disabled = isBusy && isPrinterPlug;
                    btns[1].title = isBusy && isPrinterPlug ? 'Cannot turn off printer while printing' : autoTitle;
                }
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
        setStatValue(card, 'hotend', Math.round(temps.hotend_actual), `°C / ${Math.round(temps.hotend_target)}°C`);
        setStatValue(card, 'bed', Math.round(temps.bed_actual), `°C / ${Math.round(temps.bed_target)}°C`);
        if (temps.has_chamber) {
            setStatValue(card, 'chamber', Math.round(temps.chamber_actual), `°C / ${temps.chamber_target > 0 ? Math.round(temps.chamber_target) + '°C' : 'off'}`);
        }
        if (job.total_layers > 0) {
            setStatValue(card, 'layer', job.current_layer, `/ ${job.total_layers}`);
        }
        if (totalWatts(status.power) > 0) {
            setStatValue(card, 'watts', Math.round(totalWatts(status.power)), 'W');
        }
    } else if (status && status.temps) {
        const temps = status.temps;
        setStatValue(card, 'hotend', Math.round(temps.hotend_actual), `°C / ${temps.hotend_target > 0 ? Math.round(temps.hotend_target) + '°C' : 'off'}`);
        setStatValue(card, 'bed', Math.round(temps.bed_actual), `°C / ${temps.bed_target > 0 ? Math.round(temps.bed_target) + '°C' : 'off'}`);
        if (temps.has_chamber) {
            setStatValue(card, 'chamber', Math.round(temps.chamber_actual), `°C / ${temps.chamber_target > 0 ? Math.round(temps.chamber_target) + '°C' : 'off'}`);
        }
        if (totalWatts(status.power) > 0) {
            setStatValue(card, 'watts', Math.round(totalWatts(status.power)), 'W');
        }
    }
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
    span.textContent = unit;
    el.appendChild(span);
}

// Settings modal with printer management

const NOTIFY_TYPES = ['start', 'complete', 'failed', 'error', 'checkpoint1', 'checkpoint2'];
const PUSHOVER_SOUNDS = ['pushover', 'bike', 'bugle', 'cashregister', 'classical', 'cosmic', 'falling', 'gamelan',
    'incoming', 'intermission', 'magic', 'mechanical', 'pianobar', 'siren', 'spacealarm', 'tugboat', 'alien',
    'climb', 'persistent', 'echo', 'updown', 'vibrate', 'none'];

// Per-type placeholder/hint text for the Customize panel. 'start' is the
// only type that defaults to a thumbnail-only image (see poller.go's
// sendNotification) - everything else defaults to camera-with-fallback.
const NOTIFY_CUSTOMIZE = {
    start:       {titlePh: 'Print started', messagePh: '{printer}: {file}', hint: 'Placeholders: {printer} {file}', imageDefault: 'Default (thumbnail)'},
    complete:    {titlePh: 'Print complete', messagePh: '{printer}: {file} ({material}, {filament_g}g) - {duration}', hint: 'Placeholders: {printer} {file} {material} {filament_g} {duration}', imageDefault: 'Default (camera, fallback to thumbnail)'},
    failed:      {titlePh: 'Print failed', messagePh: '{printer}: {file} ({material}, {filament_g}g) - {duration}', hint: 'Placeholders: {printer} {file} {material} {filament_g} {duration}', imageDefault: 'Default (camera, fallback to thumbnail)'},
    error:       {titlePh: '{printer}: error', messagePh: '{message}', hint: 'Placeholders: {printer} {message}', imageDefault: 'Default (camera, fallback to thumbnail)'},
    checkpoint1: {titlePh: 'Print checkpoint', messagePh: '{printer}: {file} reached {percent}%', hint: 'Placeholders: {printer} {file} {percent}', imageDefault: 'Default (camera, fallback to thumbnail)'},
    checkpoint2: {titlePh: 'Print checkpoint', messagePh: '{printer}: {file} reached {percent}%', hint: 'Placeholders: {printer} {file} {percent}', imageDefault: 'Default (camera, fallback to thumbnail)'},
};

function notifySoundOptionsHTML() {
    return '<option value="">App default</option>' +
        PUSHOVER_SOUNDS.map(s => `<option value="${s}">${s}</option>`).join('');
}

function notifyCustomizeHTML(t) {
    const c = NOTIFY_CUSTOMIZE[t];
    return `
        <details style="margin-left:1.75rem;margin-top:0.25rem">
            <summary>Customize</summary>
            <div class="form-group">
                <label for="setting-notify-${t}-title">Title</label>
                <input type="text" id="setting-notify-${t}-title" placeholder="${esc(c.titlePh)}">
            </div>
            <div class="form-group">
                <label for="setting-notify-${t}-message">Message</label>
                <input type="text" id="setting-notify-${t}-message" placeholder="${esc(c.messagePh)}">
            </div>
            <div class="form-hint">${esc(c.hint)}</div>
            <div class="form-group">
                <label for="setting-notify-${t}-sound">Sound</label>
                <select id="setting-notify-${t}-sound">${notifySoundOptionsHTML()}</select>
            </div>
            <div class="form-group">
                <label for="setting-notify-${t}-image">Image</label>
                <select id="setting-notify-${t}-image">
                    <option value="">${esc(c.imageDefault)}</option>
                    <option value="camera">Camera (fallback to thumbnail)</option>
                    <option value="thumbnail">Thumbnail only</option>
                    <option value="none">None</option>
                </select>
            </div>
            <div class="form-group">
                <label style="display:flex;align-items:center;gap:0.5rem;font-weight:normal">
                    <input type="checkbox" id="setting-notify-${t}-high-priority" style="width:auto">
                    High priority
                </label>
            </div>
        </details>`;
}

document.querySelectorAll('.notify-customize').forEach(el => {
    el.innerHTML = notifyCustomizeHTML(el.dataset.type);
});

function openSettings() {
    fetch('/api/settings').then(r => r.json()).then(settings => {
        document.getElementById('setting-snapshot-interval').value = settings.snapshot_interval || '10';
        document.getElementById('setting-hide-webcam').checked = settings.hide_webcam_when_unreachable === '1';
        document.getElementById('setting-poll-interval').value = settings.poll_interval || '';
        document.getElementById('setting-history-retention').value = settings.history_retention_days || '';
        document.getElementById('setting-print-control-timeout').value = settings.print_control_timeout_secs || '15';
        document.getElementById('setting-prusalink-ping-interval').value = settings.prusalink_ping_interval || '';
        document.getElementById('setting-auto-off-idle').value = settings.auto_off_idle_minutes || '';
        document.getElementById('setting-auto-off-cooldown').value = settings.auto_off_cooldown_temp || '40';
        document.getElementById('setting-thermal-max-bed').value = settings.thermal_max_bed_temp || '';
        document.getElementById('setting-thermal-max-extruder').value = settings.thermal_max_extruder_temp || '';
        document.getElementById('setting-pushover-user-key').value = settings.pushover_user_key || '';
        document.getElementById('setting-pushover-app-token').value = settings.pushover_app_token || '';
        const mqttURL = settings.mqtt_broker_url || '';
        document.getElementById('setting-mqtt-tls').checked = mqttURL.startsWith('ssl://');
        document.getElementById('setting-mqtt-broker-url').value = mqttURL.replace(/^(tcp|ssl):\/\//, '');
        document.getElementById('setting-mqtt-username').value = settings.mqtt_username || '';
        document.getElementById('setting-mqtt-password').value = settings.mqtt_password || '';
        document.getElementById('setting-notify-start').checked = settings.notify_on_start === '1';
        document.getElementById('setting-notify-complete').checked = settings.notify_on_complete === '1';
        document.getElementById('setting-notify-failed').checked = settings.notify_on_failed === '1';
        document.getElementById('setting-notify-error').checked = settings.notify_on_error === '1';
        document.getElementById('setting-notify-checkpoint1-enabled').checked = settings.notify_checkpoint1_enabled === '1';
        document.getElementById('setting-notify-checkpoint1-percent').value = settings.notify_checkpoint1_percent || '5';
        document.getElementById('setting-notify-checkpoint2-enabled').checked = settings.notify_checkpoint2_enabled === '1';
        document.getElementById('setting-notify-checkpoint2-percent').value = settings.notify_checkpoint2_percent || '50';
        NOTIFY_TYPES.forEach(t => {
            document.getElementById(`setting-notify-${t}-title`).value = settings[`notify_${t}_title`] || '';
            document.getElementById(`setting-notify-${t}-message`).value = settings[`notify_${t}_message`] || '';
            document.getElementById(`setting-notify-${t}-sound`).value = settings[`notify_${t}_sound`] || '';
            document.getElementById(`setting-notify-${t}-image`).value = settings[`notify_${t}_image`] || '';
            document.getElementById(`setting-notify-${t}-high-priority`).checked = settings[`notify_${t}_high_priority`] === '1';
        });
    });
    renderSettingsPrinterList();
    loadUsers();
    loadSmartPlugs();
    loadCameras();
    loadIngestKeys();
    document.getElementById('settings-modal').classList.add('active');
}

// Smart plugs (direct Tasmota, managed independently of printers)

let smartPlugs = [];

async function loadSmartPlugs() {
    try {
        const resp = await fetch('/api/smartplugs');
        smartPlugs = (await resp.json()) || [];
        renderSettingsSmartPlugList(smartPlugs);
    } catch (e) {}
}

function renderSettingsSmartPlugList(plugs) {
    const list = document.getElementById('settings-smartplug-list');
    if (!plugs.length) {
        list.innerHTML = '<div class="settings-empty">No smart plugs configured yet.</div>';
        return;
    }
    list.innerHTML = plugs.map(p => {
        const conn = p.mqtt_topic ? `MQTT: ${esc(p.mqtt_topic)}:${esc(p.idx)}` : `${esc(p.ip)}:${esc(p.idx)}`;
        return `
        <div class="settings-printer-row">
            <div class="settings-printer-info">
                <span class="settings-printer-name">${esc(p.label || p.ip || p.mqtt_topic)}</span>
                <span class="settings-printer-url">${conn} — ${p.printer_name ? esc(p.printer_name) : 'Unassigned'}${p.hide_label ? ' — label hidden' : ''}</span>
            </div>
            <div class="settings-printer-actions">
                <button class="btn btn-sm" onclick="closeModal();openEditSmartPlugModal(${p.id})" title="Edit">&#9998; Edit</button>
                <button class="btn btn-sm btn-danger" onclick="confirmAction(this, () => deleteSmartPlug(${p.id}))">Delete</button>
            </div>
        </div>`;
    }).join('');
}

function togglePlugMode() {
    const mqtt = document.getElementById('plug-mode').value === 'mqtt';
    document.getElementById('plug-ip-group').style.display = mqtt ? 'none' : '';
    document.getElementById('plug-mqtt-topic-group').style.display = mqtt ? '' : 'none';
    document.getElementById('plug-ip').required = !mqtt;
}

function populatePlugPrinterOptions(selectedId) {
    const select = document.getElementById('plug-printer');
    select.innerHTML = '<option value="">Unassigned</option>' + printers
        .map(p => `<option value="${p.config.id}" ${p.config.id === selectedId ? 'selected' : ''}>${esc(p.config.name)}</option>`)
        .join('');
}

function openAddSmartPlugModal() {
    document.getElementById('smartplug-modal-title').textContent = 'Add smart plug';
    document.getElementById('plug-id').value = '';
    document.getElementById('plug-mode').value = 'http';
    document.getElementById('plug-ip').value = '';
    document.getElementById('plug-mqtt-topic').value = '';
    document.getElementById('plug-idx').value = '1';
    document.getElementById('plug-label').value = '';
    document.getElementById('plug-hide-label').checked = false;
    togglePlugMode();
    populatePlugPrinterOptions(null);
    document.getElementById('smartplug-modal').classList.add('active');
}

function openEditSmartPlugModal(id) {
    const plug = smartPlugs.find(p => p.id === id);
    if (!plug) return;
    document.getElementById('smartplug-modal-title').textContent = 'Edit smart plug';
    document.getElementById('plug-id').value = plug.id;
    document.getElementById('plug-mode').value = plug.mqtt_topic ? 'mqtt' : 'http';
    document.getElementById('plug-ip').value = plug.ip;
    document.getElementById('plug-mqtt-topic').value = plug.mqtt_topic || '';
    document.getElementById('plug-idx').value = plug.idx;
    document.getElementById('plug-label').value = plug.label;
    document.getElementById('plug-hide-label').checked = !!plug.hide_label;
    togglePlugMode();
    populatePlugPrinterOptions(plug.printer_id);
    document.getElementById('smartplug-modal').classList.add('active');
}

async function saveSmartPlug(e) {
    e.preventDefault();
    const id = document.getElementById('plug-id').value;
    const printerIdStr = document.getElementById('plug-printer').value;
    const mqtt = document.getElementById('plug-mode').value === 'mqtt';
    const data = {
        ip: mqtt ? '' : document.getElementById('plug-ip').value,
        mqtt_topic: mqtt ? document.getElementById('plug-mqtt-topic').value : '',
        idx: document.getElementById('plug-idx').value || '1',
        label: document.getElementById('plug-label').value,
        hide_label: document.getElementById('plug-hide-label').checked,
        printer_id: printerIdStr ? parseInt(printerIdStr) : null,
    };

    try {
        const resp = id
            ? await fetch(`/api/smartplugs/${id}`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)})
            : await fetch('/api/smartplugs', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)});
        if (resp.ok) {
            closeModal();
            await loadSmartPlugs();
            openSettings();
        }
    } catch (e) {}
}

async function deleteSmartPlug(id) {
    try {
        await fetch(`/api/smartplugs/${id}`, {method: 'DELETE'});
        loadSmartPlugs();
    } catch (e) {}
}

// Slicer print-host targets (ingest keys) — one per printer model bucket

let ingestKeys = [];

async function loadIngestKeys() {
    try {
        const resp = await fetch('/api/ingest-keys');
        ingestKeys = (await resp.json()) || [];
        renderSettingsIngestKeyList(ingestKeys);
    } catch (e) {}
}

function renderSettingsIngestKeyList(targets) {
    const list = document.getElementById('settings-ingestkey-list');
    if (!targets.length) {
        list.innerHTML = '<div class="settings-empty">No slicer print targets configured yet.</div>';
        return;
    }
    list.innerHTML = targets.map(t => `
        <div class="settings-printer-row">
            <div class="settings-printer-info">
                <span class="settings-printer-name">${esc(t.label || printerName(t.printer_id) || 'Untitled')}</span>
                <span class="settings-printer-url">Printer: ${esc(printerName(t.printer_id) || `#${t.printer_id}`)} — Host: ${esc(location.origin)}/ingest/${esc(t.label || t.id)}</span>
            </div>
            <div class="settings-printer-actions">
                <button class="btn btn-sm" onclick="closeModal();openEditIngestKeyModal(${t.id})" title="Edit">&#9998; Edit</button>
                <button class="btn btn-sm btn-danger" onclick="confirmAction(this, () => deleteIngestKey(${t.id}))">Delete</button>
            </div>
        </div>`).join('');
}

function printerName(printerID) {
    const p = printers.find(p => p.config.id === printerID);
    return p ? p.config.name : '';
}

function populateIngestKeyPrinterOptions(selectedId) {
    const select = document.getElementById('ingestkey-printer');
    select.innerHTML = printers
        .filter(p => p.config.type === 'prusalink')
        .map(p => `<option value="${p.config.id}" ${p.config.id === selectedId ? 'selected' : ''}>${esc(p.config.name)}</option>`)
        .join('');
}

function openAddIngestKeyModal() {
    document.getElementById('ingestkey-modal-title').textContent = 'Add slicer print target';
    document.getElementById('ingestkey-id').value = '';
    populateIngestKeyPrinterOptions(null);
    document.getElementById('ingestkey-label').value = '';
    const result = document.getElementById('ingestkey-result');
    result.style.display = 'none';
    result.textContent = '';
    document.getElementById('ingestkey-modal').classList.add('active');
}

function openEditIngestKeyModal(id) {
    const target = ingestKeys.find(t => t.id === id);
    if (!target) return;
    document.getElementById('ingestkey-modal-title').textContent = 'Edit slicer print target';
    document.getElementById('ingestkey-id').value = target.id;
    populateIngestKeyPrinterOptions(target.printer_id || null);
    document.getElementById('ingestkey-label').value = target.label;
    const result = document.getElementById('ingestkey-result');
    result.className = 'test-result success';
    result.style.display = 'block';
    result.innerHTML = `Host: <code>${esc(location.origin)}/ingest/${esc(target.label || target.id)}</code><br>API key: <code>${esc(target.api_key)}</code>`;
    document.getElementById('ingestkey-modal').classList.add('active');
}

async function saveIngestKey(e) {
    e.preventDefault();
    const id = document.getElementById('ingestkey-id').value;
    const data = {
        printer_id: parseInt(document.getElementById('ingestkey-printer').value),
        label: document.getElementById('ingestkey-label').value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''),
    };
    try {
        const resp = id
            ? await fetch(`/api/ingest-keys/${id}`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)})
            : await fetch('/api/ingest-keys', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)});
        if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            const result = document.getElementById('ingestkey-result');
            result.className = 'test-result error';
            result.style.display = 'block';
            result.textContent = err.error || 'Failed to save.';
            return;
        }
        await loadIngestKeys();
        if (!id) {
            const created = await resp.json();
            const result = document.getElementById('ingestkey-result');
            result.className = 'test-result success';
            result.style.display = 'block';
            result.innerHTML = `Host: <code>${esc(location.origin)}/ingest/${esc(data.label || created.id)}</code><br>API key: <code>${esc(created.api_key)}</code><br>Paste these into the slicer's Physical Printer dialog (PrusaLink, API key auth).`;
            document.getElementById('ingestkey-id').value = created.id;
            return;
        }
        closeModal();
        openSettings();
    } catch (e) {}
}

async function deleteIngestKey(id) {
    try {
        await fetch(`/api/ingest-keys/${id}`, {method: 'DELETE'});
        loadIngestKeys();
    } catch (e) {}
}

// Cameras (printspy-cam, managed independently of printers)

let cameras = [];

async function loadCameras() {
    try {
        const resp = await fetch('/api/cameras');
        cameras = (await resp.json()) || [];
        renderSettingsCameraList(cameras);
    } catch (e) {}
}

function renderSettingsCameraList(cams) {
    const list = document.getElementById('settings-camera-list');
    if (!cams.length) {
        list.innerHTML = '<div class="settings-empty">No cameras configured yet.</div>';
        return;
    }
    list.innerHTML = cams.map(c => `
        <div class="settings-printer-row">
            <div class="settings-printer-info">
                <span class="settings-printer-name">${esc(c.name || c.url)}</span>
                <span class="settings-printer-url">${esc(c.url)} — ${c.printer_name ? esc(c.printer_name) : 'Unassigned'}</span>
            </div>
            <div class="settings-printer-actions">
                <button class="btn btn-sm" onclick="closeModal();openEditCameraModal(${c.id})" title="Edit">&#9998; Edit</button>
                <button class="btn btn-sm btn-danger" onclick="confirmAction(this, () => deleteCamera(${c.id}))">Delete</button>
            </div>
        </div>`).join('');
}

function populateCameraPrinterOptions(selectedId) {
    const select = document.getElementById('camera-printer');
    select.innerHTML = '<option value="">Unassigned</option>' + printers
        .map(p => `<option value="${p.config.id}" ${p.config.id === selectedId ? 'selected' : ''}>${esc(p.config.name)}</option>`)
        .join('');
}

function openAddCameraModal() {
    document.getElementById('camera-modal-title').textContent = 'Add camera';
    document.getElementById('camera-id').value = '';
    document.getElementById('camera-url').value = '';
    document.getElementById('camera-name').value = '';
    document.getElementById('camera-orientation-group').style.display = 'none';
    populateCameraPrinterOptions(null);
    document.getElementById('camera-modal').classList.add('active');
}

function openEditCameraModal(id) {
    const cam = cameras.find(c => c.id === id);
    if (!cam) return;
    document.getElementById('camera-modal-title').textContent = 'Edit camera';
    document.getElementById('camera-id').value = cam.id;
    document.getElementById('camera-url').value = cam.url;
    document.getElementById('camera-name').value = cam.name;
    populateCameraPrinterOptions(cam.printer_id);
    document.getElementById('camera-orientation-group').style.display = 'block';
    document.getElementById('camera-web-link').href = cam.url;
    document.getElementById('camera-hmirror').checked = false;
    document.getElementById('camera-vflip').checked = false;
    fetch(`/api/cameras/${id}/settings`).then(r => r.ok ? r.json() : null).then(s => {
        if (!s) return;
        document.getElementById('camera-hmirror').checked = !!s.hmirror;
        document.getElementById('camera-vflip').checked = !!s.vflip;
        if (s.resolution !== undefined) document.getElementById('camera-resolution').value = s.resolution;
        if (s.quality !== undefined) {
            document.getElementById('camera-quality').value = s.quality;
            document.getElementById('camera-quality-val').textContent = s.quality;
        }
    }).catch(() => {});
    document.getElementById('camera-modal').classList.add('active');
}

async function saveCamera(e) {
    e.preventDefault();
    const id = document.getElementById('camera-id').value;
    const printerIdStr = document.getElementById('camera-printer').value;
    const data = {
        url: document.getElementById('camera-url').value,
        name: document.getElementById('camera-name').value,
        printer_id: printerIdStr ? parseInt(printerIdStr) : null,
    };

    try {
        const resp = id
            ? await fetch(`/api/cameras/${id}`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)})
            : await fetch('/api/cameras', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)});
        if (resp.ok) {
            closeModal();
            await loadCameras();
            openSettings();
        }
    } catch (e) {}
}

async function saveCameraOrientation() {
    const id = document.getElementById('camera-id').value;
    if (!id) return;
    const data = {
        hmirror: document.getElementById('camera-hmirror').checked,
        vflip: document.getElementById('camera-vflip').checked,
        resolution: parseInt(document.getElementById('camera-resolution').value, 10),
        quality: parseInt(document.getElementById('camera-quality').value, 10),
    };
    try {
        await fetch(`/api/cameras/${id}/settings`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)});
    } catch (e) {}
}

async function deleteCamera(id) {
    try {
        await fetch(`/api/cameras/${id}`, {method: 'DELETE'});
        loadCameras();
    } catch (e) {}
}

// Account

async function changePassword(e) {
    e.preventDefault();
    const current_password = document.getElementById('current-password').value;
    const new_password = document.getElementById('new-password').value;
    try {
        const resp = await fetch('/api/account/password', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({current_password, new_password}),
        });
        if (resp.ok) {
            document.getElementById('current-password').value = '';
            document.getElementById('new-password').value = '';
            alert('Password changed.');
        } else {
            const err = await resp.json();
            alert(err.error || 'Failed to change password');
        }
    } catch (e) {}
}

// Users

async function loadUsers() {
    try {
        const resp = await fetch('/api/users');
        const users = await resp.json();
        renderSettingsUserList(users || []);
    } catch (e) {}
}

function renderSettingsUserList(users) {
    const list = document.getElementById('settings-user-list');
    list.innerHTML = users.map(u => `
        <div class="settings-printer-row">
            <div class="settings-printer-info">
                <span class="settings-printer-name">${esc(u.username)}</span>
            </div>
            <div class="settings-printer-actions">
                <button class="btn btn-sm btn-danger" onclick="confirmAction(this, () => deleteUser(${u.id}))">Delete</button>
            </div>
        </div>`).join('');
}

async function addUser(e) {
    e.preventDefault();
    const username = document.getElementById('new-user-username').value;
    const password = document.getElementById('new-user-password').value;
    try {
        const resp = await fetch('/api/users', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({username, password}),
        });
        if (resp.ok) {
            document.getElementById('new-user-username').value = '';
            document.getElementById('new-user-password').value = '';
            loadUsers();
        } else {
            const err = await resp.json();
            alert(err.error || 'Failed to add user');
        }
    } catch (e) {}
}

async function deleteUser(id) {
    try {
        const resp = await fetch(`/api/users/${id}`, {method: 'DELETE'});
        if (resp.ok) {
            loadUsers();
        } else {
            const err = await resp.json();
            alert(err.error || 'Failed to delete user');
        }
    } catch (e) {}
}

function renderSettingsPrinterList() {
    const list = document.getElementById('settings-printer-list');
    if (!printers || printers.length === 0) {
        list.innerHTML = '<div class="settings-empty">No printers configured yet.</div>';
        return;
    }
    list.innerHTML = printers.map((p, idx) => {
        const cfg = p.config;
        const isFirst = idx === 0;
        const isLast = idx === printers.length - 1;
        return `
            <div class="settings-printer-row">
                <div class="settings-printer-reorder">
                    <button class="reorder-btn" onclick="movePrinter(${cfg.id},-1)" ${isFirst ? 'disabled' : ''} title="Move up">&#9650;</button>
                    <button class="reorder-btn" onclick="movePrinter(${cfg.id},1)" ${isLast ? 'disabled' : ''} title="Move down">&#9660;</button>
                </div>
                <div class="settings-printer-info">
                    <span class="settings-printer-name">${esc(cfg.name)}</span>
                    <span class="settings-printer-url">${esc(cfg.url)} (${esc(p.display_name)})</span>
                </div>
                <div class="settings-printer-actions">
                    <button class="btn btn-sm" onclick="closeModal();openEditModal(${cfg.id})" title="Edit">&#9998; Edit</button>
                    <a class="printer-link" href="${esc(cfg.url)}" target="_blank" rel="noopener" title="Open ${esc(p.display_name)}">${esc(p.display_name)} &#8599;</a>
                    <button class="btn btn-sm btn-maintenance ${cfg.maintenance ? 'active' : ''}" onclick="toggleMaintenance(${cfg.id},${!cfg.maintenance})" title="${cfg.maintenance ? 'End maintenance' : 'Mark as in maintenance'}">Maintenance</button>
                    <button class="btn btn-sm btn-danger" onclick="confirmAction(this, () => deletePrinter(${cfg.id}))">Delete</button>
                </div>
            </div>`;
    }).join('');
}

async function toggleMaintenance(id, maintenance) {
    try {
        await fetch(`/api/printers/${id}/maintenance`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({maintenance}),
        });
        await fetchPrinters();
        renderSettingsPrinterList();
    } catch (e) {}
}

// Printer reordering

async function movePrinter(id, direction) {
    const idx = printers.findIndex(p => p.config.id === id);
    if (idx < 0) return;
    const newIdx = idx + direction;
    if (newIdx < 0 || newIdx >= printers.length) return;

    [printers[idx], printers[newIdx]] = [printers[newIdx], printers[idx]];
    const ids = printers.map(p => p.config.id);

    prevPrinterIDs = [];
    updateDashboard();
    renderSettingsPrinterList();

    await fetch('/api/printers/reorder', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({ids}),
    });
}

// Add/Edit printer modals

function onPrinterTypeChange() {
    const type = document.getElementById('printer-type').value;
    const isPrusalink = type === 'prusalink';
    document.getElementById('username-group').style.display = isPrusalink ? '' : 'none';
    document.getElementById('apikey-label').textContent = isPrusalink ? 'Password' : 'API Key';
    document.getElementById('printer-apikey').placeholder = isPrusalink ? 'Your PrusaLink password' : 'Your OctoPrint API key';
    if (isPrusalink && !document.getElementById('printer-username').value) {
        document.getElementById('printer-username').value = 'maker';
    }
}

function openAddModal() {
    document.getElementById('modal-title').textContent = 'Add printer';
    document.getElementById('printer-id').value = '';
    document.getElementById('printer-name').value = '';
    document.getElementById('printer-model').value = '';
    document.getElementById('printer-hide-model').checked = false;
    document.getElementById('printer-type').value = 'octoprint';
    document.getElementById('printer-url').value = '';
    document.getElementById('printer-username').value = 'maker';
    document.getElementById('printer-apikey').value = '';
    document.getElementById('printer-poll').value = '10';
    document.getElementById('printer-idle-timeout').value = '0';
    document.getElementById('printer-max-bed-temp').value = '0';
    document.getElementById('printer-max-extruder-temp').value = '0';
    document.getElementById('test-btn').style.display = 'inline-flex';
    onPrinterTypeChange();
    hideTestResult();
    document.getElementById('printer-modal').classList.add('active');
}

async function openEditModal(id) {
    const printer = printers.find(p => p.config.id === id);
    if (!printer) return;
    const cfg = printer.config;

    document.getElementById('modal-title').textContent = 'Edit printer';
    document.getElementById('printer-id').value = cfg.id;
    document.getElementById('printer-name').value = cfg.name;
    document.getElementById('printer-model').value = cfg.model || '';
    document.getElementById('printer-hide-model').checked = !!cfg.hide_model;
    document.getElementById('printer-type').value = cfg.type;
    document.getElementById('printer-url').value = cfg.url;
    document.getElementById('printer-apikey').value = '';
    document.getElementById('printer-username').value = cfg.username || 'maker';
    document.getElementById('printer-poll').value = cfg.poll_interval;
    document.getElementById('printer-idle-timeout').value = cfg.idle_timeout_minutes || '0';
    document.getElementById('printer-max-bed-temp').value = cfg.max_bed_temp || '0';
    document.getElementById('printer-max-extruder-temp').value = cfg.max_extruder_temp || '0';
    document.getElementById('test-btn').style.display = 'inline-flex';
    onPrinterTypeChange();
    hideTestResult();
    document.getElementById('printer-modal').classList.add('active');

    try {
        const resp = await fetch(`/api/printers/${cfg.id}`);
        if (resp.ok) {
            const data = await resp.json();
            document.getElementById('printer-apikey').value = data.api_key || '';
            if (data.username) document.getElementById('printer-username').value = data.username;
        }
    } catch (e) {}
}

function closeModal() {
    document.querySelectorAll('.modal-overlay').forEach(m => m.classList.remove('active'));
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

function openUploadModal() {
    const select = document.getElementById('upload-printer');
    select.innerHTML = '';

    const groups = {};
    const ungrouped = [];
    for (const p of printers) {
        if (p.config.type !== 'prusalink') continue;
        if (p.config.model) {
            (groups[p.config.model] = groups[p.config.model] || []).push(p);
        } else {
            ungrouped.push(p);
        }
    }

    for (const p of ungrouped) {
        const opt = document.createElement('option');
        opt.value = p.config.id;
        opt.textContent = p.config.name;
        select.appendChild(opt);
    }
    for (const model of Object.keys(groups).sort()) {
        const optgroup = document.createElement('optgroup');
        optgroup.label = model;
        for (const p of groups[model]) {
            const opt = document.createElement('option');
            opt.value = p.config.id;
            opt.textContent = p.config.name;
            optgroup.appendChild(opt);
        }
        select.appendChild(optgroup);
    }

    if (!select.options.length) {
        const opt = document.createElement('option');
        opt.value = '';
        opt.textContent = 'No PrusaLink printers configured';
        select.appendChild(opt);
    }

    document.getElementById('upload-file').value = '';
    document.getElementById('upload-print-now').checked = false;
    const result = document.getElementById('upload-result');
    result.style.display = 'none';
    result.className = 'test-result';
    result.textContent = '';

    document.getElementById('upload-modal').classList.add('active');
}

async function submitUpload(e) {
    e.preventDefault();
    const id = document.getElementById('upload-printer').value;
    const file = document.getElementById('upload-file').files[0];
    const printNow = document.getElementById('upload-print-now').checked;
    const result = document.getElementById('upload-result');

    if (!id || !file) return;

    result.style.display = 'block';
    result.className = 'test-result';
    result.textContent = 'Uploading...';

    try {
        const url = `/api/printers/${id}/upload?filename=${encodeURIComponent(file.name)}&print_now=${printNow}`;
        const resp = await fetch(url, {
            method: 'POST',
            headers: {'Content-Type': 'application/octet-stream'},
            body: file,
        });
        const data = await resp.json();
        if (resp.ok && data.success) {
            closeModal();
            refreshFileManagerIfShowing(id);
        } else {
            result.className = 'test-result error';
            result.textContent = `Upload failed: ${data.error || 'unknown error'}`;
        }
    } catch (e) {
        result.className = 'test-result error';
        result.textContent = 'Upload failed';
    }
}

function hideTestResult() {
    const el = document.getElementById('test-result');
    el.style.display = 'none';
    el.className = 'test-result';
    el.textContent = '';
}

async function savePrinter(e) {
    e.preventDefault();
    const id = document.getElementById('printer-id').value;
    const printerType = document.getElementById('printer-type').value;
    const data = {
        name: document.getElementById('printer-name').value,
        type: printerType,
        model: document.getElementById('printer-model').value,
        hide_model: document.getElementById('printer-hide-model').checked,
        url: document.getElementById('printer-url').value.replace(/\/+$/, ''),
        api_key: document.getElementById('printer-apikey').value,
        username: printerType === 'prusalink' ? document.getElementById('printer-username').value : '',
        poll_interval: parseInt(document.getElementById('printer-poll').value) || 10,
        idle_timeout_minutes: parseInt(document.getElementById('printer-idle-timeout').value) || 0,
        max_bed_temp: parseFloat(document.getElementById('printer-max-bed-temp').value) || 0,
        max_extruder_temp: parseFloat(document.getElementById('printer-max-extruder-temp').value) || 0,
        enabled: true,
    };

    try {
        let resp;
        if (id) {
            resp = await fetch(`/api/printers/${id}`, {
                method: 'PUT',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(data),
            });
        } else {
            resp = await fetch('/api/printers', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(data),
            });
        }
        if (resp.ok) {
            closeModal();
            await fetchPrinters();
            openSettings();
        }
    } catch (e) {}
}

async function deletePrinter(id) {
    try {
        await fetch(`/api/printers/${id}`, {method: 'DELETE'});
        await fetchPrinters();
        renderSettingsPrinterList();
    } catch (e) {}
}

async function testConnection() {
    const printerURL = document.getElementById('printer-url').value.replace(/\/+$/, '');
    const apiKey = document.getElementById('printer-apikey').value;
    const printerType = document.getElementById('printer-type').value;

    if (!printerURL || !apiKey) {
        const el = document.getElementById('test-result');
        el.style.display = 'block';
        el.className = 'test-result error';
        el.textContent = 'URL and API key are required';
        return;
    }

    const el = document.getElementById('test-result');
    el.style.display = 'block';
    el.className = 'test-result';
    el.textContent = 'Testing connection...';

    try {
        const testBody = {type: printerType, url: printerURL, api_key: apiKey};
        if (printerType === 'prusalink') {
            testBody.username = document.getElementById('printer-username').value;
        }
        const resp = await fetch('/api/test', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(testBody),
        });
        const data = await resp.json();
        if (data.success) {
            el.className = 'test-result success';
            el.textContent = 'Connection successful!';
            const nameField = document.getElementById('printer-name');
            if (!nameField.value && data.name) {
                nameField.value = data.name;
            }
        } else {
            el.className = 'test-result error';
            el.textContent = `Connection failed: ${data.error}`;
        }
    } catch (e) {
        el.className = 'test-result error';
        el.textContent = 'Connection test failed';
    }
}

// General settings

async function saveSettings(e) {
    e.preventDefault();
    const settings = {
        snapshot_interval: document.getElementById('setting-snapshot-interval').value,
        hide_webcam_when_unreachable: document.getElementById('setting-hide-webcam').checked ? '1' : '0',
        history_retention_days: document.getElementById('setting-history-retention').value || '0',
        print_control_timeout_secs: document.getElementById('setting-print-control-timeout').value || '15',
        prusalink_ping_interval: document.getElementById('setting-prusalink-ping-interval').value || '0',
        auto_off_idle_minutes: document.getElementById('setting-auto-off-idle').value || '0',
        auto_off_cooldown_temp: document.getElementById('setting-auto-off-cooldown').value || '40',
        thermal_max_bed_temp: document.getElementById('setting-thermal-max-bed').value || '0',
        thermal_max_extruder_temp: document.getElementById('setting-thermal-max-extruder').value || '0',
    };
    const pollVal = document.getElementById('setting-poll-interval').value;
    if (pollVal) settings.poll_interval = pollVal;
    await fetch('/api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(settings),
    });
    snapshotInterval = parseInt(settings.snapshot_interval) || 10;
    restartSnapshotTimer();
    hideWebcamWhenUnreachable = settings.hide_webcam_when_unreachable === '1';
    prevPrinterIDs = [];
    updateDashboard();
    closeModal();
}

async function saveNotificationSettings(e) {
    e.preventDefault();
    const settings = {
        pushover_user_key: document.getElementById('setting-pushover-user-key').value,
        pushover_app_token: document.getElementById('setting-pushover-app-token').value,
        notify_on_start: document.getElementById('setting-notify-start').checked ? '1' : '0',
        notify_on_complete: document.getElementById('setting-notify-complete').checked ? '1' : '0',
        notify_on_failed: document.getElementById('setting-notify-failed').checked ? '1' : '0',
        notify_on_error: document.getElementById('setting-notify-error').checked ? '1' : '0',
        notify_checkpoint1_enabled: document.getElementById('setting-notify-checkpoint1-enabled').checked ? '1' : '0',
        notify_checkpoint1_percent: document.getElementById('setting-notify-checkpoint1-percent').value || '5',
        notify_checkpoint2_enabled: document.getElementById('setting-notify-checkpoint2-enabled').checked ? '1' : '0',
        notify_checkpoint2_percent: document.getElementById('setting-notify-checkpoint2-percent').value || '50',
    };
    NOTIFY_TYPES.forEach(t => {
        settings[`notify_${t}_title`] = document.getElementById(`setting-notify-${t}-title`).value;
        settings[`notify_${t}_message`] = document.getElementById(`setting-notify-${t}-message`).value;
        settings[`notify_${t}_sound`] = document.getElementById(`setting-notify-${t}-sound`).value;
        settings[`notify_${t}_image`] = document.getElementById(`setting-notify-${t}-image`).value;
        settings[`notify_${t}_high_priority`] = document.getElementById(`setting-notify-${t}-high-priority`).checked ? '1' : '0';
    });
    await fetch('/api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(settings),
    });
    closeModal();
}

async function sendTestNotification() {
    const result = document.getElementById('notify-test-result');
    result.textContent = 'Sending...';
    try {
        const resp = await fetch('/api/notify-test', {method: 'POST'});
        const data = await resp.json();
        result.textContent = data.success ? 'Test notification sent - check your device.' : `Failed: ${data.error}`;
    } catch (e) {
        result.textContent = `Failed: ${e.message}`;
    }
}

async function saveMQTTSettings(e) {
    e.preventDefault();
    const hostPort = document.getElementById('setting-mqtt-broker-url').value;
    const tls = document.getElementById('setting-mqtt-tls').checked;
    const settings = {
        mqtt_broker_url: hostPort ? (tls ? 'ssl://' : 'tcp://') + hostPort : '',
        mqtt_username: document.getElementById('setting-mqtt-username').value,
        mqtt_password: document.getElementById('setting-mqtt-password').value,
    };
    await fetch('/api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(settings),
    });
    closeModal();
}

async function sendMQTTTest() {
    const result = document.getElementById('mqtt-test-result');
    result.textContent = 'Connecting...';
    try {
        const resp = await fetch('/api/mqtt-test', {method: 'POST'});
        const data = await resp.json();
        result.textContent = data.success ? 'Connected to broker successfully.' : `Failed: ${data.error}`;
    } catch (e) {
        result.textContent = `Failed: ${e.message}`;
    }
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
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
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
    if (!confirm('Import will add printers and overwrite settings from this file. Continue?')) {
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
            const data = await resp.json();
            alert(`Import complete: ${data.printers_added} printer(s), ${data.plugs_added} smart plug(s), ${data.cameras_added} camera(s) added.`);
            await fetchPrinters();
            openSettings();
        }
    } catch (e) {}
    input.value = '';
}

// Event listeners
document.querySelectorAll('.modal-overlay').forEach(modal => {
    modal.addEventListener('click', function(e) {
        if (e.target === this) closeModal();
    });
});

document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape') closeModal();
});

// Slicer ingest jobs — files staged by a slicer, awaiting dispatch to a printer

let ingestJobs = [];

async function loadIngestJobs() {
    try {
        const resp = await fetch('/api/ingest-jobs');
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
            banner.className = 'ingest-banner';
            banner.innerHTML = `
                <span>&#128196; ${esc(job.filename)} ${job.error ? `failed to relay — <span class="msg-error">${esc(job.error)}</span>` : `waiting for ${esc(cfg.name)} to come online`}</span>
                <div class="ingest-banner-actions">
                    ${job.error ? `<button class="btn btn-sm btn-primary" onclick="retryIngestJob(${job.id})">Retry</button>` : ''}
                    <button class="btn btn-sm btn-danger" onclick="confirmAction(this, () => discardIngestJob(${job.id}))">Discard</button>
                </div>`;
            card.appendChild(banner);
        }
    }
}

async function retryIngestJob(jobID) {
    try {
        await fetch(`/api/ingest-jobs/${jobID}/retry`, {method: 'POST'});
        loadIngestJobs();
    } catch (e) {}
}

async function discardIngestJob(jobID) {
    try {
        await fetch(`/api/ingest-jobs/${jobID}`, {method: 'DELETE'});
        loadIngestJobs();
    } catch (e) {}
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
    if (el && data.version) el.textContent = 'v' + data.version;
}).catch(() => {});

connectSSE();
