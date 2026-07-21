// Escape untrusted, DB-stored strings before interpolating into innerHTML.
// Request logs capture attacker-controlled values (X-Client-App header, the
// requested model name, the URL path, upstream error text); rendering them raw
// is a stored-XSS vector reachable by any virtual-key holder.
function escapeHtml(value) {
    if (value === null || value === undefined) return '';
    return String(value)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

document.addEventListener('DOMContentLoaded', () => {
    let charts = {};

    // Global State Cache for instant modal loading
    const state = {
        users: [],
        keys: [],
        plans: [],
        providers: [],
        models: [],
        logs: []
    };

    // --- Networking helpers ---
    // Wraps fetch + JSON parsing so a non-2xx response or a backend
    // `{"error": "..."}` payload becomes a real thrown Error carrying the
    // actual server message, instead of silently flowing through as if it
    // were success data (which used to crash later .map()/.length calls with
    // no visible explanation — the "load error, no data shown" symptom).
    function fetchJSON(url, options) {
        return fetch(url, options).then((res) => {
            return res.json().catch(() => null).then((body) => {
                const looksLikeError = body && typeof body === 'object' && !Array.isArray(body) && 'error' in body;
                if (!res.ok || looksLikeError) {
                    const message = (body && body.error) ? body.error : `Request failed (HTTP ${res.status})`;
                    throw new Error(message);
                }
                return body;
            });
        });
    }

    // Renders a visible, styled error row inside a <tbody>, replacing what
    // used to be a silently blank table with no indication anything failed.
    function renderTableError(tbody, colspan, message) {
        if (!tbody) return;
        tbody.innerHTML = `<tr><td colspan="${colspan}" class="text-danger text-center" style="text-align:center;">⚠ Failed to load: ${escapeHtml(message)}</td></tr>`;
    }

    // Same idea for non-table containers (provider cards, router tier lists).
    function renderContainerError(el, message) {
        if (!el) return;
        el.innerHTML = `<div class="text-danger" style="grid-column: 1/-1; text-align: center; padding: 1rem;">⚠ Failed to load: ${escapeHtml(message)}</div>`;
    }

    // --- Mutation helpers (U1) ---
    // Ephemeral toast so a write's success OR failure is actually visible. Before
    // this, every create/update/delete used raw fetch with no res.ok check, so a
    // 4xx/5xx parsed as JSON and ran the success branch — the modal closed and the
    // list reloaded as if it had worked (the "I clicked Save and nothing happened"
    // report). mutateJSON routes writes through the same throw-on-error path as
    // reads (fetchJSON); the caller shows the server's message with showToast.
    function showToast(message, kind) {
        let host = document.getElementById('toast-host');
        if (!host) {
            host = document.createElement('div');
            host.id = 'toast-host';
            host.style.cssText = 'position:fixed;bottom:1.5rem;right:1.5rem;z-index:9999;display:flex;flex-direction:column;gap:0.5rem;';
            document.body.appendChild(host);
        }
        const el = document.createElement('div');
        el.textContent = message;
        el.style.cssText = 'padding:0.75rem 1rem;border-radius:8px;color:#fff;box-shadow:0 4px 12px rgba(0,0,0,0.25);max-width:360px;font-size:0.9rem;' +
            (kind === 'error' ? 'background:#c0392b;' : 'background:#1e8e4e;');
        host.appendChild(el);
        setTimeout(() => el.remove(), kind === 'error' ? 6000 : 3000);
    }

    function mutateJSON(url, method, body) {
        return fetchJSON(url, {
            method: method,
            headers: { 'Content-Type': 'application/json' },
            body: body === undefined ? undefined : JSON.stringify(body)
        });
    }

    // U8: the plaintext virtual key exists exactly once — the server returns it on
    // create and never again. Surface it (and copy it to the clipboard) so the
    // admin can capture it, instead of silently discarding the response as before.
    function showGeneratedKey(token) {
        try { navigator.clipboard.writeText(token); } catch (e) { /* clipboard may be unavailable */ }
        window.prompt('New API key — copied to clipboard. It is shown ONCE and cannot be retrieved later:', token);
    }

    // DOM Elements
    const navItems = document.querySelectorAll('.nav-item');
    const tabContents = document.querySelectorAll('.tab-content');
    const pageTitle = document.getElementById('page-title');
    const pageSubtitle = document.getElementById('page-subtitle');
    const refreshBtn = document.getElementById('btn-refresh');

    // Modals
    const modalUser = document.getElementById('modal-user');
    const modalKey = document.getElementById('modal-key');
    const modalPlan = document.getElementById('modal-plan');
    const modalProvider = document.getElementById('modal-provider');
    const modalModel = document.getElementById('modal-model');
    const modalUserTopups = document.getElementById('modal-user-topups');

    // Setup Modal Close Handlers Once globally
    document.querySelectorAll('.close-modal').forEach(btn => {
        btn.addEventListener('click', () => {
            btn.closest('.modal').classList.remove('show');
        });
    });

    window.addEventListener('click', (e) => {
        if (e.target.classList.contains('modal')) {
            e.target.classList.remove('show');
        }
    });

    // Tab Navigation
    navItems.forEach(item => {
        item.addEventListener('click', (e) => {
            e.preventDefault();
            const tab = item.getAttribute('data-tab');

            navItems.forEach(n => n.classList.remove('active'));
            item.classList.add('active');

            tabContents.forEach(content => content.classList.remove('active'));
            document.getElementById(`tab-${tab}`).classList.add('active');

            const name = item.querySelector('span').innerText;
            pageTitle.innerText = name;
            
            const subtitles = {
                dashboard: "Gateway analytics, performance, and budget windows metrics",
                users: "Manage platform users and link them to rate and budget plans",
                keys: "Generate virtual API keys belonging to users",
                plans: "Configure plans and define budget window parameters inline",
                providers: "Manage connection keys and model mapping configurations",
                logs: "Audit live API request headers, latencies, and token spendings",
                router: "Monitor and analyze the Muhiya AI Router routing tiers, mappings, and failovers",
                settings: "Customize global gateway variables and system settings"
            };
            pageSubtitle.innerText = subtitles[tab] || "";

            loadTabData(tab);
        });
    });

    if (refreshBtn) {
        refreshBtn.addEventListener('click', () => {
            const activeTab = document.querySelector('.nav-item.active').getAttribute('data-tab');
            loadTabData(activeTab);
        });
    }

    function loadTabData(tab) {
        switch (tab) {
            case 'dashboard':
                loadDashboardStats();
                break;
            case 'users':
                loadUsers();
                break;
            case 'keys':
                loadKeys();
                break;
            case 'plans':
                loadPlansAndBudgets();
                break;
            case 'providers':
                loadProvidersAndModels();
                break;
            case 'logs':
                loadLogs();
                break;
            case 'router':
                loadRouterData();
                break;
            case 'settings':
                loadSettings();
                break;
        }
    }

    // --- Modal Add Triggers Setup Once ---
    document.getElementById('btn-add-user').addEventListener('click', () => {
        const form = document.getElementById('user-form');
        form.reset();
        document.getElementById('user-id').value = '';
        document.getElementById('user-modal-title').innerText = "Create User";
        modalUser.classList.add('show');
    });

    document.getElementById('btn-add-key').addEventListener('click', () => {
        const form = document.getElementById('key-form');
        form.reset();
        document.getElementById('key-id').value = '';
        document.getElementById('key-status-group').style.display = 'none';
        document.getElementById('key-modal-title').innerText = "Generate Virtual Key";
        document.getElementById('key-submit-btn').innerText = "Generate Key";
        modalKey.classList.add('show');
    });

    document.getElementById('btn-add-plan').addEventListener('click', () => {
        const form = document.getElementById('plan-form');
        form.reset();
        document.getElementById('plan-id').value = '';
        document.getElementById('plan-id-group').style.display = 'block';
        document.getElementById('plan-id-val').value = '';
        document.getElementById('plan-id-val').disabled = false;
        document.getElementById('plan-budgets-container').innerHTML = '';

        // Default seeding rows for ease of creation
        addBudgetWindowRow({ name: 'Short Term (5h)', duration_seconds: 18000, budget_usd: 2.00 });
        addBudgetWindowRow({ name: 'Weekly Budget (7d)', duration_seconds: 604800, budget_usd: 10.00 });

        document.getElementById('plan-modal-title').innerText = "Create Plan";
        modalPlan.classList.add('show');
    });

    document.getElementById('btn-plan-add-budget').addEventListener('click', () => {
        addBudgetWindowRow();
    });

    document.getElementById('btn-add-provider').addEventListener('click', () => {
        const form = document.getElementById('provider-form');
        form.reset();
        document.getElementById('provider-id-hidden').value = '';
        document.getElementById('provider-id').value = '';
        document.getElementById('provider-id-group').style.display = 'block';
        document.getElementById('provider-id').disabled = false;
        document.getElementById('provider-status-group').style.display = 'none';
        document.getElementById('provider-modal-title').innerText = "Add Upstream Provider";
        document.getElementById('provider-submit-btn').innerText = "Save Provider";
        modalProvider.classList.add('show');
    });

    const modelTypeSelect = document.getElementById('model-type');
    function toggleModelTypeFields() {
        const isTranscription = modelTypeSelect.value === 'transcription' || modelTypeSelect.value === 'transcript';
        
        const inCostInput = document.getElementById('model-cost-in');
        const outCostInput = document.getElementById('model-cost-out');
        const readCostInput = document.getElementById('model-cost-read');
        const writeCostInput = document.getElementById('model-cost-write');
        const priceMinuteInput = document.getElementById('model-price-minute');
        
        const inCostRow = inCostInput.closest('.form-group-row');
        const readCostRow = readCostInput.closest('.form-group-row');
        const priceMinuteGroup = document.getElementById('model-price-minute-group');
        
        if (inCostRow) inCostRow.style.display = isTranscription ? 'none' : 'flex';
        if (readCostRow) readCostRow.style.display = isTranscription ? 'none' : 'flex';
        if (priceMinuteGroup) priceMinuteGroup.style.display = isTranscription ? 'block' : 'none';
        
        inCostInput.required = !isTranscription;
        outCostInput.required = !isTranscription;
        readCostInput.required = !isTranscription;
        writeCostInput.required = !isTranscription;
        priceMinuteInput.required = isTranscription;
    }
    modelTypeSelect.addEventListener('change', toggleModelTypeFields);

    document.getElementById('btn-add-model').addEventListener('click', () => {
        const form = document.getElementById('model-form');
        form.reset();
        document.getElementById('model-id').value = '';
        document.getElementById('model-type').value = 'llm';
        document.getElementById('model-price-minute').value = '';
        document.getElementById('model-transcribe').checked = false;
        // Discoverability is opt-in: a new model is hidden from the MuhiyaCode
        // picker until an operator explicitly marks it.
        document.getElementById('model-muhiyacode-visible').checked = false;
        document.getElementById('model-supports-vision').checked = false;
        document.getElementById('model-supports-thinking').checked = false;
        document.getElementById('model-supports-audio').checked = false;
        document.getElementById('model-supports-video').checked = false;
        document.getElementById('model-supports-documents').checked = false;
        document.getElementById('model-max-attachment-mb').value = '';
        document.getElementById('model-accepted-mime').value = '';
        toggleModelTypeFields();
        document.getElementById('model-test-btn').style.display = 'none';
        document.getElementById('model-test-result').style.display = 'none';
        document.getElementById('model-status-group').style.display = 'none';
        document.getElementById('model-modal-title').innerText = "Define Model Mapping";
        document.getElementById('model-submit-btn').innerText = "Save Model";
        modalModel.classList.add('show');
    });

    // --- Modal Form Submit Handlers Setup Once ---
    document.getElementById('user-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const id = document.getElementById('user-id').value;
        const name = document.getElementById('user-name').value;
        const email = document.getElementById('user-email').value;
        const plan_id = document.getElementById('user-plan-id').value;
        const status = document.getElementById('user-status').value;

        const payload = { id, name, email, plan_id, status };
        const method = id ? 'PUT' : 'POST';

        mutateJSON('/api/users', method, payload)
            .then(() => { modalUser.classList.remove('show'); loadUsers(); showToast('User saved', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    });

    document.getElementById('key-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const id = document.getElementById('key-id').value;
        const name = document.getElementById('key-name').value;
        const user_id = document.getElementById('key-user-id').value;
        const expiresVal = document.getElementById('key-expires').value;
        const status = document.getElementById('key-status').value || 'active';

        const payload = { id, name, user_id, status };
        if (expiresVal) {
            payload.expires_at = new Date(expiresVal).toISOString();
        }
        const method = id ? 'PUT' : 'POST';

        mutateJSON('/api/keys', method, payload)
            .then((created) => {
                modalKey.classList.remove('show');
                loadKeys();
                if (method === 'POST' && created && created.key) {
                    showGeneratedKey(created.key);
                } else {
                    showToast('Key saved', 'success');
                }
            })
            .catch(err => showToast(err.message, 'error'));
    });

    document.getElementById('plan-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const idInput = document.getElementById('plan-id').value;
        const idVal = document.getElementById('plan-id-val').value;
        const name = document.getElementById('plan-name').value;
        const rpm = parseInt(document.getElementById('plan-rpm').value) || 0;
        const tpm = parseInt(document.getElementById('plan-tpm').value) || 0;

        // Collect budget rows
        const budgetRows = document.querySelectorAll('.plan-budget-row');
        const budgetWindows = [];
        budgetRows.forEach(row => {
            const bId = row.querySelector('.plan-budget-id').value;
            const bName = row.querySelector('.plan-budget-name').value;
            const durationVal = parseInt(row.querySelector('.plan-budget-duration').value) || 0;
            const unit = row.querySelector('.plan-budget-unit').value;
            const budgetUSD = parseFloat(row.querySelector('.plan-budget-usd').value) || 0.0;

            let durationSeconds = durationVal * 3600;
            if (unit === 'days') {
                durationSeconds = durationVal * 86400;
            }

            budgetWindows.push({
                id: bId || undefined,
                name: bName,
                duration_seconds: durationSeconds,
                budget_usd: budgetUSD
            });
        });

        const id = idInput || idVal;
        const payload = {
            id,
            name,
            rpm_limit: rpm,
            tpm_limit: tpm,
            budget_windows: budgetWindows
        };
        const method = idInput ? 'PUT' : 'POST';

        mutateJSON('/api/plans', method, payload)
            .then(() => { modalPlan.classList.remove('show'); loadPlansAndBudgets(); showToast('Plan saved', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    });

    document.getElementById('provider-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const idInput = document.getElementById('provider-id-hidden').value;
        const idVal = document.getElementById('provider-id').value;
        const name = document.getElementById('provider-name').value;
        const api_key = document.getElementById('provider-apikey').value;
        const base_url = document.getElementById('provider-baseurl').value;
        const anthropic_base_url = document.getElementById('provider-anthropic-baseurl').value;
        const status = document.getElementById('provider-status').value || 'active';

        if (!base_url && !anthropic_base_url) {
            alert('Please specify at least one Base URL (OpenAI format or Anthropic format).');
            return;
        }

        const id = idInput || idVal;
        const payload = { id, name, api_key, base_url, anthropic_base_url, status };
        const method = idInput ? 'PUT' : 'POST';

        mutateJSON('/api/providers', method, payload)
            .then(() => { modalProvider.classList.remove('show'); loadProvidersAndModels(); showToast('Provider saved', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    });

    document.getElementById('model-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const id = document.getElementById('model-id').value;
        const name = document.getElementById('model-name').value;
        const provider_id = document.getElementById('model-provider-id').value;
        const target_model = document.getElementById('model-target').value;
        const model_type = document.getElementById('model-type').value || 'llm';
        const price_per_minute = parseFloat(document.getElementById('model-price-minute').value) || 0.0;
        const transcribe = document.getElementById('model-transcribe').checked;
        const muhiyacode_visible = document.getElementById('model-muhiyacode-visible').checked;
        const supports_vision = document.getElementById('model-supports-vision').checked;
        const supports_thinking = document.getElementById('model-supports-thinking').checked;
        const supports_audio = document.getElementById('model-supports-audio').checked;
        const supports_video = document.getElementById('model-supports-video').checked;
        const supports_documents = document.getElementById('model-supports-documents').checked;
        const max_attachment_mb = parseInt(document.getElementById('model-max-attachment-mb').value) || 0;
        const accepted_mime_types = document.getElementById('model-accepted-mime').value.trim();
        const inCost = parseFloat(document.getElementById('model-cost-in').value) || 0;
        const outCost = parseFloat(document.getElementById('model-cost-out').value) || 0;
        const readCost = parseFloat(document.getElementById('model-cost-read').value) || 0;
        const writeCost = parseFloat(document.getElementById('model-cost-write').value) || 0;
        const status = document.getElementById('model-status').value || 'active';
        const routing_tier = document.getElementById('model-routing-tier').value || 'none';

        const display_name = document.getElementById('model-display-name').value || '';
        const owned_by = document.getElementById('model-owned-by').value || '';
        const context_window = parseInt(document.getElementById('model-context-window').value) || 0;
        const max_output_tokens = parseInt(document.getElementById('model-max-output-tokens').value) || 0;
        const description = document.getElementById('model-description').value || '';

        const body = {
            id, name, provider_id, target_model, model_type, price_per_minute, transcribe,
            muhiyacode_visible,
            input_cost_per_million: inCost,
            output_cost_per_million: outCost,
            cache_read_cost_per_million: readCost,
            cache_write_cost_per_million: writeCost,
            status,
            routing_tier,
            display_name,
            owned_by,
            context_window,
            max_output_tokens,
            description,
            supports_vision,
            supports_thinking,
            supports_audio,
            supports_video,
            supports_documents,
            max_attachment_mb,
            accepted_mime_types
        };

        const method = id ? 'PUT' : 'POST';

        mutateJSON('/api/models', method, body)
            .then((res) => {
                modalModel.classList.remove('show');
                loadProvidersAndModels();
                showToast('Model saved', 'success');
                // The server returns non-blocking warnings (e.g. "$0 and active",
                // "provider inactive") — surface each so the operator sees why a
                // model might not route as expected.
                const warnings = (res && res.warnings) || [];
                warnings.forEach((wmsg, i) => setTimeout(() => showToast(wmsg, 'warning'), 400 * (i + 1)));
            })
            .catch(err => showToast(err.message, 'error'));
    });

    // One-time: probe the currently-edited model against its provider.
    document.getElementById('model-test-btn').addEventListener('click', (ev) => {
        const mid = document.getElementById('model-id').value;
        if (mid) window.testModel(mid, ev.currentTarget);
    });

    // --- Inline Plan Budget Window Editor Builder ---
    function addBudgetWindowRow(data = {}) {
        const container = document.getElementById('plan-budgets-container');
        const row = document.createElement('div');
        row.className = 'plan-budget-row';
        row.style = 'display: flex; gap: 0.5rem; align-items: center; margin-bottom: 0.5rem;';
        
        let unit = 'hours';
        let val = 0;
        if (data.duration_seconds) {
            val = data.duration_seconds / 3600;
            if (data.duration_seconds % 86400 === 0) {
                val = data.duration_seconds / 86400;
                unit = 'days';
            }
        }

        row.innerHTML = `
            <input type="hidden" class="plan-budget-id" value="${escapeHtml(data.id || '')}">
            <input type="text" placeholder="Label" class="plan-budget-name" value="${escapeHtml(data.name || '')}" style="flex: 2; font-size: 0.85rem;" required>
            <input type="number" placeholder="Value" class="plan-budget-duration" value="${val || ''}" style="flex: 1; font-size: 0.85rem;" required>
            <select class="plan-budget-unit" style="flex: 1.2; font-size: 0.85rem;" required>
                <option value="hours" ${unit === 'hours' ? 'selected' : ''}>Hours</option>
                <option value="days" ${unit === 'days' ? 'selected' : ''}>Days</option>
            </select>
            <input type="number" step="0.01" placeholder="USD" class="plan-budget-usd" value="${data.budget_usd !== undefined ? data.budget_usd : ''}" style="flex: 1.5; font-size: 0.85rem;" required>
            <button type="button" class="btn-remove-plan-budget" style="background: none; border: none; color: var(--danger); cursor: pointer; padding: 0.25rem 0.5rem; font-size: 1rem;">
                <i class="fa-solid fa-trash"></i>
            </button>
        `;

        row.querySelector('.btn-remove-plan-budget').addEventListener('click', () => {
            row.remove();
        });

        container.appendChild(row);
    }

    // --- Load Tab Data Methods ---

    // Load Dashboard Stats
    function loadDashboardStats() {
        Promise.all([
            fetchJSON('/api/stats'),
            fetchJSON('/api/users'),
            fetchJSON('/api/keys')
        ]).then(([data, users, keys]) => {
            users = users || [];
            keys = keys || [];
            state.users = users;
            state.keys = keys;

            document.getElementById('stat-requests').innerText = data.total_requests.toLocaleString();
            document.getElementById('stat-cost').innerText = '$' + data.total_cost.toFixed(6);
            document.getElementById('stat-tokens').innerText = data.total_tokens.toLocaleString();
            document.getElementById('stat-latency').innerText = Math.round(data.avg_latency) + ' ms';
            document.getElementById('stat-success').innerText = data.success_rate.toFixed(1) + '%';
            
            // Caching stats
            document.getElementById('stat-cache-hit-rate').innerText = data.cache_hit_rate.toFixed(1) + '%';
            document.getElementById('stat-cache-reads').innerText = data.cache_read_tokens.toLocaleString();
            document.getElementById('stat-cache-writes').innerText = data.cache_write_tokens.toLocaleString();

            // 4 New Dashboard stats
            document.getElementById('stat-active-keys').innerText = keys.length.toLocaleString();
            document.getElementById('stat-active-users').innerText = users.length.toLocaleString();
            
            const costPer1K = data.total_requests > 0 ? (data.total_cost / data.total_requests * 1000) : 0;
            document.getElementById('stat-cost-1k').innerText = '$' + costPer1K.toFixed(4);

            const avgTokensReq = data.total_requests > 0 ? Math.round(data.total_tokens / data.total_requests) : 0;
            document.getElementById('stat-avg-tokens-req').innerText = avgTokensReq.toLocaleString();
            
            renderCharts(data);
            renderBudgetWindows(users);
        })
        .catch(err => {
            console.error('Error loading dashboard stats:', err);
            ['stat-requests', 'stat-cost', 'stat-tokens', 'stat-latency', 'stat-success',
             'stat-cache-hit-rate', 'stat-cache-reads', 'stat-cache-writes',
             'stat-active-keys', 'stat-active-users', 'stat-cost-1k', 'stat-avg-tokens-req'].forEach((id) => {
                const el = document.getElementById(id);
                if (el) el.innerText = 'Error';
            });
            const container = document.getElementById('dashboard-budget-windows');
            if (container) renderContainerError(container, err.message);
        });
    }

    function renderCharts(data) {
        const daily = data.daily_stats || [];
        const labels = daily.map(d => d.date);
        const requestCounts = daily.map(d => d.requests);
        const costs = daily.map(d => d.cost);
        const tokens = daily.map(d => d.tokens);

        if (charts.requests) charts.requests.destroy();
        const ctxReq = document.getElementById('requestsChart').getContext('2d');
        charts.requests = new Chart(ctxReq, {
            type: 'line',
            data: {
                labels: labels.length ? labels : ['No Data'],
                datasets: [
                    {
                        label: 'Requests',
                        data: requestCounts.length ? requestCounts : [0],
                        borderColor: '#10b981',
                        backgroundColor: 'rgba(16, 185, 129, 0.05)',
                        borderWidth: 2,
                        tension: 0.2,
                        yAxisID: 'y'
                    },
                    {
                        label: 'Cost (USD)',
                        data: costs.length ? costs : [0],
                        borderColor: '#a855f7',
                        backgroundColor: 'rgba(168, 85, 247, 0.05)',
                        borderWidth: 2,
                        tension: 0.2,
                        yAxisID: 'y1'
                    }
                ]
            },
            options: {
                responsive: true,
                plugins: { legend: { labels: { color: '#fafafa' } } },
                scales: {
                    x: { grid: { color: '#27272a' }, ticks: { color: '#a1a1aa' } },
                    y: { position: 'left', grid: { color: '#27272a' }, ticks: { color: '#a1a1aa' } },
                    y1: { position: 'right', grid: { drawOnChartArea: false }, ticks: { color: '#a1a1aa' } }
                }
            }
        });

        if (charts.tokens) charts.tokens.destroy();
        const ctxTok = document.getElementById('tokensChart').getContext('2d');
        charts.tokens = new Chart(ctxTok, {
            type: 'bar',
            data: {
                labels: labels.length ? labels : ['No Data'],
                datasets: [{
                    label: 'Tokens Used',
                    data: tokens.length ? tokens : [0],
                    backgroundColor: 'rgba(16, 185, 129, 0.2)',
                    borderColor: '#10b981',
                    borderWidth: 1
                }]
            },
            options: {
                responsive: true,
                plugins: { legend: { labels: { color: '#fafafa' } } },
                scales: {
                    x: { grid: { color: '#27272a' }, ticks: { color: '#a1a1aa' } },
                    y: { grid: { color: '#27272a' }, ticks: { color: '#a1a1aa' } }
                }
            }
        });

        const topModels = data.top_models || [];
        if (charts.models) charts.models.destroy();
        const ctxMod = document.getElementById('modelsChart').getContext('2d');
        charts.models = new Chart(ctxMod, {
            type: 'doughnut',
            data: {
                labels: topModels.map(m => m.model).length ? topModels.map(m => m.model) : ['No Requests'],
                datasets: [{
                    data: topModels.map(m => m.count).length ? topModels.map(m => m.count) : [1],
                    backgroundColor: ['#10b981', '#34d399', '#6ee7b7', '#047857', '#065f46'],
                    borderWidth: 0
                }]
            },
            options: {
                responsive: true,
                plugins: { legend: { position: 'bottom', labels: { color: '#fafafa' } } }
            }
        });
    }

    function renderBudgetWindows(users) {
        const container = document.getElementById('dashboard-budget-windows');
        if (!container) return;
        container.innerHTML = '';

        let hasWindows = false;

        users.forEach(u => {
            if (!u.budget_usage || u.budget_usage.length === 0) return;

            hasWindows = true;
            u.budget_usage.forEach(bu => {
                const spent = bu.current_spent;
                const percentage = Math.min((spent / bu.budget_usd) * 100, 100);
                let colorClass = '';
                if (percentage >= 90) colorClass = 'danger';
                else if (percentage >= 70) colorClass = 'warning';

                const card = document.createElement('div');
                card.className = 'budget-progress-card';
                card.innerHTML = `
                    <div class="budget-progress-header">
                        <span class="budget-user-name">${escapeHtml(u.name)}</span>
                        <span class="budget-window-label">${escapeHtml(bu.name)}</span>
                    </div>
                    <div class="budget-progress-bar-container">
                        <div class="budget-progress-bar ${colorClass}" style="width: ${percentage}%"></div>
                    </div>
                    <div class="budget-progress-footer">
                        <span class="budget-spent">$${spent.toFixed(6)} spent</span>
                        <span class="budget-total">limit $${bu.budget_usd.toFixed(2)}</span>
                    </div>
                `;
                container.appendChild(card);
            });
        });

        if (!hasWindows) {
            container.innerHTML = `<div class="text-muted">No budget windows configured for active user plans. Set them up in the Plans tab.</div>`;
        }
    }

    // --- Users CRUD ---
    function loadUsers() {
        const tbody = document.querySelector('#users-table tbody');
        Promise.all([
            fetchJSON('/api/plans'),
            fetchJSON('/api/users')
        ]).then(([plans, users]) => {
            plans = plans || [];
            users = users || [];
            state.plans = plans;
            state.users = users;

            const select = document.getElementById('user-plan-id');
            select.innerHTML = plans.map(p => `<option value="${escapeHtml(p.id)}">${escapeHtml(p.name)}</option>`).join('');

            if (!users || users.length === 0) {
                tbody.innerHTML = `<tr><td colspan="8" class="text-muted text-center" style="text-align: center;">No users registered yet.</td></tr>`;
                return;
            }

            tbody.innerHTML = users.map(u => {
                let budgetsHTML = '';
                if (u.budget_usage && u.budget_usage.length > 0) {
                    budgetsHTML = u.budget_usage.map(bu => {
                        const spent = bu.current_spent;
                        const percentage = Math.min((spent / bu.budget_usd) * 100, 100);
                        let color = 'var(--primary)';
                        if (percentage >= 90) color = 'var(--danger)';
                        else if (percentage >= 70) color = '#f59e0b'; // warning orange

                        let resetStr = 'No Active Load';
                        if (bu.reset_time) {
                            const resetTime = new Date(bu.reset_time);
                            resetStr = resetTime.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) + ' ' + resetTime.toLocaleDateString([], { month: 'short', day: 'numeric' });
                        }

                        return `
                            <div style="margin-bottom: 0.5rem; min-width: 180px;">
                                <div style="display: flex; justify-content: space-between; font-size: 0.75rem; font-weight: 600; margin-bottom: 2px;">
                                    <span style="color:var(--text-muted);">${escapeHtml(bu.name)}</span>
                                    <span>$${spent.toFixed(4)} / $${bu.budget_usd.toFixed(2)}</span>
                                </div>
                                <div style="background: var(--panel-border); height: 6px; border-radius: 3px; overflow: hidden; width: 100%;">
                                    <div style="background: ${color}; width: ${percentage}%; height: 100%; border-radius: 3px; transition: var(--transition);"></div>
                                </div>
                                <div style="font-size: 0.65rem; color: var(--text-muted-dark); margin-top: 1px; text-align: right;">
                                    Reset: ${resetStr}
                                </div>
                            </div>
                        `;
                    }).join('');
                } else {
                    budgetsHTML = '<span class="text-muted" style="font-size:0.75rem;">No limits configured</span>';
                }

                let extraCreditsHTML = '';
                if (u.extra_credits > 0) {
                    const pct = Math.min((u.remaining_extra_credits / u.extra_credits) * 100, 100);
                    extraCreditsHTML = `
                        <div style="min-width: 150px;">
                            <div style="display: flex; justify-content: space-between; font-size: 0.75rem; font-weight: 600; margin-bottom: 2px;">
                                <span style="color:var(--text-muted);"><i class="fa-solid fa-coins" style="color:#fbbf24; margin-right:4px;"></i>Balance</span>
                                <span>${u.remaining_extra_credits.toFixed(2)} / ${u.extra_credits.toFixed(2)}</span>
                            </div>
                            <div style="background: var(--panel-border); height: 6px; border-radius: 3px; overflow: hidden; width: 100%;">
                                <div style="background: #fbbf24; width: ${pct}%; height: 100%; border-radius: 3px; transition: var(--transition);"></div>
                            </div>
                        </div>
                    `;
                } else {
                    extraCreditsHTML = `<span class="text-muted" style="font-size:0.75rem;">0 Credits</span>`;
                }

                return `
                    <tr>
                        <td><strong>${escapeHtml(u.name)}</strong></td>
                        <td><code>${escapeHtml(u.email)}</code></td>
                        <td><span class="badge">${escapeHtml(u.plan_id)}</span></td>
                        <td>
                            <div style="display: flex; flex-direction: column; gap: 0.25rem;">
                                ${budgetsHTML}
                            </div>
                        </td>
                        <td>
                            ${extraCreditsHTML}
                        </td>
                        <td><span class="status-pill ${u.status === 'active' ? 'active' : 'suspended'}">${escapeHtml(u.status)}</span></td>
                        <td>${new Date(u.created_at).toLocaleDateString()}</td>
                        <td>
                            <button class="btn btn-secondary btn-sm" onclick="manageUserTopups('${u.id}')" title="Manage Top-ups"><i class="fa-solid fa-coins"></i> Top-ups</button>
                            <button class="btn btn-secondary btn-sm" onclick="resetUserUsage('${u.id}')" title="Reset current usage now (schedule unchanged)"><i class="fa-solid fa-rotate-left"></i> Reset</button>
                            <button class="btn btn-secondary btn-sm" onclick="editUser('${u.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="deleteUser('${u.id}')"><i class="fa-solid fa-trash"></i> Delete</button>
                        </td>
                    </tr>
                `;
            }).join('');
        }).catch(err => {
            console.error("Error loading users & budgets:", err);
            renderTableError(tbody, 8, err.message);
        });
    }

    window.editUser = (id) => {
        const u = state.users.find(x => x.id === id);
        if (u) {
            document.getElementById('user-id').value = u.id;
            document.getElementById('user-name').value = u.name;
            document.getElementById('user-email').value = u.email;
            document.getElementById('user-plan-id').value = u.plan_id;
            document.getElementById('user-status').value = u.status;
            
            document.getElementById('user-modal-title').innerText = "Edit User";
            modalUser.classList.add('show');
        }
    };

    window.deleteUser = (id) => {
        if (!confirm('Are you sure you want to delete this user? All associated virtual keys will be permanently deleted.')) return;
        mutateJSON(`/api/users?id=${id}`, 'DELETE')
            .then(() => { loadUsers(); showToast('User deleted', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    // B: bonus "gift" reset — zeroes CURRENT in-window usage without moving any
    // scheduled reset time (the floor rises; the schedule stays anchored).
    window.resetUserUsage = (id) => {
        if (!confirm("Reset this user's current usage to zero now? Their scheduled reset time is unchanged.")) return;
        mutateJSON('/api/users/reset-usage', 'POST', { scope: 'user', user_id: id, note: 'admin panel (single user)' })
            .then(() => { loadUsers(); showToast('Usage reset for user', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    window.resetAllUsage = () => {
        if (!confirm('Reset CURRENT usage to zero for ALL users now (a usage gift)? Scheduled reset times are unchanged.')) return;
        mutateJSON('/api/users/reset-usage', 'POST', { scope: 'all', note: 'admin panel (all users)' })
            .then((res) => { loadUsers(); showToast('Usage reset for ' + ((res && res.users_reset) || 'all') + ' user(s)', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    // --- Virtual Keys CRUD ---
    function loadKeys() {
        fetchJSON('/api/users')
            .then(users => {
                users = users || [];
                state.users = users;
                const select = document.getElementById('key-user-id');
                select.innerHTML = users.map(u => `<option value="${escapeHtml(u.id)}">${escapeHtml(u.name)} (${escapeHtml(u.email)})</option>`).join('');
            })
            .catch(err => console.error('Error loading users for key form:', err));

        const tbody = document.querySelector('#keys-table tbody');
        Promise.all([
            fetchJSON('/api/keys'),
            fetchJSON('/api/users')
        ]).then(([keys, users]) => {
            keys = keys || [];
            users = users || [];
            state.keys = keys;
            state.users = users;
            if (!keys || keys.length === 0) {
                tbody.innerHTML = `<tr><td colspan="7" class="text-muted text-center" style="text-align: center;">No virtual keys created yet.</td></tr>`;
                return;
            }
            tbody.innerHTML = keys.map(k => {
                const owner = users.find(u => u.id === k.user_id);
                const ownerName = owner ? owner.name : 'Unknown User';
                const expDate = k.expires_at ? new Date(k.expires_at).toLocaleDateString() : 'Never';
                return `
                    <tr>
                        <td><strong>${escapeHtml(k.name)}</strong></td>
                        <td>
                            <div class="copy-block">
                                <span>${escapeHtml(k.id)}</span>
                                <button class="copy-btn" onclick="copyKey('${k.id}', this)"><i class="fa-regular fa-copy"></i></button>
                            </div>
                        </td>
                        <td><span class="badge" style="background:#27272a;">${escapeHtml(ownerName)}</span></td>
                        <td><span class="status-pill ${k.status === 'active' ? 'active' : 'revoked'}">${escapeHtml(k.status)}</span></td>
                        <td>${expDate}</td>
                        <td>${new Date(k.created_at).toLocaleDateString()}</td>
                        <td>
                            <button class="btn btn-secondary btn-sm" onclick="editKey('${k.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="revokeKey('${k.id}')"><i class="fa-solid fa-ban"></i> Revoke</button>
                        </td>
                    </tr>
                `;
            }).join('');
        }).catch(err => {
            console.error("Error listing keys:", err);
            renderTableError(tbody, 7, err.message);
        });
    }

    window.editKey = (id) => {
        const k = state.keys.find(x => x.id === id);
        if (k) {
            document.getElementById('key-id').value = k.id;
            document.getElementById('key-name').value = k.name;
            document.getElementById('key-user-id').value = k.user_id;
            if (k.expires_at) {
                document.getElementById('key-expires').value = new Date(k.expires_at).toISOString().split('T')[0];
            } else {
                document.getElementById('key-expires').value = '';
            }

            document.getElementById('key-status-group').style.display = 'block';
            document.getElementById('key-status').value = k.status;
            document.getElementById('key-submit-btn').innerText = "Save Key Changes";
            document.getElementById('key-modal-title').innerText = "Edit Virtual Key";
            modalKey.classList.add('show');
        }
    };

    window.revokeKey = (id) => {
        if (!confirm('Are you sure you want to delete this API key? Connection clients will be locked out immediately.')) return;
        mutateJSON(`/api/keys?id=${id}`, 'DELETE')
            .then(() => { loadKeys(); showToast('Key revoked', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    // --- Plans CRUD ---
    function loadPlansAndBudgets() {
        const tbody = document.querySelector('#plans-table tbody');
        fetchJSON('/api/plans')
            .then(plans => {
                plans = plans || [];
                state.plans = plans;
                if (!plans || plans.length === 0) {
                    tbody.innerHTML = `<tr><td colspan="6" class="text-muted text-center" style="text-align: center;">No plans registered.</td></tr>`;
                    return;
                }
                tbody.innerHTML = plans.map(p => {
                    const planBudgets = p.budget_windows || [];
                    const budgetBadges = planBudgets.map(b => `<span class="badge" style="background:rgba(16,185,129,0.05); margin-right:4px;">${escapeHtml(b.name)}: $${b.budget_usd.toFixed(2)}</span>`).join(' ');
                    
                    return `
                        <tr>
                            <td><code>${escapeHtml(p.id)}</code></td>
                            <td><strong>${escapeHtml(p.name)}</strong></td>
                            <td>${p.rpm_limit > 0 ? p.rpm_limit + ' RPM' : '<span class="text-muted">Unlimited</span>'}</td>
                            <td>${p.tpm_limit > 0 ? p.tpm_limit.toLocaleString() + ' TPM' : '<span class="text-muted">Unlimited</span>'}</td>
                            <td>${budgetBadges ? budgetBadges : '<span class="text-muted">No limits configured</span>'}</td>
                            <td>
                                <button class="btn btn-secondary btn-sm" onclick="editPlan('${p.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                                <button class="btn btn-danger btn-sm" onclick="deletePlan('${p.id}')" ${p.id === 'plan-dev' ? 'disabled' : ''}><i class="fa-solid fa-trash"></i> Delete</button>
                            </td>
                        </tr>
                    `;
                }).join('');
            }).catch(err => {
                console.error("Error loading plans:", err);
                renderTableError(tbody, 6, err.message);
            });
    }

    window.editPlan = (id) => {
        const p = state.plans.find(x => x.id === id);
        if (p) {
            document.getElementById('plan-id').value = p.id;
            document.getElementById('plan-id-val').value = p.id;
            document.getElementById('plan-id-group').style.display = 'none'; // Lock plan ID
            document.getElementById('plan-name').value = p.name;
            document.getElementById('plan-rpm').value = p.rpm_limit;
            document.getElementById('plan-tpm').value = p.tpm_limit;

            const container = document.getElementById('plan-budgets-container');
            container.innerHTML = '';
            if (p.budget_windows && p.budget_windows.length > 0) {
                p.budget_windows.forEach(bw => {
                    addBudgetWindowRow(bw);
                });
            }

            document.getElementById('plan-modal-title').innerText = "Edit Plan";
            modalPlan.classList.add('show');
        }
    };

    window.deletePlan = (id) => {
        if (id === 'plan-dev') return alert('Cannot delete default system plans.');
        if (!confirm('Are you sure you want to delete this plan? All budget windows and users under this plan will be affected.')) return;
        mutateJSON(`/api/plans?id=${id}`, 'DELETE')
            .then(() => { loadPlansAndBudgets(); showToast('Plan deleted', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    // --- Providers & Models CRUD ---
    // --- Provider / model connectivity tests + capability coverage ---
    window.testProvider = (id, btn) => {
        const card = btn.closest('.provider-card');
        const out = card ? card.querySelector('.provider-test-result') : null;
        if (out) { out.style.display = 'block'; out.style.color = 'var(--text-muted)'; out.textContent = 'Testing…'; }
        btn.disabled = true;
        mutateJSON('/api/providers/test', 'POST', { id })
            .then(res => {
                if (!out) return;
                out.style.color = res.ok ? '#16a34a' : '#dc2626';
                out.textContent = res.ok
                    ? `✓ Reachable (HTTP ${res.status}${res.model_count ? `, ${res.model_count} models` : ''})`
                    : `✗ ${res.status || ''} ${res.message || 'failed'}`.trim();
            })
            .catch(err => { if (out) { out.style.color = '#dc2626'; out.textContent = '✗ ' + err.message; } })
            .finally(() => { btn.disabled = false; });
    };

    window.testModel = (id, btn) => {
        const out = document.getElementById('model-test-result');
        if (out) { out.style.display = 'block'; out.style.color = 'var(--text-muted)'; out.textContent = 'Testing…'; }
        if (btn) btn.disabled = true;
        mutateJSON('/api/models/test', 'POST', { id })
            .then(res => {
                if (!out) return;
                out.style.color = res.ok ? '#16a34a' : '#dc2626';
                out.textContent = res.ok
                    ? `✓ Model responded (HTTP ${res.status}, ${res.latency_ms}ms)`
                    : `✗ ${res.status || ''} ${res.upstream_message || 'failed'}`.trim();
            })
            .catch(err => { if (out) { out.style.color = '#dc2626'; out.textContent = '✗ ' + err.message; } })
            .finally(() => { if (btn) btn.disabled = false; });
    };

    function loadCoverageBanner() {
        const banner = document.getElementById('coverage-banner');
        if (!banner) return;
        fetchJSON('/api/coverage')
            .then(cov => {
                const warnings = (cov && cov.warnings) || [];
                banner.style.display = 'block';
                if (warnings.length === 0) {
                    banner.innerHTML = `<div style="padding:0.6rem 0.9rem; border-radius:8px; background:rgba(22,163,74,0.12); border:1px solid rgba(22,163,74,0.4); color:#16a34a; font-size:0.82rem;">
                        <i class="fa-solid fa-circle-check"></i> Capability coverage OK — ${cov.active_models} active models · ${cov.active_vision} vision · ${cov.active_audio || 0} audio · ${cov.active_video || 0} video · ${cov.active_documents || 0} docs · ${cov.active_thinking} thinking · ${cov.active_providers} providers.</div>`;
                    return;
                }
                banner.innerHTML = `<div style="padding:0.7rem 0.9rem; border-radius:8px; background:rgba(217,119,6,0.12); border:1px solid rgba(217,119,6,0.45); color:#d97706; font-size:0.82rem;">
                    <div style="font-weight:600; margin-bottom:0.3rem;"><i class="fa-solid fa-triangle-exclamation"></i> Coverage warnings</div>
                    <ul style="margin:0; padding-inline-start:1.1rem;">${warnings.map(wn => `<li>${escapeHtml(wn)}</li>`).join('')}</ul></div>`;
            })
            .catch(() => { banner.style.display = 'none'; });
    }

    function loadGatewayVersion() {
        const el = document.getElementById('gateway-version');
        if (!el) return;
        fetch('/health').then(r => r.json()).then(h => {
            if (h && h.version) el.textContent = `MuhiyaLLM ${h.version} · ${h.migrations || 0} migrations`;
        }).catch(() => {});
    }
    loadGatewayVersion();

    function loadProvidersAndModels() {
        const list = document.getElementById('providers-list');
        fetchJSON('/api/providers')
            .then(providers => {
                providers = providers || [];
                state.providers = providers;
                const select = document.getElementById('model-provider-id');
                select.innerHTML = providers.map(p => `<option value="${escapeHtml(p.id)}">${escapeHtml(p.name)}</option>`).join('');

                if (!providers || providers.length === 0) {
                    list.innerHTML = `<div class="text-muted" style="grid-column: 1/-1; text-align: center;">No providers connected.</div>`;
                    return;
                }
                list.innerHTML = providers.map(p => `
                    <div class="provider-card">
                        <div class="provider-card-header">
                            <span class="provider-title">${escapeHtml(p.name)}</span>
                            <span class="provider-badge ${p.status === 'active' ? 'active' : 'inactive'}">${p.status === 'active' ? 'Active' : 'Inactive'}</span>
                        </div>
                        <div class="provider-meta">
                            ${p.base_url ? `<span><strong>OpenAI URL:</strong> ${escapeHtml(p.base_url)}</span>` : ''}
                            ${p.anthropic_base_url ? `<span><strong>Anthropic URL:</strong> ${escapeHtml(p.anthropic_base_url)}</span>` : ''}
                            <span><strong>Key:</strong> ••••••••••••••••</span>
                        </div>
                        <div class="provider-actions">
                            <button class="btn btn-secondary btn-sm" onclick="testProvider('${p.id}', this)"><i class="fa-solid fa-plug-circle-check"></i> Test</button>
                            <button class="btn btn-secondary btn-sm" onclick="editProvider('${p.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="deleteProvider('${p.id}')"><i class="fa-solid fa-trash"></i> Delete</button>
                        </div>
                        <div class="provider-test-result" style="margin-top: 0.5rem; font-size: 0.78rem; display:none;"></div>
                    </div>
                `).join('');
            })
            .catch(err => {
                console.error('Error loading providers:', err);
                renderContainerError(list, err.message);
            });

        loadCoverageBanner();

        const tbody = document.querySelector('#models-table tbody');
        fetchJSON('/api/models')
            .then(models => {
                models = models || [];
                state.models = models;
                if (!models || models.length === 0) {
                    tbody.innerHTML = `<tr><td colspan="13" class="text-muted text-center" style="text-align: center;">No virtual model mappings configured.</td></tr>`;
                    return;
                }
                tbody.innerHTML = models.map(m => {
                    const isTrans = (m.model_type || 'llm') === 'transcription' || (m.model_type || 'llm') === 'transcript';
                    // Capability badges: the operator sees at a glance whether a model
                    // is vision/thinking-capable, is a $0 (rate-limited) model, or sits
                    // on an inactive provider (so it silently cannot serve traffic).
                    const provider = (state.providers || []).find(p => p.id === m.provider_id);
                    const provInactive = provider && provider.status !== 'active';
                    const caps = [];
                    if (m.supports_vision) caps.push('<span class="badge" style="background:#6d28d9;">Vision</span>');
                    if (m.supports_thinking) caps.push('<span class="badge" style="background:#0369a1;">Thinking</span>');
                    if (m.supports_audio) caps.push('<span class="badge" style="background:#be185d;">Audio</span>');
                    if (m.supports_video) caps.push('<span class="badge" style="background:#9a3412;">Video</span>');
                    if (m.supports_documents) caps.push('<span class="badge" style="background:#15803d;">Docs</span>');
                    if (m.max_attachment_mb > 0) caps.push(`<span class="badge" style="background:#3f3f46;">≤${m.max_attachment_mb}MB</span>`);
                    if (m.muhiyacode_visible) caps.push('<span class="badge" style="background:#0d9488;">MuhiyaCode</span>');
                    if (!isTrans && m.input_cost_per_million === 0 && m.output_cost_per_million === 0) caps.push('<span class="badge" style="background:#3f3f46;">Free</span>');
                    if (provInactive) caps.push('<span class="badge" style="background:#b91c1c;">provider inactive</span>');
                    return `
                    <tr>
                        <td>
                            <div style="font-weight: 600;">${escapeHtml(m.name)}</div>
                            ${m.display_name ? `<div style="font-size: 0.75rem; color: var(--text-muted); margin-top: 1px;">${escapeHtml(m.display_name)}</div>` : ''}
                            ${!isTrans ? `<div style="font-size: 0.68rem; color: var(--text-muted-dark); margin-top: 3px;">Ctx: ${m.context_window ? m.context_window.toLocaleString() : 'N/A'} | Out: ${m.max_output_tokens ? m.max_output_tokens.toLocaleString() : 'N/A'}</div>` : ''}
                            ${caps.length ? `<div style="margin-top:4px; display:flex; gap:3px; flex-wrap:wrap;">${caps.join('')}</div>` : ''}
                        </td>
                        <td><span class="badge" style="background:#27272a;">${escapeHtml(m.provider_id)}</span></td>
                        <td><code>${escapeHtml(m.target_model)}</code></td>
                        <td><span class="badge" style="background:${isTrans ? '#0f766e' : '#1e3a8a'};">${escapeHtml((m.model_type || 'llm').toUpperCase())}</span></td>
                        <td>${m.transcribe ? '<span class="badge" style="background:#b45309;">Yes</span>' : '<span class="text-muted">No</span>'}</td>
                        <td>${isTrans ? `$${(m.price_per_minute || 0).toFixed(4)}` : '-'}</td>
                        <td>${!isTrans ? `$${m.input_cost_per_million.toFixed(4)}` : '-'}</td>
                        <td>${!isTrans ? `$${m.output_cost_per_million.toFixed(4)}` : '-'}</td>
                        <td>${!isTrans ? `$${m.cache_read_cost_per_million.toFixed(4)}` : '-'}</td>
                        <td>${!isTrans ? `$${m.cache_write_cost_per_million.toFixed(4)}` : '-'}</td>
                        <td><span class="tier-badge ${escapeHtml(m.routing_tier || 'none')}" style="font-size: 11px; padding: 2px 6px;">${m.routing_tier ? escapeHtml(m.routing_tier.toUpperCase()) : 'NONE'}</span></td>
                        <td><span class="status-pill ${m.status === 'active' ? 'active' : 'inactive'}">${escapeHtml(m.status)}</span></td>
                        <td>
                            <button class="btn btn-secondary btn-sm" onclick="editModel('${m.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="deleteModel('${m.id}')"><i class="fa-solid fa-trash"></i> Delete</button>
                        </td>
                    </tr>
                `; }).join('');
            })
            .catch(err => {
                console.error('Error loading models:', err);
                renderTableError(tbody, 13, err.message);
            });
    }

    window.editProvider = (id) => {
        const p = state.providers.find(x => x.id === id);
        if (p) {
            document.getElementById('provider-id-hidden').value = p.id;
            document.getElementById('provider-id').value = p.id;
            document.getElementById('provider-id').disabled = true;
            document.getElementById('provider-id-group').style.display = 'none'; // Lock ID
            document.getElementById('provider-name').value = p.name;
            document.getElementById('provider-apikey').value = p.api_key;
            document.getElementById('provider-baseurl').value = p.base_url || '';
            document.getElementById('provider-anthropic-baseurl').value = p.anthropic_base_url || '';

            document.getElementById('provider-status-group').style.display = 'block';
            document.getElementById('provider-status').value = p.status;
            document.getElementById('provider-submit-btn').innerText = "Save Provider Changes";

            document.getElementById('provider-modal-title').innerText = "Edit Provider";
            modalProvider.classList.add('show');
        }
    };

    window.deleteProvider = (id) => {
        if (!confirm('Deleting this provider will break all mappings linked to it. Proceed?')) return;
        mutateJSON(`/api/providers?id=${id}`, 'DELETE')
            .then(() => { loadProvidersAndModels(); showToast('Provider deleted', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    window.editModel = (id) => {
        const m = state.models.find(x => x.id === id);
        if (m) {
            document.getElementById('model-id').value = m.id;
            document.getElementById('model-name').value = m.name;
            document.getElementById('model-provider-id').value = m.provider_id;
            document.getElementById('model-target').value = m.target_model;
            document.getElementById('model-type').value = m.model_type || 'llm';
            document.getElementById('model-price-minute').value = m.price_per_minute || 0.0;
            document.getElementById('model-transcribe').checked = !!m.transcribe;
            document.getElementById('model-muhiyacode-visible').checked = !!m.muhiyacode_visible;
            document.getElementById('model-supports-vision').checked = !!m.supports_vision;
            document.getElementById('model-supports-thinking').checked = !!m.supports_thinking;
            document.getElementById('model-supports-audio').checked = !!m.supports_audio;
            document.getElementById('model-supports-video').checked = !!m.supports_video;
            document.getElementById('model-supports-documents').checked = !!m.supports_documents;
            document.getElementById('model-max-attachment-mb').value = m.max_attachment_mb || '';
            document.getElementById('model-accepted-mime').value = m.accepted_mime_types || '';
            document.getElementById('model-test-btn').style.display = 'block';
            const mtr = document.getElementById('model-test-result');
            if (mtr) { mtr.style.display = 'none'; mtr.textContent = ''; }
            toggleModelTypeFields();
            document.getElementById('model-cost-in').value = m.input_cost_per_million;
            document.getElementById('model-cost-out').value = m.output_cost_per_million;
            document.getElementById('model-cost-read').value = m.cache_read_cost_per_million;
            document.getElementById('model-cost-write').value = m.cache_write_cost_per_million;
            document.getElementById('model-routing-tier').value = m.routing_tier || 'none';

            document.getElementById('model-display-name').value = m.display_name || '';
            document.getElementById('model-owned-by').value = m.owned_by || '';
            document.getElementById('model-context-window').value = m.context_window || '';
            document.getElementById('model-max-output-tokens').value = m.max_output_tokens || '';
            document.getElementById('model-description').value = m.description || '';

            document.getElementById('model-status-group').style.display = 'block';
            document.getElementById('model-status').value = m.status;
            document.getElementById('model-submit-btn').innerText = "Save Model Changes";

            document.getElementById('model-modal-title').innerText = "Edit Model Mapping";
            modalModel.classList.add('show');
        }
    };

    window.deleteModel = (id) => {
        if (!confirm('Are you sure you want to delete this model mapping?')) return;
        mutateJSON(`/api/models?id=${id}`, 'DELETE')
            .then(() => { loadProvidersAndModels(); showToast('Model deleted', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    function loadLogs() {
        Promise.all([
            fetchJSON('/api/logs?limit=100'),
            fetchJSON('/api/users')
        ]).then(([logs, users]) => {
            logs = logs || [];
            users = users || [];
            state.logs = logs;
            state.users = users;

            // Dynamically populate model filters
            const modelFilterSelect = document.getElementById('log-filter-model');
            if (modelFilterSelect) {
                const currentVal = modelFilterSelect.value;
                const models = [...new Set(logs.map(l => l.model_id))].filter(Boolean);
                modelFilterSelect.innerHTML = '<option value="all">All Models</option>' +
                    models.map(m => `<option value="${escapeHtml(m)}">${escapeHtml(m)}</option>`).join('');
                modelFilterSelect.value = currentVal;
            }

            filterAndRenderLogs();
        }).catch(err => {
            console.error("Error loading logs:", err);
            const tbody = document.querySelector('#logs-table tbody');
            renderTableError(tbody, 12, err.message);
        });
    }

    function filterAndRenderLogs() {
        const search = (document.getElementById('log-search')?.value || '').toLowerCase();
        const status = document.getElementById('log-filter-status')?.value || 'all';
        const model = document.getElementById('log-filter-model')?.value || 'all';
        const tbody = document.querySelector('#logs-table tbody');
        if (!tbody) return;

        const filtered = (state.logs || []).filter(l => {
            const ownerUser = (state.users || []).find(u => u.id === l.user_id);
            const ownerName = ownerUser ? ownerUser.name.toLowerCase() : '';
            const app = (l.client_app || '').toLowerCase();
            const err = (l.error_message || '').toLowerCase();
            
            const matchesSearch = 
                l.virtual_key_id.toLowerCase().includes(search) || 
                l.request_path.toLowerCase().includes(search) || 
                l.model_id.toLowerCase().includes(search) ||
                ownerName.includes(search) ||
                app.includes(search) ||
                err.includes(search);
            
            const matchesStatus = 
                status === 'all' || 
                (status === 'success' && l.status_code >= 200 && l.status_code < 300) || 
                (status === 'error' && l.status_code >= 400);
            
            const matchesModel = 
                model === 'all' || 
                l.model_id === model;
            
            return matchesSearch && matchesStatus && matchesModel;
        });

        if (filtered.length === 0) {
            tbody.innerHTML = `<tr><td colspan="12" class="text-muted text-center" style="text-align: center;">No matching gateway request logs found.</td></tr>`;
            return;
        }

        tbody.innerHTML = filtered.map(l => {
            const statusClass = l.status_code >= 200 && l.status_code < 300 ? 'success' : 'error';
            const timeStr = new Date(l.created_at).toLocaleTimeString();
            
            const ownerUser = (state.users || []).find(u => u.id === l.user_id);
            const ownerName = ownerUser ? ownerUser.name : 'Unknown User';

            const costFormatted = '$' + l.cost.toFixed(6);
            // Prompt caches are per-upstream, so a re-route re-reads the whole
            // conversation at full price. Showing the upstream beside the cache
            // numbers is what makes that correlation readable at a glance:
            // a low R: on a row whose upstream differs from the row above it is
            // a placement miss, not a mystery. Absent on direct connections.
            const upstreamText = l.upstream_provider
                ? ` <small style="color:var(--text-muted-dark)">via ${escapeHtml(l.upstream_provider)}</small>`
                : '';
            const tokensText = `${l.input_tokens} / ${l.output_tokens} <small style="color:var(--text-muted-dark)">(R:${l.cache_read_tokens} W:${l.cache_write_tokens})</small>${upstreamText}`;

            const app = escapeHtml(l.client_app || 'API Client');
            const appLower = (l.client_app || 'API Client').toLowerCase();
            let clientAppBadge = '';
            if (appLower.includes('muhiyaachat') || appLower.includes('muhiya chat')) {
                clientAppBadge = `<span class="badge" style="background: rgba(16, 185, 129, 0.15); color: #10b981; border: 1px solid rgba(16, 185, 129, 0.3); text-transform:none;"><i class="fa-solid fa-comment-dots" style="margin-right: 4px;"></i>${app}</span>`;
            } else if (appLower.includes('claude code') || appLower.includes('claude-code') || appLower.includes('claude')) {
                clientAppBadge = `<span class="badge" style="background: rgba(168, 85, 247, 0.1); color: #c084fc; border: 1px solid rgba(168, 85, 247, 0.2); text-transform:none;">${app}</span>`;
            } else if (appLower.includes('curl')) {
                clientAppBadge = `<span class="badge" style="background: rgba(113, 113, 122, 0.1); color: #d4d4d8; border: 1px solid rgba(113, 113, 122, 0.2); text-transform:none;">${app}</span>`;
            } else if (appLower.includes('browser') || appLower.includes('mozilla') || appLower.includes('chrome')) {
                clientAppBadge = `<span class="badge" style="background: rgba(245, 158, 11, 0.1); color: #fbbf24; border: 1px solid rgba(245, 158, 11, 0.2); text-transform:none;">${app}</span>`;
            } else if (appLower.includes('openai')) {
                clientAppBadge = `<span class="badge" style="background: rgba(59, 130, 246, 0.1); color: #60a5fa; border: 1px solid rgba(59, 130, 246, 0.2); text-transform:none;">${app}</span>`;
            } else if (appLower.includes('postman')) {
                clientAppBadge = `<span class="badge" style="background: rgba(236, 72, 153, 0.1); color: #f472b6; border: 1px solid rgba(236, 72, 153, 0.2); text-transform:none;">${app}</span>`;
            } else {
                clientAppBadge = `<span class="badge" style="background: rgba(16, 185, 129, 0.1); color: #34d399; border: 1px solid rgba(16, 185, 129, 0.2); text-transform:none;">${app}</span>`;
            }

            return `
                <tr>
                    <td>${timeStr}</td>
                    <td><small><code>${escapeHtml(l.virtual_key_id)}</code></small></td>
                    <td><strong>${escapeHtml(ownerName)}</strong></td>
                    <td>${clientAppBadge}</td>
                    <td><strong>${escapeHtml(l.model_id)}</strong></td>
                    <td><code>${escapeHtml(l.request_path)}</code></td>
                    <td><span class="status-pill ${statusClass}">${escapeHtml(l.status_code)}</span></td>
                    <td>${tokensText}</td>
                    <td><strong>${escapeHtml(l.latency_ms)} ms</strong></td>
                    <td><strong style="color:#10b981;">${costFormatted}</strong></td>
                    <td><small class="text-muted" style="color:var(--danger); font-size:0.75rem;">${l.error_message ? escapeHtml(l.error_message.substring(0, 30)) + '...' : '-'}</small></td>
                    <td>
                        <button class="btn btn-secondary btn-sm" onclick="showLogDetails('${l.id}')"><i class="fa-solid fa-circle-info"></i> Details</button>
                    </td>
                </tr>
            `;
        }).join('');
    }

    // --- System Settings ---
    function loadSettings() {
        fetchJSON('/api/settings')
            .then(settings => {
                settings = settings || [];
                const nameObj = settings.find(s => s.key === 'gateway_name');
                if (nameObj) document.getElementById('setting-gateway-name').value = nameObj.value;
                const tavilyObj = settings.find(s => s.key === 'tavily_api_key');
                if (tavilyObj) {
                    const el = document.getElementById('setting-tavily-key');
                    el.value = '';
                    el.placeholder = tavilyObj.has_value ? '•••••••• (configured — leave blank to keep)' : 'tvly-...';
                }
            })
            .catch(err => console.error("Error loading settings:", err));

        const form = document.getElementById('settings-form');
        if (form.dataset.bound === 'true') return;
        form.dataset.bound = 'true';
        form.addEventListener('submit', (e) => {
            e.preventDefault();
            const gatewayName = document.getElementById('setting-gateway-name').value;
            const tavilyKey = document.getElementById('setting-tavily-key').value.trim();

            const saves = [
                fetch('/api/settings', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ key: 'gateway_name', value: gatewayName })
                }),
                fetch('/api/settings', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ key: 'tavily_api_key', value: tavilyKey })
                })
            ];

            Promise.all(saves).then((responses) => {
                const failed = responses.find(res => !res.ok);
                if (failed) throw new Error(`Settings request failed with ${failed.status}`);
            })
            .then(() => {
                alert('System settings applied successfully.');
                loadSettings();
            })
            .catch(err => alert('Failed to apply settings: ' + err.message));
        });
    }

    function saveSetting(key, value) {
        return fetch('/api/settings', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ key, value })
            });
    }

    // Copy virtual key identifier helper
    window.copyKey = (text, btn) => {
        navigator.clipboard.writeText(text).then(() => {
            const icon = btn.querySelector('i');
            icon.className = 'fa-solid fa-check';
            btn.style.color = '#10b981';
            setTimeout(() => {
                icon.className = 'fa-regular fa-copy';
                btn.style.color = '';
            }, 2000);
        });
    };

    // --- User Top-ups Management ---
    window.manageUserTopups = (userId) => {
        const u = state.users.find(x => x.id === userId);
        if (!u) return;

        document.getElementById('topups-user-name').innerText = `User: ${u.name}`;
        document.getElementById('topups-user-email').innerText = u.email;
        document.getElementById('topup-user-id').value = u.id;
        document.getElementById('topup-credits').value = '';

        loadUserTopups(userId);
        modalUserTopups.classList.add('show');
    };

    window.loadUserTopups = (userId) => {
        const tbody = document.querySelector('#topups-table tbody');
        tbody.innerHTML = `<tr><td colspan="7" class="text-muted text-center" style="text-align: center;">Loading top-ups...</td></tr>`;

        fetchJSON(`/api/users/topups?user_id=${userId}`)
            .then(topups => {
                topups = topups || [];
                if (topups.length === 0) {
                    tbody.innerHTML = `<tr><td colspan="7" class="text-muted text-center" style="text-align: center;">No top-up logs found for this user.</td></tr>`;
                    return;
                }
                const now = Date.now();
                tbody.innerHTML = topups.map(t => {
                    const pct = Math.min((t.used_credits / t.credits) * 100, 100);
                    // C: expired or deleted top-ups no longer count for the user; badge
                    // them here (admins keep seeing them — the money paths hide them).
                    const isDeleted = !!t.deleted_at;
                    const isExpired = t.expires_at && new Date(t.expires_at).getTime() <= now;
                    let barColor = 'var(--primary)';
                    let statusLabel = 'Active';
                    if (isDeleted) { barColor = 'var(--text-muted-dark)'; statusLabel = 'Deleted'; }
                    else if (isExpired) { barColor = 'var(--text-muted-dark)'; statusLabel = 'Expired'; }
                    else if (pct >= 100) { barColor = 'var(--text-muted-dark)'; statusLabel = 'Consumed'; }
                    else if (pct > 0) { barColor = '#fbbf24'; statusLabel = 'In Use'; }

                    const dateStr = new Date(t.created_at).toLocaleString();
                    const expiresStr = t.expires_at ? new Date(t.expires_at).toLocaleDateString() : '—';
                    // Data attributes + a delegated listener, NOT an inline
                    // onclick built by interpolation. An inline handler is a
                    // script context nested inside an attribute, so the browser
                    // entity-decodes it before the JS is compiled and escapeHtml
                    // cannot secure it. In a plain attribute, escaping is enough.
                    const actions = isDeleted ? '' : `<button class="btn btn-danger btn-sm js-delete-topup" data-topup-id="${escapeHtml(t.id)}" data-user-id="${escapeHtml(userId)}" title="Delete top-up"><i class="fa-solid fa-trash"></i></button>`;
                    return `
                        <tr${(isDeleted || isExpired) ? ' style="opacity:0.55;"' : ''}>
                            <td>${dateStr}</td>
                            <td><code>${escapeHtml(t.id.substring(0, 12))}...</code></td>
                            <td><strong>${t.credits.toFixed(2)}</strong></td>
                            <td><strong>${t.used_credits.toFixed(2)}</strong></td>
                            <td>
                                <div style="display: flex; flex-direction: column; gap: 2px; min-width: 140px;">
                                    <div style="display: flex; justify-content: space-between; font-size: 0.7rem; font-weight: 600;">
                                        <span style="color:var(--text-muted);">${statusLabel}</span>
                                        <span>${pct.toFixed(1)}%</span>
                                    </div>
                                    <div style="background: var(--panel-border); height: 6px; border-radius: 3px; overflow: hidden; width: 100%;">
                                        <div style="background: ${barColor}; width: ${pct}%; height: 100%; border-radius: 3px;"></div>
                                    </div>
                                </div>
                            </td>
                            <td>${expiresStr}</td>
                            <td>${actions}</td>
                        </tr>
                    `;
                }).join('');
            })
            .catch(err => {
                tbody.innerHTML = `<tr><td colspan="7" class="text-danger text-center" style="text-align: center;">⚠ Failed to load: ${escapeHtml(err.message)}</td></tr>`;
            });
    };

    // C: delete a top-up — its remaining credits vanish from the user immediately
    // (soft delete; the row is retained for the admin audit trail).
    window.deleteTopup = (id, userId) => {
        if (!confirm('Delete this top-up? Its remaining credits disappear from the user at once. The row is kept for audit.')) return;
        mutateJSON(`/api/users/topups?id=${encodeURIComponent(id)}`, 'DELETE')
            .then(() => { loadUserTopups(userId); loadUsers(); showToast('Top-up deleted', 'success'); })
            .catch(err => showToast(err.message, 'error'));
    };

    // Delegated handler for the top-up delete buttons. Registered once on the
    // document rather than rebuilt per row, so the markup carries only data.
    document.addEventListener('click', (event) => {
        const button = event.target.closest('.js-delete-topup');
        if (button) window.deleteTopup(button.dataset.topupId, button.dataset.userId);
    });

    // Setup submit listener for top-up form once
    document.getElementById('topup-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const userId = document.getElementById('topup-user-id').value;
        const credits = parseFloat(document.getElementById('topup-credits').value);
        const expiresVal = document.getElementById('topup-expires').value;

        if (!userId || isNaN(credits) || credits <= 0) {
            showToast('Please enter a valid amount of credits.', 'error');
            return;
        }

        const payload = { user_id: userId, credits: credits };
        if (expiresVal) { payload.expires_at = new Date(expiresVal).toISOString(); }

        mutateJSON('/api/users/topups', 'POST', payload)
            .then(() => {
                document.getElementById('topup-credits').value = '';
                document.getElementById('topup-expires').value = '';
                loadUserTopups(userId);
                loadUsers(); // reflect the new balance
                showToast('Top-up added', 'success');
            })
            .catch(err => showToast('Failed to add top-up: ' + err.message, 'error'));
    });

    window.showLogDetails = (id) => {
        const l = state.logs.find(x => x.id === id);
        if (!l) return;

        const ownerUser = state.users.find(u => u.id === l.user_id);
        const ownerName = ownerUser ? ownerUser.name : 'Unknown User';
        const ownerEmail = ownerUser ? ownerUser.email : 'Unknown';

        const statusClass = l.status_code >= 200 && l.status_code < 300 ? 'status-success' : 'status-error';
        const statusText = l.status_code >= 200 && l.status_code < 300 ? 'Success' : 'Error';
        const dateStr = new Date(l.created_at).toLocaleString();

        const inputTokens = l.input_tokens || 0;
        const outputTokens = l.output_tokens || 0;
        const cacheRead = l.cache_read_tokens || 0;
        const cacheWrite = l.cache_write_tokens || 0;
        
        const cacheHitRate = inputTokens > 0 ? ((cacheRead / inputTokens) * 100).toFixed(1) : '0.0';

        // Thinking level badge: "<requested>" or "<requested>><applied>"
        let thinkingHTML = '';
        if (l.thinking_level) {
            const parts = String(l.thinking_level).split('>');
            const label = parts.length > 1 ? `${parts[0]} → ${parts[1]}` : parts[0];
            thinkingHTML = `
                <div class="details-section" style="background: rgba(139, 92, 246, 0.05); border: 1px solid rgba(139, 92, 246, 0.15); border-radius: 8px; padding: 0.6rem 1rem; margin-bottom: 1rem; font-size: 0.85rem;">
                    <span><i class="fa-solid fa-brain" style="margin-right: 6px; color:#8b5cf6;"></i><strong>Thinking:</strong> <code>${escapeHtml(label)}</code></span>
                </div>
            `;
        }

        // Check if this request was routed by the AI Router
        const isRouter = l.requested_model === 'muhiya-ai-router';
        let routerHTML = '';
        if (isRouter) {
            const complexityBadge = escapeHtml(l.complexity ? l.complexity.toUpperCase() : 'UNKNOWN');
            const compClass = escapeHtml(l.complexity || 'simple');
            routerHTML = `
                <div class="details-section router-info" style="background: rgba(16, 185, 129, 0.03); border: 1px solid rgba(16, 185, 129, 0.15); border-radius: 8px; padding: 1rem; margin-bottom: 1rem;">
                    <div style="display: flex; justify-content: space-between; align-items: center;">
                        <span class="badge" style="background: rgba(16, 185, 129, 0.15); color: #10b981; border: 1px solid rgba(16, 185, 129, 0.3); font-weight: bold; font-size: 11px;">
                            <i class="fa-solid fa-route" style="margin-right: 4px;"></i> Routed by Muhiya AI Router
                        </span>
                        <span>Complexity: <span class="tier-badge ${compClass}" style="font-size: 11px; padding: 2px 6px;">${complexityBadge}</span></span>
                    </div>
                    <div style="display: flex; gap: 2rem; margin-top: 0.75rem; font-size: 0.85rem;">
                        <span><strong>Primary Target:</strong> <code>${escapeHtml(l.model_id)}</code></span>
                        <span><strong>Retry Index:</strong> <code>${l.failover_attempts}</code> ${l.failover_attempts > 0 ? `<span class="text-warning" style="color:#fbbf24; font-weight:bold;"><i class="fa-solid fa-triangle-exclamation"></i> Fallback Used</span>` : `<span class="text-success" style="color:#10b981; font-weight:bold;"><i class="fa-solid fa-circle-check"></i> Primary Succeeded</span>`}</span>
                    </div>
                </div>
            `;
        }

        const errorHTML = l.error_message ? `
            <div class="details-section error-info" style="background: rgba(239, 68, 68, 0.05); border: 1px solid rgba(239, 68, 68, 0.15); border-radius: 8px; padding: 1rem; margin-top: 1rem; color: var(--danger);">
                <h4 style="margin-bottom: 0.5rem; font-size: 0.9rem; color:#ef4444;"><i class="fa-solid fa-triangle-exclamation"></i> Upstream Error Log</h4>
                <pre style="white-space: pre-wrap; font-family: var(--font-mono); font-size: 0.75rem; background: rgba(0,0,0,0.2); padding: 0.5rem; border-radius: 4px; overflow-x: auto;">${escapeHtml(l.error_message)}</pre>
            </div>
        ` : '';

        const modalContent = `
            ${routerHTML}
            ${thinkingHTML}

            <div class="details-grid-two-col" style="display: grid; grid-template-columns: repeat(2, 1fr); gap: 1rem;">
                <div class="details-card" style="background: rgba(255,255,255,0.02); border: 1px solid var(--panel-border); border-radius: 8px; padding: 1rem;">
                    <h4 style="margin-bottom: 0.75rem; border-bottom: 1px solid var(--panel-border); padding-bottom: 0.25rem;">Metadata</h4>
                    <table class="details-subtable" style="width:100%; font-size: 0.8rem; line-height: 1.6;">
                        <tr><td style="color:var(--text-muted); width:100px;">Log ID:</td><td><small><code>${escapeHtml(l.id)}</code></small></td></tr>
                        <tr><td style="color:var(--text-muted);">Timestamp:</td><td>${dateStr}</td></tr>
                        <tr><td style="color:var(--text-muted);">Virtual Key:</td><td><small><code>${escapeHtml(l.virtual_key_id.substring(0,18))}...</code></small></td></tr>
                        <tr><td style="color:var(--text-muted);">User:</td><td><strong>${escapeHtml(ownerName)}</strong></td></tr>
                        <tr><td style="color:var(--text-muted);">Client App:</td><td><code>${escapeHtml(l.client_app || 'API Client')}</code></td></tr>
                        <tr><td style="color:var(--text-muted);">Path:</td><td><code>${escapeHtml(l.request_path)}</code></td></tr>
                    </table>
                </div>
                
                <div class="details-card" style="background: rgba(255,255,255,0.02); border: 1px solid var(--panel-border); border-radius: 8px; padding: 1rem;">
                    <h4 style="margin-bottom: 0.75rem; border-bottom: 1px solid var(--panel-border); padding-bottom: 0.25rem;">Execution</h4>
                    <table class="details-subtable" style="width:100%; font-size: 0.8rem; line-height: 1.6;">
                        <tr><td style="color:var(--text-muted); width:100px;">HTTP Status:</td><td><span class="status-indicator-badge ${statusClass}" style="padding: 2px 6px; border-radius:4px; font-size: 10px;">${l.status_code} ${statusText}</span></td></tr>
                        <tr><td style="color:var(--text-muted);">Latency:</td><td><strong>${l.latency_ms} ms</strong></td></tr>
                        <tr><td style="color:var(--text-muted);">Routed Model:</td><td><code>${escapeHtml(l.model_id)}</code></td></tr>
                        <tr><td style="color:var(--text-muted);">Precise Cost:</td><td><strong style="color:#10b981; font-size:1rem;">$${l.cost.toFixed(6)}</strong></td></tr>
                        <tr><td style="color:var(--text-muted);">Cache Hit Rate:</td><td><strong style="color:#3b82f6;">${cacheHitRate}%</strong></td></tr>
                    </table>
                </div>
                
                <div class="details-card" style="grid-column: 1 / -1; background: rgba(255,255,255,0.02); border: 1px solid var(--panel-border); border-radius: 8px; padding: 1rem;">
                    <h4 style="margin-bottom: 0.75rem; border-bottom: 1px solid var(--panel-border); padding-bottom: 0.25rem;">Token Consumption</h4>
                    <div style="display: flex; justify-content: space-around; text-align: center; margin-top: 0.5rem;">
                        <div>
                            <span style="font-size: 0.75rem; color: var(--text-muted);">Input (Prompt)</span>
                            <div style="font-size: 1.25rem; font-weight: bold; font-family: var(--font-mono);">${inputTokens}</div>
                        </div>
                        <div>
                            <span style="font-size: 0.75rem; color: var(--text-muted);">Output (Completion)</span>
                            <div style="font-size: 1.25rem; font-weight: bold; font-family: var(--font-mono);">${outputTokens}</div>
                        </div>
                        <div>
                            <span style="font-size: 0.75rem; color: var(--text-muted);">Cache Reads (Hits)</span>
                            <div style="font-size: 1.25rem; font-weight: bold; font-family: var(--font-mono); color:#10b981;">${cacheRead}</div>
                        </div>
                        <div>
                            <span style="font-size: 0.75rem; color: var(--text-muted);">Cache Writes (Misses)</span>
                            <div style="font-size: 1.25rem; font-weight: bold; font-family: var(--font-mono); color:#fbbf24;">${cacheWrite}</div>
                        </div>
                    </div>
                </div>
            </div>
            
            ${errorHTML}
        `;

        document.getElementById('log-details-content').innerHTML = modalContent;
        document.getElementById('modal-log-details').classList.add('show');
    };

    function loadRouterData() {
        const simpleListEl = document.getElementById('tier-list-simple');
        const mediumListEl = document.getElementById('tier-list-medium');
        const hardListEl = document.getElementById('tier-list-hard');

        // Fetch active models to populate routing tier columns dynamically
        fetchJSON('/api/models')
            .then(models => {
                models = models || [];
                state.models = models;

                const simpleList = document.getElementById('tier-list-simple');
                const mediumList = document.getElementById('tier-list-medium');
                const hardList = document.getElementById('tier-list-hard');
                
                simpleList.innerHTML = '';
                mediumList.innerHTML = '';
                hardList.innerHTML = '';
                
                let simpleCount = 0;
                let mediumCount = 0;
                let hardCount = 0;
                
                // Sort models by price (cheapest first) to match routing cost optimization!
                const activeModels = models.filter(m => m.status === 'active');
                activeModels.sort((a, b) => {
                    const costA = a.input_cost_per_million + a.output_cost_per_million;
                    const costB = b.input_cost_per_million + b.output_cost_per_million;
                    return costA - costB;
                });
                
                activeModels.forEach(m => {
                    const costUSD = m.input_cost_per_million + m.output_cost_per_million;
                    const card = document.createElement('div');
                    card.className = 'tier-model-item';
                    card.style = 'background: rgba(255,255,255,0.02); border: 1px solid var(--panel-border); border-radius: 8px; padding: 0.75rem; margin-bottom: 0.5rem;';
                    card.innerHTML = `
                        <div style="display: flex; justify-content: space-between; align-items: center; font-weight: 600; font-size: 0.85rem;">
                            <span>${escapeHtml(m.name)}</span>
                            <span class="badge" style="background:#27272a; font-size:9px; padding: 2px 4px; border-radius: 4px; text-transform:none;">${escapeHtml(m.provider_id)}</span>
                        </div>
                        <div style="display: flex; justify-content: space-between; margin-top: 0.4rem; font-size: 0.75rem; color: var(--text-muted);">
                            <span>In: $${m.input_cost_per_million.toFixed(2)}/1M</span>
                            <span>Out: $${m.output_cost_per_million.toFixed(2)}/1M</span>
                        </div>
                        <div style="font-size: 0.68rem; color: var(--text-muted-dark); margin-top: 4px; border-top: 1px solid rgba(255,255,255,0.04); padding-top: 3px;">
                            Target: <code>${escapeHtml(m.target_model)}</code>
                        </div>
                    `;
                    
                    if (m.routing_tier === 'simple') {
                        simpleList.appendChild(card);
                        simpleCount++;
                    } else if (m.routing_tier === 'medium') {
                        mediumList.appendChild(card);
                        mediumCount++;
                    } else if (m.routing_tier === 'hard') {
                        hardList.appendChild(card);
                        hardCount++;
                    }
                });
                
                if (simpleCount === 0) {
                    simpleList.innerHTML = '<div class="text-muted text-center" style="padding: 1.5rem; font-size:0.8rem; background:rgba(255,255,255,0.01); border: 1px dashed var(--panel-border); border-radius:8px;">No models assigned to Simple.</div>';
                }
                if (mediumCount === 0) {
                    mediumList.innerHTML = '<div class="text-muted text-center" style="padding: 1.5rem; font-size:0.8rem; background:rgba(255,255,255,0.01); border: 1px dashed var(--panel-border); border-radius:8px;">No models assigned to Medium.</div>';
                }
                if (hardCount === 0) {
                    hardList.innerHTML = '<div class="text-muted text-center" style="padding: 1.5rem; font-size:0.8rem; background:rgba(255,255,255,0.01); border: 1px dashed var(--panel-border); border-radius:8px;">No models assigned to Hard.</div>';
                }
            })
            .catch(err => {
                console.error('Failed to load router models:', err);
                renderContainerError(simpleListEl, err.message);
                renderContainerError(mediumListEl, err.message);
                renderContainerError(hardListEl, err.message);
            });

        // Fetch logs to compute statistics and savings
        fetchJSON('/api/logs?limit=1000')
            .then(logs => {
                logs = logs || [];
                const routerLogs = logs.filter(log => log.requested_model === 'muhiya-ai-router');
                const totalRequests = routerLogs.length;
                
                let successCount = 0;
                let failoverCount = 0;
                let totalSavings = 0.0;
                
                routerLogs.forEach(log => {
                    const isSuccess = log.status_code >= 200 && log.status_code < 300;
                    if (isSuccess) {
                        successCount++;
                        
                        // Compute estimated savings:
                        // Savings = (Cost if routed to Claude 3.5 Sonnet) - (Actual Cost)
                        // Claude 3.5 Sonnet costs $3.00/1M input and $15.00/1M output
                        const inputTokens = log.input_tokens || 0;
                        const outputTokens = log.output_tokens || 0;
                        const claudeCost = (inputTokens * 3.00 / 1000000) + (outputTokens * 15.00 / 1000000);
                        const savings = claudeCost - log.cost;
                        if (savings > 0) {
                            totalSavings += savings;
                        }
                    }
                    
                    if (isSuccess && log.failover_attempts > 0) {
                        failoverCount++;
                    }
                });

                const successRate = totalRequests > 0 ? (successCount / totalRequests * 100) : 100.0;
                
                document.getElementById('router-stat-requests').innerText = totalRequests.toLocaleString();
                document.getElementById('router-stat-savings').innerText = `$${totalSavings.toFixed(4)}`;
                document.getElementById('router-stat-success').innerText = `${successRate.toFixed(1)}%`;
                document.getElementById('router-stat-failovers').innerText = failoverCount.toLocaleString();
            })
            .catch(err => {
                console.error('Failed to load router logs:', err);
                ['router-stat-requests', 'router-stat-savings', 'router-stat-success', 'router-stat-failovers'].forEach((id) => {
                    const el = document.getElementById(id);
                    if (el) el.innerText = 'Error';
                });
            });
    }

    // Show the gateway's real reachable origin instead of a hardcoded
    // "localhost:8090" — that string was static HTML and stayed wrong on
    // every non-local deployment (e.g. the production elest.io host).
    const openaiUrlEl = document.getElementById('api-openai-url');
    const anthropicUrlEl = document.getElementById('api-anthropic-url');
    if (openaiUrlEl) openaiUrlEl.innerText = `${window.location.origin}/v1`;
    if (anthropicUrlEl) anthropicUrlEl.innerText = `${window.location.origin}/v1/messages`;

    // Load initial tab data
    loadTabData('dashboard');

    // Setup request log filters event listeners
    document.getElementById('log-search')?.addEventListener('input', filterAndRenderLogs);
    document.getElementById('log-filter-status')?.addEventListener('change', filterAndRenderLogs);
    document.getElementById('log-filter-model')?.addEventListener('change', filterAndRenderLogs);

    // Real-time auto-refresh: polls active tab stats/logs every 5 seconds
    setInterval(() => {
        const activeTab = document.querySelector('.nav-item.active');
        if (!activeTab) return;
        const tab = activeTab.getAttribute('data-tab');
        if (tab === 'dashboard') {
            loadDashboardStats();
        } else if (tab === 'logs') {
            loadLogs();
        }
    }, 5000);
});
