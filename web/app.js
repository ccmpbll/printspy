let printers = [];
let pollCounter = 0;
let prevPrinterIDs = [];
let eventSource = null;
let statusCache = {};

// SSE connection

function connectSSE() {
    if (eventSource) eventSource.close();

    eventSource = new EventSource('/api/events');

    eventSource.addEventListener('init', (e) => {
        printers = JSON.parse(e.data);
        printers.forEach(p => { if (p.status) statusCache[p.config.id] = p.status; });
        prevPrinterIDs = [];
        updateDashboard();
        showConnectionBanner(false);
    });

    eventSource.addEventListener('status', (e) => {
        const msg = JSON.parse(e.data);
        statusCache[msg.printer_id] = msg.status;
        const printer = printers.find(p => p.config.id === msg.printer_id);
        if (printer) {
            printer.status = msg.status;
            const card = document.querySelector(`[data-printer-id="${msg.printer_id}"]`);
            if (card) updateCard(card, printer);
        }
        updateHeaderCount();
        showConnectionBanner(false);
    });

    eventSource.addEventListener('error', () => {
        showConnectionBanner(true);
    });
}

function showConnectionBanner(show) {
    const banner = document.getElementById('connection-banner');
    if (banner) banner.style.display = show ? 'flex' : 'none';
}

function updateHeaderCount() {
    const count = document.getElementById('printer-count');
    if (!printers || printers.length === 0) {
        count.textContent = '';
        return;
    }
    const connected = printers.filter(p => p.status && p.status.state !== 'offline').length;
    count.textContent = `${connected}/${printers.length} connected`;
}

function refreshSnapshots() {
    pollCounter++;
    document.querySelectorAll('.webcam-img').forEach(img => {
        const id = img.closest('[data-printer-id]')?.dataset.printerId;
        if (id && getWebcamMode(parseInt(id)) === 'snapshot' && img.style.display !== 'none') {
            img.src = `/api/snapshot/${id}?t=${pollCounter}`;
        }
    });
}

// Webcam mode

function getWebcamMode(printerId) {
    return localStorage.getItem(`webcam-mode-${printerId}`) || 'snapshot';
}

function toggleWebcamMode(printerId) {
    const current = getWebcamMode(printerId);
    const next = current === 'snapshot' ? 'live' : 'snapshot';
    localStorage.setItem(`webcam-mode-${printerId}`, next);
    const card = document.querySelector(`[data-printer-id="${printerId}"]`);
    if (card) {
        const printer = printers.find(p => p.config.id === printerId);
        if (printer) card.outerHTML = renderPrinterCard(printer);
    }
}

function webcamSrc(printerId) {
    const mode = getWebcamMode(printerId);
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

    const currentIDs = printers.map(p => p.config.id);
    const structureChanged = JSON.stringify(currentIDs) !== JSON.stringify(prevPrinterIDs);

    if (structureChanged) {
        list.innerHTML = printers.map(p => renderPrinterCard(p)).join('');
        prevPrinterIDs = currentIDs;
    }
}

function renderPrinterCard(printer) {
    const cfg = printer.config;
    const status = printer.status;
    const state = status ? status.state : 'offline';
    const stateClass = `state-${state}`;
    const stateLabel = state === 'error' && status && status.state_message
        ? `Error: ${status.state_message}`
        : state.charAt(0).toUpperCase() + state.slice(1);
    const isPrinting = (state === 'printing' || state === 'paused') && status && status.job;
    const wcMode = getWebcamMode(cfg.id);
    const cardClass = `printer-card ${state === 'error' ? 'card-error' : state === 'offline' ? 'card-offline' : ''}`;

    return `
        <div class="${cardClass}" data-printer-id="${cfg.id}" data-state="${state}">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                <span class="printer-state ${stateClass}" data-field="state">${stateLabel}</span>
                <span class="printer-url">${esc(cfg.url)}</span>
                <a class="printer-link" href="${esc(cfg.url)}" target="_blank" rel="noopener">OctoPrint &#8599;</a>
            </div>
            <div class="printer-body">
                <div class="webcam-wrapper">
                    <div class="webcam-container ${isPrinting ? '' : 'webcam-idle'}">
                        <img class="webcam-img" src="${webcamSrc(cfg.id)}" alt="Webcam" onerror="this.style.display='none';this.parentElement.querySelector('.webcam-placeholder').style.display='block';this.parentElement.querySelector('.webcam-badge').style.display='none';this.parentElement.querySelector('.webcam-toggle').style.display='none';${isPrinting ? '' : "this.parentElement.classList.add('webcam-collapsed');"}">
                        <div class="webcam-placeholder" style="display:none">${state === 'offline' ? 'No camera' : 'Camera unreachable'}</div>
                        <div class="webcam-badge"><span class="${wcMode === 'live' ? 'dot' : 'dot dot-blue'}"></span> ${wcMode === 'live' ? 'LIVE' : 'SNAP'}</div>
                        <button class="webcam-toggle ${wcMode === 'live' ? 'live' : ''}" onclick="event.stopPropagation();toggleWebcamMode(${cfg.id})" title="Toggle snapshot/live">${wcMode === 'live' ? '&#9724;' : '&#9654;'}</button>
                    </div>
                </div>
                <div class="printer-stats">
                    ${isPrinting ? renderPrintingStats(cfg, status) : renderIdleStats(status, state)}
                </div>
            </div>
        </div>`;
}

function renderPrintingStats(cfg, status) {
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
            <div class="thumb-beside">
                <img src="/api/thumbnail/${cfg.id}?t=${pollCounter}" alt="Thumbnail" onerror="this.parentElement.style.display='none'">
            </div>
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
    let stateMsg;
    if (state === 'offline') {
        stateMsg = detailMsg || 'Printer is offline or unreachable';
    } else if (state === 'error') {
        stateMsg = detailMsg || 'Printer reported an error';
    } else {
        stateMsg = 'Ready for next job';
    }

    let tempsHTML = '';
    if (temps) {
        const cells = [];
        cells.push(`<div class="stat-box"><div class="stat-label">Hotend</div><div class="stat-value" data-field="hotend">${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${temps.hotend_target > 0 ? Math.round(temps.hotend_target) + '&deg;C' : 'off'}</span></div></div>`);
        cells.push(`<div class="stat-box"><div class="stat-label">Bed</div><div class="stat-value" data-field="bed">${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${temps.bed_target > 0 ? Math.round(temps.bed_target) + '&deg;C' : 'off'}</span></div></div>`);
        if (temps.has_chamber) {
            cells.push(`<div class="stat-box"><div class="stat-label">Chamber</div><div class="stat-value" data-field="chamber">${Math.round(temps.chamber_actual)}<span class="stat-unit">&deg;C / ${temps.chamber_target > 0 ? Math.round(temps.chamber_target) + '&deg;C' : 'off'}</span></div></div>`);
        }
        tempsHTML = `<div class="stat-grid stat-grid-auto">${cells.join('')}</div>`;
    }

    const msgClass = state === 'error' ? 'idle-message msg-error' :
                     state === 'offline' ? 'idle-message msg-offline' : 'idle-message';
    return `<div class="${msgClass}" data-field="idle-msg">${stateMsg}</div>${tempsHTML}`;
}

function updateCard(card, printer) {
    const cfg = printer.config;
    const status = printer.status;
    const state = status ? status.state : 'offline';
    const prevState = card.dataset.state;
    const isPrinting = (state === 'printing' || state === 'paused') && status && status.job;
    const wasPrinting = (prevState === 'printing' || prevState === 'paused');

    if ((isPrinting && !wasPrinting) || (!isPrinting && wasPrinting)) {
        card.outerHTML = renderPrinterCard(printer);
        return;
    }

    card.dataset.state = state;

    const stateEl = card.querySelector('[data-field="state"]');
    if (stateEl) {
        const stateLabel = state.charAt(0).toUpperCase() + state.slice(1);
        stateEl.textContent = stateLabel;
        stateEl.className = `printer-state state-${state}`;
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
        setHTML(card, 'hotend', `${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.hotend_target)}&deg;C</span>`);
        setHTML(card, 'bed', `${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.bed_target)}&deg;C</span>`);
        if (temps.has_chamber) {
            setHTML(card, 'chamber', `${Math.round(temps.chamber_actual)}<span class="stat-unit">&deg;C / ${temps.chamber_target > 0 ? Math.round(temps.chamber_target) + '&deg;C' : 'off'}</span>`);
        }
        if (job.total_layers > 0) {
            setHTML(card, 'layer', `${job.current_layer} <span class="stat-unit">/ ${job.total_layers}</span>`);
        }
    } else if (status && status.temps) {
        const temps = status.temps;
        setHTML(card, 'hotend', `${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${temps.hotend_target > 0 ? Math.round(temps.hotend_target) + '&deg;C' : 'off'}</span>`);
        setHTML(card, 'bed', `${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${temps.bed_target > 0 ? Math.round(temps.bed_target) + '&deg;C' : 'off'}</span>`);
        if (temps.has_chamber) {
            setHTML(card, 'chamber', `${Math.round(temps.chamber_actual)}<span class="stat-unit">&deg;C / ${temps.chamber_target > 0 ? Math.round(temps.chamber_target) + '&deg;C' : 'off'}</span>`);
        }
    }
}

function setText(card, field, value) {
    const el = card.querySelector(`[data-field="${field}"]`);
    if (el) el.textContent = value;
}

function setHTML(card, field, value) {
    const el = card.querySelector(`[data-field="${field}"]`);
    if (el) el.innerHTML = value;
}

// Settings modal with printer management

function openSettings() {
    fetch('/api/settings').then(r => r.json()).then(settings => {
        document.getElementById('setting-snapshot-interval').value = settings.snapshot_interval || '10';
    });
    renderSettingsPrinterList();
    document.getElementById('settings-modal').classList.add('active');
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
                    <span class="settings-printer-url">${esc(cfg.url)}</span>
                </div>
                <div class="settings-printer-actions">
                    <button class="btn btn-sm" onclick="closeModal();openEditModal(${cfg.id})" title="Edit">&#9998; Edit</button>
                    <button class="btn btn-sm btn-danger" onclick="deletePrinter(${cfg.id})" title="Delete">&times;</button>
                </div>
            </div>`;
    }).join('');
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

function openAddModal() {
    document.getElementById('modal-title').textContent = 'Add printer';
    document.getElementById('printer-id').value = '';
    document.getElementById('printer-name').value = '';
    document.getElementById('printer-type').value = 'octoprint';
    document.getElementById('printer-url').value = '';
    document.getElementById('printer-apikey').value = '';
    document.getElementById('printer-poll').value = '10';
    document.getElementById('test-btn').style.display = 'inline-flex';
    hideTestResult();
    document.getElementById('printer-modal').classList.add('active');
}

function openEditModal(id) {
    const printer = printers.find(p => p.config.id === id);
    if (!printer) return;
    const cfg = printer.config;

    document.getElementById('modal-title').textContent = 'Edit printer';
    document.getElementById('printer-id').value = cfg.id;
    document.getElementById('printer-name').value = cfg.name;
    document.getElementById('printer-type').value = cfg.type;
    document.getElementById('printer-url').value = cfg.url;
    document.getElementById('printer-apikey').value = cfg.api_key;
    document.getElementById('printer-poll').value = cfg.poll_interval;
    document.getElementById('test-btn').style.display = 'inline-flex';
    hideTestResult();
    document.getElementById('printer-modal').classList.add('active');
}

function closeModal() {
    document.querySelectorAll('.modal-overlay').forEach(m => m.classList.remove('active'));
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
    const data = {
        name: document.getElementById('printer-name').value,
        type: document.getElementById('printer-type').value,
        url: document.getElementById('printer-url').value.replace(/\/+$/, ''),
        api_key: document.getElementById('printer-apikey').value,
        poll_interval: parseInt(document.getElementById('printer-poll').value) || 10,
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
    if (!confirm('Remove this printer?')) return;
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
        const resp = await fetch('/api/test', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({type: printerType, url: printerURL, api_key: apiKey}),
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
    };
    await fetch('/api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(settings),
    });
    snapshotInterval = parseInt(settings.snapshot_interval) || 10;
    restartSnapshotTimer();
    closeModal();
}

let snapshotTimer = null;
let snapshotInterval = 10;

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
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
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

// Initialize
fetch('/api/settings').then(r => r.json()).then(settings => {
    snapshotInterval = parseInt(settings.snapshot_interval) || 10;
    restartSnapshotTimer();
}).catch(() => {
    restartSnapshotTimer();
});

fetch('/api/version').then(r => r.json()).then(data => {
    const el = document.getElementById('app-version');
    if (el && data.version) el.textContent = 'v' + data.version;
}).catch(() => {});

connectSSE();
