const POLL_INTERVAL = 3000;
let printers = [];

async function fetchPrinters() {
    try {
        const resp = await fetch('/api/printers');
        if (!resp.ok) return;
        printers = await resp.json();
        renderDashboard();
    } catch (e) {
        // server unreachable, keep showing last state
    }
}

function renderDashboard() {
    const list = document.getElementById('printer-list');
    const count = document.getElementById('printer-count');

    if (!printers || printers.length === 0) {
        count.textContent = '';
        list.innerHTML = `
            <div class="empty-state">
                <h2>No printers configured</h2>
                <p>Add your first printer to get started.</p>
                <button class="btn btn-primary" onclick="openAddModal()">+ Add printer</button>
            </div>`;
        return;
    }

    const connected = printers.filter(p => p.status && p.status.state !== 'offline').length;
    count.textContent = `${connected}/${printers.length} connected`;

    list.innerHTML = printers.map(p => renderPrinterCard(p)).join('');
}

function renderPrinterCard(printer) {
    const cfg = printer.config;
    const status = printer.status;
    const state = status ? status.state : 'offline';
    const stateClass = `state-${state}`;
    const stateLabel = state.charAt(0).toUpperCase() + state.slice(1);

    let bodyHTML = '';

    if (state === 'printing' && status.job) {
        bodyHTML = renderPrintingBody(cfg, status);
    } else if (state === 'paused' && status.job) {
        bodyHTML = renderPrintingBody(cfg, status);
    } else {
        bodyHTML = renderIdleBody(cfg, status, state);
    }

    return `
        <div class="printer-card">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                <span class="printer-state ${stateClass}">${stateLabel}</span>
                <span class="printer-url">${esc(cfg.url)}</span>
                <a class="printer-link" href="${esc(cfg.url)}" target="_blank" rel="noopener">OctoPrint &#8599;</a>
                <div class="printer-actions">
                    <button class="btn btn-sm" onclick="openEditModal(${cfg.id})" title="Edit">&#9998;</button>
                    <button class="btn btn-sm btn-danger" onclick="deletePrinter(${cfg.id})" title="Delete">&times;</button>
                </div>
            </div>
            <div class="printer-body">
                ${bodyHTML}
            </div>
        </div>`;
}

function renderPrintingBody(cfg, status) {
    const job = status.job;
    const progress = Math.round(job.progress || 0);
    const elapsed = formatTime(job.elapsed_secs);
    const remaining = formatTime(job.remaining_secs);
    const eta = computeETA(job.remaining_secs);
    const temps = status.temps;

    let layerHTML = '';
    if (job.total_layers > 0) {
        layerHTML = `
            <div class="stat-box">
                <div class="stat-label">Layer</div>
                <div class="stat-value">${job.current_layer} <span class="stat-unit">/ ${job.total_layers}</span></div>
            </div>`;
    }

    let filamentHTML = '';
    if (job.filament_used_mm > 0) {
        const meters = (job.filament_used_mm / 1000).toFixed(1);
        filamentHTML = `
            <div class="stat-box">
                <div class="stat-label">Filament</div>
                <div class="stat-value">${meters}<span class="stat-unit"> m</span></div>
            </div>`;
    }

    return `
        <div class="webcam-container">
            <img src="/api/webcam/${cfg.id}" alt="Webcam" onerror="this.style.display='none';this.nextElementSibling.style.display='block'">
            <div class="webcam-placeholder" style="display:none">No camera</div>
            <div class="webcam-live"><span class="dot"></span> LIVE</div>
        </div>
        <div class="thumbnail-container">
            <img src="/api/thumbnail/${cfg.id}" alt="Thumbnail" onerror="this.style.display='none';this.nextElementSibling.style.display='block'">
            <div class="thumbnail-placeholder" style="display:none">${esc(job.file_name || 'Unknown')}</div>
        </div>
        <div class="printer-stats">
            <div>
                <div class="progress-row">
                    <span class="progress-label">Progress</span>
                    <span class="progress-value">${progress}%</span>
                </div>
                <div class="progress-bar">
                    <div class="progress-fill" style="width: ${progress}%"></div>
                </div>
            </div>
            <div class="stat-grid">
                <div class="stat-box">
                    <div class="stat-label">Elapsed</div>
                    <div class="stat-value">${elapsed}</div>
                </div>
                <div class="stat-box">
                    <div class="stat-label">Remaining</div>
                    <div class="stat-value">${remaining}</div>
                </div>
                <div class="stat-box">
                    <div class="stat-label">ETA</div>
                    <div class="stat-value">${eta}</div>
                </div>
            </div>
            <div class="stat-grid stat-grid-2">
                <div class="stat-box">
                    <div class="stat-label">Hotend</div>
                    <div class="stat-value">${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.hotend_target)}&deg;C</span></div>
                </div>
                <div class="stat-box">
                    <div class="stat-label">Bed</div>
                    <div class="stat-value">${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.bed_target)}&deg;C</span></div>
                </div>
            </div>
            ${(layerHTML || filamentHTML) ? `<div class="stat-grid stat-grid-2">${layerHTML}${filamentHTML}</div>` : ''}
        </div>`;
}

function renderIdleBody(cfg, status, state) {
    const temps = status ? status.temps : null;
    const tempsHTML = temps ? `
        <div class="stat-grid stat-grid-2">
            <div class="stat-box">
                <div class="stat-label">Hotend</div>
                <div class="stat-value">${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${temps.hotend_target > 0 ? Math.round(temps.hotend_target) + '&deg;C' : 'off'}</span></div>
            </div>
            <div class="stat-box">
                <div class="stat-label">Bed</div>
                <div class="stat-value">${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${temps.bed_target > 0 ? Math.round(temps.bed_target) + '&deg;C' : 'off'}</span></div>
            </div>
        </div>` : '';

    const stateMsg = state === 'offline' ? 'Printer is offline or unreachable' :
                     state === 'error' ? 'Printer reported an error' :
                     'Ready for next job';

    return `
        <div class="webcam-container">
            <img src="/api/webcam/${cfg.id}" alt="Webcam" onerror="this.style.display='none';this.nextElementSibling.style.display='block'">
            <div class="webcam-placeholder" style="display:none">No camera</div>
            <div class="webcam-live"><span class="dot"></span> LIVE</div>
        </div>
        <div class="thumbnail-container">
            <div class="thumbnail-placeholder">No print</div>
        </div>
        <div class="printer-stats">
            <div class="idle-message">${stateMsg}</div>
            ${tempsHTML}
        </div>`;
}

// Modal handling

function openAddModal() {
    document.getElementById('modal-title').textContent = 'Add printer';
    document.getElementById('printer-id').value = '';
    document.getElementById('printer-name').value = '';
    document.getElementById('printer-type').value = 'octoprint';
    document.getElementById('printer-url').value = '';
    document.getElementById('printer-apikey').value = '';
    document.getElementById('printer-poll').value = '10';
    document.getElementById('test-btn').style.display = 'none';
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
    document.getElementById('printer-modal').classList.remove('active');
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
            fetchPrinters();
        }
    } catch (e) {
        // handle error
    }
}

async function deletePrinter(id) {
    if (!confirm('Remove this printer?')) return;
    try {
        await fetch(`/api/printers/${id}`, {method: 'DELETE'});
        fetchPrinters();
    } catch (e) {
        // handle error
    }
}

async function testConnection() {
    const id = document.getElementById('printer-id').value;
    if (!id) return;

    const el = document.getElementById('test-result');
    el.style.display = 'block';
    el.className = 'test-result';
    el.textContent = 'Testing connection...';

    try {
        const resp = await fetch(`/api/printers/${id}/test`, {method: 'POST'});
        const data = await resp.json();
        if (data.success) {
            el.className = 'test-result success';
            el.textContent = 'Connection successful!';
        } else {
            el.className = 'test-result error';
            el.textContent = `Connection failed: ${data.error}`;
        }
    } catch (e) {
        el.className = 'test-result error';
        el.textContent = 'Connection test failed';
    }
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

// Close modal on overlay click
document.getElementById('printer-modal').addEventListener('click', function(e) {
    if (e.target === this) closeModal();
});

// Close modal on Escape
document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape') closeModal();
});

// Start polling
fetchPrinters();
setInterval(fetchPrinters, POLL_INTERVAL);
