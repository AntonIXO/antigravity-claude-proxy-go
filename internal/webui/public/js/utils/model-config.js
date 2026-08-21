/**
 * Model Configuration Utilities
 * Shared functions for model configuration updates
 */
window.ModelConfigUtils = window.ModelConfigUtils || {};

/**
 * Update model configuration with authentication and optimistic updates
 * @param {string} modelId - The model ID to update
 * @param {object} configUpdates - Configuration updates (pinned, hidden, alias, mapping)
 * @returns {Promise<void>}
 */
window.ModelConfigUtils.updateModelConfig = async function(modelId, configUpdates) {
    return window.ErrorHandler.safeAsync(async () => {
        const store = Alpine.store('global');

        const { response, newPassword } = await window.utils.request('/api/models/config', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ modelId, config: configUpdates })
        }, store.webuiPassword);

        // Update password if server provided a new one
        if (newPassword) {
            store.webuiPassword = newPassword;
        }

        if (!response.ok) {
            throw new Error(store.t('failedToUpdateModelConfig') || 'Failed to update model config');
        }

        // Optimistic update of local state
        const dataStore = Alpine.store('data');
        dataStore.modelConfig[modelId] = {
            ...dataStore.modelConfig[modelId],
            ...configUpdates
        };

        // Recompute quota rows to reflect changes
        dataStore.computeQuotaRows();
    }, Alpine.store('global').t('failedToUpdateModelConfig') || 'Failed to update model config');
};

/**
 * Delete a model configuration / custom alias
 * @param {string} modelId - The model ID to delete
 * @returns {Promise<void>}
 */
window.ModelConfigUtils.deleteModelConfig = async function(modelId) {
    return window.ErrorHandler.safeAsync(async () => {
        const store = Alpine.store('global');

        const { response, newPassword } = await window.utils.request('/api/models/config', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ modelId, config: { delete: true } })
        }, store.webuiPassword);

        if (newPassword) {
            store.webuiPassword = newPassword;
        }

        if (!response.ok) {
            throw new Error(store.t('failedToDeleteModelAlias') || 'Failed to delete model alias');
        }

        // Remove from local store
        const dataStore = Alpine.store('data');
        delete dataStore.modelConfig[modelId];

        // Recompute quota rows
        dataStore.computeQuotaRows();
    }, Alpine.store('global').t('failedToDeleteModelAlias') || 'Failed to delete model alias');
};

/**
 * Save custom endpoints map to server
 * @param {object} customEndpoints - Full customEndpoints map
 * @returns {Promise<void>}
 */
window.ModelConfigUtils.saveCustomEndpoints = async function(customEndpoints) {
    return window.ErrorHandler.safeAsync(async () => {
        const store = Alpine.store('global');

        const { response, newPassword } = await window.utils.request('/api/config', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ customEndpoints })
        }, store.webuiPassword);

        if (newPassword) {
            store.webuiPassword = newPassword;
        }

        if (!response.ok) {
            throw new Error(store.t('failedToSaveEndpoint') || 'Failed to save custom endpoint');
        }

        const data = await response.json();
        if (data.config && data.config.customEndpoints) {
            Alpine.store('data').customEndpoints = data.config.customEndpoints;
        } else {
            Alpine.store('data').customEndpoints = customEndpoints;
        }
    }, Alpine.store('global').t('failedToSaveEndpoint') || 'Failed to save custom endpoint');
};
