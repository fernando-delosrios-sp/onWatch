(function () {
    'use strict';

    const metaBase = document.querySelector('meta[name="base-path"]');
    const BASE = metaBase ? metaBase.getAttribute('content') || '' : '';

    let state = {
        pools: [],
        models: [],
        excluded_models: [],
        recent_feedback: [],
        probe_settings: {},
        route: null
    };

    function esc(v) {
        if (v === null || v === undefined) return '';
        return String(v)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    function showToast(msg, isError) {
        const el = document.getElementById('toast');
        if (!el) return;
        el.textContent = msg;
        el.style.background = isError ? 'var(--status-danger)' : 'var(--text-primary)';
        el.classList.add('visible');
        clearTimeout(showToast._timer);
        showToast._timer = setTimeout(function () {
            el.classList.remove('visible');
        }, 3200);
    }

    async function apiFetch(path, opts) {
        const options = opts || {};
        const headers = Object.assign({ 'X-Requested-With': 'XMLHttpRequest' }, options.headers || {});
        if (options.body && typeof options.body === 'string' && !headers['Content-Type']) {
            headers['Content-Type'] = 'application/json';
        }
        const res = await fetch(BASE + path, Object.assign({}, options, { headers: headers }));
        const ct = res.headers.get('content-type') || '';
        const text = await res.text();
        if (!res.ok) {
            let errMsg = text;
            if (ct.includes('application/json')) {
                try {
                    const parsed = JSON.parse(text);
                    if (parsed && parsed.error) errMsg = parsed.error;
                } catch (_) {}
            }
            throw new Error(errMsg || ('HTTP ' + res.status));
        }
        if (ct.includes('application/json')) {
            return JSON.parse(text);
        }
        return text;
    }

    function initThemeToggle() {
        const btn = document.getElementById('theme-toggle');
        if (!btn) return;
        btn.addEventListener('click', function () {
            const cur = document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
            const next = cur === 'dark' ? 'light' : 'dark';
            document.documentElement.setAttribute('data-theme', next);
            try { localStorage.setItem('theme', next); } catch (_) {}
        });
    }

    function initTabs() {
        const tabs = document.querySelectorAll('.routing-tab');
        tabs.forEach(function (tab) {
            tab.addEventListener('click', function () {
                const name = tab.getAttribute('data-tab');
                tabs.forEach(function (t) {
                    const active = t === tab;
                    t.classList.toggle('active', active);
                    t.setAttribute('aria-selected', active ? 'true' : 'false');
                });
                document.querySelectorAll('.routing-panel').forEach(function (p) {
                    p.classList.toggle('active', p.id === 'panel-' + name);
                });
            });
        });
    }

    function formatTimeRel(iso) {
        if (!iso) return '-';
        const d = new Date(iso);
        if (isNaN(d.getTime())) return iso;
        const diffMs = d.getTime() - Date.now();
        const absMin = Math.round(Math.abs(diffMs) / 60000);
        if (absMin < 60) {
            return diffMs >= 0 ? ('in ' + absMin + 'm') : (absMin + 'm ago');
        }
        const absHours = Math.round(absMin / 60);
        if (absHours < 48) {
            return diffMs >= 0 ? ('in ' + absHours + 'h') : (absHours + 'h ago');
        }
        const absDays = Math.round(absHours / 24);
        return diffMs >= 0 ? ('in ' + absDays + 'd') : (absDays + 'd ago');
    }

    function bucketBadge(bucket, bucketLabel, eligible) {
        if (!eligible) {
            return '<span class="badge badge-danger">Blocked</span>';
        }
        switch (bucket) {
            case 1: return '<span class="badge badge-free">#1 Free</span>';
            case 2: return '<span class="badge badge-onpace">#2 Monthly+CD On Pace</span>';
            case 3: return '<span class="badge badge-onpace">#3 Monthly On Pace</span>';
            case 4: return '<span class="badge badge-ahead">#4 Monthly+CD Ahead</span>';
            case 5: return '<span class="badge badge-ahead">#5 Monthly Ahead</span>';
            default: return '<span class="badge badge-payg">#6 PAYG</span>';
        }
    }

    function renderKPIs() {
        const pools = state.pools || [];
        let eligible = 0;
        let onPace = 0;
        let blocked = 0;
        let headroomUSD = 0;
        let capPools = 0;

        pools.forEach(function (st) {
            if (st.eligible) {
                eligible++;
                if (st.waterfall_bucket === 2 || st.waterfall_bucket === 3) {
                    onPace++;
                }
            } else {
                blocked++;
            }
            if (st.effective_cap_usd > 0) {
                capPools++;
                const spendable = Math.max(0, st.effective_cap_usd - st.effective_spent_usd - (st.pool.reserve_usd || 0));
                headroomUSD += spendable;
            }
        });

        document.getElementById('kpi-eligible').textContent = eligible + ' / ' + pools.length;
        document.getElementById('kpi-eligible-sub').textContent = 'Active pools ready for routing';
        document.getElementById('kpi-onpace').textContent = String(onPace);
        document.getElementById('kpi-onpace-sub').textContent = 'Buckets #2 & #3 under pace target';
        document.getElementById('kpi-headroom').textContent = '$' + headroomUSD.toFixed(2);
        document.getElementById('kpi-headroom-sub').textContent = 'Across ' + capPools + ' dollar-capped pools';
        document.getElementById('kpi-blocked').textContent = String(blocked);
        document.getElementById('kpi-blocked-sub').textContent = blocked === 0 ? 'All pools healthy' : 'Cooldown, disabled, or exhausted';
    }

    function renderTierPreview(routeResp) {
        const grid = document.getElementById('tier-preview-grid');
        if (!grid) return;
        const tiersOrder = ['frontier', 'workhorse', 'lightweight'];
        const tiers = (routeResp && (routeResp.tier_selections || routeResp.tiers)) || {};

        grid.innerHTML = tiersOrder.map(function (tierName) {
            const sel = tiers[tierName] || { tier: tierName, available: false };
            const topModel = (sel.candidates && sel.candidates.length > 0) ? sel.candidates[0].model : 'Unavailable';
            const fbNames = (sel.fallbacks || []).slice(0, 4).map(function (f) {
                return esc(f.pool) + ' (' + esc(f.harness) + ')';
            }).join(' -> ');

            return '<div class="tier-card">' +
                '<div class="tier-card-top">' +
                    '<span class="tier-name">' + esc(tierName) + '</span>' +
                    (sel.available
                        ? '<span class="badge badge-onpace">' + esc(sel.pool) + ' / ' + esc(sel.harness) + '</span>'
                        : '<span class="badge badge-danger">Unavailable</span>') +
                '</div>' +
                '<div class="tier-model">' + esc(topModel) + '</div>' +
                '<div class="tier-meta">' +
                    '<span>Provider: <strong>' + esc(sel.provider || '-') + '</strong></span>' +
                    (sel.overlay ? '<span>Preset: <code>' + esc(sel.overlay) + '</code></span>' : '') +
                    (sel.available ? '<span>Used: ' + Math.round((sel.used_share || 0) * 100) + '%</span>' : '') +
                    (sel.candidates && sel.candidates.length > 1 ? '<span>Candidates: ' + sel.candidates.length + '</span>' : '') +
                '</div>' +
                '<div class="tier-fallbacks">' +
                    (sel.available
                        ? ('Fallbacks: ' + (fbNames || 'none'))
                        : esc(sel.reason || 'No matching route')) +
                '</div>' +
            '</div>';
        }).join('');
    }

    function renderEconomyTable() {
        const tbody = document.getElementById('economy-table-body');
        if (!tbody) return;
        const pools = state.pools || [];

        tbody.innerHTML = pools.map(function (st) {
            const p = st.pool;
            const h = st.health || {};
            const usedPct = Math.round((st.used_share || 0) * 100);
            const paceTarget = Math.min(100, Math.round(((st.elapsed_share || 0) + (p.pace_slack || 0)) * 100));
            const fillCls = usedPct >= 100 ? 'exhausted' : (st.on_pace ? '' : 'ahead');

            let dollarLine = '';
            if (st.effective_cap_usd > 0) {
                dollarLine = '<div class="mono" style="font-size:0.78rem;">$' +
                    st.effective_spent_usd.toFixed(2) + ' / $' + st.effective_cap_usd.toFixed(2) +
                    (p.reserve_usd > 0 ? (' (res $' + p.reserve_usd.toFixed(0) + ')') : '') +
                    '</div>';
            } else {
                dollarLine = '<div class="mono" style="font-size:0.78rem;">' + usedPct + '% used (pace target ' + paceTarget + '%)</div>';
            }

            const winChips = (st.windows || []).map(function (w) {
                return '<span class="window-chip">' + esc(w.name) + ': ' + Math.round(w.used_percent) + '%</span>';
            }).join('');

            const resetText = st.resets_at ? ('Resets ' + formatTimeRel(st.resets_at)) : 'No reset timer';
            const expText = st.effective_expires_at ? ('<div class="badge badge-ahead">Expires ' + formatTimeRel(st.effective_expires_at) + '</div>') : '';

            let healthHtml = '<span class="badge badge-free">Score ' + Math.round(h.health_score || 100) + '</span>';
            if (!st.eligible && st.ineligible_reason) {
                healthHtml = '<span class="badge badge-danger">' + esc(st.ineligible_reason) + '</span>';
            } else if (h.cooldown_until && new Date(h.cooldown_until).getTime() > Date.now()) {
                healthHtml = '<span class="badge badge-danger">CD ' + formatTimeRel(h.cooldown_until) + '</span>';
            }

            const canProbe = p.probe_source === 'chutes' || p.probe_source === 'openrouter' || p.probe_source === 'google_ai_studio';
            const inCooldown = h.cooldown_until && new Date(h.cooldown_until).getTime() > Date.now();

            return '<tr class="' + (st.eligible ? '' : 'row-ineligible') + '">' +
                '<td><span class="rank-pill ' + (st.eligible ? '' : 'ineligible') + '">#' + st.preference_rank + '</span></td>' +
                '<td>' +
                    '<strong>' + esc(p.display_name) + '</strong><br>' +
                    '<span class="mono text-muted">' + esc(p.id) + '</span>' +
                '</td>' +
                '<td>' + bucketBadge(st.waterfall_bucket, st.bucket_label, st.eligible) + '</td>' +
                '<td>' +
                    '<div class="pace-meter">' +
                        '<div class="pace-fill ' + fillCls + '" style="width:' + Math.min(100, usedPct) + '%;"></div>' +
                        '<div class="pace-tick" style="left:' + paceTarget + '%;" title="Pace target ' + paceTarget + '%"></div>' +
                    '</div>' +
                    dollarLine +
                    (winChips ? ('<div class="window-chips">' + winChips + '</div>') : '') +
                '</td>' +
                '<td>' +
                    '<div>' + esc(resetText) + '</div>' +
                    expText +
                '</td>' +
                '<td>' +
                    healthHtml +
                    '<div class="text-muted" style="margin-top:4px;">' + esc(st.probe_summary || st.probe_status) + '</div>' +
                '</td>' +
                '<td class="mono">' + p.weight + '</td>' +
                '<td>' +
                    '<div style="display:flex;flex-direction:column;gap:4px;">' +
                        (inCooldown ? '<button class="routing-btn routing-btn-sm" data-clear-cd="' + esc(p.id) + '">Clear CD</button>' : '') +
                        (canProbe ? '<button class="routing-btn routing-btn-sm" data-probe-pool="' + esc(p.id) + '">Probe</button>' : '') +
                        '<button class="routing-btn routing-btn-sm" data-toggle-pool="' + esc(p.id) + '">' + (p.enabled ? 'Disable' : 'Enable') + '</button>' +
                    '</div>' +
                '</td>' +
            '</tr>';
        }).join('');
    }

    function renderPoolsEditor() {
        const tbody = document.getElementById('pools-editor-body');
        if (!tbody) return;
        const pools = state.pools || [];

        tbody.innerHTML = pools.map(function (st) {
            const p = st.pool;
            const expVal = p.expires_at ? p.expires_at.slice(0, 10) : '';
            return '<tr data-pool-row="' + esc(p.id) + '">' +
                '<td><strong>' + esc(p.display_name) + '</strong><br><span class="mono text-muted">' + esc(p.id) + '</span></td>' +
                '<td><input type="checkbox" class="pe-enabled" ' + (p.enabled ? 'checked' : '') + '></td>' +
                '<td>' +
                    '<select class="routing-select pe-category">' +
                        optionHtml('free', 'free', p.category) +
                        optionHtml('monthly_cooldown', 'monthly_cooldown', p.category) +
                        optionHtml('monthly_no_cooldown', 'monthly_no_cooldown', p.category) +
                        optionHtml('payg', 'payg', p.category) +
                    '</select>' +
                '</td>' +
                '<td><input type="number" step="0.5" class="routing-input routing-input-num pe-cap" value="' + p.cap_usd + '"></td>' +
                '<td><input type="number" step="0.5" class="routing-input routing-input-num pe-reserve" value="' + p.reserve_usd + '"></td>' +
                '<td><input type="number" step="0.01" class="routing-input routing-input-num pe-spent" value="' + p.spent_usd + '"></td>' +
                '<td><input type="number" min="0" max="31" class="routing-input routing-input-num pe-reset" value="' + p.reset_day + '"></td>' +
                '<td><input type="text" class="routing-input routing-input-sm pe-expires" placeholder="YYYY-MM-DD" value="' + esc(expVal) + '"></td>' +
                '<td><input type="number" step="0.05" min="0" max="1" class="routing-input routing-input-num pe-slack" value="' + p.pace_slack + '"></td>' +
                '<td><input type="number" class="routing-input routing-input-num pe-weight" value="' + p.weight + '"></td>' +
                '<td><button class="routing-btn routing-btn-sm routing-btn-primary" data-save-pool="' + esc(p.id) + '">Save</button></td>' +
            '</tr>';
        }).join('');
    }

    function optionHtml(val, label, selected) {
        return '<option value="' + esc(val) + '"' + (val === selected ? ' selected' : '') + '>' + esc(label) + '</option>';
    }

    function populatePoolSelects() {
        const mPool = document.getElementById('m-pool');
        const fPool = document.getElementById('filter-pool');
        const pools = state.pools || [];
        if (mPool) {
            const prev = mPool.value;
            mPool.innerHTML = pools.map(function (st) {
                return '<option value="' + esc(st.pool.id) + '">' + esc(st.pool.id) + ' (' + esc(st.pool.display_name) + ')</option>';
            }).join('');
            if (prev) mPool.value = prev;
        }
        if (fPool) {
            const prevF = fPool.value;
            fPool.innerHTML = '<option value="">All Pools</option>' + pools.map(function (st) {
                return '<option value="' + esc(st.pool.id) + '">' + esc(st.pool.id) + '</option>';
            }).join('');
            fPool.value = prevF;
        }
    }

    function renderModelsTable() {
        const tbody = document.getElementById('models-table-body');
        if (!tbody) return;
        const fTier = (document.getElementById('filter-tier') || {}).value || '';
        const fPool = (document.getElementById('filter-pool') || {}).value || '';
        const excludedSet = new Set((state.excluded_models || []).map(function (x) { return x.toLowerCase(); }));

        const filtered = (state.models || []).filter(function (m) {
            if (fTier && m.tier !== fTier) return false;
            if (fPool && m.pool_id !== fPool) return false;
            return true;
        });

        tbody.innerHTML = filtered.map(function (m) {
            const isExcl = excludedSet.has((m.model_id || '').toLowerCase());
            return '<tr class="' + (m.enabled && !isExcl ? '' : 'row-ineligible') + '">' +
                '<td><span class="badge badge-muted">' + esc(m.tier) + '</span></td>' +
                '<td class="mono">' + esc(m.pool_id) + '</td>' +
                '<td><strong>' + esc(m.harness) + '</strong><br><span class="text-muted">' + esc(m.provider) + '</span></td>' +
                '<td><span class="mono">' + esc(m.model_id) + '</span>' +
                    (m.overlay ? '<br><code>' + esc(m.overlay) + '</code>' : '') +
                    (isExcl ? ' <span class="badge badge-danger">excluded</span>' : '') +
                '</td>' +
                '<td>' + esc(m.billing) + '</td>' +
                '<td class="mono">' + m.priority + (m.override_pi_preference ? ' *pi-ovr' : '') + '</td>' +
                '<td class="text-muted">' + esc(m.description || '') + '</td>' +
                '<td>' +
                    '<div style="display:flex;gap:4px;">' +
                        '<button class="routing-btn routing-btn-sm" data-edit-model="' + m.id + '">Edit</button>' +
                        '<button class="routing-btn routing-btn-sm routing-btn-danger" data-del-model="' + m.id + '">Del</button>' +
                    '</div>' +
                '</td>' +
            '</tr>';
        }).join('');
    }

    function renderProbeSettingsAndFeedback() {
        const ps = state.probe_settings || {};
        const cStat = document.getElementById('s-chutes-status');
        const oStat = document.getElementById('s-or-status');
        const gStat = document.getElementById('s-gcp-status');
        const gInput = document.getElementById('s-gcp-project');

        if (cStat) cStat.textContent = ps.chutes_api_key_configured ? ('(' + ps.chutes_api_key_masked + ')') : '(not configured)';
        if (oStat) oStat.textContent = ps.openrouter_payg_key_configured ? ('(' + ps.openrouter_payg_key_masked + ')') : '(using feedback cost)';
        if (gStat) gStat.textContent = ps.google_monitoring_token_source ? ('(token: ' + ps.google_monitoring_token_source + ')') : '(feedback cost mode)';
        if (gInput && !gInput.value && ps.google_cloud_project) gInput.value = ps.google_cloud_project;

        const exclBox = document.getElementById('excluded-models-text');
        if (exclBox) exclBox.value = (state.excluded_models || []).join('\n');

        const fbBody = document.getElementById('feedback-table-body');
        if (!fbBody) return;
        const events = state.recent_feedback || [];
        if (events.length === 0) {
            fbBody.innerHTML = '<tr><td colspan="7" class="text-muted">No feedback events recorded yet.</td></tr>';
            return;
        }
        fbBody.innerHTML = events.map(function (ev) {
            const cls = ev.outcome === 'ok' ? 'badge-free' : (ev.outcome === '429' ? 'badge-ahead' : 'badge-danger');
            return '<tr>' +
                '<td class="mono text-muted">' + esc(formatTimeRel(ev.created_at)) + '</td>' +
                '<td class="mono">' + esc(ev.pool_id) + '</td>' +
                '<td>' + esc(ev.harness || '-') + '<br><span class="mono text-muted">' + esc(ev.model_id || '-') + '</span></td>' +
                '<td><span class="badge ' + cls + '">' + esc(ev.outcome) + '</span></td>' +
                '<td class="mono">' + (ev.ttft_ms ? Math.round(ev.ttft_ms) + 'ms' : (ev.latency_ms ? Math.round(ev.latency_ms) + 'ms' : '-')) + '</td>' +
                '<td class="mono">' + (ev.cost_usd ? '$' + ev.cost_usd.toFixed(4) : '-') + '</td>' +
                '<td class="text-muted">' + esc(ev.note || '') + (ev.issue ? (' [' + esc(ev.issue) + ']') : '') + '</td>' +
            '</tr>';
        }).join('');
    }

    async function refreshState() {
        try {
            const harnesses = (document.getElementById('harness-preview-select') || {}).value || '';
            const qs = harnesses ? ('?harnesses=' + encodeURIComponent(harnesses)) : '';
            const data = await apiFetch('/api/routing/state' + qs);
            state = data;
            renderKPIs();
            renderTierPreview(data.route);
            renderEconomyTable();
            renderPoolsEditor();
            populatePoolSelects();
            renderModelsTable();
            renderProbeSettingsAndFeedback();
        } catch (err) {
            showToast('Failed to load routing state: ' + err.message, true);
        }
    }

    async function refreshRoutePreviewOnly() {
        try {
            const harnesses = (document.getElementById('harness-preview-select') || {}).value || '';
            const qs = harnesses ? ('?harnesses=' + encodeURIComponent(harnesses)) : '';
            const routeResp = await apiFetch('/api/route' + qs);
            renderTierPreview(routeResp);
        } catch (err) {
            showToast('Preview failed: ' + err.message, true);
        }
    }

    function bindEvents() {
        document.getElementById('btn-refresh').addEventListener('click', refreshState);
        document.getElementById('harness-preview-select').addEventListener('change', refreshRoutePreviewOnly);
        document.getElementById('filter-tier').addEventListener('change', renderModelsTable);
        document.getElementById('filter-pool').addEventListener('change', renderModelsTable);

        document.getElementById('btn-probe-all').addEventListener('click', async function () {
            try {
                await Promise.all([
                    apiFetch('/api/routing/pools/chutes/probe', { method: 'POST' }),
                    apiFetch('/api/routing/pools/openrouter-payg/probe', { method: 'POST' }),
                    apiFetch('/api/routing/pools/google-ai-studio/probe', { method: 'POST' })
                ]);
                showToast('External probes refreshed');
                await refreshState();
            } catch (err) {
                showToast('Probe error: ' + err.message, true);
            }
        });

        // Delegate table clicks on economy table
        document.getElementById('economy-table-body').addEventListener('click', async function (e) {
            const cdBtn = e.target.closest('[data-clear-cd]');
            if (cdBtn) {
                const id = cdBtn.getAttribute('data-clear-cd');
                try {
                    await apiFetch('/api/routing/pools/' + encodeURIComponent(id) + '/clear-cooldown', { method: 'POST' });
                    showToast('Cooldown cleared for ' + id);
                    await refreshState();
                } catch (err) {
                    showToast(err.message, true);
                }
                return;
            }
            const probeBtn = e.target.closest('[data-probe-pool]');
            if (probeBtn) {
                const id = probeBtn.getAttribute('data-probe-pool');
                try {
                    await apiFetch('/api/routing/pools/' + encodeURIComponent(id) + '/probe', { method: 'POST' });
                    showToast('Probed ' + id);
                    await refreshState();
                } catch (err) {
                    showToast('Probe error: ' + err.message, true);
                }
                return;
            }
            const toggleBtn = e.target.closest('[data-toggle-pool]');
            if (toggleBtn) {
                const id = toggleBtn.getAttribute('data-toggle-pool');
                const found = (state.pools || []).find(function (st) { return st.pool.id === id; });
                if (!found) return;
                const updated = Object.assign({}, found.pool, { enabled: !found.pool.enabled });
                try {
                    await apiFetch('/api/routing/pools/' + encodeURIComponent(id), {
                        method: 'PUT',
                        body: JSON.stringify(updated)
                    });
                    showToast((updated.enabled ? 'Enabled ' : 'Disabled ') + id);
                    await refreshState();
                } catch (err) {
                    showToast(err.message, true);
                }
            }
        });

        // Delegate pool save buttons
        document.getElementById('pools-editor-body').addEventListener('click', async function (e) {
            const btn = e.target.closest('[data-save-pool]');
            if (!btn) return;
            const id = btn.getAttribute('data-save-pool');
            const row = btn.closest('tr');
            const found = (state.pools || []).find(function (st) { return st.pool.id === id; });
            if (!found || !row) return;
            const expStr = row.querySelector('.pe-expires').value.trim();
            let expISO = null;
            if (expStr) {
                expISO = expStr.length === 10 ? (expStr + 'T23:59:59Z') : expStr;
            }
            const payload = Object.assign({}, found.pool, {
                enabled: row.querySelector('.pe-enabled').checked,
                category: row.querySelector('.pe-category').value,
                cap_usd: parseFloat(row.querySelector('.pe-cap').value) || 0,
                reserve_usd: parseFloat(row.querySelector('.pe-reserve').value) || 0,
                spent_usd: parseFloat(row.querySelector('.pe-spent').value) || 0,
                reset_day: parseInt(row.querySelector('.pe-reset').value, 10) || 0,
                expires_at: expISO,
                pace_slack: parseFloat(row.querySelector('.pe-slack').value) || 0,
                weight: parseInt(row.querySelector('.pe-weight').value, 10) || 100
            });
            try {
                await apiFetch('/api/routing/pools/' + encodeURIComponent(id), {
                    method: 'PUT',
                    body: JSON.stringify(payload)
                });
                showToast('Saved pool ' + id);
                await refreshState();
            } catch (err) {
                showToast('Save pool failed: ' + err.message, true);
            }
        });

        // Model add/edit form
        document.getElementById('model-form').addEventListener('submit', async function (e) {
            e.preventDefault();
            const idVal = parseInt(document.getElementById('m-id').value, 10) || 0;
            const payload = {
                id: idVal,
                tier: document.getElementById('m-tier').value,
                pool_id: document.getElementById('m-pool').value,
                harness: document.getElementById('m-harness').value.trim(),
                provider: document.getElementById('m-provider').value.trim(),
                overlay: document.getElementById('m-overlay').value.trim(),
                model_id: document.getElementById('m-model').value.trim(),
                billing: document.getElementById('m-billing').value,
                priority: parseInt(document.getElementById('m-priority').value, 10) || 100,
                input_cost_per_1m: parseFloat(document.getElementById('m-in-cost').value) || 0,
                output_cost_per_1m: parseFloat(document.getElementById('m-out-cost').value) || 0,
                description: document.getElementById('m-desc').value.trim(),
                override_pi_preference: document.getElementById('m-override-pi').checked,
                enabled: document.getElementById('m-enabled').checked
            };
            try {
                const path = idVal > 0 ? ('/api/routing/models/' + idVal) : '/api/routing/models';
                const method = idVal > 0 ? 'PUT' : 'POST';
                await apiFetch(path, { method: method, body: JSON.stringify(payload) });
                showToast('Saved route ' + payload.model_id);
                resetModelForm();
                await refreshState();
            } catch (err) {
                showToast('Save model failed: ' + err.message, true);
            }
        });

        document.getElementById('btn-reset-model').addEventListener('click', resetModelForm);

        document.getElementById('models-table-body').addEventListener('click', async function (e) {
            const editBtn = e.target.closest('[data-edit-model]');
            if (editBtn) {
                const id = parseInt(editBtn.getAttribute('data-edit-model'), 10);
                const m = (state.models || []).find(function (x) { return x.id === id; });
                if (!m) return;
                document.getElementById('m-id').value = String(m.id);
                document.getElementById('m-tier').value = m.tier;
                document.getElementById('m-pool').value = m.pool_id;
                document.getElementById('m-harness').value = m.harness;
                document.getElementById('m-provider').value = m.provider;
                document.getElementById('m-overlay').value = m.overlay || '';
                document.getElementById('m-model').value = m.model_id;
                document.getElementById('m-billing').value = m.billing || 'byok';
                document.getElementById('m-priority').value = String(m.priority || 100);
                document.getElementById('m-in-cost').value = String(m.input_cost_per_1m || 0);
                document.getElementById('m-out-cost').value = String(m.output_cost_per_1m || 0);
                document.getElementById('m-desc').value = m.description || '';
                document.getElementById('m-override-pi').checked = !!m.override_pi_preference;
                document.getElementById('m-enabled').checked = !!m.enabled;
                window.scrollTo({ top: 0, behavior: 'smooth' });
                return;
            }
            const delBtn = e.target.closest('[data-del-model]');
            if (delBtn) {
                const id = parseInt(delBtn.getAttribute('data-del-model'), 10);
                try {
                    await apiFetch('/api/routing/models/' + id, { method: 'DELETE' });
                    showToast('Deleted route #' + id);
                    await refreshState();
                } catch (err) {
                    showToast(err.message, true);
                }
            }
        });

        document.getElementById('btn-save-excluded').addEventListener('click', async function () {
            const lines = document.getElementById('excluded-models-text').value
                .split(/[\n,]/)
                .map(function (s) { return s.trim(); })
                .filter(Boolean);
            try {
                await apiFetch('/api/routing/excluded', {
                    method: 'PUT',
                    body: JSON.stringify({ excluded_models: lines })
                });
                showToast('Saved excluded models');
                await refreshState();
            } catch (err) {
                showToast(err.message, true);
            }
        });

        document.getElementById('btn-save-settings').addEventListener('click', async function () {
            const payload = {};
            const ck = document.getElementById('s-chutes-key').value.trim();
            const ok = document.getElementById('s-or-payg-key').value.trim();
            const gp = document.getElementById('s-gcp-project').value.trim();
            if (ck) payload.chutes_api_key = ck;
            if (ok) payload.openrouter_payg_api_key = ok;
            payload.google_cloud_project = gp;
            try {
                await apiFetch('/api/routing/settings', {
                    method: 'PUT',
                    body: JSON.stringify(payload)
                });
                document.getElementById('s-chutes-key').value = '';
                document.getElementById('s-or-payg-key').value = '';
                showToast('Probe settings saved');
                await refreshState();
            } catch (err) {
                showToast(err.message, true);
            }
        });

        document.getElementById('btn-load-yaml').addEventListener('click', async function () {
            try {
                const yamlText = await apiFetch('/api/route/catalog');
                document.getElementById('yaml-editor').value = yamlText;
                showToast('Loaded current YAML catalog');
            } catch (err) {
                showToast(err.message, true);
            }
        });

        document.getElementById('btn-import-yaml').addEventListener('click', async function () {
            const yamlText = document.getElementById('yaml-editor').value;
            if (!yamlText.trim()) {
                showToast('Paste YAML first', true);
                return;
            }
            try {
                await apiFetch('/api/route/catalog', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/yaml' },
                    body: yamlText
                });
                showToast('Imported YAML catalog');
                await refreshState();
            } catch (err) {
                showToast('Import failed: ' + err.message, true);
            }
        });
    }

    function resetModelForm() {
        document.getElementById('m-id').value = '';
        document.getElementById('m-model').value = '';
        document.getElementById('m-desc').value = '';
        document.getElementById('m-overlay').value = '';
        document.getElementById('m-priority').value = '100';
        document.getElementById('m-in-cost').value = '0';
        document.getElementById('m-out-cost').value = '0';
        document.getElementById('m-override-pi').checked = false;
        document.getElementById('m-enabled').checked = true;
    }

    initThemeToggle();
    initTabs();
    bindEvents();
    refreshState();
})();
