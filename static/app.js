document.addEventListener('DOMContentLoaded', () => {
    let charts = {};

    // Global State Cache for instant modal loading
    const state = {
        users: [],
        keys: [],
        plans: [],
        providers: [],
        models: []
    };

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

    document.getElementById('btn-add-model').addEventListener('click', () => {
        const form = document.getElementById('model-form');
        form.reset();
        document.getElementById('model-id').value = '';
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

        fetch('/api/users', {
            method: method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(res => res.json())
        .then(() => {
            modalUser.classList.remove('show');
            loadUsers();
        })
        .catch(err => console.error(err));
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

        fetch('/api/keys', {
            method: method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(res => res.json())
        .then(() => {
            modalKey.classList.remove('show');
            loadKeys();
        })
        .catch(err => console.error(err));
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

        fetch('/api/plans', {
            method: method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(res => res.json())
        .then(() => {
            modalPlan.classList.remove('show');
            loadPlansAndBudgets();
        })
        .catch(err => console.error(err));
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

        fetch('/api/providers', {
            method: method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(res => res.json())
        .then(() => {
            modalProvider.classList.remove('show');
            loadProvidersAndModels();
        })
        .catch(err => console.error(err));
    });

    document.getElementById('model-form').addEventListener('submit', (e) => {
        e.preventDefault();
        const id = document.getElementById('model-id').value;
        const name = document.getElementById('model-name').value;
        const provider_id = document.getElementById('model-provider-id').value;
        const target_model = document.getElementById('model-target').value;
        const inCost = parseFloat(document.getElementById('model-cost-in').value) || 0;
        const outCost = parseFloat(document.getElementById('model-cost-out').value) || 0;
        const readCost = parseFloat(document.getElementById('model-cost-read').value) || 0;
        const writeCost = parseFloat(document.getElementById('model-cost-write').value) || 0;
        const status = document.getElementById('model-status').value || 'active';

        const payload = {
            id, name, provider_id, target_model,
            input_cost_per_million: inCost,
            output_cost_per_million: outCost,
            cache_read_cost_per_million: readCost,
            cache_write_cost_per_million: writeCost,
            status
        };
        const method = id ? 'PUT' : 'POST';

        fetch('/api/models', {
            method: method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(res => res.json())
        .then(() => {
            modalModel.classList.remove('show');
            loadProvidersAndModels();
        })
        .catch(err => console.error(err));
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
            <input type="hidden" class="plan-budget-id" value="${data.id || ''}">
            <input type="text" placeholder="Label" class="plan-budget-name" value="${data.name || ''}" style="flex: 2; font-size: 0.85rem;" required>
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
        fetch('/api/stats')
            .then(res => res.json())
            .then(data => {
                document.getElementById('stat-requests').innerText = data.total_requests.toLocaleString();
                document.getElementById('stat-cost').innerText = '$' + data.total_cost.toFixed(6);
                document.getElementById('stat-tokens').innerText = data.total_tokens.toLocaleString();
                document.getElementById('stat-latency').innerText = Math.round(data.avg_latency) + ' ms';
                document.getElementById('stat-success').innerText = data.success_rate.toFixed(1) + '%';
                
                // Caching stats
                document.getElementById('stat-cache-hit-rate').innerText = data.cache_hit_rate.toFixed(1) + '%';
                document.getElementById('stat-cache-reads').innerText = data.cache_read_tokens.toLocaleString();
                document.getElementById('stat-cache-writes').innerText = data.cache_write_tokens.toLocaleString();
                
                renderCharts(data);
                loadBudgetWindowsDashboard();
            })
            .catch(err => console.error('Error loading dashboard stats:', err));
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

    function loadBudgetWindowsDashboard() {
        fetch('/api/users')
            .then(res => res.json())
            .then(users => {
                const container = document.getElementById('dashboard-budget-windows');
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
                                <span class="budget-user-name">${u.name}</span>
                                <span class="budget-window-label">${bu.name}</span>
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
            })
            .catch(err => console.error("Error loading dashboard budgets:", err));
    }

    // --- Users CRUD ---
    function loadUsers() {
        Promise.all([
            fetch('/api/plans').then(res => res.json()),
            fetch('/api/users').then(res => res.json())
        ]).then(([plans, users]) => {
            state.plans = plans;
            state.users = users;

            const select = document.getElementById('user-plan-id');
            select.innerHTML = plans.map(p => `<option value="${p.id}">${p.name}</option>`).join('');

            const tbody = document.querySelector('#users-table tbody');
            if (!users || users.length === 0) {
                tbody.innerHTML = `<tr><td colspan="7" class="text-muted text-center" style="text-align: center;">No users registered yet.</td></tr>`;
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
                                    <span style="color:var(--text-muted);">${bu.name}</span>
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

                return `
                    <tr>
                        <td><strong>${u.name}</strong></td>
                        <td><code>${u.email}</code></td>
                        <td><span class="badge">${u.plan_id}</span></td>
                        <td>
                            <div style="display: flex; flex-direction: column; gap: 0.25rem;">
                                ${budgetsHTML}
                            </div>
                        </td>
                        <td><span class="status-pill ${u.status === 'active' ? 'active' : 'suspended'}">${u.status}</span></td>
                        <td>${new Date(u.created_at).toLocaleDateString()}</td>
                        <td>
                            <button class="btn btn-secondary btn-sm" onclick="editUser('${u.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="deleteUser('${u.id}')"><i class="fa-solid fa-trash"></i> Delete</button>
                        </td>
                    </tr>
                `;
            }).join('');
        }).catch(err => console.error("Error loading users & budgets:", err));
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
        fetch(`/api/users?id=${id}`, { method: 'DELETE' })
            .then(() => loadUsers())
            .catch(err => console.error(err));
    };

    // --- Virtual Keys CRUD ---
    function loadKeys() {
        fetch('/api/users')
            .then(res => res.json())
            .then(users => {
                state.users = users;
                const select = document.getElementById('key-user-id');
                select.innerHTML = users.map(u => `<option value="${u.id}">${u.name} (${u.email})</option>`).join('');
            });

        Promise.all([
            fetch('/api/keys').then(res => res.json()),
            fetch('/api/users').then(res => res.json())
        ]).then(([keys, users]) => {
            state.keys = keys;
            state.users = users;
            const tbody = document.querySelector('#keys-table tbody');
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
                        <td><strong>${k.name}</strong></td>
                        <td>
                            <div class="copy-block">
                                <span>${k.id}</span>
                                <button class="copy-btn" onclick="copyKey('${k.id}', this)"><i class="fa-regular fa-copy"></i></button>
                            </div>
                        </td>
                        <td><span class="badge" style="background:#27272a;">${ownerName}</span></td>
                        <td><span class="status-pill ${k.status === 'active' ? 'active' : 'revoked'}">${k.status}</span></td>
                        <td>${expDate}</td>
                        <td>${new Date(k.created_at).toLocaleDateString()}</td>
                        <td>
                            <button class="btn btn-secondary btn-sm" onclick="editKey('${k.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="revokeKey('${k.id}')"><i class="fa-solid fa-ban"></i> Revoke</button>
                        </td>
                    </tr>
                `;
            }).join('');
        }).catch(err => console.error("Error listing keys:", err));
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
        fetch(`/api/keys?id=${id}`, { method: 'DELETE' })
            .then(() => loadKeys())
            .catch(err => console.error(err));
    };

    // --- Plans CRUD ---
    function loadPlansAndBudgets() {
        fetch('/api/plans')
            .then(res => res.json())
            .then(plans => {
                state.plans = plans;
                const tbody = document.querySelector('#plans-table tbody');
                if (!plans || plans.length === 0) {
                    tbody.innerHTML = `<tr><td colspan="6" class="text-muted text-center" style="text-align: center;">No plans registered.</td></tr>`;
                    return;
                }
                tbody.innerHTML = plans.map(p => {
                    const planBudgets = p.budget_windows || [];
                    const budgetBadges = planBudgets.map(b => `<span class="badge" style="background:rgba(16,185,129,0.05); margin-right:4px;">${b.name}: $${b.budget_usd.toFixed(2)}</span>`).join(' ');
                    
                    return `
                        <tr>
                            <td><code>${p.id}</code></td>
                            <td><strong>${p.name}</strong></td>
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
            }).catch(err => console.error("Error loading plans:", err));
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
        fetch(`/api/plans?id=${id}`, { method: 'DELETE' })
            .then(() => loadPlansAndBudgets())
            .catch(err => console.error(err));
    };

    // --- Providers & Models CRUD ---
    function loadProvidersAndModels() {
        fetch('/api/providers')
            .then(res => res.json())
            .then(providers => {
                state.providers = providers;
                const select = document.getElementById('model-provider-id');
                select.innerHTML = providers.map(p => `<option value="${p.id}">${p.name}</option>`).join('');

                const list = document.getElementById('providers-list');
                if (!providers || providers.length === 0) {
                    list.innerHTML = `<div class="text-muted" style="grid-column: 1/-1; text-align: center;">No providers connected.</div>`;
                    return;
                }
                list.innerHTML = providers.map(p => `
                    <div class="provider-card">
                        <div class="provider-card-header">
                            <span class="provider-title">${p.name}</span>
                            <span class="provider-badge ${p.status === 'active' ? 'active' : 'inactive'}">${p.status === 'active' ? 'Active' : 'Inactive'}</span>
                        </div>
                        <div class="provider-meta">
                            ${p.base_url ? `<span><strong>OpenAI URL:</strong> ${p.base_url}</span>` : ''}
                            ${p.anthropic_base_url ? `<span><strong>Anthropic URL:</strong> ${p.anthropic_base_url}</span>` : ''}
                            <span><strong>Key:</strong> ••••••••••••••••</span>
                        </div>
                        <div class="provider-actions">
                            <button class="btn btn-secondary btn-sm" onclick="editProvider('${p.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="deleteProvider('${p.id}')"><i class="fa-solid fa-trash"></i> Delete</button>
                        </div>
                    </div>
                `).join('');
            });

        fetch('/api/models')
            .then(res => res.json())
            .then(models => {
                state.models = models;
                const tbody = document.querySelector('#models-table tbody');
                if (!models || models.length === 0) {
                    tbody.innerHTML = `<tr><td colspan="9" class="text-muted text-center" style="text-align: center;">No virtual model mappings configured.</td></tr>`;
                    return;
                }
                tbody.innerHTML = models.map(m => `
                    <tr>
                        <td><strong>${m.name}</strong></td>
                        <td><span class="badge" style="background:#27272a;">${m.provider_id}</span></td>
                        <td><code>${m.target_model}</code></td>
                        <td>$${m.input_cost_per_million.toFixed(4)}</td>
                        <td>$${m.output_cost_per_million.toFixed(4)}</td>
                        <td>$${m.cache_read_cost_per_million.toFixed(4)}</td>
                        <td>$${m.cache_write_cost_per_million.toFixed(4)}</td>
                        <td><span class="status-pill ${m.status === 'active' ? 'active' : 'inactive'}">${m.status}</span></td>
                        <td>
                            <button class="btn btn-secondary btn-sm" onclick="editModel('${m.id}')"><i class="fa-solid fa-pen"></i> Edit</button>
                            <button class="btn btn-danger btn-sm" onclick="deleteModel('${m.id}')"><i class="fa-solid fa-trash"></i> Delete</button>
                        </td>
                    </tr>
                `).join('');
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
        fetch(`/api/providers?id=${id}`, { method: 'DELETE' })
            .then(() => loadProvidersAndModels())
            .catch(err => console.error(err));
    };

    window.editModel = (id) => {
        const m = state.models.find(x => x.id === id);
        if (m) {
            document.getElementById('model-id').value = m.id;
            document.getElementById('model-name').value = m.name;
            document.getElementById('model-provider-id').value = m.provider_id;
            document.getElementById('model-target').value = m.target_model;
            document.getElementById('model-cost-in').value = m.input_cost_per_million;
            document.getElementById('model-cost-out').value = m.output_cost_per_million;
            document.getElementById('model-cost-read').value = m.cache_read_cost_per_million;
            document.getElementById('model-cost-write').value = m.cache_write_cost_per_million;

            document.getElementById('model-status-group').style.display = 'block';
            document.getElementById('model-status').value = m.status;
            document.getElementById('model-submit-btn').innerText = "Save Model Changes";

            document.getElementById('model-modal-title').innerText = "Edit Model Mapping";
            modalModel.classList.add('show');
        }
    };

    window.deleteModel = (id) => {
        if (!confirm('Are you sure you want to delete this model mapping?')) return;
        fetch(`/api/models?id=${id}`, { method: 'DELETE' })
            .then(() => loadProvidersAndModels())
            .catch(err => console.error(err));
    };

    // --- Request Logs ---
    function loadLogs() {
        Promise.all([
            fetch('/api/logs?limit=100').then(res => res.json()),
            fetch('/api/users').then(res => res.json())
        ]).then(([logs, users]) => {
            const tbody = document.querySelector('#logs-table tbody');
            if (!logs || logs.length === 0) {
                tbody.innerHTML = `<tr><td colspan="11" class="text-muted text-center" style="text-align: center;">No gateway request logs audited yet.</td></tr>`;
                return;
            }
            tbody.innerHTML = logs.map(l => {
                const statusClass = l.status_code >= 200 && l.status_code < 300 ? 'success' : 'error';
                const timeStr = new Date(l.created_at).toLocaleTimeString();
                
                const ownerUser = users.find(u => u.id === l.user_id);
                const ownerName = ownerUser ? ownerUser.name : 'Unknown User';

                // Format cost to 6 decimals
                const costFormatted = '$' + l.cost.toFixed(6);

                // Display token breakdown
                const tokensText = `${l.input_tokens} / ${l.output_tokens} <small style="color:var(--text-muted-dark)">(R:${l.cache_read_tokens} W:${l.cache_write_tokens})</small>`;

                // Dynamic client app badge
                const app = l.client_app || 'API Client';
                const appLower = app.toLowerCase();
                let clientAppBadge = '';
                if (appLower.includes('claude code') || appLower.includes('claude-code') || appLower.includes('claude')) {
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
                        <td><small><code>${l.virtual_key_id}</code></small></td>
                        <td><strong>${ownerName}</strong></td>
                        <td>${clientAppBadge}</td>
                        <td><strong>${l.model_id}</strong></td>
                        <td><code>${l.request_path}</code></td>
                        <td><span class="status-pill ${statusClass}">${l.status_code}</span></td>
                        <td>${tokensText}</td>
                        <td><strong>${l.latency_ms} ms</strong></td>
                        <td><strong style="color:#10b981;">${costFormatted}</strong></td>
                        <td><small class="text-muted" style="color:var(--danger); font-size:0.75rem;">${l.error_message ? l.error_message.substring(0, 30) + '...' : '-'}</small></td>
                    </tr>
                `;
            }).join('');
        }).catch(err => console.error("Error loading logs:", err));
    }

    // --- System Settings ---
    function loadSettings() {
        fetch('/api/settings')
            .then(res => res.json())
            .then(settings => {
                const nameObj = settings.find(s => s.key === 'gateway_name');
                if (nameObj) document.getElementById('setting-gateway-name').value = nameObj.value;
            })
            .catch(err => console.error("Error loading settings:", err));

        // Submit listener
        const form = document.getElementById('settings-form');
        form.addEventListener('submit', (e) => {
            e.preventDefault();
            const gatewayName = document.getElementById('setting-gateway-name').value;

            fetch('/api/settings', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ key: 'gateway_name', value: gatewayName })
            })
            .then(() => {
                alert('System settings applied successfully!');
                loadSettings();
            })
            .catch(err => alert('Failed to apply settings: ' + err));
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

    // Load initial tab data
    loadTabData('dashboard');
});
