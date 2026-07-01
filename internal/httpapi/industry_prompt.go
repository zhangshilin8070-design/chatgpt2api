package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"chatgpt2api/internal/util"
)

// ---- admin ----

func (a *App) handleAdminIndustryPrompts(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	base := "/api/admin/industry-prompts"
	if r.URL.Path == base {
		switch r.Method {
		case http.MethodGet:
			search := strings.TrimSpace(r.URL.Query().Get("search"))
			status := strings.TrimSpace(r.URL.Query().Get("status"))
			items := a.industry.ListPresets(search, status)
			counts := map[string]int{}
			for _, item := range items {
				key := util.Clean(item["industry_key"])
				counts[key] = a.industry.CountOverrides(key)
			}
			util.WriteJSON(w, http.StatusOK, map[string]any{
				"items":                    items,
				"total":                    len(items),
				"overrides_count_by_key":   counts,
			})
		case http.MethodPost:
			body, err := readJSONMap(r)
			if err != nil {
				util.WriteError(w, http.StatusBadRequest, "invalid json body")
				return
			}
			item, err := a.industry.CreatePreset(body, identityDisplayName(identity))
			if err != nil {
				util.WriteError(w, industryHTTPStatus(err), err.Error())
				return
			}
			a.logs.Add("行业提示词-创建", map[string]any{
				"module":         "industry-prompt",
				"operation_type": "industry_prompt_admin",
				"industry_key":   item["industry_key"],
				"operator":       identityDisplayName(identity),
			})
			util.WriteJSON(w, http.StatusOK, map[string]any{"item": item, "items": a.industry.ListPresets("", "")})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if r.URL.Path == base+"/import" && r.Method == http.MethodPost {
		items, err := readIndustryImportBody(r)
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		created, updated, err := a.industry.ImportPresets(items, identityDisplayName(identity))
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.logs.Add("行业提示词-导入", map[string]any{
			"module":         "industry-prompt",
			"operation_type": "industry_prompt_admin",
			"created":        created,
			"updated":        updated,
		})
		util.WriteJSON(w, http.StatusOK, map[string]any{
			"created": created, "updated": updated, "items": a.industry.ListPresets("", ""),
		})
		return
	}
	if r.URL.Path == base+"/export" && r.Method == http.MethodGet {
		util.WriteJSON(w, http.StatusOK, map[string]any{"items": a.industry.ExportPresets()})
		return
	}

	parts := splitPath(r.URL.Path)
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "admin" || parts[2] != "industry-prompts" {
		http.NotFound(w, r)
		return
	}
	id := parts[3]
	if len(parts) == 4 {
		switch r.Method {
		case http.MethodPatch, http.MethodPost:
			body, err := readJSONMap(r)
			if err != nil {
				util.WriteError(w, http.StatusBadRequest, "invalid json body")
				return
			}
			item, err := a.industry.UpdatePreset(id, body, identityDisplayName(identity))
			if err != nil {
				util.WriteError(w, industryHTTPStatus(err), err.Error())
				return
			}
			a.logs.Add("行业提示词-更新", map[string]any{
				"module":         "industry-prompt",
				"operation_type": "industry_prompt_admin",
				"industry_key":   item["industry_key"],
				"operator":       identityDisplayName(identity),
			})
			util.WriteJSON(w, http.StatusOK, map[string]any{"item": item, "items": a.industry.ListPresets("", "")})
		case http.MethodDelete:
			if !a.industry.DeletePreset(id) {
				util.WriteError(w, http.StatusNotFound, "industry_prompt_not_found")
				return
			}
			a.logs.Add("行业提示词-删除", map[string]any{
				"module":         "industry-prompt",
				"operation_type": "industry_prompt_admin",
				"preset_id":      id,
				"operator":       identityDisplayName(identity),
			})
			util.WriteJSON(w, http.StatusOK, map[string]any{"items": a.industry.ListPresets("", "")})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	http.NotFound(w, r)
}

// ---- profile (all authenticated users) ----

func (a *App) handleProfileIndustryPrompts(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	ownerID := util.Clean(identity.OwnerID)
	if ownerID == "" {
		ownerID = util.Clean(identity.ID)
	}
	if ownerID == "" {
		util.WriteError(w, http.StatusForbidden, "industry prompt requires a bound user account")
		return
	}
	base := "/api/profile/industry-prompts"
	if r.URL.Path == base {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		util.WriteJSON(w, http.StatusOK, map[string]any{"items": a.industry.ListForUser(ownerID)})
		return
	}
	parts := splitPath(r.URL.Path)
	if len(parts) != 4 || parts[0] != "api" || parts[1] != "profile" || parts[2] != "industry-prompts" {
		http.NotFound(w, r)
		return
	}
	industryKey := parts[3]
	switch r.Method {
	case http.MethodGet:
		item, ok := a.industry.GetForUser(ownerID, industryKey)
		if !ok {
			util.WriteError(w, http.StatusNotFound, "industry_prompt_not_found")
			return
		}
		util.WriteJSON(w, http.StatusOK, map[string]any{"item": item})
	case http.MethodPut:
		body, err := readJSONMap(r)
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		result, err := a.industry.PutOverride(ownerID, industryKey, util.Clean(body["prompt"]))
		if err != nil {
			util.WriteError(w, industryHTTPStatus(err), err.Error())
			return
		}
		a.logs.Add("行业提示词-自定义", map[string]any{
			"module":         "industry-prompt",
			"operation_type": "industry_prompt_user",
			"industry_key":   industryKey,
		})
		util.WriteJSON(w, http.StatusOK, map[string]any{"override": result})
	case http.MethodDelete:
		if !a.industry.DeleteOverride(ownerID, industryKey) {
			util.WriteError(w, http.StatusNotFound, "override not found")
			return
		}
		a.logs.Add("行业提示词-恢复默认", map[string]any{
			"module":         "industry-prompt",
			"operation_type": "industry_prompt_user",
			"industry_key":   industryKey,
		})
		util.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleProfileCurrentIndustry(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	ownerID := util.Clean(identity.OwnerID)
	if ownerID == "" {
		ownerID = util.Clean(identity.ID)
	}
	if ownerID == "" {
		util.WriteError(w, http.StatusForbidden, "industry prompt requires a bound user account")
		return
	}
	switch r.Method {
	case http.MethodGet:
		key, effective := a.industry.GetCurrentIndustry(ownerID)
		util.WriteJSON(w, http.StatusOK, map[string]any{"industry_key": key, "effective": effective})
	case http.MethodPut:
		body, err := readJSONMap(r)
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		if err := a.industry.SetCurrentIndustry(ownerID, util.Clean(body["industry_key"])); err != nil {
			util.WriteError(w, industryHTTPStatus(err), err.Error())
			return
		}
		key, effective := a.industry.GetCurrentIndustry(ownerID)
		util.WriteJSON(w, http.StatusOK, map[string]any{"industry_key": key, "effective": effective})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func industryHTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	switch msg {
	case "industry_prompt_not_found":
		return http.StatusNotFound
	case "industry_prompt_too_long":
		return http.StatusBadRequest
	}
	return http.StatusBadRequest
}

func readIndustryImportBody(r *http.Request) ([]map[string]any, error) {
	body, err := readJSONMap(r)
	if err != nil {
		return nil, err
	}
	items := util.AsMapSlice(body["items"])
	if len(items) == 0 {
		// fallback: root array
		var arr []map[string]any
		if err := json.NewDecoder(strings.NewReader(jsonString(body))).Decode(&arr); err == nil {
			items = arr
		}
	}
	return items, nil
}
