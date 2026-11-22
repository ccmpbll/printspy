// Files and History pages (per printer), the Upload dialog and the Raw API
// window. Routes: #/files/<printer id> and #/history/<printer id>.

const HISTORY_PAGE_SIZE = 20;

let filesState = {printerId: null, files: [], filter: 'all', sort: 'new', query: ''};
let historyState = {printerId: null, page: 0, result: '', query: ''};
let historyReq = 0; // module-level so a printer switch can't reuse an in-flight request's number
let filesReq = 0;

function pageTitle(name, label) {
    return `<div class="set-title"><h2><a class="crumb" href="#/">Dashboard</a> <span class="sep">&rsaquo;</span> ${label}${name ? ` <span class="sep">&rsaquo;</span> ${esc(name)}` : ''}</h2></div>`;
}

function printerChips(kind, currentId) {
    return `<div class="fleet">${printers.map(p => {
        const state = p.config.maintenance ? 'maintenance' : (p.status ? p.status.state : 'offline');
        return `<a class="fleet-chip ${esc(fleetGroup(state))} ${p.config.id === currentId ? 'on' : ''}" href="#/${kind}/${p.config.id}"><span class="dot"></span>${esc(p.config.name)}</a>`;
    }).join('')}</div>`;
}

function segPills(current, handler, options) {
    return `<div class="seg-pills">${options.map(([v, l]) => `<button class="${current === v ? 'active' : ''}" onclick="${handler}('${v}')">${l}</button>`).join('')}</div>`;
}

const ICON_UP = '<svg class="ic" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 20V5"/><path d="M5.5 11.5 12 5l6.5 6.5"/></svg>';
const ICON_LEFT = '<svg class="ic" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 12H5"/><path d="M11.5 5.5 5 12l6.5 6.5"/></svg>';

function materialChips(tools) {
    return (tools || []).map(t => `<span class="mat" title="Tool ${t.tool_index + 1}">T${t.tool_index + 1} ${esc(t.material)}</span>`).join('');
}

// Thumbnails load as they scroll into view, not all at once when the page opens.
const lazyThumbObserver = new IntersectionObserver(entries => {
    for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.src = entry.target.dataset.src;
        lazyThumbObserver.unobserve(entry.target);
    }
}, {rootMargin: '200px'});

function observeLazyThumbs(container) {
    container.querySelectorAll('.lazy-thumb').forEach(img => lazyThumbObserver.observe(img));
}

const thumbOrBlank = (src) => `<img class="lazy-thumb" data-src="${src}" alt="" onerror="this.style.display='none'">`;

// ---------- Files ----------

async function renderFilesRoute(printerId) {
    const token = ++pageToken;
    if (!printers.length) await fetchPrinters();
    const printer = printers.find(p => p.config.id === printerId);
    if (!printer) { $('page').innerHTML = '<div class="empty-state"><h2>Printer not found</h2><p><a class="crumb" href="#/">Back to the dashboard</a></p></div>'; return; }
    if (filesState.printerId !== printerId) filesState = {printerId, files: [], filter: 'all', sort: 'new', query: ''};
    const layout = lsGet('files-layout') === 'grid' ? 'grid' : 'list';
    const canUpload = printer.config.type === 'prusalink';
    $('page').innerHTML = pageTitle(printer.config.name, 'Files') + printerChips('files', printerId) + `
        <div class="f-toolbar">
            <div class="inp search"><span class="mag">&#9906;</span><input type="text" placeholder="Search files" aria-label="Search files" value="${esc(filesState.query)}" oninput="filesState.query=this.value;renderFilesBody()"></div>
            <div id="f-filter">${segPills(filesState.filter, 'setFilesFilter', [['all', 'All'], ['new', 'New'], ['printed', 'Printed'], ['failed', 'Failed']])}</div>
            <div class="inp sel mini"><select aria-label="Sort" onchange="filesState.sort=this.value;renderFilesBody()"><option value="new" ${filesState.sort === 'new' ? 'selected' : ''}>Newest first</option><option value="name" ${filesState.sort === 'name' ? 'selected' : ''}>Name</option><option value="printed" ${filesState.sort === 'printed' ? 'selected' : ''}>Most printed</option></select></div>
            <div id="f-layout">${segPills(layout, 'setFilesLayout', [['list', 'List'], ['grid', 'Grid']])}</div>
            <span class="spacer"></span>
            ${canUpload ? `<button class="btn" onclick="openUploadDialog(${printerId})">${ICON_UP} Upload to ${esc(printer.config.name)}</button>` : `<button class="btn" disabled title="Uploads are only supported for PrusaLink printers">${ICON_UP} Upload to ${esc(printer.config.name)}</button>`}
            <a class="btn btn-dash" href="#/">${ICON_LEFT} Dashboard</a>
        </div>
        <div class="f-count" id="f-count"></div>
        <div id="f-body"><div class="set-empty">Loading…</div></div>`;
    await loadFiles(token);
}

async function loadFiles(token) {
    const printerId = filesState.printerId;
    const req = ++filesReq; // a slower earlier response must not overwrite a newer one
    try {
        const resp = await fetch(`/api/printers/${printerId}/recent?all=1`);
        const files = await resp.json();
        if (req !== filesReq || (token !== undefined && token !== pageToken)) return;
        if (!resp.ok) { filesState.error = (files && files.error) || 'Failed to load files.'; filesState.files = []; }
        else { filesState.error = ''; filesState.files = files || []; }
    } catch (e) {
        if (req !== filesReq) return;
        filesState.error = 'Failed to load files.';
        filesState.files = [];
    }
    renderFilesBody();
}

function setFilesFilter(v) { filesState.filter = v; $('f-filter').innerHTML = segPills(v, 'setFilesFilter', [['all', 'All'], ['new', 'New'], ['printed', 'Printed'], ['failed', 'Failed']]); renderFilesBody(); }
function setFilesLayout(v) { lsSet('files-layout', v); $('f-layout').innerHTML = segPills(v, 'setFilesLayout', [['list', 'List'], ['grid', 'Grid']]); renderFilesBody(); }

function fileStatus(f, printingNow) {
    if (printingNow) return '<span class="fchip now">Printing now</span>';
    if (f.last_success === false) return '<span class="fchip bad">Failed</span>';
    if (f.success_count > 0) return `<span class="fchip ok">${f.success_count}x printed</span>`;
    return '<span class="fchip new">New</span>';
}

function fileActions(f, printerId, printingNow) {
    const data = `data-printer="${printerId}" data-origin="${esc(f.origin)}" data-path="${esc(f.path)}"`;
    const label = f.success_count > 0 ? '&#8634; Reprint' : '<span class="icon-play"></span> Print';
    return `<div class="f-actions">
        <button class="btn btn-acc" ${data} ${printingNow ? 'disabled title="Printing now"' : ''} onclick="confirmAction(this, () => startReprint(this))" title="Print">${label}</button>
        <a class="btn" href="/api/printers/${printerId}/download?origin=${encodeURIComponent(f.origin)}&path=${encodeURIComponent(f.path)}&filename=${encodeURIComponent(f.file_name)}" title="Download">&#8595; Download</a>
        <button class="btn btn-danger" ${data} ${printingNow ? 'disabled' : ''} onclick="confirmAction(this, () => deleteManagedFile(this))">Delete</button>
    </div>`;
}

function visibleFiles() {
    const q = filesState.query.trim().toLowerCase();
    const list = filesState.files.filter(f => {
        if (q && !f.file_name.toLowerCase().includes(q)) return false;
        switch (filesState.filter) {
            case 'new': return !(f.success_count > 0) && f.last_success !== false;
            case 'printed': return f.success_count > 0 && f.last_success !== false;
            case 'failed': return f.last_success === false;
        }
        return true;
    });
    if (filesState.sort === 'name') list.sort((a, b) => a.file_name.localeCompare(b.file_name));
    else if (filesState.sort === 'printed') list.sort((a, b) => (b.success_count || 0) - (a.success_count || 0));
    else list.sort((a, b) => (b.uploaded_at || 0) - (a.uploaded_at || 0));
    return list;
}

function renderFilesBody() {
    const body = $('f-body');
    if (!body) return;
    const printerId = filesState.printerId;
    const printer = printers.find(p => p.config.id === printerId);
    const count = $('f-count');
    if (filesState.error) {
        if (count) count.textContent = '';
        body.innerHTML = `<div class="f-empty"><b>Can't load files for ${esc(printer ? printer.config.name : 'this printer')}</b><span>${esc(filesState.error)}</span><button class="btn" onclick="renderFilesRoute(${printerId})">Try again</button></div>`;
        return;
    }
    const list = visibleFiles();
    if (count) count.textContent = `${list.length} ${list.length === 1 ? 'file' : 'files'}`;
    if (!list.length) {
        const filtered = filesState.query || filesState.filter !== 'all';
        body.innerHTML = `<div class="f-empty"><b>${filtered ? 'No files match' : 'No files found'}</b><span>${filtered ? 'Try clearing the search or filter.' : 'Upload a file to get started.'}</span></div>`;
        return;
    }
    const s = printer && printer.status;
    const nowName = s && (s.state === 'printing' || s.state === 'paused') && s.job ? s.job.file_name : '';
    const layout = lsGet('files-layout') === 'grid' ? 'grid' : 'list';
    const thumb = (f) => thumbOrBlank(`/api/file-thumbnail/${printerId}?path=${encodeURIComponent(f.path)}&uploaded_at=${f.uploaded_at}${f.thumbnail_path ? '&thumb=' + encodeURIComponent(f.thumbnail_path) : ''}`);
    body.innerHTML = layout === 'grid'
        ? `<div class="f-grid">${list.map(f => { const now = f.file_name === nowName; return `<div class="f-card">
            <div class="f-cardimg">${thumb(f)}<span class="f-cardstat">${fileStatus(f, now)}</span></div>
            <div class="f-cardbody"><span class="f-name" title="${esc(f.file_name)}">${esc(f.file_name)}</span><span class="f-meta"><span>${formatDate(f.uploaded_at)}</span></span><span class="f-tools">${materialChips(f.tools)}</span></div>
            ${fileActions(f, printerId, now)}</div>`; }).join('')}</div>`
        : `<div class="f-list">${list.map(f => { const now = f.file_name === nowName; return `<div class="f-row">
            <div class="f-thumb">${thumb(f)}</div>
            <div class="f-info"><span class="f-name" title="${esc(f.file_name)}">${esc(f.file_name)}</span><span class="f-meta">${fileStatus(f, now)}<span>${formatDate(f.uploaded_at)}</span></span>${f.tools && f.tools.length ? `<span class="f-tools">${materialChips(f.tools)}</span>` : ''}</div>
            ${fileActions(f, printerId, now)}</div>`; }).join('')}</div>`;
    observeLazyThumbs(body);
}

async function startReprint(btn) {
    const printerId = btn.dataset.printer;
    try {
        const resp = await fetch(`/api/printers/${printerId}/print`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({origin: btn.dataset.origin, path: btn.dataset.path}),
        });
        const data = await resp.json().catch(() => ({}));
        if (resp.ok) {
            if (data.status) applyStatusUpdate(parseInt(printerId), data.status);
            if (filesState.printerId === parseInt(printerId) && location.hash.startsWith('#/files/')) renderFilesBody();
        } else {
            alert(data.error || 'Failed to start print.');
        }
    } catch (e) { netFail('Failed to start print.'); }
}

async function deleteManagedFile(btn) {
    const printerId = btn.dataset.printer;
    try {
        const resp = await fetch(`/api/printers/${printerId}/recent?origin=${encodeURIComponent(btn.dataset.origin)}&path=${encodeURIComponent(btn.dataset.path)}`, {method: 'DELETE'});
        if (await checkOk(resp, 'Failed to delete file.') && location.hash.startsWith('#/files/')) await loadFiles();
    } catch (e) { netFail('Failed to delete file.'); }
}

// ---------- History ----------

async function renderHistoryRoute(printerId) {
    const token = ++pageToken;
    if (!printers.length) await fetchPrinters();
    const printer = printers.find(p => p.config.id === printerId);
    if (!printer) { $('page').innerHTML = '<div class="empty-state"><h2>Printer not found</h2><p><a class="crumb" href="#/">Back to the dashboard</a></p></div>'; return; }
    if (historyState.printerId !== printerId) historyState = {printerId, page: 0, result: '', query: ''};
    $('page').innerHTML = pageTitle(printer.config.name, 'History') + printerChips('history', printerId) + `
        <div class="h-stats" id="h-stats"></div>
        <div class="f-toolbar">
            <div class="inp search"><span class="mag">&#9906;</span><input type="text" placeholder="Search history" aria-label="Search history" value="${esc(historyState.query)}" oninput="historySearch(this.value)"></div>
            <div id="h-filter">${segPills(historyState.result, 'setHistoryResult', [['', 'All'], ['completed', 'Completed'], ['failed', 'Failed'], ['cancelled', 'Cancelled']])}</div>
            <span class="spacer"></span>
            <a class="btn btn-dash" href="#/">${ICON_LEFT} Dashboard</a>
        </div>
        <div id="h-body"><div class="set-empty">Loading…</div></div>`;
    loadHistorySummary(printerId, token);
    loadHistoryPage(token);
}

async function loadHistorySummary(printerId, token) {
    try {
        const resp = await fetch(`/api/printers/${printerId}/history`);
        if (!resp.ok || token !== pageToken) return;
        const s = await resp.json();
        const el = $('h-stats');
        if (!el || !s) return;
        el.innerHTML = `<div class="h-stat"><span>Prints</span><b>${s.count}</b></div>
            <div class="h-stat"><span>Time printed</span><b>${s.total_hours.toFixed(1)}<small>h</small></b></div>
            <div class="h-stat"><span>Success rate</span><b>${s.success_rate}<small>%</small></b><i class="h-bar"><u style="width:${s.success_rate}%"></u></i></div>`;
    } catch (e) {}
}

let historySearchTimer = null;
function historySearch(value) {
    historyState.query = value;
    historyState.page = 0;
    clearTimeout(historySearchTimer);
    historySearchTimer = setTimeout(() => loadHistoryPage(), 250);
}
function setHistoryResult(v) {
    historyState.result = v;
    historyState.page = 0;
    $('h-filter').innerHTML = segPills(v, 'setHistoryResult', [['', 'All'], ['completed', 'Completed'], ['failed', 'Failed'], ['cancelled', 'Cancelled']]);
    loadHistoryPage();
}
function historyPage(delta) { historyState.page = Math.max(0, historyState.page + delta); loadHistoryPage(); }

async function loadHistoryPage(token) {
    const body = $('h-body');
    if (!body) return;
    const req = ++historyReq; // latest request wins
    const st = historyState;
    const params = new URLSearchParams({limit: HISTORY_PAGE_SIZE, offset: st.page * HISTORY_PAGE_SIZE});
    if (st.result) params.set('result', st.result);
    if (st.query.trim()) params.set('q', st.query.trim());
    try {
        const resp = await fetch(`/api/printers/${st.printerId}/history/list?${params}`);
        const data = await resp.json();
        if (req !== historyReq || (token !== undefined && token !== pageToken) || !$('h-body')) return;
        if (!resp.ok) { body.innerHTML = `<div class="f-empty"><b>Failed to load history</b><span>${esc(data.error || '')}</span></div>`; return; }
        const entries = data.entries || [];
        if (!entries.length) {
            const filtered = st.result || st.query.trim();
            body.innerHTML = `<div class="f-empty"><b>${st.page > 0 ? 'No more entries' : filtered ? 'No prints match' : 'No print history yet'}</b><span>${filtered ? 'Try clearing the search or filter.' : 'Finished prints show up here.'}</span></div>`;
            return;
        }
        body.innerHTML = `<div class="f-list">${entries.map(historyRowHTML).join('')}</div>
            <div class="h-pager"><button class="btn" ${st.page === 0 ? 'disabled' : ''} onclick="historyPage(-1)">&larr; Prev</button><span>Page ${st.page + 1}</span><button class="btn" ${data.has_more ? '' : 'disabled'} onclick="historyPage(1)">Next &rarr;</button></div>`;
        observeLazyThumbs(body);
    } catch (e) {
        if (req === historyReq && $('h-body')) body.innerHTML = '<div class="f-empty"><b>Failed to load history</b></div>';
    }
}

function historyRowHTML(h) {
    const status = h.result === 'failed' ? '<span class="fchip bad">Failed</span>' : h.result === 'cancelled' ? '<span class="fchip">Cancelled</span>' : '<span class="fchip ok">Completed</span>';
    const tools = (h.tools && h.tools.length) ? h.tools : (h.material ? [{material: h.material, tool_index: h.tool_index}] : []);
    const filament = h.filament_used_g ? (h.filament_cost ? `${h.filament_used_g.toFixed(0)}g ($${h.filament_cost.toFixed(2)})` : `${h.filament_used_g.toFixed(0)}g`) : '';
    const details = [
        h.layer_height_mm ? `Layer ${h.layer_height_mm}mm` : '',
        h.fill_density ? `Fill ${esc(h.fill_density)}` : '',
        filament ? `Filament ${filament}` : '',
        h.tool_changes ? `${h.tool_changes} tool changes` : '',
    ].filter(Boolean).join(' · ');
    return `<div class="f-row h-row">
        <div class="f-thumb">${h.has_thumbnail ? thumbOrBlank(`/api/history-thumbnail/${h.id}`) : '<div class="nothumb">No preview</div>'}</div>
        <div class="f-info"><span class="f-name" title="${esc(h.filename)}">${esc(h.filename)}</span>
            <span class="f-meta">${status}<span>${esc(formatHistoryDate(h.completed_at))}</span></span>
            <span class="f-tools">${materialChips(tools)}${details ? `<span class="h-det">${details}</span>` : ''}</span></div>
        <div class="h-dur"><b>${formatDuration(h.duration_secs)}</b>${h.estimated_secs ? `<small>est. ${formatDuration(h.estimated_secs)}</small>` : ''}</div>
    </div>`;
}

// ---------- Upload dialog ----------

let upload = null; // {printerId, file, printNow, status: 'idle'|'uploading'|'error', error}

function uploadPrinters() { return printers.filter(p => p.config.type === 'prusalink'); }

let dialogOpener = null; // element to refocus when the upload / raw-API dialog closes
function restoreDialogFocus() {
    if (dialogOpener && dialogOpener.isConnected) dialogOpener.focus();
    dialogOpener = null;
}

function openUploadDialog(printerId) {
    dialogOpener = document.activeElement;
    const list = uploadPrinters();
    const preferred = list.find(p => p.config.id === printerId) || list.find(p => p.config.id === filesState.printerId && location.hash.startsWith('#/files/')) || list[0];
    upload = {printerId: preferred ? preferred.config.id : null, file: null, printNow: false, status: 'idle', error: ''};
    drawUploadDialog();
}

function closeUploadDialog() {
    upload = null;
    const root = $('upload-root');
    if (root) { root.remove(); restoreDialogFocus(); }
}

function fmtSize(bytes) {
    return bytes >= 1048576 ? (bytes / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(bytes / 1024)) + ' KB';
}

function uploadPick(file) {
    if (!file) return;
    if (!/\.(b?gcode|gco|g)$/i.test(file.name)) {
        upload.file = null; upload.status = 'error'; upload.error = 'Only .gcode, .bgcode, .gco and .g files can be uploaded.';
    } else {
        upload.file = file; upload.status = 'idle'; upload.error = '';
    }
    drawUploadDialog();
}

function drawUploadDialog() {
    let root = $('upload-root');
    if (!root) { root = document.createElement('div'); root.id = 'upload-root'; document.body.appendChild(root); }
    const list = uploadPrinters();
    const groups = {}, ungrouped = [];
    list.forEach(p => { if (p.config.model) (groups[p.config.model] = groups[p.config.model] || []).push(p); else ungrouped.push(p); });
    const opt = p => `<option value="${p.config.id}" ${p.config.id === upload.printerId ? 'selected' : ''}>${esc(p.config.name)}</option>`;
    const options = list.length
        ? ungrouped.map(opt).join('') + Object.keys(groups).sort().map(m => `<optgroup label="${esc(m)}">${groups[m].map(opt).join('')}</optgroup>`).join('')
        : '<option value="">No PrusaLink printers configured</option>';
    const busy = upload.status === 'uploading', file = upload.file;
    root.innerHTML = `<div class="dialog-overlay" onclick="if(event.target===this)closeUploadDialog()">
        <div class="dialog" role="dialog" aria-label="Upload a file">
            <h2>Upload a file</h2>
            <div class="fld"><label for="u-printer">Printer</label><div class="inp sel"><select id="u-printer" onchange="upload.printerId=parseInt(this.value)||null" ${busy ? 'disabled' : ''}>${options}</select></div><small>Uploads go to PrusaLink printers.</small></div>
            <div class="fld"><label>File</label>
                ${file ? `<div class="up-file"><span class="up-fname"><b title="${esc(file.name)}">${esc(file.name)}</b><small>${fmtSize(file.size)}</small></span>${busy ? '' : '<button class="btn" onclick="upload.file=null;upload.status=\'idle\';upload.error=\'\';drawUploadDialog()">Remove</button>'}</div>`
                : `<div class="up-drop" ondragover="event.preventDefault();this.classList.add('over')" ondragleave="this.classList.remove('over')" ondrop="event.preventDefault();this.classList.remove('over');uploadPick(event.dataTransfer.files[0])" onclick="$('u-file').click()" role="button" tabindex="0" onkeydown="if(event.key==='Enter'||event.key===' '){event.preventDefault();$('u-file').click()}">
                    <svg width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 16V4"/><polyline points="7 9 12 4 17 9"/><path d="M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3"/></svg>
                    <b>Drop a file here</b><span>or <u>browse</u> &middot; .gcode, .bgcode</span></div>
                  <input id="u-file" type="file" accept=".gcode,.bgcode,.gco,.g" hidden onchange="uploadPick(this.files[0])">`}
            </div>
            ${busy ? '<div class="up-progress"><i></i></div>' : ''}
            <div class="swrow flat"><span class="swtext"><b>Print immediately</b></span><span class="sw ${upload.printNow ? 'on' : ''}" role="switch" tabindex="0" aria-checked="${upload.printNow}" onclick="upload.printNow=!upload.printNow;this.classList.toggle('on')"></span></div>
            ${busy ? '<div class="up-status">Uploading…</div>' : upload.status === 'error' ? `<div class="up-status err">${esc(upload.error)}</div>` : ''}
            <div class="dialog-foot"><button class="btn btn-quiet" onclick="closeUploadDialog()" ${busy ? 'disabled' : ''}>Cancel</button>
                <button class="btn btn-primary" onclick="submitUpload()" ${!file || busy || !upload.printerId ? 'disabled' : ''}>${busy ? 'Uploading…' : 'Upload'}</button></div>
        </div></div>`;
}

async function submitUpload() {
    if (!upload || !upload.file || !upload.printerId) return;
    const {printerId, file, printNow} = upload;
    upload.status = 'uploading';
    drawUploadDialog();
    try {
        const url = `/api/printers/${printerId}/upload?filename=${encodeURIComponent(file.name)}&print_now=${printNow}`;
        const resp = await fetch(url, {method: 'POST', headers: {'Content-Type': 'application/octet-stream'}, body: file});
        const data = await resp.json();
        if (!upload) return;
        if (resp.ok && data.success) {
            closeUploadDialog();
            if (filesState.printerId === printerId && location.hash.startsWith('#/files/')) loadFiles();
        } else {
            upload.status = 'error';
            upload.error = `Upload failed: ${data.error || 'unknown error'}`;
            drawUploadDialog();
        }
    } catch (e) {
        if (!upload) return;
        upload.status = 'error';
        upload.error = 'Upload failed';
        drawUploadDialog();
    }
}

// ---------- Raw API window (PrusaLink debug view) ----------

let rawTimer = null;
const RAW_POLL_MS = 5000;

function openRawApi(printerId) {
    closeRawApi();
    dialogOpener = document.activeElement;
    let root = document.createElement('div');
    root.id = 'raw-root';
    root.innerHTML = `<div class="dialog-overlay" onclick="if(event.target===this)closeRawApi()">
        <div class="dialog wide" role="dialog" aria-label="Raw API">
            <h2>Raw API <span class="dialog-sub">${esc(printerName(printerId))}</span></h2>
            <p class="raw-note"><span class="live-dot"></span>Every PrusaLink endpoint, unfiltered. Refreshes every 5 seconds while this is open.</p>
            <div class="raw-body" id="raw-body"><div class="set-empty">Loading…</div></div>
            <div class="dialog-foot"><button class="btn" onclick="closeRawApi()">Close</button></div>
        </div></div>`;
    document.body.appendChild(root);
    loadRawApi(printerId);
    rawTimer = setInterval(() => loadRawApi(printerId), RAW_POLL_MS);
}

function closeRawApi() {
    if (rawTimer) { clearInterval(rawTimer); rawTimer = null; }
    const root = $('raw-root');
    if (root) { root.remove(); restoreDialogFocus(); }
}

async function loadRawApi(printerId) {
    const body = $('raw-body');
    if (!body) return;
    try {
        const resp = await fetch(`/api/printers/${printerId}/debug`);
        if (!resp.ok) {
            body.innerHTML = `<div class="set-empty">${esc((await resp.json().catch(() => ({}))).error || 'Failed to load.')}</div>`;
            return;
        }
        const dump = await resp.json();
        const scroll = body.scrollTop;
        body.innerHTML = Object.keys(dump).sort().map(key => `<div class="raw-sec"><h3>${esc(key)}</h3><pre>${esc(JSON.stringify(dump[key], null, 2))}</pre></div>`).join('');
        body.scrollTop = scroll;
    } catch (e) {
        body.innerHTML = '<div class="set-empty">Failed to load.</div>';
    }
}

document.addEventListener('keydown', e => {
    if (e.key === 'Tab') {
        // Keep Tab inside the open upload / raw-API dialog.
        const root = $('upload-root') || $('raw-root');
        if (!root || !root.firstElementChild) return;
        const f = [...root.querySelectorAll('button, [href], input:not([hidden]), select, textarea, [tabindex="0"]')].filter(x => !x.disabled);
        if (!f.length) return;
        const i = f.indexOf(document.activeElement);
        if (e.shiftKey && i <= 0) { e.preventDefault(); f[f.length - 1].focus(); }
        else if (!e.shiftKey && (i === -1 || i === f.length - 1)) { e.preventDefault(); f[0].focus(); }
        return;
    }
    if (e.key !== 'Escape') return;
    if (upload) closeUploadDialog();
    closeRawApi();
});
