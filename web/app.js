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

    eventSource.addEventListener('refresh', () => {
        fetchPrinters();
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
        document.getElementById('power-controls').style.display = 'none';
        return;
    }
    const connected = printers.filter(p => p.status && p.status.state !== 'offline' && p.status.state !== 'disconnected').length;
    count.textContent = `${connected}/${printers.length} connected`;
    const hasPower = printers.some(p => p.status && p.status.power && p.status.power.length > 0);
    document.getElementById('power-controls').style.display = hasPower ? 'inline-flex' : 'none';
}

// Power control

// Recent prints and reprint

function reloadIdlePrinterRecentPrints() {
    printers.forEach(p => {
        if (p.status && p.status.state === 'idle') loadRecentPrints(p.config.id);
    });
}

async function loadRecentPrints(printerId) {
    const card = document.querySelector(`[data-printer-id="${printerId}"]`);
    if (!card) return;
    const container = card.querySelector('[data-field="recent-prints"]');
    if (!container) return;

    try {
        const resp = await fetch(`/api/printers/${printerId}/recent`);
        if (!resp.ok) { container.innerHTML = ''; return; }
        const prints = await resp.json();
        if (!prints || prints.length === 0) {
            container.innerHTML = '';
            return;
        }
        container.innerHTML = `
            <div class="recent-dropdown">
                <button class="btn btn-sm btn-recent-toggle" onclick="event.stopPropagation();toggleRecentDropdown(this.parentElement)">&#128196; Recent files (${prints.length})</button>
                <div class="recent-menu">${prints.map(p => {
                    const thumb = p.thumbnail_path ? `<img class="recent-thumb" src="/api/file-thumbnail/${printerId}?path=${encodeURIComponent(p.thumbnail_path)}" alt="" onerror="this.style.display='none'">` : '';
                    let status = 'New';
                    let statusClass = 'recent-status-new';
                    if (p.success_count > 0 && p.last_success !== false) {
                        status = `${p.success_count}x printed`;
                        statusClass = 'recent-status-success';
                    } else if (p.last_success === false) {
                        status = 'Failed';
                        statusClass = 'recent-status-failed';
                    }
                    const btnLabel = p.success_count > 0 ? '&#8634; Reprint' : '&#9654; Print';
                    return `<div class="recent-item">
                        ${thumb}
                        <div class="recent-item-info">
                            <span class="recent-name" title="${esc(p.file_name)}">${esc(p.file_name)}</span>
                            <span class="recent-meta"><span class="${statusClass}">${status}</span> &middot; ${formatDate(p.uploaded_at)}</span>
                        </div>
                        <button class="btn btn-sm btn-reprint" data-printer="${printerId}" data-origin="${esc(p.origin)}" data-path="${esc(p.path)}" onclick="event.stopPropagation();startReprint(this)" title="Print">${btnLabel}</button>
                    </div>`;
                }).join('')}
                </div>
            </div>`;
    } catch (e) { container.innerHTML = ''; }
}

function toggleRecentDropdown(el) {
    const wasOpen = el.classList.contains('open');
    document.querySelectorAll('.recent-dropdown.open').forEach(d => d.classList.remove('open'));
    if (!wasOpen) el.classList.add('open');
}

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
        if (!resp.ok) {
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
    if ((action === 'cancel' || action === 'pause') && !confirm(action === 'cancel' ? 'Cancel this print?' : 'Pause this print?')) return;
    try {
        await fetch(`/api/printers/${printerId}/control`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({action}),
        });
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
    if (!card) return;

    const img = card.querySelector('.webcam-img');
    if (img) {
        img.style.display = '';
        img.src = next === 'live' ? `/api/webcam/${printerId}` : `/api/snapshot/${printerId}?t=${pollCounter}`;
    }
    const badge = card.querySelector('.webcam-badge');
    if (badge) {
        const dot = badge.querySelector('.dot');
        if (dot) dot.className = next === 'live' ? 'dot' : 'dot dot-blue';
        badge.lastChild.textContent = next === 'live' ? ' LIVE' : ' SNAP';
    }
    const btn = card.querySelector('.webcam-toggle');
    if (btn) {
        btn.className = next === 'live' ? 'webcam-toggle live' : 'webcam-toggle';
        btn.innerHTML = next === 'live' ? '&#9724;' : '&#9654;';
    }
    const placeholder = card.querySelector('.webcam-placeholder');
    if (placeholder) placeholder.style.display = 'none';
}

function webcamError(img, isPrinting) {
    const container = img.parentElement;
    img.style.display = 'none';
    container.querySelector('.webcam-placeholder').style.display = 'block';
    container.querySelector('.webcam-badge').style.display = 'none';
    const toggle = container.querySelector('.webcam-toggle');
    if (toggle) toggle.style.display = 'none';
    if (!isPrinting) container.classList.add('webcam-collapsed');
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
        reloadIdlePrinterRecentPrints();
    }
}

function renderPrinterCard(printer) {
    const cfg = printer.config;
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
    const isPrinting = (state === 'printing' || state === 'paused') && status && status.job;
    const wcMode = cfg.type === 'prusalink' ? 'snapshot' : getWebcamMode(cfg.id);
    const cardClass = `printer-card ${state === 'error' ? 'card-error' : state === 'offline' ? 'card-offline' : state === 'disconnected' ? 'card-disconnected' : ''}`;

    let powerHTML = '';
    if (status && status.power && status.power.length > 0) {
        const isBusy = state === 'printing' || state === 'paused';
        const singlePlug = status.power.length === 1;
        powerHTML = status.power.map(ps => {
            const onClass = ps.on ? 'power-btn-active power-on' : '';
            const offClass = !ps.on ? 'power-btn-active power-off' : '';
            const isPrinterPlug = singlePlug || (ps.label && ps.label.toLowerCase().includes('printer'));
            const offDisabled = isBusy && isPrinterPlug ? 'disabled title="Cannot turn off printer while printing"' : '';
            const label = !singlePlug && ps.label ? esc(ps.label) + ' ' : '';
            return `<span class="power-btn-group" data-field="power" data-plug-id="${esc(ps.id)}"><button class="power-toggle-btn ${onClass}" onclick="event.stopPropagation();setPower(${cfg.id},'on','${esc(ps.id)}')">${label}&#9889; On</button><button class="power-toggle-btn ${offClass}" onclick="event.stopPropagation();setPower(${cfg.id},'off','${esc(ps.id)}')" ${offDisabled}>Off</button></span>`;
        }).join('');
    }

    let controlHTML = '';
    if (state === 'printing') {
        controlHTML = `<span class="print-controls" data-field="print-controls"><button class="btn btn-sm" onclick="event.stopPropagation();controlPrint(${cfg.id},'pause')">&#10074;&#10074; Pause</button><button class="btn btn-sm btn-danger" onclick="event.stopPropagation();controlPrint(${cfg.id},'cancel')">&#9724; Cancel</button></span>`;
    } else if (state === 'paused') {
        controlHTML = `<span class="print-controls" data-field="print-controls"><button class="btn btn-sm btn-primary" onclick="event.stopPropagation();controlPrint(${cfg.id},'resume')">&#9654; Resume</button><button class="btn btn-sm btn-danger" onclick="event.stopPropagation();controlPrint(${cfg.id},'cancel')">&#9724; Cancel</button></span>`;
    }

    let recentHTML = '';
    if (state === 'idle') {
        recentHTML = `<span class="recent-prints" data-field="recent-prints"></span>`;
    }

    return `
        <div class="${cardClass}" data-printer-id="${cfg.id}" data-state="${state}">
            <div class="printer-header">
                <span class="printer-name">${esc(cfg.name)}</span>
                <span class="printer-state ${stateClass}" data-field="state">${stateLabel}</span>
                ${powerHTML}
                ${controlHTML}
                ${recentHTML}
                <a class="printer-link" href="${esc(cfg.url)}" target="_blank" rel="noopener">${cfg.type === 'prusalink' ? 'PrusaLink' : 'OctoPrint'} &#8599;</a>
            </div>
            <div class="printer-body">
                <div class="webcam-wrapper">
                    <div class="webcam-container ${isPrinting ? '' : 'webcam-idle'}">
                        <img class="webcam-img" src="${webcamSrc(cfg.id)}" alt="Webcam" onerror="webcamError(this,${isPrinting})">
                        <div class="webcam-placeholder" style="display:none">${state === 'offline' ? 'No camera' : 'Camera unreachable'}</div>
                        <div class="webcam-badge"><span class="${wcMode === 'live' ? 'dot' : 'dot dot-blue'}"></span> ${wcMode === 'live' ? 'LIVE' : 'SNAP'}</div>
                        ${cfg.type !== 'prusalink' ? `<button class="webcam-toggle ${wcMode === 'live' ? 'live' : ''}" onclick="event.stopPropagation();toggleWebcamMode(${cfg.id})" title="Toggle snapshot/live">${wcMode === 'live' ? '&#9724;' : '&#9654;'}</button>` : ''}
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
        stateMsg = detailMsg || 'Unable to reach printer';
    } else if (state === 'disconnected') {
        stateMsg = 'Printer disconnected';
    } else if (state === 'error') {
        stateMsg = detailMsg || 'Printer reported an error';
    } else {
        stateMsg = 'Ready for next job';
    }

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

    const wasDown = prevState === 'offline' || prevState === 'disconnected';
    const isDown = state === 'offline' || state === 'disconnected';

    const hadPower = !!card.querySelector('[data-field="power"]');
    const hasPower = status && status.power && status.power.length > 0;

    if ((isPrinting && !wasPrinting) || (!isPrinting && wasPrinting) || (wasDown !== isDown) || (hasPower && !hadPower)) {
        card.outerHTML = renderPrinterCard(printer);
        if (state === 'idle') loadRecentPrints(cfg.id);
        return;
    }

    card.dataset.state = state;

    // Update card border class on state change
    if (prevState !== state) {
        card.className = `printer-card ${state === 'error' ? 'card-error' : state === 'offline' ? 'card-offline' : state === 'disconnected' ? 'card-disconnected' : ''}`;
        const idleMsg = card.querySelector('[data-field="idle-msg"]');
        if (idleMsg) {
            const detailMsg = status && status.state_message ? status.state_message : '';
            const msgClass = state === 'error' ? 'idle-message msg-error' :
                             state === 'disconnected' ? 'idle-message msg-disconnected' :
                             state === 'offline' ? 'idle-message msg-offline' : 'idle-message';
            idleMsg.className = msgClass;
            if (state === 'offline') idleMsg.textContent = detailMsg || 'Unable to reach printer';
            else if (state === 'disconnected') idleMsg.textContent = 'Printer disconnected';
            else if (state === 'error') idleMsg.textContent = detailMsg || 'Printer reported an error';
            else idleMsg.textContent = 'Ready for next job';
        }
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
            const group = card.querySelector(`[data-field="power"][data-plug-id="${ps.id}"]`);
            if (!group) return;
            const btns = group.querySelectorAll('.power-toggle-btn');
            const isPrinterPlug = singlePlug || (ps.label && ps.label.toLowerCase().includes('printer'));
            if (btns[0]) btns[0].className = `power-toggle-btn ${ps.on ? 'power-btn-active power-on' : ''}`;
            if (btns[1]) {
                btns[1].className = `power-toggle-btn ${!ps.on ? 'power-btn-active power-off' : ''}`;
                btns[1].disabled = isBusy && isPrinterPlug;
                btns[1].title = isBusy && isPrinterPlug ? 'Cannot turn off printer while printing' : '';
            }
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

function openSettings() {
    fetch('/api/settings').then(r => r.json()).then(settings => {
        document.getElementById('setting-snapshot-interval').value = settings.snapshot_interval || '10';
        document.getElementById('setting-poll-interval').value = settings.poll_interval || '';
        document.getElementById('setting-recent-files').value = settings.recent_files_count || '5';
    });
    renderSettingsPrinterList();
    loadUsers();
    loadSmartPlugs();
    document.getElementById('settings-modal').classList.add('active');
}

// Smart plugs (direct Tasmota, managed independently of printers)

async function loadSmartPlugs() {
    try {
        const resp = await fetch('/api/smartplugs');
        const plugs = await resp.json();
        renderSettingsSmartPlugList(plugs || []);
    } catch (e) {}
}

function assignablePrinterOptions(selectedId) {
    return printers
        .map(p => `<option value="${p.config.id}" ${p.config.id === selectedId ? 'selected' : ''}>${esc(p.config.name)}</option>`)
        .join('');
}

function renderSettingsSmartPlugList(plugs) {
    const list = document.getElementById('settings-smartplug-list');
    if (!plugs.length) {
        list.innerHTML = '<div class="settings-empty">No smart plugs configured yet.</div>';
        return;
    }
    list.innerHTML = plugs.map(p => `
        <div class="settings-printer-row">
            <div class="settings-printer-info">
                <span class="settings-printer-name">${esc(p.label || p.ip)}</span>
                <span class="settings-printer-url">${esc(p.ip)}:${esc(p.idx)}</span>
            </div>
            <select onchange="reassignSmartPlug(${p.id}, this.value)">
                <option value="">Unassigned</option>
                ${assignablePrinterOptions(p.printer_id)}
            </select>
            <div class="settings-printer-actions">
                <button class="btn btn-sm btn-danger" onclick="deleteSmartPlug(${p.id})" title="Delete">&times;</button>
            </div>
        </div>`).join('');
}

async function addSmartPlug(e) {
    e.preventDefault();
    const ip = document.getElementById('new-plug-ip').value;
    const idx = document.getElementById('new-plug-idx').value || '1';
    const label = document.getElementById('new-plug-label').value;
    try {
        const resp = await fetch('/api/smartplugs', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({ip, idx, label}),
        });
        if (resp.ok) {
            document.getElementById('new-plug-ip').value = '';
            document.getElementById('new-plug-idx').value = '1';
            document.getElementById('new-plug-label').value = '';
            loadSmartPlugs();
        }
    } catch (e) {}
}

async function reassignSmartPlug(id, printerIdStr) {
    const plugs = await (await fetch('/api/smartplugs')).json();
    const plug = plugs.find(p => p.id === id);
    if (!plug) return;
    try {
        await fetch(`/api/smartplugs/${id}`, {
            method: 'PUT',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
                ip: plug.ip,
                idx: plug.idx,
                label: plug.label,
                printer_id: printerIdStr ? parseInt(printerIdStr) : null,
            }),
        });
        loadSmartPlugs();
    } catch (e) {}
}

async function deleteSmartPlug(id) {
    if (!confirm('Remove this smart plug?')) return;
    try {
        await fetch(`/api/smartplugs/${id}`, {method: 'DELETE'});
        loadSmartPlugs();
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
                <button class="btn btn-sm btn-danger" onclick="deleteUser(${u.id})" title="Delete">&times;</button>
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
    if (!confirm('Remove this user?')) return;
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
                    <span class="settings-printer-url">${esc(cfg.url)} (${cfg.type === 'prusalink' ? 'PrusaLink' : 'OctoPrint'})</span>
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
    document.getElementById('printer-type').value = 'octoprint';
    document.getElementById('printer-url').value = '';
    document.getElementById('printer-username').value = 'maker';
    document.getElementById('printer-apikey').value = '';
    document.getElementById('printer-poll').value = '10';
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
    document.getElementById('printer-type').value = cfg.type;
    document.getElementById('printer-url').value = cfg.url;
    document.getElementById('printer-apikey').value = '';
    document.getElementById('printer-username').value = cfg.username || 'maker';
    document.getElementById('printer-poll').value = cfg.poll_interval;
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
        url: document.getElementById('printer-url').value.replace(/\/+$/, ''),
        api_key: document.getElementById('printer-apikey').value,
        username: printerType === 'prusalink' ? document.getElementById('printer-username').value : '',
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
        recent_files_count: document.getElementById('setting-recent-files').value,
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
    closeModal();
    reloadIdlePrinterRecentPrints();
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
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
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
            alert(`Import complete: ${data.printers_added} printer(s) added.`);
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
    if (e.key === 'Escape') {
        closeModal();
        document.querySelectorAll('.recent-dropdown.open').forEach(d => d.classList.remove('open'));
    }
});

document.addEventListener('click', function(e) {
    if (!e.target.closest('.recent-dropdown')) {
        document.querySelectorAll('.recent-dropdown.open').forEach(d => d.classList.remove('open'));
    }
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
