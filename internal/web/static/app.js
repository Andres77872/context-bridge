/* Context Bridge dashboard.
 *
 * Ships as one plain script: no bundler, no CDN, no framework. The page is
 * served from a loopback origin under a strict CSP, so everything here is
 * same-origin and works with the machine offline.
 */
(() => {
  'use strict';

  // ── constants ─────────────────────────────────────────────────────────────

  const REFRESH_SECONDS = 10;
  const SESSION_PAGE_LIMIT = 200;
  const CAPTURE_PAGE_SIZE = 50;
  const MAX_VIEWER_LINES = 5000;
  const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

  // Agent identity is a fixed slot per agent name, never assigned by rank, so
  // filtering a list never repaints the survivors.
  const AGENT_SLOTS = {
    grep: 1,
    explore: 3,
    executor: 2,
    designer: 7,
    triage: 5,
    web: 4,
    build: 6,
    test: 6,
    general: 0,
    unknown: 0,
  };

  const SHORTCUTS = [
    { keys: ['g', 'o'], label: 'Go to overview' },
    { keys: ['g', 'a'], label: 'Go to analytics' },
    { keys: ['g', 's'], label: 'Go to sessions' },
    { keys: ['g', 'f'], label: 'Go to search' },
    { keys: ['/'], label: 'Focus the search or filter input' },
    { keys: ['j'], label: 'Next item in the focused list' },
    { keys: ['k'], label: 'Previous item in the focused list' },
    { keys: ['Enter'], label: 'Open the highlighted item' },
    { keys: ['r'], label: 'Refresh the current view' },
    { keys: ['t'], label: 'Toggle light and dark theme' },
    { keys: ['p'], label: 'Pause or resume auto refresh' },
    { keys: ['?'], label: 'Show this help' },
    { keys: ['Esc'], label: 'Close dialog, clear filter, or go back' },
  ];

  // ── state ─────────────────────────────────────────────────────────────────

  const state = {
    route: { name: 'overview', params: {}, query: {} },
    meta: null,
    config: { search_mode: 'regex' },
    stats: null,
    analytics: null,
    analyticsDays: 30,
    sessions: [],
    sessionFilters: { q: '', sort: 'recent', includeDeleted: false },
    selectedSession: null,
    sessionDetail: null,
    sessionAgentView: false,
    capturePage: null,
    captureFilters: { agent: '', q: '', order: 'asc', offset: 0 },
    capture: null,
    captureAgentView: null,
    captureView: { wrap: true, lines: true, source: 'agent', find: '', findIndex: 0, findTotal: 0 },
    search: { query: '', scope: 'all', session: '', agent: '', context: 3, response: null, running: false, agentView: false },
    agents: [],
    autoRefresh: true,
    countdown: REFRESH_SECONDS,
    cursor: { list: null, index: -1 },
    confirm: null,
    lastFocus: null,
  };

  // ── tiny DOM helpers ──────────────────────────────────────────────────────

  const $ = (id) => document.getElementById(id);
  const qs = (sel, root = document) => root.querySelector(sel);
  const qsa = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  function esc(value) {
    return String(value === null || value === undefined ? '' : value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function attr(value) {
    return esc(value);
  }

  function icon(name, cls = 'icon') {
    return `<svg class="${cls}" aria-hidden="true"><use href="#i-${esc(name)}"/></svg>`;
  }

  function setHTML(node, html) {
    if (node) node.innerHTML = html;
  }

  // ── formatting ────────────────────────────────────────────────────────────

  function formatBytes(bytes) {
    const value = Number(bytes) || 0;
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(value < 10240 ? 1 : 0)} KB`;
    if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`;
    return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }

  function formatNumber(value) {
    return new Intl.NumberFormat().format(Number(value) || 0);
  }

  function parseDate(value) {
    if (!value) return null;
    const date = new Date(value);
    if (Number.isNaN(date.getTime()) || date.getTime() === 0) return null;
    // Go encodes an absent timestamp as the zero time.
    if (date.getUTCFullYear() <= 1) return null;
    return date;
  }

  function formatRelative(value) {
    const date = parseDate(value);
    if (!date) return '—';
    const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
    if (seconds < 5) return 'just now';
    if (seconds < 60) return `${seconds}s ago`;
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    if (days < 30) return `${days}d ago`;
    return date.toLocaleDateString();
  }

  function formatDateTime(value) {
    const date = parseDate(value);
    if (!date) return '—';
    return date.toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit',
    });
  }

  function formatDuration(seconds) {
    const total = Math.max(0, Math.floor(Number(seconds) || 0));
    if (total < 60) return `${total}s`;
    if (total < 3600) return `${Math.floor(total / 60)}m`;
    if (total < 86400) return `${Math.floor(total / 3600)}h ${Math.floor((total % 3600) / 60)}m`;
    return `${Math.floor(total / 86400)}d ${Math.floor((total % 86400) / 3600)}h`;
  }

  function shortId(id, max = 22) {
    const value = String(id || '');
    if (value.length <= max) return value;
    const head = Math.max(6, Math.ceil((max - 1) / 2));
    const tail = Math.max(4, Math.floor((max - 1) / 2) - 2);
    return `${value.slice(0, head)}…${value.slice(-tail)}`;
  }

  function agentSlot(agent) {
    const key = String(agent || 'unknown').toLowerCase().split(/[-/_.]/)[0];
    if (Object.prototype.hasOwnProperty.call(AGENT_SLOTS, key)) return AGENT_SLOTS[key];
    let hash = 2166136261;
    for (let i = 0; i < key.length; i += 1) {
      hash ^= key.charCodeAt(i);
      hash = Math.imul(hash, 16777619) >>> 0;
    }
    return (hash % 7) + 1;
  }

  function agentColor(agent) {
    const slot = agentSlot(agent);
    return slot === 0 ? 'var(--series-other)' : `var(--series-${slot})`;
  }

  function agentBadge(agent) {
    const label = agent || 'unknown';
    return `<span class="agent-badge" title="${attr(label)}">
      <span class="agent-dot" style="background:${agentColor(label)}"></span>${esc(label)}
    </span>`;
  }

  // ── feedback ──────────────────────────────────────────────────────────────

  function toast(message, tone = 'info') {
    const host = $('toasts');
    if (!host) return;
    const node = document.createElement('div');
    node.className = 'toast';
    node.dataset.tone = tone;
    const glyph = tone === 'error' ? 'alert' : tone === 'success' ? 'check' : 'activity';
    node.innerHTML = `${icon(glyph, 'icon icon-sm')}<span>${esc(message)}</span>`;
    host.appendChild(node);
    setTimeout(() => node.remove(), tone === 'error' ? 7000 : 4000);
  }

  function banner(message, tone = 'info') {
    const node = $('feedback-banner');
    if (!node) return;
    if (!message) {
      node.hidden = true;
      node.textContent = '';
      return;
    }
    node.hidden = false;
    node.dataset.tone = tone;
    node.textContent = message;
  }

  function setConnection(state_, label) {
    const dot = $('connection-dot');
    const text = $('connection-label');
    if (dot) dot.dataset.state = state_;
    if (text) text.textContent = label;
  }

  // ── API ───────────────────────────────────────────────────────────────────

  async function api(path, options = {}) {
    const response = await fetch(path, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json', ...(options.headers || {}) },
      ...options,
    });

    let payload = null;
    const type = response.headers.get('Content-Type') || '';
    if (type.includes('application/json')) {
      payload = await response.json().catch(() => null);
    }
    if (!response.ok) {
      const message = (payload && payload.error) || `HTTP ${response.status}`;
      const error = new Error(message);
      error.status = response.status;
      throw error;
    }
    setConnection('up', 'SQLite connected');
    return payload;
  }

  function queryString(params) {
    const search = new URLSearchParams();
    Object.entries(params).forEach(([key, value]) => {
      if (value === undefined || value === null || value === '' || value === false) return;
      search.set(key, String(value));
    });
    const encoded = search.toString();
    return encoded ? `?${encoded}` : '';
  }

  const API = {
    meta: () => api('/api/meta'),
    stats: () => api('/api/stats'),
    analytics: (days) => api(`/api/analytics${queryString({ days })}`),
    agents: () => api('/api/agents'),
    config: () => api('/api/config'),
    saveConfig: (searchMode) => api('/api/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ search_mode: searchMode }),
    }),
    sessions: (params) => api(`/api/sessions${queryString(params)}`),
    session: (id) => api(`/api/sessions/${encodeURIComponent(id)}`),
    deleteSession: (id) => api(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    captures: (id, params) => api(`/api/sessions/${encodeURIComponent(id)}/captures${queryString(params)}`),
    capture: (id, seq) => api(`/api/sessions/${encodeURIComponent(id)}/captures/${encodeURIComponent(seq)}`),
    deleteCapture: (id, seq) => api(`/api/sessions/${encodeURIComponent(id)}/captures/${encodeURIComponent(seq)}`, { method: 'DELETE' }),
    search: (params) => api(`/api/search${queryString(params)}`),
    agentRead: (id, seq) => api(`/api/sessions/${encodeURIComponent(id)}/mcp/read/${encodeURIComponent(seq)}`),
    agentList: (id, params) => api(`/api/sessions/${encodeURIComponent(id)}/mcp/list${queryString(params || {})}`),
    agentSearch: (id, params) => api(`/api/sessions/${encodeURIComponent(id)}/mcp/search${queryString(params)}`),
  };

  function rawCaptureURL(id, seq) {
    return `/api/sessions/${encodeURIComponent(id)}/captures/${encodeURIComponent(seq)}/raw`;
  }

  function handleError(error, context) {
    if (error && error.status === 403) {
      setConnection('down', 'Session expired');
      banner('Dashboard session expired. Restart `context-bridge web` and open the printed URL again.', 'error');
      return;
    }
    setConnection('idle', 'Request failed');
    toast(`${context}: ${error && error.message ? error.message : 'unknown error'}`, 'error');
  }

  // ── shared fragments ──────────────────────────────────────────────────────

  /**
   * Renders an MCP tool payload verbatim in a monospace block, with the byte
   * size and truncation state the agent would have seen. No re-formatting: the
   * value of this view is that it is the literal text the model received.
   */
  function agentPayloadHTML(view, toolCall) {
    const lines = String(view.text || '').split('\n');
    const rows = lines.map((line, index) =>
      `<span class="ln">${index + 1}</span><span class="lc">${esc(line) || '&nbsp;'}</span>`).join('');
    return `<div class="card" style="margin:14px">
      <div class="card-head">
        <span class="card-title">Agent view — MCP <span class="mono">${esc(view.tool)}</span></span>
        <span class="chip chip-mono">${esc(formatBytes(view.bytes))} of ${esc(formatBytes(view.max_bytes))}</span>
        <span class="chip chip-mono">${esc(formatNumber(view.lines))} lines</span>
        ${view.truncated ? '<span class="chip chip-warn">truncated at the tool limit</span>' : ''}
        <span class="topbar-spacer"></span>
        <span class="chip chip-mono">${esc(toolCall)}</span>
      </div>
      <div class="card-body tight">
        <div class="viewer-code wrap" style="max-height:60vh;overflow:auto">${rows}</div>
      </div>
    </div>`;
  }

  function emptyState(iconName, title, hint = '') {
    return `<div class="empty">
      ${icon(iconName, 'icon icon-xl')}
      <span>${esc(title)}</span>
      ${hint ? `<span class="empty-hint">${esc(hint)}</span>` : ''}
    </div>`;
  }

  function loadingState(message = 'Loading…') {
    return `<div class="loading"><span class="spinner"></span>${esc(message)}</div>`;
  }

  function statTile({ label, iconName, value, mono, meta, spark }) {
    return `<article class="stat">
      <span class="stat-label">${icon(iconName, 'icon icon-sm')}${esc(label)}</span>
      <span class="stat-value${mono ? ' mono' : ''}">${esc(value)}</span>
      <span class="stat-meta">${meta || ''}</span>
      ${spark ? `<span class="stat-spark">${spark}</span>` : ''}
    </article>`;
  }

  // ── charts ────────────────────────────────────────────────────────────────

  const tooltip = {
    node: null,
    show(html, x, y) {
      if (!this.node) this.node = $('chart-tooltip');
      if (!this.node) return;
      this.node.innerHTML = html;
      this.node.dataset.visible = 'true';
      const rect = this.node.getBoundingClientRect();
      const left = Math.min(Math.max(8, x - rect.width / 2), window.innerWidth - rect.width - 8);
      const top = y - rect.height - 12 < 8 ? y + 16 : y - rect.height - 12;
      this.node.style.left = `${left}px`;
      this.node.style.top = `${top}px`;
    },
    hide() {
      if (!this.node) this.node = $('chart-tooltip');
      if (this.node) this.node.dataset.visible = 'false';
    },
  };

  function niceCeil(value) {
    if (value <= 5) return Math.max(1, value);
    const magnitude = 10 ** Math.floor(Math.log10(value));
    const scaled = value / magnitude;
    const step = scaled <= 1 ? 1 : scaled <= 2 ? 2 : scaled <= 5 ? 5 : 10;
    return step * magnitude;
  }

  function barPath(x, y, width, height, radius) {
    const r = Math.min(radius, width / 2, height);
    if (height <= 0) return '';
    if (height <= r) {
      return `M${x},${y + height} L${x},${y + height} L${x + width},${y + height} Z`;
    }
    return `M${x},${y + height} L${x},${y + r} Q${x},${y} ${x + r},${y} L${x + width - r},${y} Q${x + width},${y} ${x + width},${y + r} L${x + width},${y + height} Z`;
  }

  /**
   * Vertical bar chart for a single time series. Bars carry rounded data-ends,
   * a 2px surface gap, a recessive grid, and a per-bar hover tooltip.
   */
  function renderBarChart(container, points, options = {}) {
    if (!container) return;
    const width = Math.max(280, container.clientWidth || 560);
    const height = options.height || 190;
    const padLeft = 38;
    const padRight = 8;
    const padTop = 14;
    const padBottom = 24;
    const plotWidth = width - padLeft - padRight;
    const plotHeight = height - padTop - padBottom;

    if (!points.length) {
      container.innerHTML = emptyState('inbox', 'No activity in this window');
      return;
    }
    const maxValue = Math.max(...points.map((p) => p.value));
    if (maxValue <= 0) {
      container.innerHTML = emptyState('inbox', 'No captures recorded in this window', 'Run an agent through Context Bridge to populate this chart.');
      return;
    }

    const top = niceCeil(maxValue);
    const slot = plotWidth / points.length;
    const barWidth = Math.max(2, slot - 2);
    const yFor = (value) => padTop + plotHeight - (value / top) * plotHeight;
    const gridValues = [0, top / 2, top];
    const peakIndex = points.reduce((best, point, index) => (point.value > points[best].value ? index : best), 0);
    const labelEvery = Math.max(1, Math.ceil(points.length / (width < 480 ? 5 : 9)));

    const grid = gridValues.map((value) => {
      const y = yFor(value);
      return `<line class="chart-grid-line" x1="${padLeft}" y1="${y.toFixed(1)}" x2="${width - padRight}" y2="${y.toFixed(1)}"></line>
        <text class="chart-label" x="${padLeft - 6}" y="${(y + 3).toFixed(1)}" text-anchor="end">${esc(options.axisFormat ? options.axisFormat(value) : formatNumber(Math.round(value)))}</text>`;
    }).join('');

    const bars = points.map((point, index) => {
      const x = padLeft + index * slot + (slot - barWidth) / 2;
      const barHeight = point.value > 0 ? Math.max(2, (point.value / top) * plotHeight) : 0;
      const y = padTop + plotHeight - barHeight;
      const fill = point.color || options.color || 'var(--series-1)';
      const shape = point.value > 0
        ? `<path class="chart-bar" d="${barPath(x, y, barWidth, barHeight, 4)}" style="fill:${fill}"></path>`
        : '';
      const label = index % labelEvery === 0 || index === points.length - 1
        ? `<text class="chart-label" x="${(x + barWidth / 2).toFixed(1)}" y="${height - 8}" text-anchor="middle">${esc(point.label)}</text>`
        : '';
      const peak = index === peakIndex && point.value > 0
        ? `<text class="chart-value-label" x="${(x + barWidth / 2).toFixed(1)}" y="${Math.max(10, y - 5).toFixed(1)}" text-anchor="middle">${esc(options.valueFormat ? options.valueFormat(point.value) : formatNumber(point.value))}</text>`
        : '';
      const hit = `<rect class="chart-hit" x="${(padLeft + index * slot).toFixed(1)}" y="${padTop}" width="${slot.toFixed(1)}" height="${plotHeight}" data-index="${index}"></rect>`;
      return shape + label + peak + hit;
    }).join('');

    container.innerHTML = `<svg class="chart" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}" role="img" aria-label="${attr(options.ariaLabel || 'Bar chart')}">
      ${grid}
      <line class="chart-axis-line" x1="${padLeft}" y1="${padTop + plotHeight}" x2="${width - padRight}" y2="${padTop + plotHeight}"></line>
      ${bars}
    </svg>`;

    const svg = qs('svg', container);
    if (!svg) return;
    svg.addEventListener('mousemove', (event) => {
      const target = event.target.closest('.chart-hit');
      if (!target) { tooltip.hide(); return; }
      const point = points[Number(target.dataset.index)];
      if (!point) return;
      tooltip.show(
        `<div class="tooltip-title">${esc(point.tooltipTitle || point.fullLabel || point.label)}</div>
         <div class="tooltip-row">${esc(options.valueLabel || 'captures')}: <b>${esc(formatNumber(point.value))}</b></div>
         ${point.sub ? `<div class="tooltip-row">${esc(point.sub)}</div>` : ''}`,
        event.clientX, event.clientY,
      );
    });
    svg.addEventListener('mouseleave', () => tooltip.hide());
  }

  /**
   * Ranked horizontal bars. Every row is directly labelled, so identity never
   * depends on colour alone.
   */
  function renderRankedBars(container, rows, options = {}) {
    if (!container) return;
    if (!rows.length) {
      container.innerHTML = emptyState('inbox', options.emptyTitle || 'Nothing to show yet');
      return;
    }
    const max = Math.max(...rows.map((row) => row.value), 1);
    container.innerHTML = rows.map((row) => {
      const pct = Math.max(row.value > 0 ? 3 : 0, (row.value / max) * 100);
      return `<div class="bar-row">
        <span>${row.labelHTML || esc(row.label)}</span>
        <span class="bar-track"><span class="bar-fill" style="width:${pct.toFixed(1)}%;background:${row.color || 'var(--series-1)'}"></span></span>
        <span class="bar-value">${esc(row.valueLabel !== undefined ? row.valueLabel : formatNumber(row.value))}${row.sub ? ` <span class="subtle">${esc(row.sub)}</span>` : ''}</span>
      </div>`;
    }).join('');
  }

  /** Weekday × hour heatmap using a single-hue sequential ramp. */
  function renderHeatmap(container, cells) {
    if (!container) return;
    const total = cells.reduce((sum, cell) => sum + cell.captures, 0);
    if (!cells.length || total === 0) {
      container.innerHTML = emptyState('clock', 'No captures to profile yet', 'The heatmap fills in once agents run at different times.');
      return;
    }
    const max = Math.max(...cells.map((cell) => cell.captures));
    const grid = Array.from({ length: 7 }, () => Array(24).fill(0));
    cells.forEach((cell) => {
      if (cell.weekday >= 0 && cell.weekday < 7 && cell.hour >= 0 && cell.hour < 24) {
        grid[cell.weekday][cell.hour] = cell.captures;
      }
    });

    const step = (value) => {
      if (value <= 0) return 'var(--seq-0)';
      const ratio = value / max;
      if (ratio <= 0.2) return 'var(--seq-1)';
      if (ratio <= 0.4) return 'var(--seq-2)';
      if (ratio <= 0.6) return 'var(--seq-3)';
      if (ratio <= 0.8) return 'var(--seq-4)';
      return 'var(--seq-5)';
    };

    const width = Math.max(320, container.clientWidth || 520);
    const labelWidth = 34;
    const cellGap = 2;
    const cellWidth = (width - labelWidth) / 24;
    const cellSize = Math.max(6, cellWidth - cellGap);
    const rowHeight = Math.min(20, Math.max(12, cellSize + cellGap));
    const height = rowHeight * 7 + 20;

    let svg = '';
    for (let day = 0; day < 7; day += 1) {
      svg += `<text class="chart-label" x="${labelWidth - 8}" y="${day * rowHeight + rowHeight / 2 + 3}" text-anchor="end">${WEEKDAYS[day]}</text>`;
      for (let hour = 0; hour < 24; hour += 1) {
        const value = grid[day][hour];
        svg += `<rect x="${(labelWidth + hour * cellWidth).toFixed(2)}" y="${day * rowHeight}" width="${cellSize.toFixed(2)}" height="${(rowHeight - cellGap).toFixed(2)}" rx="2" style="fill:${step(value)}" data-day="${day}" data-hour="${hour}" data-value="${value}"></rect>`;
      }
    }
    for (let hour = 0; hour < 24; hour += 3) {
      svg += `<text class="chart-label" x="${(labelWidth + hour * cellWidth + cellSize / 2).toFixed(2)}" y="${height - 5}" text-anchor="middle">${hour}</text>`;
    }

    const legend = ['var(--seq-0)', 'var(--seq-1)', 'var(--seq-2)', 'var(--seq-3)', 'var(--seq-4)', 'var(--seq-5)']
      .map((color) => `<span class="legend-swatch" style="background:${color}"></span>`).join('');

    container.innerHTML = `<svg class="chart" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}" role="img" aria-label="Captures by weekday and hour">${svg}</svg>
      <div class="legend"><span class="legend-item">less ${legend} more</span><span class="legend-item">peak ${formatNumber(max)} captures in one hour slot</span></div>`;

    const node = qs('svg', container);
    if (!node) return;
    node.addEventListener('mousemove', (event) => {
      const rect = event.target.closest('rect[data-hour]');
      if (!rect) { tooltip.hide(); return; }
      const day = WEEKDAYS[Number(rect.dataset.day)];
      const hour = Number(rect.dataset.hour);
      tooltip.show(
        `<div class="tooltip-title">${esc(day)} ${String(hour).padStart(2, '0')}:00 UTC</div>
         <div class="tooltip-row">captures: <b>${esc(formatNumber(rect.dataset.value))}</b></div>`,
        event.clientX, event.clientY,
      );
    });
    node.addEventListener('mouseleave', () => tooltip.hide());
  }

  /** Compact sparkline for stat tiles. */
  function sparkline(values, options = {}) {
    const series = values.filter((value) => Number.isFinite(value));
    if (series.length < 2) return '';
    const width = options.width || 132;
    const height = options.height || 26;
    const max = Math.max(...series, 1);
    const stepX = width / (series.length - 1);
    const points = series.map((value, index) => [index * stepX, height - (value / max) * (height - 3) - 1.5]);
    const line = points.map(([x, y], index) => `${index === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`).join(' ');
    const area = `${line} L${width},${height} L0,${height} Z`;
    const color = options.color || 'var(--series-1)';
    return `<svg class="chart" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}" aria-hidden="true">
      <path d="${area}" style="fill:${color};opacity:0.14"></path>
      <path d="${line}" style="stroke:${color};fill:none;stroke-width:2;stroke-linecap:round;stroke-linejoin:round"></path>
    </svg>`;
  }

  // ── router ────────────────────────────────────────────────────────────────

  const VIEWS = {
    overview: { title: 'Overview', node: 'view-overview' },
    analytics: { title: 'Analytics', node: 'view-analytics' },
    sessions: { title: 'Sessions', node: 'view-sessions' },
    search: { title: 'Search', node: 'view-search' },
  };

  function parseRoute() {
    const raw = location.hash.replace(/^#\/?/, '');
    const [pathPart, queryPart] = raw.split('?');
    const segments = pathPart.split('/').filter(Boolean).map(decodeURIComponent);
    const query = Object.fromEntries(new URLSearchParams(queryPart || ''));
    const name = segments[0] && VIEWS[segments[0]] ? segments[0] : 'overview';
    const params = {};
    if (name === 'sessions') {
      if (segments[1]) params.session = segments[1];
      if (segments[2]) params.seq = segments[2];
    }
    return { name, params, query };
  }

  function buildHash(name, params = {}, query = {}) {
    let path = `#/${name}`;
    if (name === 'sessions' && params.session) {
      path += `/${encodeURIComponent(params.session)}`;
      if (params.seq !== undefined && params.seq !== null && params.seq !== '') {
        path += `/${encodeURIComponent(params.seq)}`;
      }
    }
    return path + queryString(query);
  }

  function navigate(name, params = {}, query = {}, replace = false) {
    const hash = buildHash(name, params, query);
    if (location.hash === hash) {
      render();
      return;
    }
    if (replace) location.replace(hash);
    else location.hash = hash;
  }

  function setActiveView(name) {
    Object.entries(VIEWS).forEach(([key, view]) => {
      const node = $(view.node);
      if (node) node.hidden = key !== name;
    });
    qsa('[data-route]').forEach((button) => {
      if (button.dataset.route === name) button.setAttribute('aria-current', 'page');
      else button.removeAttribute('aria-current');
    });
    const title = $('topbar-title');
    if (title) title.textContent = VIEWS[name] ? VIEWS[name].title : name;
  }

  function setTopbarSub(text) {
    const node = $('topbar-sub');
    if (node) node.textContent = text || '';
  }

  async function render() {
    const previous = state.route;
    state.route = parseRoute();
    setActiveView(state.route.name);
    setTopbarSub('');
    state.cursor = { list: null, index: -1 };

    switch (state.route.name) {
      case 'overview':
        await loadOverview();
        break;
      case 'analytics':
        if (state.route.query.days) state.analyticsDays = Number(state.route.query.days) || 30;
        await loadAnalytics();
        break;
      case 'sessions':
        await loadSessionsView(previous);
        break;
      case 'search':
        await loadSearchView();
        break;
      default:
        break;
    }
  }

  // ── overview ──────────────────────────────────────────────────────────────

  async function loadOverview() {
    const statsHost = $('overview-stats');
    if (statsHost && !statsHost.childElementCount) {
      setHTML(statsHost, Array.from({ length: 4 }, () => '<div class="stat skeleton" style="height:112px"></div>').join(''));
    }
    setTopbarSub('');

    try {
      const [analytics, sessions] = await Promise.all([
        API.analytics(14),
        API.sessions({ limit: 8 }),
      ]);
      state.analytics = analytics;
      state.sessions = sessions || [];
      renderOverviewStats(analytics);
      renderOverviewActivity(analytics);
      renderOverviewAgents(analytics);
      renderOverviewSessions(state.sessions);
      setTopbarSub(`${formatNumber(analytics.captures)} outputs · ${formatBytes(analytics.bytes)}`);
    } catch (error) {
      handleError(error, 'Load overview');
      setHTML($('overview-stats'), '');
      setHTML($('overview-sessions'), emptyState('alert', 'Could not load dashboard data', error.message || ''));
    }
  }

  function renderOverviewStats(analytics) {
    const daily = analytics.daily || [];
    const captureSeries = daily.map((bucket) => bucket.captures);
    const lastActive = parseDate(analytics.last_captured_at);
    const prevWindow = captureSeries.slice(0, Math.floor(captureSeries.length / 2)).reduce((a, b) => a + b, 0);
    const thisWindow = captureSeries.slice(Math.floor(captureSeries.length / 2)).reduce((a, b) => a + b, 0);
    const trend = prevWindow === 0 ? (thisWindow > 0 ? 100 : 0) : Math.round(((thisWindow - prevWindow) / prevWindow) * 100);
    const trendClass = trend > 0 ? 'delta-up' : trend < 0 ? 'delta-down' : 'delta-flat';
    const trendLabel = trend > 0 ? `+${trend}%` : `${trend}%`;

    setHTML($('overview-stats'), [
      statTile({
        label: 'Sessions',
        iconName: 'sessions',
        value: formatNumber(analytics.sessions),
        meta: `<span>${formatNumber(analytics.active_sessions)} active in window</span>`,
      }),
      statTile({
        label: 'Outputs captured',
        iconName: 'output',
        value: formatNumber(analytics.captures),
        meta: `<span class="delta ${trendClass}">${esc(trendLabel)}</span><span class="subtle">vs previous 7 days</span>`,
        spark: sparkline(captureSeries, { color: 'var(--series-1)' }),
      }),
      statTile({
        label: 'Stored output',
        iconName: 'database',
        value: formatBytes(analytics.bytes),
        mono: true,
        meta: `<span>avg ${formatBytes(analytics.avg_capture_bytes)} per output</span>`,
        spark: sparkline(daily.map((bucket) => bucket.bytes), { color: 'var(--series-3)' }),
      }),
      statTile({
        label: 'Last activity',
        iconName: 'clock',
        value: lastActive ? formatRelative(analytics.last_captured_at) : '—',
        meta: `<span>${formatNumber(analytics.captures_24h)} outputs in 24h</span>`,
      }),
    ].join(''));
  }

  function renderOverviewActivity(analytics) {
    const sub = $('overview-activity-sub');
    if (sub) sub.textContent = `last ${analytics.window_days} days`;
    const points = (analytics.daily || []).map((bucket) => ({
      label: bucket.day.slice(5),
      fullLabel: bucket.day,
      value: bucket.captures,
      sub: formatBytes(bucket.bytes),
    }));
    renderBarChart($('overview-activity'), points, { ariaLabel: 'Captures per day', height: 200 });
  }

  function renderOverviewAgents(analytics) {
    const agents = (analytics.agents || []).slice(0, 7);
    renderRankedBars($('overview-agents'), agents.map((agent) => ({
      labelHTML: agentBadge(agent.agent),
      value: agent.captures,
      color: agentColor(agent.agent),
      sub: formatBytes(agent.bytes),
    })), { emptyTitle: 'No agent activity yet' });
  }

  function renderOverviewSessions(sessions) {
    const host = $('overview-sessions');
    if (!host) return;
    if (!sessions.length) {
      setHTML(host, emptyState('inbox', 'No sessions captured yet', 'Start an OpenCode session with the Context Bridge plugin installed.'));
      return;
    }
    const rows = sessions.map((session) => `<tr data-action="open-session" data-session="${attr(session.id)}" tabindex="0">
      <td><span class="mono" title="${attr(session.id)}">${esc(shortId(session.id, 26))}</span></td>
      <td class="num">${esc(formatNumber(session.capture_count))}</td>
      <td class="num mono">${esc(formatBytes(session.bytes))}</td>
      <td class="num">${esc(formatNumber(session.agent_count))}</td>
      <td>${esc(formatRelative(session.last_captured_at || session.created_at))}</td>
      <td>${sessionStatusChip(session)}</td>
    </tr>`).join('');

    setHTML(host, `<div class="table-wrap"><table class="data">
      <thead><tr>
        <th>Session</th><th class="num">Outputs</th><th class="num">Size</th>
        <th class="num">Agents</th><th>Last activity</th><th>Status</th>
      </tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`);
  }

  function sessionStatusChip(session) {
    if (session.deleted_at) return '<span class="chip chip-danger">deleted</span>';
    if (session.ended_at) return '<span class="chip">ended</span>';
    return '<span class="chip chip-good">active</span>';
  }

  // ── analytics ─────────────────────────────────────────────────────────────

  async function loadAnalytics() {
    qsa('#analytics-window button').forEach((button) => {
      button.setAttribute('aria-pressed', String(Number(button.dataset.days) === state.analyticsDays));
    });
    const kpis = $('analytics-kpis');
    if (kpis && !kpis.childElementCount) {
      setHTML(kpis, Array.from({ length: 4 }, () => '<div class="stat skeleton" style="height:104px"></div>').join(''));
    }

    try {
      const analytics = await API.analytics(state.analyticsDays);
      state.analytics = analytics;
      renderAnalytics(analytics);
      setTopbarSub(`window ${analytics.window_days}d · retention ${analytics.retention_days}d`);
    } catch (error) {
      handleError(error, 'Load analytics');
    }
  }

  function renderAnalytics(analytics) {
    const generated = $('analytics-generated');
    if (generated) generated.textContent = `generated ${formatRelative(analytics.generated_at)}`;

    setHTML($('analytics-kpis'), [
      statTile({
        label: 'Captures in window',
        iconName: 'output',
        value: formatNumber(analytics.captures),
        meta: `<span>${formatNumber(analytics.captures_24h)} in 24h · ${formatNumber(analytics.captures_7d)} in 7d</span>`,
      }),
      statTile({
        label: 'Active sessions',
        iconName: 'sessions',
        value: formatNumber(analytics.active_sessions),
        meta: `<span>${formatNumber(analytics.sessions)} live · ${formatNumber(analytics.ended_sessions)} ended</span>`,
      }),
      statTile({
        label: 'Output size',
        iconName: 'layers',
        value: formatBytes(analytics.median_capture_bytes),
        mono: true,
        meta: `<span>median · p95 ${formatBytes(analytics.p95_capture_bytes)}</span>`,
      }),
      statTile({
        label: 'Database on disk',
        iconName: 'database',
        value: formatBytes(analytics.storage ? analytics.storage.database_bytes : 0),
        mono: true,
        meta: `<span>${formatBytes(analytics.bytes)} of captured output</span>`,
      }),
    ].join(''));

    const dailySub = $('analytics-daily-sub');
    if (dailySub) {
      const busiest = (analytics.daily || []).reduce((best, bucket) => (bucket.captures > best.captures ? bucket : best), { captures: 0, day: '' });
      dailySub.textContent = busiest.captures > 0
        ? `busiest day ${busiest.day} with ${formatNumber(busiest.captures)} outputs`
        : `last ${analytics.window_days} days`;
    }

    renderBarChart($('analytics-daily'), (analytics.daily || []).map((bucket) => ({
      label: bucket.day.slice(5),
      fullLabel: bucket.day,
      value: bucket.captures,
      sub: formatBytes(bucket.bytes),
    })), { ariaLabel: 'Captures per day', height: 220 });

    renderHeatmap($('analytics-heatmap'), analytics.heatmap || []);

    renderRankedBars($('analytics-sizes'), (analytics.size_buckets || []).map((bucket, index) => ({
      label: bucket.label,
      value: bucket.captures,
      color: `var(--seq-${Math.min(5, index + 1)})`,
      valueLabel: formatNumber(bucket.captures),
    })), { emptyTitle: 'No captures to measure yet' });

    renderAnalyticsAgents(analytics);
    renderTopSessions(analytics);
    renderStorage(analytics);
  }

  function renderAnalyticsAgents(analytics) {
    const host = $('analytics-agents');
    if (!host) return;
    const agents = analytics.agents || [];
    if (!agents.length) {
      setHTML(host, emptyState('inbox', 'No agent activity in this window'));
      return;
    }
    const totalCaptures = agents.reduce((sum, agent) => sum + agent.captures, 0) || 1;
    const maxCaptures = Math.max(...agents.map((agent) => agent.captures), 1);

    const rows = agents.map((agent) => {
      const share = (agent.captures / totalCaptures) * 100;
      const width = (agent.captures / maxCaptures) * 100;
      return `<tr>
        <td>${agentBadge(agent.agent)}</td>
        <td style="min-width:120px">
          <span class="bar-track" style="display:block"><span class="bar-fill" style="width:${width.toFixed(1)}%;background:${agentColor(agent.agent)}"></span></span>
        </td>
        <td class="num">${esc(formatNumber(agent.captures))}</td>
        <td class="num">${esc(share.toFixed(1))}%</td>
        <td class="num mono">${esc(formatBytes(agent.bytes))}</td>
        <td class="num mono">${esc(formatBytes(agent.captures ? Math.round(agent.bytes / agent.captures) : 0))}</td>
        <td class="num">${esc(formatNumber(agent.sessions))}</td>
        <td>${esc(formatRelative(agent.last_captured_at))}</td>
      </tr>`;
    }).join('');

    setHTML(host, `<div class="table-wrap"><table class="data">
      <thead><tr>
        <th>Agent</th><th>Share</th><th class="num">Outputs</th><th class="num">%</th>
        <th class="num">Bytes</th><th class="num">Avg</th><th class="num">Sessions</th><th>Last seen</th>
      </tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`);
  }

  function renderTopSessions(analytics) {
    const host = $('analytics-top-sessions');
    if (!host) return;
    const sessions = analytics.top_sessions || [];
    if (!sessions.length) {
      setHTML(host, emptyState('inbox', 'No sessions in this window'));
      return;
    }
    const rows = sessions.map((session) => {
      const first = parseDate(session.first_captured_at);
      const last = parseDate(session.last_captured_at);
      const span = first && last ? formatDuration((last - first) / 1000) : '—';
      return `<tr data-action="open-session" data-session="${attr(session.id)}" tabindex="0">
        <td><span class="mono" title="${attr(session.id)}">${esc(shortId(session.id, 20))}</span></td>
        <td class="num">${esc(formatNumber(session.captures))}</td>
        <td class="num mono">${esc(formatBytes(session.bytes))}</td>
        <td class="num">${esc(formatNumber(session.agents))}</td>
        <td class="num">${esc(span)}</td>
      </tr>`;
    }).join('');

    setHTML(host, `<div class="table-wrap"><table class="data">
      <thead><tr><th>Session</th><th class="num">Outputs</th><th class="num">Size</th><th class="num">Agents</th><th class="num">Span</th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`);
  }

  function renderStorage(analytics) {
    const host = $('analytics-storage');
    if (!host) return;
    const storage = analytics.storage || { capture_bytes: 0, database_bytes: 0 };
    const overhead = Math.max(0, storage.database_bytes - storage.capture_bytes);
    const largest = analytics.largest_capture;

    setHTML(host, `
      <div class="detail-stats" style="margin-bottom:12px">
        <span class="detail-stat"><b class="mono">${esc(formatBytes(storage.capture_bytes))}</b>captured output</span>
        <span class="detail-stat"><b class="mono">${esc(formatBytes(storage.database_bytes))}</b>database file</span>
        <span class="detail-stat"><b class="mono">${esc(formatBytes(overhead))}</b>index &amp; overhead</span>
      </div>
      <div class="note">
        Captures are pruned after <b>${esc(analytics.retention_days)} days</b>. Every number on this page is scoped to live
        sessions inside that window, which is why deleting a session changes the totals.
      </div>
      ${largest ? `
        <div class="note" style="margin-top:10px">
          <div class="meta-key">Largest single output</div>
          <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-top:6px">
            ${agentBadge(largest.agent)}
            <span class="mono">#${esc(largest.seq)}</span>
            <span class="chip chip-mono">${esc(formatBytes(largest.bytes))}</span>
            <span class="subtle">${esc(formatRelative(largest.captured_at))}</span>
          </div>
          <div style="margin-top:6px">${esc(largest.description || '(no description)')}</div>
          <button class="btn" type="button" style="margin-top:8px" data-action="open-capture" data-session="${attr(largest.session_id)}" data-seq="${attr(largest.seq)}">
            Open output ${icon('chevron-right', 'icon icon-sm')}
          </button>
        </div>` : ''}
    `);
  }

  // ── sessions ──────────────────────────────────────────────────────────────

  async function loadSessionsView(previousRoute) {
    const { session, seq } = state.route.params;
    const sessionChanged = session !== state.selectedSession;
    state.selectedSession = session || null;

    await loadSessionList();

    if (!session) {
      showSessionPanel();
      setHTML($('session-detail-body'), emptyState('sessions', 'Select a session', 'Pick a session on the left to review its captured outputs.'));
      hide($('session-detail-head'));
      hide($('capture-toolbar'));
      hide($('capture-pager'));
      setTopbarSub('');
      return;
    }

    if (sessionChanged) {
      state.captureFilters = { agent: '', q: '', order: state.captureFilters.order, offset: 0 };
      syncCaptureFilterInputs();
    }

    if (seq !== undefined && seq !== null && seq !== '') {
      await openCapture(session, seq);
      return;
    }

    showSessionPanel();
    const found = await loadSessionDetail(session);
    if (!found) {
      hide($('capture-toolbar'));
      hide($('capture-pager'));
      setHTML($('session-detail-body'), emptyState('alert', 'Session not found',
        'It may have been deleted, or pruned after the retention window. Pick another session on the left.'));
      return;
    }
    await loadCaptures(session);
    if (previousRoute && previousRoute.name !== 'sessions') {
      focusSessionList();
    }
  }

  function hide(node) { if (node) node.hidden = true; }
  function show(node) { if (node) node.hidden = false; }

  function showSessionPanel() {
    show($('session-panel'));
    hide($('capture-panel'));
  }

  function showCapturePanel() {
    hide($('session-panel'));
    show($('capture-panel'));
  }

  async function loadSessionList() {
    const host = $('session-list');
    if (host && !host.childElementCount) setHTML(host, loadingState('Loading sessions…'));
    try {
      const sessions = await API.sessions({
        limit: SESSION_PAGE_LIMIT,
        q: state.sessionFilters.q,
        sort: state.sessionFilters.sort,
        include_deleted: state.sessionFilters.includeDeleted,
      });
      state.sessions = sessions || [];
      renderSessionList();
    } catch (error) {
      handleError(error, 'Load sessions');
      setHTML(host, emptyState('alert', 'Could not load sessions', error.message || ''));
    }
  }

  function renderSessionList() {
    const host = $('session-list');
    const count = $('session-count');
    if (count) {
      count.textContent = state.sessions.length === 1 ? '1 session' : `${formatNumber(state.sessions.length)} sessions`;
    }
    if (!host) return;
    if (!state.sessions.length) {
      setHTML(host, emptyState('inbox', 'No sessions match', state.sessionFilters.q ? 'Clear the filter to see every session.' : 'Captures appear here as agents run.'));
      return;
    }

    setHTML(host, state.sessions.map((session) => `
      <button class="session-row" type="button" data-action="open-session" data-session="${attr(session.id)}"
              aria-current="${session.id === state.selectedSession ? 'true' : 'false'}">
        <span class="session-row-top">
          <span class="session-id" title="${attr(session.id)}">${esc(shortId(session.id, 28))}</span>
          ${session.deleted_at ? '<span class="chip chip-danger">deleted</span>' : ''}
        </span>
        <span class="session-row-meta">
          <span>${esc(formatRelative(session.last_captured_at || session.created_at))}</span>
          <span>·</span>
          <span>${esc(formatNumber(session.capture_count))} outputs</span>
          <span>·</span>
          <span class="mono">${esc(formatBytes(session.bytes))}</span>
        </span>
      </button>`).join(''));
  }

  async function loadSessionDetail(sessionId) {
    try {
      const detail = await API.session(sessionId);
      state.sessionDetail = detail;
      renderSessionDetailHead(detail);
      setTopbarSub(`${shortId(sessionId, 24)} · ${formatNumber(detail.session.capture_count)} outputs`);
      return true;
    } catch (error) {
      hide($('session-detail-head'));
      if (error.status === 404) {
        toast(`Session ${shortId(sessionId, 18)} is no longer available.`, 'error');
      } else {
        handleError(error, 'Load session');
      }
      return false;
    }
  }

  function renderSessionDetailHead(detail) {
    const host = $('session-detail-head');
    if (!host) return;
    const session = detail.session;
    const first = parseDate(detail.first_captured_at);
    const last = parseDate(detail.last_captured_at);
    const span = first && last ? formatDuration((last - first) / 1000) : '—';

    setHTML(host, `
      <div class="detail-head-row">
        <span class="mono" title="${attr(session.id)}">${esc(shortId(session.id, 34))}</span>
        ${sessionStatusChip(session)}
        ${detail.child_sessions ? `<span class="chip">${esc(formatNumber(detail.child_sessions))} sub-sessions</span>` : ''}
        <span class="topbar-spacer"></span>
        <a class="btn" href="/api/sessions/${encodeURIComponent(session.id)}/export" download>
          ${icon('download', 'icon icon-sm')} Export JSON
        </a>
        <button class="btn btn-danger" type="button" id="session-delete-button" data-action="delete-session" data-session="${attr(session.id)}">
          ${icon('trash', 'icon icon-sm')} Delete session
        </button>
      </div>
      <div class="detail-stats">
        <span class="detail-stat"><b>${esc(formatNumber(session.capture_count))}</b>outputs</span>
        <span class="detail-stat"><b class="mono">${esc(formatBytes(detail.bytes))}</b>stored</span>
        <span class="detail-stat"><b>${esc(formatNumber((detail.agents || []).length))}</b>agents</span>
        <span class="detail-stat"><b class="mono">${esc(formatBytes(detail.largest_bytes))}</b>largest</span>
        <span class="detail-stat"><b>${esc(span)}</b>span</span>
        <span class="detail-stat"><b>${esc(formatRelative(detail.last_captured_at))}</b>last output</span>
      </div>
      ${(detail.agents || []).length ? `<div class="legend">${detail.agents.map((agent) => `
        <span class="legend-item">${agentBadge(agent.agent)}<span class="nums">${esc(formatNumber(agent.captures))}</span></span>`).join('')}</div>` : ''}
    `);
    show(host);
  }

  function syncCaptureFilterInputs() {
    const agentSelect = $('capture-agent-filter');
    const queryInput = $('capture-query-filter');
    if (agentSelect) agentSelect.value = state.captureFilters.agent;
    if (queryInput && queryInput.value !== state.captureFilters.q) queryInput.value = state.captureFilters.q;
    qsa('#capture-order button').forEach((button) => {
      button.setAttribute('aria-pressed', String(button.dataset.order === state.captureFilters.order));
    });
  }

  async function loadCaptures(sessionId) {
    const host = $('session-detail-body');
    if (host && !host.childElementCount) setHTML(host, loadingState('Loading outputs…'));
    show($('capture-toolbar'));

    if (state.sessionAgentView) {
      hide($('capture-pager'));
      try {
        const view = await API.agentList(sessionId, { agent: state.captureFilters.agent });
        setHTML(host, agentPayloadHTML(view, `list session_id=${sessionId}`));
      } catch (error) {
        handleError(error, 'Load agent view');
        setHTML(host, emptyState('alert', 'Could not render the agent view', error.message || ''));
      }
      return;
    }

    try {
      const page = await API.captures(sessionId, {
        agent: state.captureFilters.agent,
        q: state.captureFilters.q,
        order: state.captureFilters.order,
        limit: CAPTURE_PAGE_SIZE,
        offset: state.captureFilters.offset,
      });
      state.capturePage = page;
      renderCaptureAgentOptions(page.agents || []);
      renderCaptureList(sessionId, page);
      renderCapturePager(page);
    } catch (error) {
      handleError(error, 'Load outputs');
      setHTML(host, emptyState('alert', 'Could not load outputs', error.message || ''));
      hide($('capture-pager'));
    }
  }

  function renderCaptureAgentOptions(agents) {
    const select = $('capture-agent-filter');
    if (!select) return;
    const current = state.captureFilters.agent;
    const options = ['<option value="">All agents</option>']
      .concat(agents.map((agent) => `<option value="${attr(agent)}"${agent === current ? ' selected' : ''}>${esc(agent)}</option>`));
    select.innerHTML = options.join('');
    select.value = current;
  }

  function renderCaptureList(sessionId, page) {
    const host = $('session-detail-body');
    if (!host) return;
    const items = page.items || [];

    if (!items.length) {
      const filtered = state.captureFilters.agent || state.captureFilters.q;
      setHTML(host, filtered
        ? emptyState('filter', 'No outputs match these filters', 'Clear the agent or text filter to see the whole session.')
        : emptyState('inbox', 'This session has no outputs', 'Outputs appear as subagents finish their work.'));
      return;
    }

    setHTML(host, items.map((capture) => `
      <button class="capture-row" type="button" data-action="open-capture" data-session="${attr(sessionId)}" data-seq="${attr(capture.seq)}">
        <span class="capture-seq">#${esc(capture.seq)}</span>
        <span class="capture-main">
          <span class="capture-meta">
            ${agentBadge(capture.agent)}
            <span>${esc(formatRelative(capture.captured_at))}</span>
            <span class="mono">${esc(formatBytes(capture.bytes))}</span>
            ${capture.child_session_id ? `<span class="chip">sub-session</span>` : ''}
          </span>
          <span class="capture-desc">${esc(capture.description || '(no description)')}</span>
          <span class="capture-preview">${esc((capture.preview || '').split('\n')[0])}</span>
        </span>
        ${icon('chevron-right', 'icon icon-sm subtle')}
      </button>`).join(''));
  }

  function renderCapturePager(page) {
    const host = $('capture-pager');
    if (!host) return;
    const shown = (page.items || []).length;
    const start = shown ? page.offset + 1 : 0;
    const end = page.offset + shown;
    const hasPrev = page.offset > 0;
    const hasNext = end < page.filtered;

    if (!page.filtered) { hide(host); return; }
    show(host);
    setHTML(host, `
      <span class="nums">Showing ${esc(formatNumber(start))}–${esc(formatNumber(end))} of ${esc(formatNumber(page.filtered))}${page.filtered !== page.total ? ` (filtered from ${esc(formatNumber(page.total))})` : ''}</span>
      <span style="display:flex;gap:6px">
        <button class="btn" type="button" data-action="captures-prev" ${hasPrev ? '' : 'disabled'}>${icon('back', 'icon icon-sm')} Previous</button>
        <button class="btn" type="button" data-action="captures-next" ${hasNext ? '' : 'disabled'}>Next ${icon('forward', 'icon icon-sm')}</button>
      </span>`);
  }

  // ── capture detail ────────────────────────────────────────────────────────

  async function openCapture(sessionId, seq) {
    showCapturePanel();
    setHTML($('capture-viewer'), loadingState('Loading output…'));
    try {
      const [capture, agentView] = await Promise.all([
        API.capture(sessionId, seq),
        API.agentRead(sessionId, seq).catch(() => null),
      ]);
      state.capture = capture;
      state.captureAgentView = agentView;
      state.captureView.find = '';
      state.captureView.findIndex = 0;
      syncCaptureSource();
      const findInput = $('capture-find');
      if (findInput) findInput.value = '';
      renderCaptureMeta(capture);
      renderCaptureContent();
      setTopbarSub(`${shortId(sessionId, 20)} · output #${capture.seq}`);
    } catch (error) {
      handleError(error, 'Load output');
      setHTML($('capture-viewer'), emptyState('alert', 'Could not load this output', error.message || ''));
    }
  }

  /**
   * The dashboard exists to inspect what the agent actually received, so the
   * viewer defaults to the agent view: the exact text `read` returns over MCP,
   * trust boundary, header, and truncation included. The Stored source drops to
   * the raw document in SQLite for when you are debugging capture itself.
   */
  function visibleDocument() {
    if (state.captureView.source === 'agent' && state.captureAgentView) {
      return { lines: String(state.captureAgentView.text || '').split('\n'), firstLine: 1 };
    }
    if (!state.capture) return { lines: [], firstLine: 1 };
    return { lines: String(state.capture.content || '').split('\n'), firstLine: 1 };
  }

  function syncCaptureSource() {
    qsa('#capture-source button').forEach((button) => {
      const isActive = button.dataset.source === state.captureView.source;
      button.setAttribute('aria-pressed', String(isActive));
      // Without an agent payload (render failed) the stored document is all we have.
      if (button.dataset.source === 'agent') button.disabled = !state.captureAgentView;
    });
  }

  function renderCaptureMeta(capture) {
    const host = $('capture-meta');
    if (host) {
      const agentView = state.captureAgentView;
      const showingAgent = state.captureView.source === 'agent' && agentView;
      setHTML(host, `<div class="detail-head-row">
        <span class="card-title">Output #${esc(capture.seq)}</span>
        ${agentBadge(capture.agent)}
        <span class="chip chip-mono">${esc(formatBytes(capture.bytes))}</span>
        <span class="chip chip-mono">${esc(formatNumber(visibleDocument().lines.length))} lines</span>
        ${showingAgent ? `<span class="chip chip-accent" title="Size of the MCP read payload the agent received">agent payload ${esc(formatBytes(agentView.bytes))}</span>` : ''}
        ${showingAgent && agentView.truncated ? '<span class="chip chip-warn">truncated at the tool limit</span>' : ''}
        <span class="subtle">${esc(formatDateTime(capture.captured_at))}</span>
      </div>
      <div style="margin-top:6px">${esc(capture.description || '(no description)')}</div>
      ${capture.child_session_id ? `<div class="subtle mono" style="margin-top:4px">sub-session ${esc(capture.child_session_id)}</div>` : ''}`);
    }
    const download = $('capture-download');
    if (download) download.href = rawCaptureURL(capture.session_id, capture.seq);
    const del = $('capture-delete-button');
    if (del) {
      del.dataset.session = capture.session_id;
      del.dataset.seq = String(capture.seq);
    }
  }

  function highlightLine(line, needle) {
    if (!needle) return esc(line);
    const lower = line.toLowerCase();
    const target = needle.toLowerCase();
    let out = '';
    let index = 0;
    let found = lower.indexOf(target, index);
    while (found !== -1) {
      out += esc(line.slice(index, found));
      out += `<mark data-hit>${esc(line.slice(found, found + needle.length))}</mark>`;
      index = found + needle.length;
      found = lower.indexOf(target, index);
    }
    return out + esc(line.slice(index));
  }

  function renderCaptureContent() {
    const host = $('capture-viewer');
    if (!host || !state.capture) return;
    const needle = state.captureView.find.trim();
    const visible = visibleDocument();
    let lines = visible.lines;
    let truncated = false;
    if (lines.length > MAX_VIEWER_LINES) {
      lines = lines.slice(0, MAX_VIEWER_LINES);
      truncated = true;
    }

    const rows = lines.map((line, index) => {
      const rendered = highlightLine(line, needle);
      const hit = needle && line.toLowerCase().includes(needle.toLowerCase());
      return `<span class="ln">${visible.firstLine + index}</span><span class="lc${hit ? ' hit' : ''}">${rendered || '&nbsp;'}</span>`;
    }).join('');

    const classes = ['viewer-code'];
    if (state.captureView.wrap) classes.push('wrap');
    setHTML(host, `<div class="${classes.join(' ')}" data-lines="${state.captureView.lines ? 'on' : 'off'}">${rows}</div>
      ${truncated ? `<div class="note" style="margin:12px">Showing the first ${formatNumber(MAX_VIEWER_LINES)} lines. Download the raw output to read all of it.</div>` : ''}`);

    if (!state.captureView.lines) {
      qsa('.viewer-code .ln', host).forEach((node) => { node.style.display = 'none'; });
      const code = qs('.viewer-code', host);
      if (code) code.style.gridTemplateColumns = 'minmax(0, 1fr)';
    }

    const marks = qsa('mark[data-hit]', host);
    state.captureView.findTotal = marks.length;
    updateFindUI();
    if (marks.length) focusMatch(0);
  }

  function updateFindUI() {
    const counter = $('capture-find-count');
    if (!counter) return;
    const total = state.captureView.findTotal;
    counter.textContent = total ? `${state.captureView.findIndex + 1}/${total}` : '0';
    const prev = $('capture-find-prev');
    const next = $('capture-find-next');
    if (prev) prev.disabled = total === 0;
    if (next) next.disabled = total === 0;
  }

  function focusMatch(index) {
    const host = $('capture-viewer');
    if (!host) return;
    const marks = qsa('mark[data-hit]', host);
    if (!marks.length) return;
    const bounded = ((index % marks.length) + marks.length) % marks.length;
    state.captureView.findIndex = bounded;
    marks.forEach((mark, position) => mark.classList.toggle('active', position === bounded));
    marks[bounded].scrollIntoView({ block: 'center', behavior: 'smooth' });
    updateFindUI();
  }

  // ── search ────────────────────────────────────────────────────────────────

  async function loadSearchView() {
    const query = state.route.query;
    state.search.query = query.q || state.search.query;
    state.search.scope = query.scope === 'session' ? 'session' : 'all';
    state.search.session = query.session || state.search.session;
    state.search.agent = query.agent || '';
    state.search.context = snapContextLines(query.context);

    syncSearchControls();
    await ensureSearchOptions();

    if (state.search.query) {
      await runSearch(false);
    } else if (!$('search-results').childElementCount) {
      setHTML($('search-results'), emptyState('search', 'Search every captured output',
        'Query across all sessions at once, or narrow to a single session. Results link straight to the output.'));
    }
    const input = $('search-input');
    if (input && !state.search.query) input.focus();
  }

  // The context-lines control offers a fixed ladder; a deep link carrying any
  // other value snaps to the closest rung so the select never renders blank.
  function snapContextLines(value) {
    const allowed = [0, 1, 3, 6, 10, 20];
    const requested = Number(value);
    if (!Number.isFinite(requested)) return state.search.context || 3;
    return allowed.reduce((best, option) => (
      Math.abs(option - requested) < Math.abs(best - requested) ? option : best
    ), allowed[0]);
  }

  async function ensureSearchOptions() {
    try {
      const [sessions, agents] = await Promise.all([
        state.sessions.length ? Promise.resolve(state.sessions) : API.sessions({ limit: SESSION_PAGE_LIMIT }),
        state.agents.length ? Promise.resolve(state.agents) : API.agents(),
      ]);
      state.sessions = sessions || [];
      state.agents = agents || [];
    } catch (error) {
      handleError(error, 'Load search filters');
      return;
    }

    const sessionSelect = $('search-session');
    if (sessionSelect) {
      sessionSelect.innerHTML = state.sessions.length
        ? state.sessions.map((session) => `<option value="${attr(session.id)}"${session.id === state.search.session ? ' selected' : ''}>${esc(shortId(session.id, 30))} · ${esc(formatNumber(session.capture_count))} outputs</option>`).join('')
        : '<option value="">No sessions available</option>';
      if (!state.search.session && state.sessions.length) {
        state.search.session = state.sessions[0].id;
        sessionSelect.value = state.search.session;
      }
    }

    const agentSelect = $('search-agent');
    if (agentSelect) {
      agentSelect.innerHTML = ['<option value="">All agents</option>']
        .concat(state.agents.map((agent) => `<option value="${attr(agent)}"${agent === state.search.agent ? ' selected' : ''}>${esc(agent)}</option>`))
        .join('');
      agentSelect.value = state.search.agent;
    }
  }

  function syncSearchControls() {
    const input = $('search-input');
    if (input) {
      input.value = state.search.query;
      input.placeholder = state.config.search_mode === 'fts5'
        ? 'FTS5 terms, "quoted phrase", or prefix*…'
        : 'Regex pattern or literal text…';
    }
    qsa('#search-scope button').forEach((button) => {
      button.setAttribute('aria-pressed', String(button.dataset.scope === state.search.scope));
    });
    const sessionField = $('search-session-field');
    if (sessionField) sessionField.hidden = state.search.scope !== 'session';
    const contextSelect = $('search-context');
    if (contextSelect) contextSelect.value = String(state.search.context);

    const note = $('search-mode-note');
    if (note) {
      note.innerHTML = state.config.search_mode === 'fts5'
        ? 'Engine <b>FTS5</b>: whole-word tokens, <span class="mono">"quoted phrases"</span> and <span class="mono">prefix*</span> wildcards. Change it in Settings — it is a shared setting for the dashboard, TUI, and MCP.'
        : 'Engine <b>regex</b>: case-insensitive Go regular expressions, e.g. <span class="mono">error|panic</span> or <span class="mono">TODO\\(.*\\)</span>. Change it in Settings — it is a shared setting for the dashboard, TUI, and MCP.';
    }
  }

  async function runSearch(updateHash = true) {
    const query = state.search.query.trim();
    if (!query) {
      setHTML($('search-results'), emptyState('search', 'Enter a query to search captured output'));
      setHTML($('search-summary'), '');
      return;
    }
    if (state.search.scope === 'session' && !state.search.session) {
      toast('Pick a session to search, or switch the scope to all sessions.', 'error');
      return;
    }

    if (updateHash) {
      const hash = buildHash('search', {}, {
        q: query,
        scope: state.search.scope,
        session: state.search.scope === 'session' ? state.search.session : '',
        agent: state.search.agent,
        context: state.search.context,
      });
      if (location.hash !== hash) {
        history.replaceState(null, '', hash);
        state.route = parseRoute();
      }
    }

    state.search.running = true;
    setSearchBusy(true);
    setHTML($('search-results'), loadingState('Searching captured output…'));
    setHTML($('search-summary'), '');

    try {
      const response = await API.search({
        q: query,
        session: state.search.scope === 'session' ? state.search.session : '',
        agent: state.search.agent,
        context: state.search.context,
      });
      state.search.response = response;
      renderSearchResults(response);
      if (state.search.agentView) await renderSearchAgentView();
      setTopbarSub(`${formatNumber(response.results.length)} outputs · ${response.mode} · ${response.elapsed_ms} ms`);
    } catch (error) {
      setHTML($('search-results'), emptyState('alert', 'Search failed', error.message || ''));
      if (error.status !== 400) handleError(error, 'Search');
    } finally {
      state.search.running = false;
      setSearchBusy(false);
    }
  }

  function setSearchBusy(busy) {
    const button = $('search-submit');
    if (!button) return;
    button.disabled = busy;
    button.setAttribute('aria-busy', String(busy));
  }

  // The MCP search tool is session-scoped, so the agent view only exists when
  // the dashboard search is narrowed to one session.
  async function renderSearchAgentView() {
    const host = $('search-results');
    if (!host) return;
    if (state.search.scope !== 'session' || !state.search.session) {
      setHTML(host, emptyState('alert', 'The agent view needs a single session',
        'MCP search always runs inside one session. Switch the scope to One session to see the payload the agent gets.'));
      return;
    }
    setHTML(host, loadingState('Rendering the MCP search payload…'));
    try {
      const view = await API.agentSearch(state.search.session, {
        q: state.search.query.trim(),
        context: state.search.context,
      });
      setHTML(host, agentPayloadHTML(view, `search session_id=${state.search.session}`));
    } catch (error) {
      setHTML(host, emptyState('alert', 'Could not render the agent view', error.message || ''));
    }
  }

  function renderSearchResults(response) {
    const results = response.results || [];
    const summary = $('search-summary');
    if (summary) {
      setHTML(summary, `<div class="toolbar" style="align-items:center">
        <span class="chip chip-accent">${esc(formatNumber(results.length))} output${results.length === 1 ? '' : 's'}</span>
        <span class="chip">${esc(formatNumber(response.total_matches))} matching lines</span>
        <span class="chip chip-mono">${esc(response.mode)}</span>
        <span class="chip">${esc(response.scope === 'all' ? 'all sessions' : shortId(response.session_id, 22))}</span>
        ${response.agent ? `<span class="chip">agent ${esc(response.agent)}</span>` : ''}
        <span class="chip chip-mono">${esc(response.elapsed_ms)} ms</span>
        ${response.truncated ? '<span class="chip chip-warn">truncated — narrow the query for the full picture</span>' : ''}
      </div>`);
    }

    const host = $('search-results');
    if (!host) return;
    if (!results.length) {
      setHTML(host, emptyState('search', `No matches for “${response.query}”`,
        response.mode === 'fts5'
          ? 'FTS5 matches whole words. Try a shorter term or switch the engine to regex in Settings.'
          : 'Regex is case-insensitive. Try a shorter pattern or switch the engine to FTS5 in Settings.'));
      return;
    }

    const groups = new Map();
    results.forEach((result) => {
      const id = result.capture.session_id;
      if (!groups.has(id)) groups.set(id, []);
      groups.get(id).push(result);
    });

    const blocks = [];
    groups.forEach((items, sessionId) => {
      if (response.scope === 'all') {
        blocks.push(`<div class="result-group"><span class="mono">${esc(shortId(sessionId, 30))}</span><span class="subtle">${esc(formatNumber(items.length))} output${items.length === 1 ? '' : 's'}</span></div>`);
      }
      items.forEach((result) => {
        const capture = result.capture;
        blocks.push(`<button class="result" type="button" data-action="open-capture" data-session="${attr(capture.session_id)}" data-seq="${attr(capture.seq)}">
          <span class="result-head">
            <span class="mono">#${esc(capture.seq)}</span>
            ${agentBadge(capture.agent)}
            <span>${esc(formatRelative(capture.captured_at))}</span>
            <span class="mono">${esc(formatBytes(capture.bytes))}</span>
            <span class="topbar-spacer"></span>
            <span class="chip chip-warn">${esc(formatNumber(result.match_count))} match${result.match_count === 1 ? '' : 'es'}</span>
          </span>
          <span class="result-desc">${esc(capture.description || '(no description)')}</span>
          ${renderSnippet(result.snippet, response.query, response.mode)}
        </button>`);
      });
    });

    setHTML(host, `<div style="display:flex;flex-direction:column;gap:10px">${blocks.join('')}</div>`);
  }

  /**
   * The store returns snippets as fenced blocks of "<prefix> <line>: <text>".
   * Rendering that structure beats dumping the raw text: matched lines get a
   * gutter, a highlight, and the query marked inside them.
   */
  function renderSnippet(snippet, query, mode) {
    if (!snippet) return '';
    const matcher = buildMatcher(query, mode);
    const lines = snippet.split('\n');
    const rendered = [];

    lines.forEach((line) => {
      if (line.trim() === '```') return;
      if (line.trim() === '[snippet truncated]') {
        rendered.push('<span class="ln"></span><span class="lc subtle">[snippet truncated]</span>');
        return;
      }
      const match = line.match(/^(>>>|\s{3})\s(\d+):\s?(.*)$/);
      if (!match) {
        rendered.push(`<span class="ln"></span><span class="lc">${esc(line)}</span>`);
        return;
      }
      const [, prefix, number, text] = match;
      const isHit = prefix === '>>>';
      rendered.push(`<span class="ln">${esc(number)}</span><span class="lc${isHit ? ' hit' : ''}">${isHit ? markMatches(text, matcher) : esc(text)}</span>`);
    });

    if (!rendered.length) return '';
    return `<span class="snippet" style="display:block;padding:0"><span class="viewer-code wrap" style="display:grid">${rendered.join('')}</span></span>`;
  }

  function buildMatcher(query, mode) {
    const trimmed = String(query || '').trim();
    if (!trimmed) return null;
    if (mode === 'fts5') {
      const terms = trimmed.replace(/["*]/g, ' ').split(/\s+/).filter((term) => term && !/^(AND|OR|NOT|NEAR)$/i.test(term));
      if (!terms.length) return null;
      try {
        return new RegExp(`(${terms.map(escapeRegExp).join('|')})`, 'gi');
      } catch (error) {
        return null;
      }
    }
    try {
      return new RegExp(`(${trimmed})`, 'gi');
    } catch (error) {
      try {
        return new RegExp(`(${escapeRegExp(trimmed)})`, 'gi');
      } catch (innerError) {
        return null;
      }
    }
  }

  function escapeRegExp(value) {
    return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }

  function markMatches(text, matcher) {
    if (!matcher) return esc(text);
    matcher.lastIndex = 0;
    let out = '';
    let last = 0;
    let guard = 0;
    let match = matcher.exec(text);
    while (match && guard < 500) {
      guard += 1;
      if (match.index >= last) {
        out += esc(text.slice(last, match.index));
        out += `<mark>${esc(match[0])}</mark>`;
        last = match.index + match[0].length;
      }
      if (match[0].length === 0) matcher.lastIndex += 1;
      match = matcher.exec(text);
    }
    return out + esc(text.slice(last));
  }

  // ── modals ────────────────────────────────────────────────────────────────

  function openModal(id) {
    const modal = $(id);
    if (!modal) return;
    state.lastFocus = document.activeElement;
    modal.hidden = false;
    const focusable = qs('input, button, select, [tabindex]:not([tabindex="-1"])', modal);
    if (focusable) focusable.focus();
  }

  function closeModal(id) {
    const modal = $(id);
    if (!modal) return;
    modal.hidden = true;
    if (state.lastFocus && document.contains(state.lastFocus)) state.lastFocus.focus();
  }

  function anyModalOpen() {
    return qsa('.modal-backdrop').some((modal) => !modal.hidden);
  }

  function trapFocus(event) {
    const modal = qsa('.modal-backdrop').find((node) => !node.hidden);
    if (!modal || event.key !== 'Tab') return;
    const focusable = qsa('a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])', modal);
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  // ── settings ──────────────────────────────────────────────────────────────

  function applyConfig(config) {
    if (!config || !config.search_mode) return;
    state.config = config;
    const chip = $('settings-mode-chip');
    if (chip) chip.textContent = config.search_mode;
    qsa('input[name="settings-search-mode"]').forEach((input) => {
      input.checked = input.value === config.search_mode;
    });
    syncSearchControls();
  }

  async function openSettings() {
    openModal('settings-modal');
    setSettingsFeedback('');
    try {
      applyConfig(await API.config());
    } catch (error) {
      setSettingsFeedback(`Could not load settings: ${error.message}`, 'error');
    }
  }

  function setSettingsFeedback(message, tone = 'muted') {
    const node = $('settings-feedback');
    if (!node) return;
    node.textContent = message || '';
    node.style.color = tone === 'error' ? 'var(--danger)' : tone === 'success' ? 'var(--good)' : 'var(--text-subtle)';
  }

  async function saveSettings() {
    const selected = qs('input[name="settings-search-mode"]:checked');
    const mode = selected ? selected.value : 'regex';
    const button = $('settings-save');
    if (button) button.disabled = true;
    setSettingsFeedback('Saving…');
    try {
      applyConfig(await API.saveConfig(mode));
      setSettingsFeedback('Saved.', 'success');
      toast(`Search engine set to ${mode}. The dashboard uses it immediately.`, 'success');
      setTimeout(() => closeModal('settings-modal'), 400);
      if (state.route.name === 'search' && state.search.query) runSearch(false);
    } catch (error) {
      setSettingsFeedback(`Could not save: ${error.message}`, 'error');
    } finally {
      if (button) button.disabled = false;
    }
  }

  // ── confirm ───────────────────────────────────────────────────────────────

  function openConfirm(config) {
    state.confirm = config;
    const title = $('confirm-title');
    const copy = $('confirm-copy');
    const meta = $('confirm-meta');
    const button = $('confirm-action-button');
    if (title) title.textContent = config.title;
    if (copy) copy.textContent = config.copy;
    if (button) button.textContent = config.actionLabel || 'Delete';
    if (meta) {
      setHTML(meta, (config.meta || []).map((item) => `<div class="meta-item">
        <div class="meta-key">${esc(item.label)}</div>
        <div class="meta-val">${esc(item.value)}</div>
      </div>`).join(''));
    }
    const feedback = $('confirm-feedback');
    if (feedback) feedback.textContent = '';
    openModal('confirm-modal');
  }

  async function submitConfirm() {
    if (!state.confirm) return;
    const button = $('confirm-action-button');
    const feedback = $('confirm-feedback');
    if (button) button.disabled = true;
    if (feedback) feedback.textContent = 'Working…';

    try {
      await state.confirm.run();
      closeModal('confirm-modal');
      state.confirm = null;
    } catch (error) {
      if (feedback) feedback.textContent = `Failed: ${error.message}`;
      toast(`Delete failed: ${error.message}`, 'error');
    } finally {
      if (button) button.disabled = false;
    }
  }

  function confirmDeleteSession(sessionId) {
    openConfirm({
      title: 'Delete this session from the dashboard?',
      copy: 'The session is soft deleted: its outputs stop appearing in listings, search, and analytics, and the row stays in the database until retention prunes it.',
      meta: [
        { label: 'Session', value: sessionId },
        { label: 'Effect', value: 'Soft delete — hidden from every dashboard surface' },
      ],
      run: async () => {
        await API.deleteSession(sessionId);
        toast(`Session ${shortId(sessionId, 18)} deleted.`, 'success');
        state.selectedSession = null;
        state.sessionDetail = null;
        navigate('sessions');
        await loadSessionList();
      },
    });
  }

  function confirmDeleteCapture(sessionId, seq) {
    openConfirm({
      title: `Delete output #${seq}?`,
      copy: 'This permanently removes the stored output and its search index entry. Other outputs in the session are untouched.',
      meta: [
        { label: 'Session', value: sessionId },
        { label: 'Output', value: `#${seq}` },
        { label: 'Effect', value: 'Hard delete — the content cannot be recovered' },
      ],
      run: async () => {
        await API.deleteCapture(sessionId, seq);
        toast(`Output #${seq} deleted.`, 'success');
        navigate('sessions', { session: sessionId });
        await loadCaptures(sessionId);
      },
    });
  }

  // ── theme ─────────────────────────────────────────────────────────────────

  function preferredTheme() {
    try {
      const stored = localStorage.getItem('context-bridge-theme');
      if (stored === 'light' || stored === 'dark') return stored;
    } catch (error) { /* storage may be blocked; fall through */ }
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }

  function applyTheme(theme) {
    document.documentElement.dataset.theme = theme;
    const button = $('toggle-theme');
    if (button) {
      setHTML(button, `<svg class="icon icon-sm" aria-hidden="true"><use href="#i-${theme === 'light' ? 'sun' : 'moon'}"/></svg>`);
      button.title = theme === 'light' ? 'Switch to dark theme (t)' : 'Switch to light theme (t)';
    }
    try {
      localStorage.setItem('context-bridge-theme', theme);
    } catch (error) { /* ignore */ }
    redrawCharts();
  }

  function toggleTheme() {
    applyTheme(document.documentElement.dataset.theme === 'light' ? 'dark' : 'light');
  }

  // ── auto refresh ──────────────────────────────────────────────────────────

  function refreshBlocked() {
    if (!state.autoRefresh) return true;
    if (document.hidden) return true;
    if (anyModalOpen()) return true;
    const active = document.activeElement;
    if (active && (active.tagName === 'INPUT' || active.tagName === 'SELECT' || active.tagName === 'TEXTAREA')) return true;
    // Never yank content out from under someone reading a single output.
    if (state.route.name === 'sessions' && state.route.params.seq) return true;
    return false;
  }

  function tick() {
    const chip = $('refresh-countdown');
    if (refreshBlocked()) {
      if (chip) chip.textContent = state.autoRefresh ? 'idle' : 'paused';
      return;
    }
    state.countdown -= 1;
    if (state.countdown <= 0) {
      state.countdown = REFRESH_SECONDS;
      refreshCurrent(true);
    }
    if (chip) chip.textContent = `${state.countdown}s`;
  }

  // statsChanged polls the cheap totals endpoint so an idle dashboard costs one
  // query every tick instead of the full analytics sweep.
  async function statsChanged() {
    try {
      const stats = await API.stats();
      const previous = state.stats;
      state.stats = stats;
      if (!previous) return true;
      return previous.sessions !== stats.sessions
        || previous.captures !== stats.captures
        || previous.total_bytes !== stats.total_bytes;
    } catch (error) {
      handleError(error, 'Check for new captures');
      return false;
    }
  }

  async function refreshCurrent(silent = false) {
    state.countdown = REFRESH_SECONDS;
    switch (state.route.name) {
      case 'overview':
        if (silent && !(await statsChanged())) return;
        await loadOverview();
        break;
      case 'analytics':
        if (silent && !(await statsChanged())) return;
        await loadAnalytics();
        break;
      case 'sessions':
        if (silent && !(await statsChanged())) return;
        await loadSessionList();
        if (state.selectedSession && !state.route.params.seq) {
          await Promise.all([loadSessionDetail(state.selectedSession), loadCaptures(state.selectedSession)]);
        }
        break;
      case 'search':
        if (state.search.query) await runSearch(false);
        break;
      default:
        break;
    }
    if (!silent) toast('Refreshed', 'success');
  }

  function setAutoRefresh(enabled) {
    state.autoRefresh = enabled;
    const button = $('toggle-autorefresh');
    if (button) {
      button.setAttribute('aria-pressed', String(enabled));
      button.title = enabled ? 'Pause auto refresh (p)' : 'Resume auto refresh (p)';
    }
    const chip = $('refresh-countdown');
    if (chip) chip.textContent = enabled ? `${state.countdown}s` : 'paused';
  }

  let redrawTimer = null;
  function redrawCharts() {
    clearTimeout(redrawTimer);
    redrawTimer = setTimeout(() => {
      if (!state.analytics) return;
      if (state.route.name === 'overview') {
        renderOverviewActivity(state.analytics);
      } else if (state.route.name === 'analytics') {
        renderAnalytics(state.analytics);
      }
    }, 120);
  }

  // ── list keyboard navigation ──────────────────────────────────────────────

  function currentListItems() {
    if (state.route.name === 'sessions' && !state.route.params.seq) {
      const captures = qsa('#session-detail-body .capture-row');
      if (captures.length && state.cursor.list === 'captures') return captures;
      const sessions = qsa('#session-list .session-row');
      if (state.cursor.list === 'sessions') return sessions;
      return captures.length ? captures : sessions;
    }
    if (state.route.name === 'search') return qsa('#search-results .result');
    if (state.route.name === 'overview') return qsa('#overview-sessions tr[data-action]');
    return [];
  }

  function moveCursor(delta) {
    const items = currentListItems();
    if (!items.length) return;
    const activeIndex = items.findIndex((item) => item === document.activeElement);
    const base = activeIndex >= 0 ? activeIndex : state.cursor.index;
    const next = Math.min(Math.max(0, base + delta), items.length - 1);
    state.cursor.index = next;
    items[next].focus();
    items[next].scrollIntoView({ block: 'nearest' });
  }

  function focusSessionList() {
    const first = qs('#session-list .session-row');
    if (first) {
      state.cursor.list = 'sessions';
      state.cursor.index = 0;
    }
  }

  // ── events ────────────────────────────────────────────────────────────────

  function onAction(event) {
    const trigger = event.target.closest('[data-action], [data-route], [data-close-modal]');
    if (!trigger) return;

    if (trigger.hasAttribute('data-close-modal')) {
      const modal = trigger.closest('.modal-backdrop');
      if (modal) closeModal(modal.id);
      return;
    }
    if (trigger.dataset.route) {
      navigate(trigger.dataset.route);
      return;
    }

    const { action, session, seq } = trigger.dataset;
    switch (action) {
      case 'open-session':
        state.cursor.list = 'captures';
        navigate('sessions', { session });
        break;
      case 'open-capture':
        navigate('sessions', { session, seq });
        break;
      case 'delete-session':
        confirmDeleteSession(session);
        break;
      case 'delete-capture':
        confirmDeleteCapture(session, Number(seq));
        break;
      case 'captures-prev':
        state.captureFilters.offset = Math.max(0, state.captureFilters.offset - CAPTURE_PAGE_SIZE);
        loadCaptures(state.selectedSession);
        break;
      case 'captures-next':
        state.captureFilters.offset += CAPTURE_PAGE_SIZE;
        loadCaptures(state.selectedSession);
        break;
      default:
        break;
    }
  }

  function debounce(fn, delay) {
    let timer = null;
    return (...args) => {
      clearTimeout(timer);
      timer = setTimeout(() => fn(...args), delay);
    };
  }

  function bindEvents() {
    document.addEventListener('click', onAction);
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') {
        const row = event.target.closest('tr[data-action]');
        if (row) {
          event.preventDefault();
          onAction({ target: row });
        }
      }
    });

    qsa('.modal-backdrop').forEach((modal) => {
      modal.addEventListener('mousedown', (event) => {
        if (event.target === modal) closeModal(modal.id);
      });
    });

    $('open-settings').addEventListener('click', openSettings);
    $('settings-save').addEventListener('click', saveSettings);
    $('open-shortcuts').addEventListener('click', () => openModal('shortcuts-modal'));
    $('confirm-action-button').addEventListener('click', submitConfirm);
    $('toggle-theme').addEventListener('click', toggleTheme);
    $('refresh-now').addEventListener('click', () => refreshCurrent(false));
    $('toggle-autorefresh').addEventListener('click', () => setAutoRefresh(!state.autoRefresh));

    // Sessions view controls.
    $('session-filter').addEventListener('input', debounce((event) => {
      state.sessionFilters.q = event.target.value.trim();
      loadSessionList();
    }, 220));
    $('session-sort').addEventListener('change', (event) => {
      state.sessionFilters.sort = event.target.value;
      loadSessionList();
    });
    $('session-include-deleted').addEventListener('change', (event) => {
      state.sessionFilters.includeDeleted = event.target.checked;
      loadSessionList();
    });

    $('capture-agent-filter').addEventListener('change', (event) => {
      state.captureFilters.agent = event.target.value;
      state.captureFilters.offset = 0;
      if (state.selectedSession) loadCaptures(state.selectedSession);
    });
    $('capture-query-filter').addEventListener('input', debounce((event) => {
      state.captureFilters.q = event.target.value.trim();
      state.captureFilters.offset = 0;
      if (state.selectedSession) loadCaptures(state.selectedSession);
    }, 220));
    $('session-agent-view').addEventListener('click', () => {
      state.sessionAgentView = !state.sessionAgentView;
      $('session-agent-view').setAttribute('aria-pressed', String(state.sessionAgentView));
      if (state.selectedSession) loadCaptures(state.selectedSession);
    });
    $('search-agent-view').addEventListener('click', () => {
      state.search.agentView = !state.search.agentView;
      $('search-agent-view').setAttribute('aria-pressed', String(state.search.agentView));
      if (!state.search.query.trim()) return;
      if (state.search.agentView) renderSearchAgentView();
      else runSearch(false);
    });
    $('capture-order').addEventListener('click', (event) => {
      const button = event.target.closest('button[data-order]');
      if (!button) return;
      state.captureFilters.order = button.dataset.order;
      state.captureFilters.offset = 0;
      syncCaptureFilterInputs();
      if (state.selectedSession) loadCaptures(state.selectedSession);
    });

    // Capture viewer controls.
    $('capture-back').addEventListener('click', () => navigate('sessions', { session: state.selectedSession }));
    $('capture-copy').addEventListener('click', async () => {
      if (!state.capture) return;
      try {
        // Copy exactly what is on screen; Download always ships the stored document.
        await navigator.clipboard.writeText(visibleDocument().lines.join('\n'));
        toast(state.captureView.source === 'agent'
          ? 'Copied the exact payload the agent received.'
          : 'Copied the stored document.', 'success');
      } catch (error) {
        toast('Clipboard blocked by the browser. Use Download instead.', 'error');
      }
    });
    $('capture-find').addEventListener('input', debounce((event) => {
      state.captureView.find = event.target.value;
      state.captureView.findIndex = 0;
      renderCaptureContent();
    }, 180));
    $('capture-find-next').addEventListener('click', () => focusMatch(state.captureView.findIndex + 1));
    $('capture-find-prev').addEventListener('click', () => focusMatch(state.captureView.findIndex - 1));
    $('capture-wrap').addEventListener('click', () => {
      state.captureView.wrap = !state.captureView.wrap;
      $('capture-wrap').setAttribute('aria-pressed', String(state.captureView.wrap));
      renderCaptureContent();
    });
    $('capture-lines').addEventListener('click', () => {
      state.captureView.lines = !state.captureView.lines;
      $('capture-lines').setAttribute('aria-pressed', String(state.captureView.lines));
      renderCaptureContent();
    });
    $('capture-source').addEventListener('click', (event) => {
      const button = event.target.closest('button[data-source]');
      if (!button) return;
      state.captureView.source = button.dataset.source;
      syncCaptureSource();
      if (state.capture) renderCaptureMeta(state.capture);
      renderCaptureContent();
    });

    // Search controls.
    $('search-form').addEventListener('submit', (event) => {
      event.preventDefault();
      state.search.query = $('search-input').value;
      runSearch();
    });
    $('search-scope').addEventListener('click', (event) => {
      const button = event.target.closest('button[data-scope]');
      if (!button) return;
      state.search.scope = button.dataset.scope;
      syncSearchControls();
      if (state.search.query.trim()) runSearch();
    });
    $('search-session').addEventListener('change', (event) => {
      state.search.session = event.target.value;
      if (state.search.query.trim()) runSearch();
    });
    $('search-agent').addEventListener('change', (event) => {
      state.search.agent = event.target.value;
      if (state.search.query.trim()) runSearch();
    });
    $('search-context').addEventListener('change', (event) => {
      state.search.context = Number(event.target.value) || 0;
      if (state.search.query.trim()) runSearch();
    });

    $('analytics-window').addEventListener('click', (event) => {
      const button = event.target.closest('button[data-days]');
      if (!button) return;
      state.analyticsDays = Number(button.dataset.days) || 30;
      navigate('analytics', {}, { days: state.analyticsDays });
      loadAnalytics();
    });

    window.addEventListener('hashchange', render);
    window.addEventListener('resize', redrawCharts);
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) state.countdown = REFRESH_SECONDS;
    });
    window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', (event) => {
      let stored = null;
      try { stored = localStorage.getItem('context-bridge-theme'); } catch (error) { /* ignore */ }
      if (!stored) applyTheme(event.matches ? 'light' : 'dark');
    });

    bindShortcuts();
  }

  let pendingChord = null;
  function bindShortcuts() {
    document.addEventListener('keydown', (event) => {
      trapFocus(event);

      const target = event.target;
      const typing = target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.isContentEditable);

      if (event.key === 'Escape') {
        const openBackdrop = qsa('.modal-backdrop').find((node) => !node.hidden);
        if (openBackdrop) { closeModal(openBackdrop.id); return; }
        if (typing) {
          target.blur();
          return;
        }
        if (state.route.name === 'sessions' && state.route.params.seq) {
          navigate('sessions', { session: state.selectedSession });
        }
        return;
      }

      if (typing || event.metaKey || event.ctrlKey || event.altKey) return;

      if (pendingChord === 'g') {
        pendingChord = null;
        const routes = { o: 'overview', a: 'analytics', s: 'sessions', f: 'search' };
        if (routes[event.key]) {
          event.preventDefault();
          navigate(routes[event.key]);
        }
        return;
      }

      switch (event.key) {
        case 'g':
          pendingChord = 'g';
          setTimeout(() => { pendingChord = null; }, 900);
          break;
        case '/': {
          event.preventDefault();
          const input = state.route.name === 'search'
            ? $('search-input')
            : state.route.name === 'sessions'
              ? (state.selectedSession ? $('capture-query-filter') : $('session-filter'))
              : null;
          if (input) input.focus();
          else navigate('search');
          break;
        }
        case 'j':
          event.preventDefault();
          moveCursor(1);
          break;
        case 'k':
          event.preventDefault();
          moveCursor(-1);
          break;
        case 'r':
          event.preventDefault();
          refreshCurrent(false);
          break;
        case 't':
          toggleTheme();
          break;
        case 'p':
          setAutoRefresh(!state.autoRefresh);
          break;
        case '?':
          openModal('shortcuts-modal');
          break;
        default:
          break;
      }
    });
  }

  function renderShortcuts() {
    setHTML($('shortcuts-body'), SHORTCUTS.map((shortcut) => `<div class="shortcut-row">
      <span>${esc(shortcut.label)}</span>
      <span class="shortcut-keys">${shortcut.keys.map((key) => `<kbd>${esc(key)}</kbd>`).join('')}</span>
    </div>`).join(''));
  }

  // ── boot ──────────────────────────────────────────────────────────────────

  async function boot() {
    applyTheme(preferredTheme());
    renderShortcuts();
    bindEvents();
    setAutoRefresh(true);

    try {
      const [meta, config] = await Promise.all([API.meta(), API.config()]);
      state.meta = meta;
      applyConfig(config);
      const version = $('brand-version');
      if (version) version.textContent = meta.version;
      const footer = $('footer-meta');
      if (footer) {
        footer.textContent = `${meta.version} · retention ${meta.retention_days}d · up ${formatDuration(meta.uptime_seconds)}`;
        footer.title = meta.database_path || '';
      }
    } catch (error) {
      handleError(error, 'Load dashboard metadata');
    }

    if (!location.hash) navigate('overview', {}, {}, true);
    await render();
    setInterval(tick, 1000);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
