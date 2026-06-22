const POLL_INTERVAL = 3000;
let printers = [];
let pollCounter = 0;
let prevPrinterIDs = [];

function getWebcamMode(printerId) {
    return localStorage.getItem(`webcam-mode-${printerId}`) || 'snapshot';
}

function toggleWebcamMode(printerId) {
    const current = getWebcamMode(printerId);
    const next = current === 'snapshot' ? 'live' : 'snapshot';
    localStorage.setItem(`webcam-mode-${printerId}`, next);
    const card = document.querySelector(`[data-printer-id="${printerId}"]`);
    if (card) updateCard(card, printers.find(p => p.config.id === printerId));
}

function webcamSrc(printerId) {
    const mode = getWebcamMode(printerId);
    if (mode === 'live') {
        return `/api/webcam/${printerId}`;
    }
    return `/api/snapshot/${printerId}?t=${pollCounter}`;
}

async function fetchPrinters() {
    try {
        const resp = await fetch('/api/printers');
        if (!resp.ok) return;
        printers = await resp.json();
        pollCounter++;
        updateDashboard();
    } catch (e) {
        // server unreachable, keep showing last state
    }
}

function updateDashboard() {
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
        prevPrinterIDs = [];
        return;
    }

    const connected = printers.filter(p => p.status && p.status.state !== 'offline').length;
    count.textContent = `${connected}/${printers.length} connected`;

    const currentIDs = printers.map(p => p.config.id);
    const structureChanged = JSON.stringify(currentIDs) !== JSON.stringify(prevPrinterIDs);

    if (structureChanged) {
        list.innerHTML = printers.map(p => renderPrinterCard(p)).join('');
        prevPrinterIDs = currentIDs;
    } else {
        printers.forEach(p => {
            const card = list.querySelector(`[data-printer-id="${p.config.id}"]`);
            if (card) updateCard(card, p);
        });
    }
}

function renderPrinterCard(printer) {
    const cfg = printer.config;
    const status = printer.status;
    const state = status ? status.state : 'offline';
    const stateClass = `state-${state}`;
    const stateLabel = state.charAt(0).toUpperCase() + state.slice(1);
    const isPrinting = (state === 'printing' || state === 'paused') && status && status.job;
    const wcMode = getWebcamMode(cfg.id);

    return `
        <div class="printer-card" data-printer-id="${cfg.id}" data-state="${state}">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                <span class="printer-state ${stateClass}" data-field="state">${stateLabel}</span>
                <span class="printer-url">${esc(cfg.url)}</span>
                <a class="printer-link" href="${esc(cfg.url)}" target="_blank" rel="noopener">OctoPrint &#8599;</a>
                <div class="printer-actions">
                    <button class="btn btn-sm" onclick="openEditModal(${cfg.id})" title="Edit">&#9998;</button>
                    <button class="btn btn-sm btn-danger" onclick="deletePrinter(${cfg.id})" title="Delete">&times;</button>
                </div>
            </div>
            <div class="printer-body">
                <div class="webcam-container ${isPrinting ? '' : 'webcam-idle'}">
                    <img class="webcam-img" src="${webcamSrc(cfg.id)}" alt="Webcam" onerror="this.style.display='none';this.parentElement.querySelector('.webcam-placeholder').style.display='block';this.parentElement.querySelector('.webcam-badge').style.display='none';this.parentElement.querySelector('.webcam-toggle').style.display='none';${isPrinting ? '' : "this.parentElement.classList.add('webcam-collapsed');"}">
                    <div class="webcam-placeholder" style="display:none">No camera</div>
                    <div class="webcam-badge"><span class="${wcMode === 'live' ? 'dot' : 'dot dot-blue'}"></span> ${wcMode === 'live' ? 'LIVE' : 'SNAP'}</div>
                    <button class="webcam-toggle ${wcMode === 'live' ? 'live' : ''}" onclick="event.stopPropagation();toggleWebcamMode(${cfg.id})" title="Toggle snapshot/live">${wcMode === 'live' ? '&#9724;' : '&#9654;'}</button>
                </div>
                ${isPrinting ? renderThumbnail(cfg, status.job) : ''}
                <div class="printer-stats">
                    ${isPrinting ? renderPrintingStats(status) : renderIdleStats(status, state)}
                </div>
            </div>
        </div>`;
}

function renderThumbnail(cfg, job) {
    return `
        <div class="thumbnail-container">
            <img class="thumbnail-img" src="/api/thumbnail/${cfg.id}?t=${pollCounter}" alt="Thumbnail" onerror="this.style.display='none';this.nextElementSibling.style.display='block'">
            <div class="thumbnail-placeholder" style="display:none">${esc(job.file_name || 'Unknown')}</div>
        </div>`;
}

function renderPrintingStats(status) {
    const job = status.job;
    const progress = Math.round(job.progress || 0);
    const elapsed = formatTime(job.elapsed_secs);
    const remaining = formatTime(job.remaining_secs);
    const eta = computeETA(job.remaining_secs);
    const temps = status.temps;

    let extraRow = '';
    const parts = [];
    if (job.total_layers > 0) {
        parts.push(`<div class="stat-box"><div class="stat-label">Layer</div><div class="stat-value" data-field="layer">${job.current_layer} <span class="stat-unit">/ ${job.total_layers}</span></div></div>`);
    }
    if (job.filament_used_mm > 0) {
        const meters = (job.filament_used_mm / 1000).toFixed(1);
        parts.push(`<div class="stat-box"><div class="stat-label">Filament (est.)</div><div class="stat-value" data-field="filament">${meters}<span class="stat-unit"> m</span></div></div>`);
    }
    if (parts.length > 0) {
        extraRow = `<div class="stat-grid stat-grid-2">${parts.join('')}</div>`;
    }

    return `
        <div>
            <div class="progress-row">
                <span class="progress-label">Progress</span>
                <span class="progress-value" data-field="progress-text">${progress}%</span>
            </div>
            <div class="progress-bar">
                <div class="progress-fill" data-field="progress-bar" style="width: ${progress}%"></div>
            </div>
        </div>
        <div class="stat-grid">
            <div class="stat-box"><div class="stat-label">Elapsed</div><div class="stat-value" data-field="elapsed">${elapsed}</div></div>
            <div class="stat-box"><div class="stat-label">Remaining</div><div class="stat-value" data-field="remaining">${remaining}</div></div>
            <div class="stat-box"><div class="stat-label">ETA</div><div class="stat-value" data-field="eta">${eta}</div></div>
        </div>
        <div class="stat-grid stat-grid-2">
            <div class="stat-box"><div class="stat-label">Hotend</div><div class="stat-value" data-field="hotend">${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.hotend_target)}&deg;C</span></div></div>
            <div class="stat-box"><div class="stat-label">Bed</div><div class="stat-value" data-field="bed">${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.bed_target)}&deg;C</span></div></div>
        </div>
        ${extraRow}`;
}

function renderIdleStats(status, state) {
    const temps = status ? status.temps : null;
    const stateMsg = state === 'offline' ? 'Printer is offline or unreachable' :
                     state === 'error' ? 'Printer reported an error' :
                     'Ready for next job';

    let tempsHTML = '';
    if (temps) {
        tempsHTML = `
            <div class="stat-grid stat-grid-2">
                <div class="stat-box"><div class="stat-label">Hotend</div><div class="stat-value" data-field="hotend">${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${temps.hotend_target > 0 ? Math.round(temps.hotend_target) + '&deg;C' : 'off'}</span></div></div>
                <div class="stat-box"><div class="stat-label">Bed</div><div class="stat-value" data-field="bed">${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${temps.bed_target > 0 ? Math.round(temps.bed_target) + '&deg;C' : 'off'}</span></div></div>
            </div>`;
    }

    return `<div class="idle-message" data-field="idle-msg">${stateMsg}</div>${tempsHTML}`;
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

    // Update webcam snapshot src (but NOT live streams)
    const wcMode = getWebcamMode(cfg.id);
    const wcImg = card.querySelector('.webcam-img');
    if (wcImg && wcMode === 'snapshot' && wcImg.style.display !== 'none') {
        wcImg.src = `/api/snapshot/${cfg.id}?t=${pollCounter}`;
    }

    // Update badge
    const badge = card.querySelector('.webcam-badge');
    if (badge) {
        const dot = badge.querySelector('.dot');
        if (dot) dot.className = wcMode === 'live' ? 'dot' : 'dot dot-blue';
        badge.lastChild.textContent = wcMode === 'live' ? ' LIVE' : ' SNAP';
    }
    const toggleBtn = card.querySelector('.webcam-toggle');
    if (toggleBtn) {
        toggleBtn.className = `webcam-toggle ${wcMode === 'live' ? 'live' : ''}`;
        toggleBtn.innerHTML = wcMode === 'live' ? '&#9724;' : '&#9654;';
    }

    if (isPrinting && status.job) {
        const job = status.job;
        const progress = Math.round(job.progress || 0);
        const temps = status.temps;

        setText(card, 'progress-text', `${progress}%`);
        const bar = card.querySelector('[data-field="progress-bar"]');
        if (bar) bar.style.width = `${progress}%`;
        setText(card, 'elapsed', formatTime(job.elapsed_secs));
        setText(card, 'remaining', formatTime(job.remaining_secs));
        setText(card, 'eta', computeETA(job.remaining_secs));
        setHTML(card, 'hotend', `${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.hotend_target)}&deg;C</span>`);
        setHTML(card, 'bed', `${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${Math.round(temps.bed_target)}&deg;C</span>`);

        if (job.total_layers > 0) {
            setHTML(card, 'layer', `${job.current_layer} <span class="stat-unit">/ ${job.total_layers}</span>`);
        }
        if (job.filament_used_mm > 0) {
            const meters = (job.filament_used_mm / 1000).toFixed(1);
            setHTML(card, 'filament', `${meters}<span class="stat-unit"> m</span>`);
        }
    } else if (status && status.temps) {
        const temps = status.temps;
        setHTML(card, 'hotend', `${Math.round(temps.hotend_actual)}<span class="stat-unit">&deg;C / ${temps.hotend_target > 0 ? Math.round(temps.hotend_target) + '&deg;C' : 'off'}</span>`);
        setHTML(card, 'bed', `${Math.round(temps.bed_actual)}<span class="stat-unit">&deg;C / ${temps.bed_target > 0 ? Math.round(temps.bed_target) + '&deg;C' : 'off'}</span>`);
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

// Modal handling

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
            prevPrinterIDs = [];
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
        prevPrinterIDs = [];
        fetchPrinters();
    } catch (e) {
        // handle error
    }
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
            // Auto-fill name if empty and OctoPrint returned one
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
