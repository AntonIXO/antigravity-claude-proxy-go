package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"antigravity-go-proxy/internal/accounts"
	"antigravity-go-proxy/internal/config"
)

func (server *Server) checkWebUIPassword(request *http.Request) bool {
	cfg := config.Get()
	password := cfg.WebUIPassword
	if password == "" {
		return true
	}
	provided := request.Header.Get("x-webui-password")
	if provided == "" {
		provided = request.URL.Query().Get("password")
	}
	return provided == password
}

func (server *Server) handleManagement(writer http.ResponseWriter, request *http.Request, path string) bool {
	method := request.Method

	// Claude Code event logging swallow
	if path == "/api/event_logging/batch" && method == http.MethodPost {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
		return true
	}

	// WebUI Password protection for protected routes
	isPublicAuthRoute := path == "/api/auth/url"
	isPublicConfigGet := path == "/api/config" && method == http.MethodGet
	if strings.HasPrefix(path, "/api/") && !isPublicAuthRoute && !isPublicConfigGet {
		if !server.checkWebUIPassword(request) {
			writeJSON(writer, http.StatusUnauthorized, map[string]any{"status": "error", "error": "Unauthorized: Password required"})
			return true
		}
	}
	if path == "/account-limits" && !server.checkWebUIPassword(request) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"status": "error", "error": "Unauthorized: Password required"})
		return true
	}

	// Dispatch routes
	switch {
	case path == "/health" && method == http.MethodGet:
		server.handleHealth(writer, request)
		return true
	case path == "/account-limits" && method == http.MethodGet:
		server.handleAccountLimits(writer, request)
		return true
	case path == "/api/accounts" && method == http.MethodGet:
		server.handleAccountsList(writer, request)
		return true
	case strings.HasPrefix(path, "/api/accounts/") && strings.HasSuffix(path, "/refresh") && method == http.MethodPost:
		email := strings.TrimSuffix(strings.TrimPrefix(path, "/api/accounts/"), "/refresh")
		server.handleAccountRefresh(writer, request, email)
		return true
	case strings.HasPrefix(path, "/api/accounts/") && strings.HasSuffix(path, "/toggle") && method == http.MethodPost:
		email := strings.TrimSuffix(strings.TrimPrefix(path, "/api/accounts/"), "/toggle")
		server.handleAccountToggle(writer, request, email)
		return true
	case strings.HasPrefix(path, "/api/accounts/") && method == http.MethodDelete:
		email := strings.TrimPrefix(path, "/api/accounts/")
		server.handleAccountDelete(writer, request, email)
		return true
	case strings.HasPrefix(path, "/api/accounts/") && method == http.MethodPatch:
		email := strings.TrimPrefix(path, "/api/accounts/")
		server.handleAccountPatch(writer, request, email)
		return true
	case (path == "/api/accounts/reload" || path == "/refresh-token") && method == http.MethodPost:
		server.handleAccountsReload(writer, request)
		return true
	case path == "/api/accounts/export" && method == http.MethodGet:
		server.handleAccountsExport(writer, request)
		return true
	case path == "/api/accounts/import" && method == http.MethodPost:
		server.handleAccountsImport(writer, request)
		return true
	case path == "/api/config" && method == http.MethodGet:
		server.handleConfigGet(writer, request)
		return true
	case path == "/api/config" && method == http.MethodPost:
		server.handleConfigSave(writer, request)
		return true
	case path == "/api/config/password" && method == http.MethodPost:
		server.handleConfigPassword(writer, request)
		return true
	case path == "/api/settings" && method == http.MethodGet:
		server.handleSettingsGet(writer, request)
		return true
	case path == "/api/claude/config" && method == http.MethodGet:
		server.handleClaudeConfigGet(writer, request)
		return true
	case path == "/api/claude/config" && method == http.MethodPost:
		server.handleClaudeConfigUpdate(writer, request)
		return true
	case path == "/api/claude/config/restore" && method == http.MethodPost:
		server.handleClaudeConfigRestore(writer, request)
		return true
	case path == "/api/claude/mode" && method == http.MethodGet:
		server.handleClaudeModeGet(writer, request)
		return true
	case path == "/api/claude/mode" && method == http.MethodPost:
		server.handleClaudeModeSet(writer, request)
		return true
	case path == "/api/claude/presets" && method == http.MethodGet:
		server.handleClaudePresetsGet(writer, request)
		return true
	case path == "/api/claude/presets" && method == http.MethodPost:
		server.handleClaudePresetsSave(writer, request)
		return true
	case strings.HasPrefix(path, "/api/claude/presets/") && method == http.MethodDelete:
		name := strings.TrimPrefix(path, "/api/claude/presets/")
		server.handleClaudePresetsDelete(writer, request, name)
		return true
	case path == "/api/server/presets" && method == http.MethodGet:
		server.handleServerPresetsGet(writer, request)
		return true
	case path == "/api/server/presets" && method == http.MethodPost:
		server.handleServerPresetsSave(writer, request)
		return true
	case strings.HasPrefix(path, "/api/server/presets/") && method == http.MethodPatch:
		name := strings.TrimPrefix(path, "/api/server/presets/")
		server.handleServerPresetsPatch(writer, request, name)
		return true
	case strings.HasPrefix(path, "/api/server/presets/") && method == http.MethodDelete:
		name := strings.TrimPrefix(path, "/api/server/presets/")
		server.handleServerPresetsDelete(writer, request, name)
		return true
	case path == "/api/models/config" && method == http.MethodPost:
		server.handleModelsConfigPost(writer, request)
		return true
	case path == "/api/strategy/health" && method == http.MethodGet:
		server.handleStrategyHealthGet(writer, request)
		return true
	case path == "/api/stats/history" && method == http.MethodGet:
		server.handleStatsHistory(writer, request)
		return true
	case path == "/api/logs" && method == http.MethodGet:
		server.handleLogsGet(writer, request)
		return true
	case path == "/api/logs/stream" && method == http.MethodGet:
		server.handleLogsStream(writer, request)
		return true
	case path == "/api/auth/url" && method == http.MethodGet:
		server.handleAuthURLGet(writer, request)
		return true
	case path == "/api/auth/complete" && method == http.MethodPost:
		server.handleAuthCompletePost(writer, request)
		return true
	}

	return false
}

func (server *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if server.accountManager != nil {
		status := server.accountManager.GetStatus()
		writeJSON(writer, http.StatusOK, map[string]any{
			"status":    "ok",
			"timestamp": server.now().UTC().Format(time.RFC3339Nano),
			"accounts":  status,
		})
		return
	}
	server.health(writer)
}

func (server *Server) handleAccountLimits(writer http.ResponseWriter, request *http.Request) {
	cfg := config.Get()
	includeHistory := request.URL.Query().Get("includeHistory") == "true"
	if server.accountManager == nil {
		modelMapping := cfg.ModelMapping
		if modelMapping == nil {
			modelMapping = make(map[string]any)
		}
		publicCfg := config.GetPublicConfig()
		res := map[string]any{
			"status":               "ok",
			"timestamp":            server.now().UTC().Format(time.RFC3339Nano),
			"totalAccounts":        0,
			"models":               []string{},
			"modelConfig":          modelMapping,
			"customEndpoints":      publicCfg["customEndpoints"],
			"globalQuotaThreshold": cfg.GlobalQuotaThreshold,
			"accounts":             []any{},
		}
		if includeHistory && server.tracker != nil {
			res["history"] = server.tracker.GetHistory()
		}
		writeJSON(writer, http.StatusOK, res)
		return
	}

	accountsList := server.accountManager.GetAllAccounts()
	format := request.URL.Query().Get("format")

	if format == "table" {
		var buf bytes.Buffer
		w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "EMAIL\tSTATUS\tTIER\tPROJECT ID\tRATE LIMITS")
		for _, acc := range accountsList {
			status := "enabled"
			if !acc.Enabled {
				status = "disabled"
			} else if acc.IsInvalid {
				status = "invalid"
			}
			tier := acc.Subscription.Tier
			if tier == "" {
				tier = "unknown"
			}
			projectID := acc.ProjectID
			if projectID == "" {
				projectID = "-"
			}
			rlCount := 0
			now := server.now().UnixMilli()
			for _, rl := range acc.ModelRateLimits {
				if rl != nil && rl.IsRateLimited && rl.ResetTimeMS > now {
					rlCount++
				}
			}
			rlStr := "none"
			if rlCount > 0 {
				rlStr = fmt.Sprintf("%d active", rlCount)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", acc.Email, status, tier, projectID, rlStr)
		}
		w.Flush()
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		writer.Write(buf.Bytes())
		return
	}

	modelSet := make(map[string]bool)
	if catalog, err := server.fetchModelCatalog(request.Context()); err == nil && catalog != nil {
		for _, m := range catalog.Selectable() {
			modelSet[m.ID] = true
		}
	}
	for _, acc := range accountsList {
		for m := range acc.Quota.Models {
			modelSet[m] = true
		}
		for m := range acc.ModelRateLimits {
			modelSet[m] = true
		}
	}
	sortedModels := make([]string, 0, len(modelSet))
	for m := range modelSet {
		sortedModels = append(sortedModels, m)
	}
	sort.Strings(sortedModels)

	result := make([]map[string]any, 0, len(accountsList))
	now := server.now().UnixMilli()
	for _, acc := range accountsList {
		rateLimits := make(map[string]any)
		for model, rl := range acc.ModelRateLimits {
			if rl != nil && rl.IsRateLimited && rl.ResetTimeMS > now {
				rateLimits[model] = map[string]any{
					"isRateLimited": true,
					"waitMs":        rl.ResetTimeMS - now,
					"actualResetMs": rl.ActualResetMS,
				}
			}
		}

		limits := make(map[string]any, len(sortedModels))
		for _, modelId := range sortedModels {
			q, exists := acc.Quota.Models[modelId]
			if !exists {
				limits[modelId] = nil
				continue
			}
			remStr := "N/A"
			if q.RemainingFraction != nil {
				remStr = fmt.Sprintf("%d%%", int(*q.RemainingFraction*100))
			}
			limits[modelId] = map[string]any{
				"remaining":         remStr,
				"remainingFraction": q.RemainingFraction,
				"resetTime":         q.ResetTime,
			}
		}

		status := "ok"
		if !acc.Enabled {
			status = "disabled"
		} else if acc.IsInvalid {
			status = "invalid"
		}

		result = append(result, map[string]any{
			"email":                acc.Email,
			"status":               status,
			"error":                acc.InvalidReason,
			"source":               acc.Source,
			"enabled":              acc.Enabled,
			"projectId":            acc.ProjectID,
			"isInvalid":            acc.IsInvalid,
			"invalidReason":        acc.InvalidReason,
			"verifyUrl":            acc.VerifyURL,
			"lastUsed":             acc.LastUsedMS,
			"subscription":         acc.Subscription,
			"quota":                acc.Quota,
			"rateLimits":           rateLimits,
			"modelRateLimits":      acc.ModelRateLimits,
			"limits":               limits,
			"quotaThreshold":       acc.QuotaThreshold,
			"modelQuotaThresholds": acc.ModelThreshold,
		})
	}

	modelMapping := cfg.ModelMapping
	if modelMapping == nil {
		modelMapping = make(map[string]any)
	}

	publicCfg := config.GetPublicConfig()
	res := map[string]any{
		"status":               "ok",
		"timestamp":            server.now().UTC().Format(time.RFC3339Nano),
		"totalAccounts":        len(accountsList),
		"models":               sortedModels,
		"modelConfig":          modelMapping,
		"customEndpoints":      publicCfg["customEndpoints"],
		"globalQuotaThreshold": cfg.GlobalQuotaThreshold,
		"accounts":             result,
	}
	if includeHistory && server.tracker != nil {
		res["history"] = server.tracker.GetHistory()
	}

	writeJSON(writer, http.StatusOK, res)
}

func (server *Server) handleAccountsList(writer http.ResponseWriter, request *http.Request) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusOK, map[string]any{
			"status":   "ok",
			"accounts": []any{},
			"summary": map[string]any{
				"total": 0, "available": 0, "rateLimited": 0, "invalid": 0,
			},
		})
		return
	}
	status := server.accountManager.GetStatus()
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":   "ok",
		"accounts": status["accounts"],
		"summary": map[string]any{
			"total":       status["total"],
			"available":   status["available"],
			"rateLimited": status["rateLimited"],
			"invalid":     status["invalid"],
		},
	})
}

func (server *Server) handleAccountRefresh(writer http.ResponseWriter, request *http.Request, email string) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": "No account manager"})
		return
	}
	server.accountManager.ClearTokenCache(email)
	server.accountManager.ClearProjectCache(email)

	// If account had verification required URL, clear isInvalid on manual user refresh
	for _, acc := range server.accountManager.GetAllAccounts() {
		if acc.Email == email && acc.IsInvalid && acc.VerifyURL != "" {
			server.accountManager.ClearInvalid(email)
			break
		}
	}

	if refresher, ok := server.backend.(AccountRefresher); ok {
		acc, err := refresher.RefreshAccount(request.Context(), email)
		if err != nil {
			server.logger.Warn("refresh account upstream failed", "email", email, "error", err)
			writeJSON(writer, http.StatusOK, map[string]any{
				"status":  "ok",
				"message": fmt.Sprintf("Token cache cleared for %s (upstream refresh failed: %v)", email, err),
			})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": fmt.Sprintf("Account %s refreshed successfully", email),
			"account": acc,
		})
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": fmt.Sprintf("Token cache cleared for %s", email),
	})
}

func (server *Server) handleAccountToggle(writer http.ResponseWriter, request *http.Request, email string) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": "No account manager"})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Enabled == nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "enabled must be a boolean"})
		return
	}

	if err := server.accountManager.SetAccountEnabled(email, *body.Enabled); err != nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": err.Error()})
		return
	}

	state := "disabled"
	if *body.Enabled {
		state = "enabled"
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": fmt.Sprintf("Account %s %s", email, state),
	})
}

func (server *Server) handleAccountDelete(writer http.ResponseWriter, request *http.Request, email string) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": "No account manager"})
		return
	}
	if err := server.accountManager.RemoveAccount(email); err != nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": fmt.Sprintf("Account %s removed", email),
	})
}

func (server *Server) handleAccountPatch(writer http.ResponseWriter, request *http.Request, email string) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": "No account manager"})
		return
	}
	var body struct {
		QuotaThreshold       *float64           `json:"quotaThreshold"`
		ModelQuotaThresholds map[string]float64 `json:"modelQuotaThresholds"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "Invalid request body"})
		return
	}

	if body.QuotaThreshold != nil && (*body.QuotaThreshold < 0 || *body.QuotaThreshold >= 1) {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "quotaThreshold must be 0-0.99 or null"})
		return
	}
	for model, th := range body.ModelQuotaThresholds {
		if th < 0 || th >= 1 {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": fmt.Sprintf("Invalid threshold for model %s: must be 0-0.99", model)})
			return
		}
	}

	if err := server.accountManager.UpdateThresholds(email, body.QuotaThreshold, body.ModelQuotaThresholds); err != nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": err.Error()})
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": fmt.Sprintf("Account %s thresholds updated", email),
	})
}

func (server *Server) handleAccountsReload(writer http.ResponseWriter, request *http.Request) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": "No account manager"})
		return
	}
	if err := server.accountManager.Reload(""); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	status := server.accountManager.GetStatus()
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": "Accounts reloaded from disk",
		"summary": status["summary"],
	})
}

func (server *Server) handleAccountsExport(writer http.ResponseWriter, request *http.Request) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusOK, []any{})
		return
	}
	accountsList := server.accountManager.GetAllAccounts()
	result := make([]map[string]any, 0, len(accountsList))
	for _, acc := range accountsList {
		if acc.Source == "database" {
			continue
		}
		item := map[string]any{"email": acc.Email}
		if acc.RefreshToken != "" {
			item["refresh_token"] = acc.RefreshToken
		}
		if acc.APIKey != "" {
			item["api_key"] = acc.APIKey
		}
		result = append(result, item)
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) handleAccountsImport(writer http.ResponseWriter, request *http.Request) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "error", "error": "No account manager"})
		return
	}
	var rawData json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&rawData); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "Invalid JSON"})
		return
	}

	var importList []map[string]any
	var wrapper struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(rawData, &wrapper); err == nil && len(wrapper.Accounts) > 0 {
		importList = wrapper.Accounts
	} else if err := json.Unmarshal(rawData, &importList); err != nil || len(importList) == 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "accounts must be a non-empty array"})
		return
	}

	results := map[string][]any{
		"added":   {},
		"updated": {},
		"failed":  {},
	}

	existingMap := make(map[string]bool)
	for _, acc := range server.accountManager.GetAllAccounts() {
		existingMap[acc.Email] = true
	}

	for _, item := range importList {
		email, _ := item["email"].(string)
		if email == "" {
			results["failed"] = append(results["failed"], map[string]string{"email": "unknown", "reason": "Missing email"})
			continue
		}
		refreshToken, _ := item["refresh_token"].(string)
		if refreshToken == "" {
			refreshToken, _ = item["refreshToken"].(string)
		}
		apiKey, _ := item["api_key"].(string)
		if apiKey == "" {
			apiKey, _ = item["apiKey"].(string)
		}

		if refreshToken == "" && apiKey == "" {
			results["failed"] = append(results["failed"], map[string]string{"email": email, "reason": "Missing refresh_token or api_key"})
			continue
		}

		source := "oauth"
		if apiKey != "" {
			source = "manual"
		}
		acc := &accounts.Account{
			Email:        email,
			Source:       source,
			RefreshToken: refreshToken,
			APIKey:       apiKey,
			Enabled:      true,
		}
		exists := existingMap[email]
		if err := server.accountManager.AddOrUpdateAccount(acc); err != nil {
			results["failed"] = append(results["failed"], map[string]string{"email": email, "reason": err.Error()})
		} else {
			if exists {
				results["updated"] = append(results["updated"], email)
			} else {
				results["added"] = append(results["added"], email)
			}
		}
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"results": results,
		"message": fmt.Sprintf("Imported %d accounts", len(results["added"])+len(results["updated"])),
	})
}

func (server *Server) handleConfigGet(writer http.ResponseWriter, request *http.Request) {
	publicCfg := config.GetPublicConfig()
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"config":  publicCfg,
		"version": "1.0.0",
		"note":    "Edit ~/.config/antigravity-proxy/config.json or use env vars to change these values",
	})
}

func (server *Server) handleConfigSave(writer http.ResponseWriter, request *http.Request) {
	var updates map[string]any
	if err := json.NewDecoder(request.Body).Decode(&updates); err != nil || len(updates) == 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "No valid configuration updates provided"})
		return
	}

	updated, err := config.Save(updates)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}

	if server.accountManager != nil {
		server.accountManager.SetSelectionConfig(updated.AccountSelection, updated.GlobalQuotaThreshold)
	}
	if updater, ok := server.backend.(ConfigUpdater); ok {
		updater.UpdateConfig(updated)
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": "Configuration saved",
		"updates": updates,
		"config":  config.GetPublicConfig(),
	})
}

func (server *Server) handleConfigPassword(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.NewPassword == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "New password is required"})
		return
	}

	current := config.Get()
	if current.WebUIPassword != "" && current.WebUIPassword != body.OldPassword {
		writeJSON(writer, http.StatusForbidden, map[string]any{"status": "error", "error": "Invalid current password"})
		return
	}

	if _, err := config.Save(map[string]any{"webuiPassword": body.NewPassword}); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": "Failed to save password"})
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": "Password changed successfully",
	})
}

func (server *Server) handleSettingsGet(writer http.ResponseWriter, request *http.Request) {
	settings := make(map[string]any)
	if server.accountManager != nil {
		settings = server.accountManager.GetSettings()
	}
	settings["port"] = 8080
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":   "ok",
		"settings": settings,
	})
}

func (server *Server) handleClaudeConfigGet(writer http.ResponseWriter, request *http.Request) {
	claudeCfg, err := config.ReadClaudeConfig()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	path, _ := config.ClaudeConfigPath()
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok",
		"config": claudeCfg,
		"path":   path,
	})
}

func (server *Server) handleClaudeConfigUpdate(writer http.ResponseWriter, request *http.Request) {
	var updates map[string]any
	if err := json.NewDecoder(request.Body).Decode(&updates); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "Invalid config updates"})
		return
	}
	newCfg, err := config.UpdateClaudeConfig(updates)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"config":  newCfg,
		"message": "Claude configuration updated",
	})
}

func (server *Server) handleClaudeConfigRestore(writer http.ResponseWriter, request *http.Request) {
	newCfg, err := config.RestoreClaudeConfig()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"config":  newCfg,
		"message": "Claude CLI configuration restored to defaults",
	})
}

func (server *Server) handleClaudeModeGet(writer http.ResponseWriter, request *http.Request) {
	claudeCfg, _ := config.ReadClaudeConfig()
	baseUrl := ""
	if env, ok := claudeCfg["env"].(map[string]any); ok {
		baseUrl, _ = env["ANTHROPIC_BASE_URL"].(string)
	}
	isProxy := baseUrl != "" && (strings.Contains(baseUrl, "localhost") || strings.Contains(baseUrl, "127.0.0.1") || strings.Contains(baseUrl, "0.0.0.0"))
	mode := "paid"
	if isProxy {
		mode = "proxy"
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok",
		"mode":   mode,
	})
}

func (server *Server) handleClaudeModeSet(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || (body.Mode != "proxy" && body.Mode != "paid") {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": `mode must be "proxy" or "paid"`})
		return
	}

	claudeCfg, _ := config.ReadClaudeConfig()
	if body.Mode == "proxy" {
		claudeCfg["env"] = map[string]any{
			"ANTHROPIC_AUTH_TOKEN": "test",
			"ANTHROPIC_BASE_URL":   "http://localhost:8080",
			"ANTHROPIC_MODEL":      "claude-opus-4-6-thinking",
		}
	} else {
		delete(claudeCfg, "env")
	}

	if err := config.ReplaceClaudeConfig(claudeCfg); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"mode":    body.Mode,
		"config":  claudeCfg,
		"message": fmt.Sprintf("Switched to %s mode", body.Mode),
	})
}

func (server *Server) handleClaudePresetsGet(writer http.ResponseWriter, request *http.Request) {
	presets, err := config.ReadClaudePresets()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "presets": presets})
}

func (server *Server) handleClaudePresetsSave(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name   string         `json:"name"`
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Name == "" || body.Config == nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "Preset name and config required"})
		return
	}
	presets, err := config.SaveClaudePreset(body.Name, body.Config)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "presets": presets, "message": fmt.Sprintf("Preset %q saved", body.Name)})
}

func (server *Server) handleClaudePresetsDelete(writer http.ResponseWriter, request *http.Request, name string) {
	presets, err := config.DeleteClaudePreset(name)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "presets": presets, "message": fmt.Sprintf("Preset %q deleted", name)})
}

func (server *Server) handleServerPresetsGet(writer http.ResponseWriter, request *http.Request) {
	presets, err := config.ReadServerPresets()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "presets": presets})
}

func (server *Server) handleServerPresetsSave(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Config      map[string]any `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Name == "" || body.Config == nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "Preset name and config required"})
		return
	}
	presets, err := config.SaveServerPreset(body.Name, body.Config, body.Description)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "presets": presets, "message": fmt.Sprintf("Server preset %q saved", body.Name)})
}

func (server *Server) handleServerPresetsPatch(writer http.ResponseWriter, request *http.Request, name string) {
	var body struct {
		Description string         `json:"description"`
		Config      map[string]any `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "Invalid request body"})
		return
	}
	presets, err := config.SaveServerPreset(name, body.Config, body.Description)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "presets": presets, "message": fmt.Sprintf("Server preset %q updated", name)})
}

func (server *Server) handleServerPresetsDelete(writer http.ResponseWriter, request *http.Request, name string) {
	presets, err := config.DeleteServerPreset(name)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "presets": presets, "message": fmt.Sprintf("Server preset %q deleted", name)})
}

func (server *Server) handleModelsConfigPost(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ModelID string         `json:"modelId"`
		Config  map[string]any `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.ModelID == "" || body.Config == nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "error", "error": "Invalid parameters"})
		return
	}

	cfg := config.Get()
	if cfg.ModelMapping == nil {
		cfg.ModelMapping = make(map[string]any)
	}

	if del, ok := body.Config["delete"].(bool); ok && del {
		delete(cfg.ModelMapping, body.ModelID)
		if _, err := config.Save(map[string]any{"modelMapping": cfg.ModelMapping}); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": "Failed to save configuration"})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "deleted": true, "modelId": body.ModelID})
		return
	}

	existing, _ := cfg.ModelMapping[body.ModelID].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}
	for k, v := range body.Config {
		existing[k] = v
	}
	cfg.ModelMapping[body.ModelID] = existing

	if _, err := config.Save(map[string]any{"modelMapping": cfg.ModelMapping}); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error", "error": "Failed to save configuration"})
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "modelConfig": existing})
}

func (server *Server) handleStrategyHealthGet(writer http.ResponseWriter, request *http.Request) {
	if server.accountManager == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "trackers": nil})
		return
	}
	healthData := server.accountManager.GetStrategyHealthData()
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":   "ok",
		"strategy": healthData["strategy"],
		"trackers": healthData["trackers"],
	})
}

func (server *Server) handleLogsGet(writer http.ResponseWriter, request *http.Request) {
	if server.broadcaster == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "logs": []any{}})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "logs": server.broadcaster.GetHistory()})
}

func (server *Server) handleLogsStream(writer http.ResponseWriter, request *http.Request) {
	if server.broadcaster == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "logs": []any{}})
		return
	}

	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	// If history requested, send recent logs
	if request.URL.Query().Get("history") == "true" {
		history := server.broadcaster.GetHistory()
		for _, entry := range history {
			data, _ := json.Marshal(entry)
			fmt.Fprintf(writer, "data: %s\n\n", data)
		}
		flusher.Flush()
	}

	ch, cancel := server.broadcaster.Subscribe(100)
	defer cancel()

	ctx := request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(entry)
			if err == nil {
				fmt.Fprintf(writer, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

func (server *Server) handleAuthURLGet(writer http.ResponseWriter, request *http.Request) {
	if server.oauthHandler != nil {
		server.oauthHandler.ServeHTTP(writer, request)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok",
		"url":    "",
		"state":  "",
		"note":   "OAuth server handler not configured",
	})
}

func (server *Server) handleAuthCompletePost(writer http.ResponseWriter, request *http.Request) {
	if server.oauthHandler != nil {
		server.oauthHandler.ServeHTTP(writer, request)
		return
	}
	writeJSON(writer, http.StatusBadRequest, map[string]any{
		"status": "error",
		"error":  "OAuth callback handler not configured",
	})
}

func (server *Server) handleStatsHistory(writer http.ResponseWriter, request *http.Request) {
	if server.tracker == nil {
		writeJSON(writer, http.StatusOK, map[string]any{
			"status":  "ok",
			"history": map[string]any{},
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"history": server.tracker.GetHistory(),
	})
}
