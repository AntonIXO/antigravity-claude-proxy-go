/**
 * Models Component
 * Displays model quota/status list
 * Registers itself to window.Components for Alpine.js to consume
 */
window.Components = window.Components || {};

window.Components.models = () => ({
    // Color palette for per-account threshold markers
    thresholdColors: [
        { bg: '#eab308', shadow: 'rgba(234,179,8,0.5)' },    // yellow
        { bg: '#06b6d4', shadow: 'rgba(6,182,212,0.5)' },     // cyan
        { bg: '#a855f7', shadow: 'rgba(168,85,247,0.5)' },    // purple
        { bg: '#22c55e', shadow: 'rgba(34,197,94,0.5)' },     // green
        { bg: '#ef4444', shadow: 'rgba(239,68,68,0.5)' },     // red
        { bg: '#f97316', shadow: 'rgba(249,115,22,0.5)' },    // orange
        { bg: '#ec4899', shadow: 'rgba(236,72,153,0.5)' },    // pink
        { bg: '#8b5cf6', shadow: 'rgba(139,92,246,0.5)' },    // violet
    ],

    getThresholdColor(index) {
        return this.thresholdColors[index % this.thresholdColors.length];
    },

    // Drag state for threshold markers
    dragging: {
        active: false,
        email: null,
        modelId: null,
        barRect: null,
        currentPct: 0,
        originalPct: 0
    },

    // Tracks which model rows have their per-account breakdown expanded beyond the cap
    expandedModels: new Set(),

    isExpanded(modelId) {
        return this.expandedModels.has(modelId);
    },

    toggleExpanded(modelId) {
        if (this.expandedModels.has(modelId)) {
            this.expandedModels.delete(modelId);
        } else {
            this.expandedModels.add(modelId);
        }
        // Force Alpine reactivity
        this.expandedModels = new Set(this.expandedModels);
    },

    /**
     * Get visible account rows for a model's breakdown, respecting the cap
     */
    getVisibleAccounts(row) {
        const all = row.quotaInfo || [];
        if (Alpine.store('settings').showAllAccounts || this.isExpanded(row.modelId)) {
            return all;
        }
        const limit = window.AppConstants.LIMITS.ACCOUNT_BREAKDOWN_LIMIT;
        return all.slice(0, limit);
    },

    /**
     * Get the number of hidden accounts for a model row
     */
    getHiddenCount(row) {
        const all = row.quotaInfo || [];
        const limit = window.AppConstants.LIMITS.ACCOUNT_BREAKDOWN_LIMIT;
        if (Alpine.store('settings').showAllAccounts || this.isExpanded(row.modelId)) {
            return 0;
        }
        return Math.max(0, all.length - limit);
    },

    // Model editing state (from main)
    editingModelId: null,
    newMapping: '',

    isEditing(modelId) {
        return this.editingModelId === modelId;
    },

    startEditing(modelId) {
        this.editingModelId = modelId;
    },

    stopEditing() {
        this.editingModelId = null;
    },

    /**
     * Start dragging a threshold marker
     */
    startDrag(event, q, row) {
        // Find the progress bar element (closest .relative container)
        const markerEl = event.currentTarget;
        const barContainer = markerEl.parentElement;
        const barRect = barContainer.getBoundingClientRect();

        this.dragging = {
            active: true,
            email: q.fullEmail,
            modelId: row.modelId,
            barRect,
            currentPct: q.thresholdPct,
            originalPct: q.thresholdPct
        };

        // Prevent text selection while dragging
        document.body.classList.add('select-none');

        // Bind document-level listeners for smooth dragging outside the marker
        this._onDrag = (e) => this.onDrag(e);
        this._endDrag = () => this.endDrag();
        document.addEventListener('mousemove', this._onDrag);
        document.addEventListener('mouseup', this._endDrag);
        document.addEventListener('touchmove', this._onDrag, { passive: false });
        document.addEventListener('touchend', this._endDrag);
    },

    /**
     * Handle drag movement — compute percentage from mouse position
     */
    onDrag(event) {
        if (!this.dragging.active) return;
        event.preventDefault();

        const clientX = event.touches ? event.touches[0].clientX : event.clientX;
        const { left, width } = this.dragging.barRect;
        let pct = Math.round((clientX - left) / width * 100);
        pct = Math.max(0, Math.min(99, pct));

        this.dragging.currentPct = pct;
    },

    /**
     * End drag — save the new threshold value
     */
    endDrag() {
        if (!this.dragging.active) return;

        // Clean up listeners
        document.removeEventListener('mousemove', this._onDrag);
        document.removeEventListener('mouseup', this._endDrag);
        document.removeEventListener('touchmove', this._onDrag);
        document.removeEventListener('touchend', this._endDrag);
        document.body.classList.remove('select-none');

        const { email, modelId, currentPct, originalPct } = this.dragging;

        // Only save if value actually changed
        if (currentPct !== originalPct) {
            // Optimistic in-place update: mutate existing quotaInfo entries directly
            // to avoid full DOM rebuild from computeQuotaRows()
            const dataStore = Alpine.store('data');
            const account = dataStore.accounts.find(a => a.email === email);
            if (account) {
                if (!account.modelQuotaThresholds) account.modelQuotaThresholds = {};
                if (currentPct === 0) {
                    delete account.modelQuotaThresholds[modelId];
                } else {
                    account.modelQuotaThresholds[modelId] = currentPct / 100;
                }
            }
            // Patch quotaRows in-place so Alpine updates without tearing down DOM
            const rows = dataStore.quotaRows || [];
            for (const row of rows) {
                if (row.modelId !== modelId) continue;
                for (const q of row.quotaInfo) {
                    if (q.fullEmail !== email) continue;
                    q.thresholdPct = currentPct;
                }
                // Recompute row-level threshold stats
                const activePcts = row.quotaInfo.map(q => q.thresholdPct).filter(t => t > 0);
                row.effectiveThresholdPct = activePcts.length > 0 ? Math.max(...activePcts) : 0;
                row.hasVariedThresholds = new Set(activePcts).size > 1;
            }
            this.dragging.active = false;
            this.saveModelThreshold(email, modelId, currentPct);
        } else {
            this.dragging.active = false;
        }
    },

    /**
     * Save a per-model threshold for an account via PATCH
     */
    async saveModelThreshold(email, modelId, pct) {
        const store = Alpine.store('global');
        const dataStore = Alpine.store('data');

        const account = dataStore.accounts.find(a => a.email === email);
        if (!account) return;

        // Snapshot for rollback on failure
        const previousModelThresholds = account.modelQuotaThresholds ? { ...account.modelQuotaThresholds } : {};

        // Build full modelQuotaThresholds for API (full replacement, not merge)
        const existingModelThresholds = { ...(account.modelQuotaThresholds || {}) };

        // Preserve the account-level quotaThreshold
        const quotaThreshold = account.quotaThreshold !== undefined ? account.quotaThreshold : null;

        try {
            const { response, newPassword } = await window.utils.request(
                `/api/accounts/${encodeURIComponent(email)}`,
                {
                    method: 'PATCH',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ quotaThreshold, modelQuotaThresholds: existingModelThresholds })
                },
                store.webuiPassword
            );
            if (newPassword) store.webuiPassword = newPassword;

            const data = await response.json();
            if (data.status === 'ok') {
                const label = pct === 0 ? 'removed' : pct + '%';
                store.showToast(`${email.split('@')[0]} ${modelId} threshold: ${label}`, 'success');
                // Skip fetchData() — optimistic update is already applied,
                // next polling cycle will sync server state
            } else {
                throw new Error(data.error || 'Failed to save threshold');
            }
        } catch (e) {
            // Revert optimistic update on failure
            account.modelQuotaThresholds = previousModelThresholds;
            dataStore.computeQuotaRows();
            store.showToast('Failed to save threshold: ' + e.message, 'error');
        }
    },

    /**
     * Check if a specific marker is currently being dragged
     */
    isDragging(q, row) {
        return this.dragging.active && this.dragging.email === q.fullEmail && this.dragging.modelId === row.modelId;
    },

    /**
     * Get the display percentage for a marker (live during drag, stored otherwise)
     */
    getMarkerPct(q, row) {
        if (this.isDragging(q, row)) return this.dragging.currentPct;
        return q.thresholdPct;
    },

    /**
     * Compute pixel offset for overlapping markers so stacked ones fan out.
     * Markers within 2% of each other are considered overlapping.
     * Returns a CSS pixel offset string (e.g., '6px' or '-6px').
     */
    getMarkerOffset(q, row, qIdx) {
        const pct = this.getMarkerPct(q, row);
        const visible = row.quotaInfo.filter(item => item.thresholdPct > 0 || this.isDragging(item, row));
        // Find all markers within 2% of this one
        const cluster = [];
        visible.forEach((item, idx) => {
            const itemPct = this.getMarkerPct(item, row);
            if (Math.abs(itemPct - pct) <= 2) {
                cluster.push({ item, idx });
            }
        });
        if (cluster.length <= 1) return '0px';
        // Find position of this marker within its cluster
        const posInCluster = cluster.findIndex(c => c.item.fullEmail === q.fullEmail);
        // Spread markers 10px apart, centered on the base position
        const spread = 10;
        const totalWidth = (cluster.length - 1) * spread;
        return (posInCluster * spread - totalWidth / 2) + 'px';
    },

    init() {
        // Ensure data is fetched when this tab becomes active (skip initial trigger)
        this.$watch('$store.global.activeTab', (val, oldVal) => {
            if (val === 'models' && oldVal !== undefined) {
                // Trigger recompute to ensure filters are applied
                this.$nextTick(() => {
                    Alpine.store('data').computeQuotaRows();
                });
            }
        });

        this.$watch('$store.global.settingsTab', (val) => {
            if (val === 'models') {
                this.fetchOpenRouterConfig();
            }
        });

        // Initial compute if already on models tab
        if (this.$store.global.activeTab === 'models') {
            this.$nextTick(() => {
                Alpine.store('data').computeQuotaRows();
            });
        }
        if (this.$store.global.settingsTab === 'models') {
            this.fetchOpenRouterConfig();
        }
    },

    // OpenRouter State & Methods
    openRouterConfig: {
        enabled: false,
        baseUrl: 'https://openrouter.ai/api',
        apiKey: '',
        hasApiKey: false,
        allowlist: []
    },
    openRouterSaving: false,
    openRouterError: '',
    discoveredModels: [],
    discoveryLoading: false,
    discoveryError: '',
    discoverySearch: '',
    discoveryProvider: 'all',
    addAllowlistAliasMap: {},

    async fetchOpenRouterConfig() {
        const password = Alpine.store('global').webuiPassword;
        try {
            const { response, newPassword } = await window.utils.request('/api/openrouter/config', {}, password);
            if (newPassword) Alpine.store('global').webuiPassword = newPassword;
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const data = await response.json();
            if (data.config) {
                this.openRouterConfig = {
                    enabled: !!data.config.enabled,
                    baseUrl: data.config.baseUrl || 'https://openrouter.ai/api',
                    apiKey: '',
                    hasApiKey: !!data.config.hasApiKey,
                    allowlist: data.config.allowlist || []
                };
                Alpine.store('data').openrouter = this.openRouterConfig;
            }
        } catch (e) {
            console.error('Failed to fetch OpenRouter config:', e);
        }
    },

    async saveOpenRouterConfig() {
        const store = Alpine.store('global');
        const password = store.webuiPassword;
        this.openRouterSaving = true;
        this.openRouterError = '';
        try {
            const payload = {
                enabled: this.openRouterConfig.enabled,
                baseUrl: this.openRouterConfig.baseUrl,
                hasApiKey: this.openRouterConfig.hasApiKey,
                allowlist: this.openRouterConfig.allowlist || []
            };
            if (this.openRouterConfig.apiKey && this.openRouterConfig.apiKey.trim()) {
                payload.apiKey = this.openRouterConfig.apiKey.trim();
            }
            const { response, newPassword } = await window.utils.request('/api/openrouter/config', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            }, password);
            if (newPassword) store.webuiPassword = newPassword;
            if (!response.ok) {
                const errData = await response.json().catch(() => ({}));
                throw new Error(errData.error || `HTTP ${response.status}`);
            }
            const data = await response.json();
            if (data.config) {
                this.openRouterConfig.hasApiKey = !!data.config.hasApiKey;
                if (payload.apiKey) {
                    this.openRouterConfig.apiKey = '';
                }
                Alpine.store('data').openrouter = this.openRouterConfig;
            }
            store.showToast(store.t('openRouterSavedSuccess') || 'OpenRouter settings saved', 'success');
        } catch (e) {
            this.openRouterError = e.message || 'Failed to save OpenRouter settings';
            store.showToast(this.openRouterError, 'error');
        } finally {
            this.openRouterSaving = false;
        }
    },

    async openDiscoverModal() {
        this.discoveryError = '';
        const dialog = document.getElementById('openrouter_discover_modal');
        if (dialog && typeof dialog.showModal === 'function') {
            dialog.showModal();
        }
        if (!this.discoveredModels || this.discoveredModels.length === 0) {
            await this.fetchDiscoveredModels(false);
        }
    },

    closeDiscoverModal() {
        const dialog = document.getElementById('openrouter_discover_modal');
        if (dialog && typeof dialog.close === 'function') {
            dialog.close();
        }
    },

    async fetchDiscoveredModels(forceFresh = false) {
        this.discoveryLoading = true;
        this.discoveryError = '';
        const password = Alpine.store('global').webuiPassword;

        try {
            if (!forceFresh) {
                const { response: cachedResp } = await window.utils.request('/api/openrouter/models/cached', {}, password);
                if (cachedResp.ok) {
                    const cachedData = await cachedResp.json();
                    if (cachedData.models && cachedData.models.length > 0) {
                        this.discoveredModels = cachedData.models;
                        this.discoveryLoading = false;
                        return;
                    }
                }
            }

            const payload = {
                baseUrl: this.openRouterConfig.baseUrl
            };
            if (this.openRouterConfig.apiKey) {
                payload.apiKey = this.openRouterConfig.apiKey;
            }

            const { response, newPassword } = await window.utils.request('/api/openrouter/models/fetch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            }, password);
            if (newPassword) Alpine.store('global').webuiPassword = newPassword;

            if (!response.ok) {
                const errData = await response.json().catch(() => ({}));
                throw new Error(errData.error || `HTTP ${response.status}`);
            }

            const data = await response.json();
            this.discoveredModels = data.models || [];
        } catch (e) {
            this.discoveryError = e.message || 'Failed to fetch models from OpenRouter';
        } finally {
            this.discoveryLoading = false;
        }
    },

    get filteredDiscoveredModels() {
        let list = this.discoveredModels || [];
        if (this.discoveryProvider !== 'all') {
            const prov = this.discoveryProvider.toLowerCase();
            list = list.filter(m => m.id.toLowerCase().startsWith(prov + '/'));
        }
        if (this.discoverySearch && this.discoverySearch.trim()) {
            const q = this.discoverySearch.trim().toLowerCase();
            list = list.filter(m =>
                (m.id && m.id.toLowerCase().includes(q)) ||
                (m.name && m.name.toLowerCase().includes(q)) ||
                (m.description && m.description.toLowerCase().includes(q))
            );
        }
        return list;
    },

    isAllowlisted(modelId) {
        return (this.openRouterConfig.allowlist || []).some(m => m.id === modelId);
    },

    async addToAllowlist(discoveredModel, alias = '') {
        if (this.isAllowlisted(discoveredModel.id)) return;
        const newItem = {
            id: discoveredModel.id,
            alias: (alias || '').trim(),
            displayName: discoveredModel.name || discoveredModel.id,
            contextLength: discoveredModel.context_length || 0,
            enabled: true
        };
        this.openRouterConfig.allowlist.push(newItem);
        await this.saveOpenRouterConfig();
    },

    async removeFromAllowlist(modelId) {
        const store = Alpine.store('global');
        if (!confirm(store.t('confirmRemoveAllowlist') || `Remove ${modelId} from allowlist?`)) return;
        this.openRouterConfig.allowlist = (this.openRouterConfig.allowlist || []).filter(m => m.id !== modelId);
        await this.saveOpenRouterConfig();
    },

    async toggleAllowlistModel(modelId) {
        const item = (this.openRouterConfig.allowlist || []).find(m => m.id === modelId);
        if (item) {
            item.enabled = !item.enabled;
            await this.saveOpenRouterConfig();
        }
    },

    async updateAllowlistAlias(modelId, alias) {
        const item = (this.openRouterConfig.allowlist || []).find(m => m.id === modelId);
        if (item) {
            item.alias = (alias || '').trim();
            await this.saveOpenRouterConfig();
        }
    },

    /**
     * Update model configuration (delegates to shared utility)
     * @param {string} modelId - The model ID to update
     * @param {object} configUpdates - Configuration updates (pinned, hidden)
     */
    async updateModelConfig(modelId, configUpdates) {
        return window.ModelConfigUtils.updateModelConfig(modelId, configUpdates);
    },

    // Custom Endpoints state and methods
    endpointModal: {
        show: false,
        isEdit: false,
        modelId: '',
        url: '',
        apiKey: '',
        hasApiKey: false,
        saving: false,
        error: ''
    },

    openAddEndpointModal() {
        this.endpointModal = {
            show: true,
            isEdit: false,
            modelId: '',
            url: '',
            apiKey: '',
            hasApiKey: false,
            saving: false,
            error: ''
        };
        const dialog = document.getElementById('custom_endpoint_modal');
        if (dialog && typeof dialog.showModal === 'function') {
            dialog.showModal();
        }
    },

    openEditEndpointModal(modelId, ep) {
        this.endpointModal = {
            show: true,
            isEdit: true,
            modelId: modelId,
            url: ep?.url || '',
            apiKey: '',
            hasApiKey: !!ep?.hasApiKey,
            saving: false,
            error: ''
        };
        const dialog = document.getElementById('custom_endpoint_modal');
        if (dialog && typeof dialog.showModal === 'function') {
            dialog.showModal();
        }
    },

    closeEndpointModal() {
        this.endpointModal.show = false;
        const dialog = document.getElementById('custom_endpoint_modal');
        if (dialog && typeof dialog.close === 'function') {
            dialog.close();
        }
    },

    async saveEndpoint() {
        const { modelId, url, apiKey, hasApiKey, isEdit } = this.endpointModal;
        const store = Alpine.store('global');
        const dataStore = Alpine.store('data');

        const cleanModelId = (modelId || '').trim();
        const cleanUrl = (url || '').trim();

        if (!cleanModelId) {
            this.endpointModal.error = store.t('modelIdRequired') || 'Model ID is required';
            return;
        }
        if (!cleanUrl) {
            this.endpointModal.error = store.t('urlRequired') || 'Endpoint URL is required';
            return;
        }

        this.endpointModal.saving = true;
        this.endpointModal.error = '';

        try {
            const currentEndpoints = { ...(dataStore.customEndpoints || {}) };
            const epData = {
                url: cleanUrl
            };
            if (apiKey && apiKey.trim()) {
                epData.apiKey = apiKey.trim();
            } else if (hasApiKey && isEdit) {
                epData.hasApiKey = true;
                epData.apiKey = '';
            }

            currentEndpoints[cleanModelId] = epData;

            await window.ModelConfigUtils.saveCustomEndpoints(currentEndpoints);
            store.showToast(store.t('endpointSavedSuccess') || `Endpoint for ${cleanModelId} saved`, 'success');
            this.closeEndpointModal();
        } catch (e) {
            this.endpointModal.error = e.message || 'Failed to save endpoint';
        } finally {
            this.endpointModal.saving = false;
        }
    },

    async deleteEndpoint(modelId) {
        const store = Alpine.store('global');
        const dataStore = Alpine.store('data');

        if (!confirm(store.t('confirmDeleteEndpoint') || `Are you sure you want to remove the custom endpoint for ${modelId}?`)) {
            return;
        }

        try {
            const currentEndpoints = { ...(dataStore.customEndpoints || {}) };
            delete currentEndpoints[modelId];

            await window.ModelConfigUtils.saveCustomEndpoints(currentEndpoints);
            store.showToast(store.t('endpointDeletedSuccess') || `Endpoint for ${modelId} removed`, 'success');
        } catch (e) {
            store.showToast((store.t('failedToDeleteEndpoint') || 'Failed to delete endpoint') + ': ' + e.message, 'error');
        }
    },

    // Model Alias state and methods
    aliasModal: {
        show: false,
        sourceModel: '',
        targetModel: '',
        saving: false,
        error: ''
    },

    openAddAliasModal() {
        this.aliasModal = {
            show: true,
            sourceModel: '',
            targetModel: '',
            saving: false,
            error: ''
        };
        const dialog = document.getElementById('add_model_alias_modal');
        if (dialog && typeof dialog.showModal === 'function') {
            dialog.showModal();
        }
    },

    closeAliasModal() {
        this.aliasModal.show = false;
        const dialog = document.getElementById('add_model_alias_modal');
        if (dialog && typeof dialog.close === 'function') {
            dialog.close();
        }
    },

    async saveAlias() {
        const { sourceModel, targetModel } = this.aliasModal;
        const store = Alpine.store('global');

        const cleanSource = (sourceModel || '').trim();
        const cleanTarget = (targetModel || '').trim();

        if (!cleanSource) {
            this.aliasModal.error = store.t('sourceModelRequired') || 'Source Model ID is required';
            return;
        }
        if (!cleanTarget) {
            this.aliasModal.error = store.t('targetModelRequired') || 'Target Model ID is required';
            return;
        }

        this.aliasModal.saving = true;
        this.aliasModal.error = '';

        try {
            await window.ModelConfigUtils.updateModelConfig(cleanSource, { mapping: cleanTarget });
            store.showToast(store.t('aliasSavedSuccess') || `Alias ${cleanSource} -> ${cleanTarget} saved`, 'success');
            this.closeAliasModal();
        } catch (e) {
            this.aliasModal.error = e.message || 'Failed to save alias';
        } finally {
            this.aliasModal.saving = false;
        }
    },

    async deleteModelAlias(modelId) {
        const store = Alpine.store('global');

        if (!confirm(store.t('confirmDeleteAlias') || `Are you sure you want to remove model alias ${modelId}?`)) {
            return;
        }

        try {
            await window.ModelConfigUtils.deleteModelConfig(modelId);
            store.showToast(store.t('aliasDeletedSuccess') || `Alias ${modelId} removed`, 'success');
        } catch (e) {
            store.showToast((store.t('failedToDeleteModelAlias') || 'Failed to delete model alias') + ': ' + e.message, 'error');
        }
    },

    /**
     * Get list of all models (discovered + custom alias keys)
     */
    get allConfiguredModels() {
        const dataStore = Alpine.store('data');
        const modelSet = new Set(dataStore.models || []);

        if (dataStore.modelConfig) {
            Object.keys(dataStore.modelConfig).forEach(m => modelSet.add(m));
        }

        return Array.from(modelSet).sort();
    },

    /**
     * Get list of custom endpoints as an array
     */
    get customEndpointsList() {
        const dataStore = Alpine.store('data');
        const endpoints = dataStore.customEndpoints || {};
        return Object.keys(endpoints).map(modelId => ({
            modelId,
            ...endpoints[modelId]
        }));
    },

    /**
     * Check if a model is a purely custom alias (not in discovered cloudcode models)
     */
    isCustomAlias(modelId) {
        const dataStore = Alpine.store('data');
        const discovered = dataStore.models || [];
        return !discovered.includes(modelId);
    }
});
