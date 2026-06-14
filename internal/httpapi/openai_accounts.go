package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

// handleOpenAIAccounts 提供 OpenAI 协议账号池的管理接口（仅 admin）。
//
// 路由表（与 design.md §8.1 对齐）：
//
//	GET    /api/openai-accounts                                      -> List（脱敏）
//	POST   /api/openai-accounts                                      -> Create
//	PATCH  /api/openai-accounts/{id}                                 -> Update
//	DELETE /api/openai-accounts/{id}                                 -> Delete
//	PATCH  /api/openai-accounts/{id}/model-states/{model}            -> UpdateModelState
//
// /model-states/{model} 子路径委派到 handleOpenAIAccountModelStates。其它任意层级
// 的路径返回 404。
func (a *App) handleOpenAIAccounts(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	if identity.Role != service.AuthRoleAdmin {
		util.WriteError(w, http.StatusForbidden, "admin permission required")
		return
	}

	parts := splitPath(r.URL.Path)
	// parts[0]=api parts[1]=openai-accounts
	if len(parts) < 2 || parts[0] != "api" || parts[1] != "openai-accounts" {
		http.NotFound(w, r)
		return
	}

	switch len(parts) {
	case 2:
		a.handleOpenAIAccountsCollection(w, r)
	case 3:
		a.handleOpenAIAccountByID(w, r, parts[2])
	case 5:
		// /api/openai-accounts/{id}/model-states/{model}
		if parts[3] != "model-states" {
			http.NotFound(w, r)
			return
		}
		a.handleOpenAIAccountModelStates(w, r, parts[2], parts[4])
	default:
		http.NotFound(w, r)
	}
}

// handleOpenAIAccountsCollection 处理 /api/openai-accounts 集合资源：GET / POST。
func (a *App) handleOpenAIAccountsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		util.WriteJSON(w, http.StatusOK, map[string]any{"items": a.openaiAccounts.List()})
	case http.MethodPost:
		body, err := readJSONMap(r)
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		item, err := a.openaiAccounts.Create(body)
		if err != nil {
			util.WriteError(w, openAIAccountErrorStatus(err), err.Error())
			return
		}
		util.WriteJSON(w, http.StatusOK, map[string]any{
			"item":  item,
			"items": a.openaiAccounts.List(),
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleOpenAIAccountByID 处理 /api/openai-accounts/{id}：PATCH / DELETE。
func (a *App) handleOpenAIAccountByID(w http.ResponseWriter, r *http.Request, rawID string) {
	id := strings.TrimSpace(rawID)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		body, err := readJSONMap(r)
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		item, err := a.openaiAccounts.Update(id, body)
		if err != nil {
			util.WriteError(w, openAIAccountErrorStatus(err), err.Error())
			return
		}
		util.WriteJSON(w, http.StatusOK, map[string]any{
			"item":  item,
			"items": a.openaiAccounts.List(),
		})
	case http.MethodDelete:
		if !a.openaiAccounts.Delete(id) {
			util.WriteError(w, http.StatusNotFound, fmt.Sprintf("openai account %s not found", id))
			return
		}
		util.WriteJSON(w, http.StatusOK, map[string]any{"items": a.openaiAccounts.List()})
	default:
		w.Header().Set("Allow", "PATCH, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleOpenAIAccountModelStates 处理 /api/openai-accounts/{id}/model-states/{model}：
// 仅支持 PATCH，body 中允许更新 status 与 error_message 两个键，其他键被服务层忽略。
func (a *App) handleOpenAIAccountModelStates(w http.ResponseWriter, r *http.Request, rawID, rawModel string) {
	id := strings.TrimSpace(rawID)
	model := strings.TrimSpace(rawModel)
	if id == "" || model == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPatch {
		w.Header().Set("Allow", "PATCH")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := readJSONMap(r)
	if err != nil {
		util.WriteError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	item, err := a.openaiAccounts.UpdateModelState(id, model, body)
	if err != nil {
		util.WriteError(w, openAIAccountErrorStatus(err), err.Error())
		return
	}
	util.WriteJSON(w, http.StatusOK, map[string]any{
		"item":  item,
		"items": a.openaiAccounts.List(),
	})
}

// openAIAccountErrorStatus 把 OpenAIAccountService 暴露的错误映射到 HTTP 状态：
//   - 找不到记录的错误（信息中带 "not found"）→ 404
//   - 其它一律按 400（校验类错误，含 "not in allowed_models"）
func openAIAccountErrorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if strings.Contains(err.Error(), "not found") {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
