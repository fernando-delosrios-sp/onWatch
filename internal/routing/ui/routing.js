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
            case 2: return '<span class="badge badge-onpace">#2 Subscription On Pace</span>';
            case 3: return '<span class="badge badge-onpace">#3 Allowance On Pace</span>';
            case 4: return '<span class="badge badge-ahead">#4 Subscription Ahead</span>';
            case 5: return '<span class="badge badge-ahead">#5 Allowance Ahead</span>';
            default: return '<span class="badge badge-payg">#6 PAYG</span>';
        }
    }

    function getShortBlockedReason(st) {
        if (!st.pool.enabled) return 'disabled';
        if (st.health && st.health.cooldown_until) {
            const until = new Date(st.health.cooldown_until);
            if (!isNaN(until.getTime()) && until > Date.now()) {
                const diffMin = Math.round((until.getTime() - Date.now()) / 60000);
                return diffMin >= 60 ? (Math.round(diffMin / 60) + 'h cd') : (diffMin + 'm cd');
            }
        }
        const reason = (st.ineligible_reason || (st.health && st.health.last_reason) || '').toLowerCase();
        if (reason.includes('cooldown')) return 'cooldown';
        if (reason.includes('exhausted') || reason.includes('cap')) return 'exhausted';
        if (reason.includes('credit') || reason.includes('balance')) return '$0 credit';
        if (reason.includes('error')) return 'errors';
        return 'ineligible';
    }

    function renderKPIs() {
        const pools = state.pools || [];
        let eligible = 0;
        let onPace = 0;
        let blocked = 0;
        let headroomUSD = 0;
        let capPools = 0;

        const eligibleChips = [];
        const onPaceChips = [];
        const headroomChips = [];
        const blockedChips = [];

        pools.forEach(function (st) {
            const dName = esc(st.pool.display_name || st.pool.id);

            if (st.eligible) {
                eligible++;
                let chipColor = 'chip-teal';
                if (st.waterfall_bucket === 1) {
                    chipColor = 'chip-teal';
                } else if (st.waterfall_bucket === 4 || st.waterfall_bucket === 5) {
                    chipColor = 'chip-amber';
                } else if (st.waterfall_bucket === 6) {
                    chipColor = 'chip-blue';
                }
                const title = esc((st.bucket_label || ('Bucket #' + st.waterfall_bucket)) + (st.used_share ? ' (' + Math.round(st.used_share * 100) + '% used)' : ''));
                eligibleChips.push(
                    '<span class="routing-pool-chip ' + chipColor + '" title="' + title + '">' +
                    '<span class="routing-chip-dot"></span>' + dName +
                    '</span>'
                );

                if (st.waterfall_bucket === 2 || st.waterfall_bucket === 3) {
                    onPace++;
                    onPaceChips.push(
                        '<span class="routing-pool-chip chip-teal" title="' + title + '">' +
                        '<span class="routing-chip-dot"></span>' + dName +
                        '</span>'
                    );
                }
            } else {
                blocked++;
                const shortR = getShortBlockedReason(st);
                const fullR = esc(st.ineligible_reason || (st.health && st.health.last_reason) || 'Ineligible');
                const chipColor = (!st.pool.enabled) ? 'chip-amber' : 'chip-danger';
                blockedChips.push(
                    '<span class="routing-pool-chip ' + chipColor + '" title="' + fullR + '">' +
                    '<span class="routing-chip-dot"></span>' + dName +
                    ' <span class="routing-chip-sub">' + esc(shortR) + '</span>' +
                    '</span>'
                );
            }

            if (st.effective_cap_usd > 0) {
                capPools++;
                const spendable = Math.max(0, st.effective_cap_usd - st.effective_spent_usd - (st.pool.reserve_usd || 0));
                headroomUSD += spendable;
                const spendColor = spendable > 0 ? 'chip-teal' : 'chip-amber';
                const capTitle = esc(st.pool.display_name + ': Cap $' + st.effective_cap_usd.toFixed(2) + ', Spent $' + st.effective_spent_usd.toFixed(2) + (st.pool.reserve_usd > 0 ? ', Reserve $' + st.pool.reserve_usd.toFixed(2) : ''));
                headroomChips.push(
                    '<span class="routing-pool-chip ' + spendColor + '" title="' + capTitle + '">' +
                    '<span class="routing-chip-dot"></span>' + dName +
                    ' <span class="routing-chip-val">$' + spendable.toFixed(2) + '</span>' +
                    '</span>'
                );
            }
        });

        document.getElementById('kpi-eligible').textContent = eligible + ' / ' + pools.length;
        document.getElementById('kpi-eligible-sub').textContent = 'Active pools ready for routing';
        const elChips = document.getElementById('kpi-eligible-chips');
        if (elChips) {
            elChips.innerHTML = eligibleChips.length > 0 ? eligibleChips.join('') : '<span class="routing-chip-empty">No eligible pools</span>';
        }

        document.getElementById('kpi-onpace').textContent = String(onPace);
        document.getElementById('kpi-onpace-sub').textContent = 'Buckets #2 & #3 under pace target';
        const opChips = document.getElementById('kpi-onpace-chips');
        if (opChips) {
            opChips.innerHTML = onPaceChips.length > 0 ? onPaceChips.join('') : '<span class="routing-chip-empty">No subscription pools on pace</span>';
        }

        document.getElementById('kpi-headroom').textContent = '$' + headroomUSD.toFixed(2);
        document.getElementById('kpi-headroom-sub').textContent = 'Across ' + capPools + ' dollar-capped pools';
        const hrChips = document.getElementById('kpi-headroom-chips');
        if (hrChips) {
            hrChips.innerHTML = headroomChips.length > 0 ? headroomChips.join('') : '<span class="routing-chip-empty">No dollar caps configured</span>';
        }

        document.getElementById('kpi-blocked').textContent = String(blocked);
        document.getElementById('kpi-blocked-sub').textContent = blocked === 0 ? 'All pools healthy' : 'Cooldown, disabled, or exhausted';
        const blChips = document.getElementById('kpi-blocked-chips');
        if (blChips) {
            blChips.innerHTML = blockedChips.length > 0 ? blockedChips.join('') : '<span class="routing-pool-chip chip-teal"><span class="routing-chip-dot"></span>All pools active</span>';
        }
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

    function isBuiltinPool(p) {
        const src = (p.probe_source || '').toLowerCase();
        return src && src !== 'manual';
    }

    function renderPoolsEditor() {
        const tbody = document.getElementById('pools-editor-body');
        if (!tbody) return;
        const pools = state.pools || [];

        tbody.innerHTML = pools.map(function (st) {
            const p = st.pool;
            const expVal = p.expires_at ? p.expires_at.slice(0, 10) : '';
            const builtin = isBuiltinPool(p);
            const cat = p.category || 'subscription';

            // Category: locked badge for integrated pools, dropdown for custom pools
            let catHtml = '';
            if (builtin) {
                catHtml = '<span class="badge badge-category" title="Category locked by ' + esc(p.probe_source) + ' integration">' + esc(cat) + '</span>';
            } else {
                catHtml = '<select class="routing-select routing-select-sm pe-category">' +
                    optionHtml('subscription', 'subscription', cat) +
                    optionHtml('allowance', 'allowance', cat) +
                    optionHtml('payg', 'payg', cat) +
                    optionHtml('free', 'free', cat) +
                '</select>';
            }

            // Cap ($): input for allowance and payg; N/A for subscription and free
            let capHtml = '';
            if (cat === 'allowance' || cat === 'payg') {
                capHtml = '<input type="number" step="0.5" class="routing-input pe-cap" value="' + (p.cap_usd || 0) + '">';
            } else {
                capHtml = '<span class="text-muted mono" style="font-size:0.8rem;">--</span>';
            }

            // Cycle: daily/weekly/monthly for allowance, weekly/monthly for subscription
            let cycleHtml = '';
            if (cat === 'allowance') {
                const curCycle = p.reset_cycle || 'monthly';
                cycleHtml = '<select class="routing-select routing-select-sm pe-cycle">' +
                    optionHtml('daily', 'Daily', curCycle) +
                    optionHtml('weekly', 'Weekly', curCycle) +
                    optionHtml('monthly', 'Monthly', curCycle) +
                '</select>';
            } else if (cat === 'subscription') {
                const curCycle = p.reset_cycle || 'weekly';
                cycleHtml = '<select class="routing-select routing-select-sm pe-cycle">' +
                    optionHtml('weekly', 'Weekly', curCycle) +
                    optionHtml('monthly', 'Monthly', curCycle) +
                '</select>';
            } else if (cat === 'payg') {
                cycleHtml = '<span class="text-muted mono" style="font-size:0.8rem;">Monthly</span>';
            } else {
                cycleHtml = '<span class="text-muted mono" style="font-size:0.8rem;">--</span>';
            }

            // Inner Window:
            let innerWhHtml = '';
            if (cat === 'subscription') {
                if (p.probe_source && p.probe_source.indexOf('antigravity') === 0) {
                    innerWhHtml = '<span class="badge badge-muted" title="Probed automatically (5h rolling + weekly)">5h inner</span>';
                } else {
                    innerWhHtml = '<div style="display:flex;align-items:center;gap:2px;"><input type="number" min="0" max="72" class="routing-input pe-inner-wh" value="' + (p.inner_window_hours || 0) + '"><span class="text-muted" style="font-size:0.75rem;">h</span></div>';
                }
            } else {
                innerWhHtml = '<span class="text-muted mono" style="font-size:0.8rem;">--</span>';
            }

            const curRouting = p.routing_provider || '';
            const routingHtml = '<select class="routing-select routing-select-sm pe-routing">' +
                optionHtml('', 'Direct', curRouting) +
                optionHtml('openrouter', 'OpenRouter', curRouting) +
            '</select>';

            const inCost = (p.input_cost_per_1m != null) ? p.input_cost_per_1m : 0;
            const outCost = (p.output_cost_per_1m != null) ? p.output_cost_per_1m : 0;
            const costHtml = '<div style="display:flex;align-items:center;gap:2px;">' +
                '<input type="number" step="0.01" min="0" class="routing-input pe-in-cost" title="Input $/1M" placeholder="in" value="' + inCost + '">' +
                '<span class="text-muted" style="font-size:0.75rem;">/</span>' +
                '<input type="number" step="0.01" min="0" class="routing-input pe-out-cost" title="Output $/1M" placeholder="out" value="' + outCost + '">' +
            '</div>';

            return '<tr data-pool-row="' + esc(p.id) + '">' +
                '<td style="white-space:nowrap;"><strong>' + esc(p.display_name) + '</strong><br><span class="mono text-muted">' + esc(p.id) + '</span></td>' +
                '<td style="text-align:center;"><input type="checkbox" class="pe-enabled" ' + (p.enabled ? 'checked' : '') + '></td>' +
                '<td>' + catHtml + '</td>' +
                '<td>' + routingHtml + '</td>' +
                '<td>' + capHtml + '</td>' +
                '<td>' + cycleHtml + '</td>' +
                '<td>' + innerWhHtml + '</td>' +
                '<td><input type="text" class="routing-input pe-expires" placeholder="YYYY-MM-DD" value="' + esc(expVal) + '"></td>' +
                '<td>' + costHtml + '</td>' +
                '<td><input type="number" step="0.05" min="0" max="1" class="routing-input pe-slack" value="' + p.pace_slack + '"></td>' +
                '<td><input type="number" class="routing-input pe-weight" value="' + p.weight + '"></td>' +
                '<td>' +
                    '<div class="pe-actions">' +
                        '<button class="routing-btn routing-btn-sm routing-btn-primary" data-save-pool="' + esc(p.id) + '">Save</button>' +
                        '<button class="routing-btn routing-btn-sm routing-btn-ghost" data-reset-pool="' + esc(p.id) + '" title="Reset spend to $0 and restart cycle">Reset</button>' +
                    '</div>' +
                '</td>' +
            '</tr>';
        }).join('');
    }

    function optionHtml(val, label, selected) {
        return '<option value="' + esc(val) + '"' + (val === selected ? ' selected' : '') + '>' + esc(label) + '</option>';
    }

    function generateOpenRouterModel(provider, modelId) {
        const model = (modelId || '').trim();
        if (!model) return '';
        if (model.indexOf('/') !== -1) return model;
        const p = (provider || '').trim().toLowerCase();
        if (p.includes('google') || p === 'gemini') return 'google/' + model;
        if (p.includes('xai') || p.includes('grok')) return 'x-ai/' + model;
        if (p.includes('nvidia')) return 'nvidia/' + model;
        if (p.includes('anthropic') || p.includes('claude')) return 'anthropic/' + model;
        if (p.includes('openai') || p.includes('codex') || p.includes('chatgpt')) return 'openai/' + model;
        if (p.includes('deepseek')) return 'deepseek/' + model;
        if (p.includes('moonshot') || p.includes('kimi')) return 'moonshotai/' + model;
        if (p.includes('chutes')) return 'chutes/' + model;
        if (p && p !== 'openrouter') return p + '/' + model;
        return model;
    }

    function stripOpenRouterPrefix(modelId) {
        let m = (modelId || '').trim();
        if (!m) return '';
        const prefixes = ['google/', 'x-ai/', 'nvidia/', 'anthropic/', 'openai/', 'deepseek/', 'moonshotai/', 'chutes/'];
        for (let i = 0; i < prefixes.length; i++) {
            if (m.startsWith(prefixes[i])) {
                return m.slice(prefixes[i].length);
            }
        }
        const idx = m.indexOf('/');
        if (idx !== -1) {
            return m.slice(idx + 1);
        }
        return m;
    }

    function onPoolChange() {
        const poolEl = document.getElementById('m-pool');
        if (!poolEl) return;
        const poolId = poolEl.value || '';
        const found = (state.pools || []).find(function (st) { return st.pool.id === poolId; });
        if (!found) return;
        const p = found.pool;
        const orCheckbox = document.getElementById('m-route-openrouter');
        const modelEl = document.getElementById('m-model');

        const isOR = p.routing_provider === 'openrouter';
        if (orCheckbox) {
            orCheckbox.checked = isOR;
        }

        if (modelEl && modelEl.value) {
            const srcProv = p.probe_source ? p.probe_source.split('_')[0] : p.id;
            if (isOR) {
                modelEl.value = generateOpenRouterModel(srcProv, modelEl.value);
            } else {
                modelEl.value = stripOpenRouterPrefix(modelEl.value);
            }
        }
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
            onPoolChange();
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

        const filtered = (state.models || []).filter(function (m) {
            if (fTier && m.tier !== fTier) return false;
            if (fPool && m.pool_id !== fPool) return false;
            return true;
        });

        tbody.innerHTML = filtered.map(function (m) {
            const isOR = m.provider === 'openrouter';
            const routingBadge = isOR
                ? '<button type="button" class="badge badge-category" style="cursor:pointer;border:none;" title="Routed via OpenRouter. Click to switch to Direct." data-toggle-routing="' + m.id + '">OpenRouter</button>'
                : '<button type="button" class="badge badge-muted" style="cursor:pointer;border:none;" title="Routed directly. Click to route via OpenRouter." data-toggle-routing="' + m.id + '">Direct</button>';
            return '<tr class="' + (m.enabled ? '' : 'row-ineligible') + '">' +
                '<td><span class="badge badge-muted">' + esc(m.tier) + '</span></td>' +
                '<td class="mono">' + esc(m.pool_id) + '</td>' +
                '<td><strong>' + esc(m.harness) + '</strong></td>' +
                '<td><span class="mono">' + esc(m.model_id) + '</span></td>' +
                '<td>' + routingBadge + '</td>' +
                '<td class="mono">' + m.priority + '</td>' +
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

    function populateHarnessPreviewSelect() {
        const sel = document.getElementById('harness-preview-select');
        if (!sel) return false;
        const prev = sel.value || '';
        const harnessSet = new Set();
        (state.models || []).forEach(function (m) {
            if (!m || !m.enabled) return;
            const h = (m.harness || '').trim();
            if (h) harnessSet.add(h);
        });
        const harnesses = Array.from(harnessSet).sort(function (a, b) {
            return a.toLowerCase().localeCompare(b.toLowerCase());
        });
        sel.innerHTML = '<option value="">All harnesses</option>' + harnesses.map(function (h) {
            return '<option value="' + esc(h) + '">' + esc(h) + '</option>';
        }).join('');
        if (prev && harnessSet.has(prev)) {
            sel.value = prev;
            return false;
        }
        sel.value = '';
        return prev !== '';
    }

    async function refreshState() {
        try {
            const harnesses = (document.getElementById('harness-preview-select') || {}).value || '';
            const qs = harnesses ? ('?harnesses=' + encodeURIComponent(harnesses)) : '';
            const data = await apiFetch('/api/routing/state' + qs);
            state = data;
            const resetToAll = populateHarnessPreviewSelect();
            renderKPIs();
            renderTierPreview(data.route);
            renderEconomyTable();
            renderPoolsEditor();
            populatePoolSelects();
            renderModelsTable();
            renderProbeSettingsAndFeedback();
            if (resetToAll) {
                await refreshRoutePreviewOnly();
            }
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

        // Delegate pool save and reset buttons
        document.getElementById('pools-editor-body').addEventListener('click', async function (e) {
            const resetBtn = e.target.closest('[data-reset-pool]');
            if (resetBtn) {
                const id = resetBtn.getAttribute('data-reset-pool');
                try {
                    await apiFetch('/api/routing/pools/' + encodeURIComponent(id) + '/reset-cycle', { method: 'POST' });
                    showToast('Cycle reset for ' + id);
                    await refreshState();
                } catch (err) {
                    showToast('Reset cycle failed: ' + err.message, true);
                }
                return;
            }

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
            const catEl = row.querySelector('.pe-category');
            const capEl = row.querySelector('.pe-cap');
            const cycleEl = row.querySelector('.pe-cycle');
            const innerWhEl = row.querySelector('.pe-inner-wh');
            const routingEl = row.querySelector('.pe-routing');
            const inCostEl = row.querySelector('.pe-in-cost');
            const outCostEl = row.querySelector('.pe-out-cost');

            const payload = Object.assign({}, found.pool, {
                enabled: row.querySelector('.pe-enabled').checked,
                category: catEl ? catEl.value : found.pool.category,
                cap_usd: capEl ? (parseFloat(capEl.value) || 0) : (found.pool.cap_usd || 0),
                reset_cycle: cycleEl ? cycleEl.value : (found.pool.reset_cycle || 'monthly'),
                inner_window_hours: innerWhEl ? (parseInt(innerWhEl.value, 10) || 0) : (found.pool.inner_window_hours || 0),
                expires_at: expISO,
                routing_provider: routingEl ? routingEl.value : (found.pool.routing_provider || ''),
                input_cost_per_1m: inCostEl ? (parseFloat(inCostEl.value) || 0) : (found.pool.input_cost_per_1m || 0),
                output_cost_per_1m: outCostEl ? (parseFloat(outCostEl.value) || 0) : (found.pool.output_cost_per_1m || 0),
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

        const poolSelect = document.getElementById('m-pool');
        if (poolSelect) {
            poolSelect.addEventListener('change', onPoolChange);
        }
        const orCheckbox = document.getElementById('m-route-openrouter');
        if (orCheckbox) {
            orCheckbox.addEventListener('change', function () {
                const poolId = (document.getElementById('m-pool') || {}).value || '';
                const found = (state.pools || []).find(function (st) { return st.pool.id === poolId; });
                const srcProv = found && found.pool.probe_source ? found.pool.probe_source.split('_')[0] : (found ? found.pool.id : '');
                const modelEl = document.getElementById('m-model');
                if (modelEl && modelEl.value) {
                    if (orCheckbox.checked) {
                        modelEl.value = generateOpenRouterModel(srcProv, modelEl.value);
                    } else {
                        modelEl.value = stripOpenRouterPrefix(modelEl.value);
                    }
                }
            });
        }
        const modelInput = document.getElementById('m-model');
        if (modelInput) {
            modelInput.addEventListener('blur', function () {
                const orCb = document.getElementById('m-route-openrouter');
                if (orCb && orCb.checked && modelInput.value) {
                    const poolId = (document.getElementById('m-pool') || {}).value || '';
                    const found = (state.pools || []).find(function (st) { return st.pool.id === poolId; });
                    const srcProv = found && found.pool.probe_source ? found.pool.probe_source.split('_')[0] : (found ? found.pool.id : '');
                    modelInput.value = generateOpenRouterModel(srcProv, modelInput.value);
                }
            });
        }

        // Model add/edit form
        document.getElementById('model-form').addEventListener('submit', async function (e) {
            e.preventDefault();
            const idVal = parseInt(document.getElementById('m-id').value, 10) || 0;
            const poolId = document.getElementById('m-pool').value;
            const poolObj = (state.pools || []).find(function (st) { return st.pool.id === poolId; });
            const isOR = document.getElementById('m-route-openrouter') ? document.getElementById('m-route-openrouter').checked : false;

            let provider = 'direct';
            let srcProv = '';
            if (poolObj) {
                if (poolObj.pool.probe_source) {
                    const s = poolObj.pool.probe_source;
                    if (s.startsWith('google')) srcProv = 'google';
                    else if (s.startsWith('nvidia')) srcProv = 'nvidia';
                    else if (s.startsWith('chutes')) srcProv = 'chutes';
                    else srcProv = s.split('_')[0];
                } else if (poolObj.pool.id.startsWith('antigravity')) {
                    srcProv = 'antigravity';
                } else if (poolObj.pool.id.startsWith('cursor')) {
                    srcProv = 'cursor';
                } else if (poolObj.pool.id.startsWith('xai')) {
                    srcProv = 'xai';
                } else {
                    srcProv = poolObj.pool.id;
                }
                provider = srcProv;
            }
            let rawModel = document.getElementById('m-model').value.trim();
            if (isOR) {
                provider = 'openrouter';
                rawModel = generateOpenRouterModel(srcProv, rawModel);
            } else {
                rawModel = stripOpenRouterPrefix(rawModel);
            }

            const payload = {
                id: idVal,
                tier: document.getElementById('m-tier').value,
                pool_id: poolId,
                harness: document.getElementById('m-harness').value.trim(),
                provider: provider,
                overlay: '',
                model_id: rawModel,
                priority: parseInt(document.getElementById('m-priority').value, 10) || 100,
                input_cost_per_1m: poolObj ? (poolObj.pool.input_cost_per_1m || 0) : 0,
                output_cost_per_1m: poolObj ? (poolObj.pool.output_cost_per_1m || 0) : 0,
                description: document.getElementById('m-desc').value.trim(),
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
            const toggleBtn = e.target.closest('[data-toggle-routing]');
            if (toggleBtn) {
                const id = parseInt(toggleBtn.getAttribute('data-toggle-routing'), 10);
                const m = (state.models || []).find(function (x) { return x.id === id; });
                if (!m) return;
                const poolObj = (state.pools || []).find(function (st) { return st.pool.id === m.pool_id; });
                const isCurrentlyOR = m.provider === 'openrouter';
                const srcProv = poolObj && poolObj.pool.probe_source ? poolObj.pool.probe_source.split('_')[0] : (poolObj ? poolObj.pool.id : '');
                let newProvider = '';
                let newModel = m.model_id;
                if (isCurrentlyOR) {
                    newProvider = srcProv || 'direct';
                    newModel = stripOpenRouterPrefix(m.model_id);
                } else {
                    newProvider = 'openrouter';
                    newModel = generateOpenRouterModel(srcProv, m.model_id);
                }
                const updated = Object.assign({}, m, { provider: newProvider, model_id: newModel });
                try {
                    await apiFetch('/api/routing/models/' + id, { method: 'PUT', body: JSON.stringify(updated) });
                    showToast((isCurrentlyOR ? 'Direct routing for ' : 'OpenRouter routing for ') + m.harness + ' / ' + newModel);
                    await refreshState();
                } catch (err) {
                    showToast('Toggle routing failed: ' + err.message, true);
                }
                return;
            }

            const editBtn = e.target.closest('[data-edit-model]');
            if (editBtn) {
                const id = parseInt(editBtn.getAttribute('data-edit-model'), 10);
                const m = (state.models || []).find(function (x) { return x.id === id; });
                if (!m) return;
                document.getElementById('m-id').value = String(m.id);
                document.getElementById('m-tier').value = m.tier;
                document.getElementById('m-pool').value = m.pool_id;
                document.getElementById('m-harness').value = m.harness;
                document.getElementById('m-model').value = m.model_id;
                document.getElementById('m-priority').value = String(m.priority || 100);
                document.getElementById('m-desc').value = m.description || '';
                const orCb = document.getElementById('m-route-openrouter');
                if (orCb) {
                    orCb.checked = (m.provider === 'openrouter');
                }
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

        const fetchModelsBtn = document.getElementById('btn-fetch-provider-models');
        if (fetchModelsBtn) {
            fetchModelsBtn.addEventListener('click', async function () {
                const poolInput = (document.getElementById('m-pool').value || '').trim().toLowerCase();
                const found = (state.pools || []).find(function (st) { return st.pool.id === poolInput; });
                let provider = poolInput;
                if (found && found.pool.probe_source) {
                    if (found.pool.probe_source.startsWith('google')) provider = 'googleaistudio';
                    else if (found.pool.probe_source.startsWith('nvidia')) provider = 'nvidia';
                    else if (found.pool.probe_source.startsWith('chutes')) provider = 'chutes';
                }
                if (provider === 'google-ai-studio') provider = 'googleaistudio';
                if (provider === 'nvidia-free') provider = 'nvidia';
                if (!provider) {
                    showToast('Select a pool first', true);
                    return;
                }
                try {
                    const data = await apiFetch('/api/providers/' + encodeURIComponent(provider) + '/models');
                    const dl = document.getElementById('m-model-list');
                    const isOR = found && found.pool.routing_provider === 'openrouter';
                    if (dl && Array.isArray(data.models)) {
                        dl.innerHTML = '';
                        data.models.forEach(function (m) {
                            const opt = document.createElement('option');
                            const modelId = isOR ? generateOpenRouterModel(provider, m.id) : m.id;
                            opt.value = modelId;
                            opt.label = (m.name || m.id) + (isOR ? ' (via OpenRouter)' : '');
                            dl.appendChild(opt);
                        });
                    }
                    showToast('Loaded ' + (data.count || 0) + ' models from ' + provider);
                } catch (err) {
                    showToast(err.message, true);
                }
            });
        }

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
        const harnessEl = document.getElementById('m-harness');
        if (harnessEl) harnessEl.value = 'pi';
        document.getElementById('m-model').value = '';
        document.getElementById('m-desc').value = '';
        document.getElementById('m-priority').value = '100';
        if (document.getElementById('m-override-pi')) {
            document.getElementById('m-override-pi').checked = false;
        }
        document.getElementById('m-enabled').checked = true;
        onPoolChange();
    }

    initThemeToggle();
    initTabs();
    bindEvents();
    refreshState();
})();
