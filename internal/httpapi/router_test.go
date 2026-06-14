package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

func TestMatchAppRoute(t *testing.T) {
	routes := []appRoute{
		exact(http.MethodGet, "/version", nil),
		exact("", "/api/settings", nil),
		subtree("/api/auth/users", nil),
		prefix("/images/", nil),
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   string
	}{
		{name: "exact method", method: http.MethodGet, path: "/version", want: "/version"},
		{name: "exact method mismatch", method: http.MethodPost, path: "/version", want: ""},
		{name: "methodless exact", method: http.MethodPost, path: "/api/settings", want: "/api/settings"},
		{name: "subtree base", method: http.MethodGet, path: "/api/auth/users", want: "/api/auth/users"},
		{name: "subtree child", method: http.MethodGet, path: "/api/auth/users/123/key", want: "/api/auth/users"},
		{name: "subtree boundary", method: http.MethodGet, path: "/api/auth/users123", want: ""},
		{name: "static prefix", method: http.MethodHead, path: "/images/2026/04/a.png", want: "/images/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := matchAppRoute(routes, tc.method, tc.path)
			got := ""
			if route != nil {
				got = route.path
			}
			if got != tc.want {
				t.Fatalf("matchAppRoute(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestAppRouterKeepsAPIMissesOutOfSPA(t *testing.T) {
	app := newTestApp(t)
	defer app.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/missing", nil)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("missing API status = %d body = %s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/settings", nil)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("SPA route status/body = %d %q", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/auth/linuxdo/callback", nil)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("Linuxdo frontend callback status/body = %d %q", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/auth/missing", nil)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("missing auth API status = %d body = %s", res.Code, res.Body.String())
	}
}

// TestOpenAIAccountsCRUDFullFlow 验证 task 10.1 引入的
// /api/openai-accounts 路由整套 CRUD 流程：
//
//   - POST 创建账号成功，响应中 api_key 已脱敏，model_states 与 allowed_models 对齐。
//   - POST 缺失 api_key / 非法 base_url / 空 allowed_models 全部返回 400。
//   - GET 列出后含已创建账号，且 api_key 始终脱敏。
//   - PATCH 更新 priority / allowed_models 时正确扩展 model_states。
//   - PATCH /model-states/{model} 单独修改某模型 status。
//   - DELETE 后再次 GET 列表为空。
//
// _Requirements: 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 1.10, 1.11
func TestOpenAIAccountsCRUDFullFlow(t *testing.T) {
	app := newTestApp(t)
	defer app.Close()

	auth := adminAuthHeader(t, app)

	// --- 1. POST 校验失败：缺 api_key
	req := httptest.NewRequest(http.MethodPost, "/api/openai-accounts", strings.NewReader(`{"name":"missing-key","base_url":"https://api.example.com","allowed_models":["gpt-image-2"]}`))
	req.Header.Set("Authorization", auth)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing api_key create status = %d body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "api_key is required") {
		t.Fatalf("missing api_key create body = %s", res.Body.String())
	}

	// --- 2. POST 校验失败：非法 base_url
	req = httptest.NewRequest(http.MethodPost, "/api/openai-accounts", strings.NewReader(`{"name":"bad-url","api_key":"sk-test-1234","base_url":"ftp://nope","allowed_models":["gpt-image-2"]}`))
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad base_url status = %d body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "base_url must be a valid http(s) absolute URL") {
		t.Fatalf("bad base_url body = %s", res.Body.String())
	}

	// --- 3. POST 校验失败：空 allowed_models
	req = httptest.NewRequest(http.MethodPost, "/api/openai-accounts", strings.NewReader(`{"name":"empty-models","api_key":"sk-test-1234","base_url":"https://api.example.com","allowed_models":[]}`))
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("empty allowed_models status = %d body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "allowed_models must be a non-empty subset") {
		t.Fatalf("empty allowed_models body = %s", res.Body.String())
	}

	// --- 4. POST 校验失败：allowed_models 含非法元素
	req = httptest.NewRequest(http.MethodPost, "/api/openai-accounts", strings.NewReader(`{"name":"bad-model","api_key":"sk-test-1234","base_url":"https://api.example.com","allowed_models":["codex-gpt-image-2"]}`))
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid allowed_models status = %d body = %s", res.Code, res.Body.String())
	}

	// --- 5. POST 创建成功
	createBody := `{"name":"primary","api_key":"sk-test-secret-XYZ9","base_url":"https://api.example.com","allowed_models":["gpt-image-2"],"priority":3,"concurrency":2}`
	req = httptest.NewRequest(http.MethodPost, "/api/openai-accounts", strings.NewReader(createBody))
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("create status = %d body = %s", res.Code, res.Body.String())
	}
	var createResp map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("create json: %v", err)
	}
	created := util.StringMap(createResp["item"])
	accountID := util.Clean(created["id"])
	if accountID == "" || !strings.HasPrefix(accountID, "oa_") {
		t.Fatalf("created id = %#v", created["id"])
	}
	if util.Clean(created["api_key"]) != "sk-***XYZ9" {
		t.Fatalf("created api_key = %#v, want sk-***XYZ9", created["api_key"])
	}
	if util.Clean(created["name"]) != "primary" || util.Clean(created["base_url"]) != "https://api.example.com" {
		t.Fatalf("created basic fields = %#v", created)
	}
	if util.ToInt(created["priority"], -1) != 3 || util.ToInt(created["concurrency"], -1) != 2 {
		t.Fatalf("created priority/concurrency = %#v", created)
	}
	allowed := util.AsStringSlice(created["allowed_models"])
	if len(allowed) != 1 || allowed[0] != util.ImageModelGPTImage2 {
		t.Fatalf("created allowed_models = %#v", allowed)
	}
	states := util.StringMap(created["model_states"])
	state := util.StringMap(states[util.ImageModelGPTImage2])
	if util.Clean(state["status"]) != "正常" {
		t.Fatalf("created model_states[%s] = %#v", util.ImageModelGPTImage2, state)
	}

	// --- 6. GET 列表脱敏
	req = httptest.NewRequest(http.MethodGet, "/api/openai-accounts", nil)
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", res.Code, res.Body.String())
	}
	var listResp map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("list json: %v", err)
	}
	rawItems, _ := listResp["items"].([]any)
	if len(rawItems) != 1 {
		t.Fatalf("list items length = %d body = %s", len(rawItems), res.Body.String())
	}
	listed := util.StringMap(rawItems[0])
	if util.Clean(listed["api_key"]) != "sk-***XYZ9" {
		t.Fatalf("listed api_key = %#v, want sk-***XYZ9", listed["api_key"])
	}

	// --- 7. PATCH 更新 priority + 扩展 allowed_models
	patchBody := `{"priority":1,"allowed_models":["gpt-image-2","gemini-3.1-flash-image"]}`
	req = httptest.NewRequest(http.MethodPatch, "/api/openai-accounts/"+url.PathEscape(accountID), strings.NewReader(patchBody))
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", res.Code, res.Body.String())
	}
	var patchResp map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &patchResp); err != nil {
		t.Fatalf("patch json: %v", err)
	}
	patched := util.StringMap(patchResp["item"])
	if util.ToInt(patched["priority"], -1) != 1 {
		t.Fatalf("patched priority = %#v", patched["priority"])
	}
	patchedAllowed := util.AsStringSlice(patched["allowed_models"])
	if len(patchedAllowed) != 2 {
		t.Fatalf("patched allowed_models = %#v", patchedAllowed)
	}
	patchedStates := util.StringMap(patched["model_states"])
	if _, ok := patchedStates[util.ImageModelGeminiFlashImage]; !ok {
		t.Fatalf("patched model_states missing %s: %#v", util.ImageModelGeminiFlashImage, patchedStates)
	}
	if _, ok := patchedStates[util.ImageModelGPTImage2]; !ok {
		t.Fatalf("patched model_states dropped %s: %#v", util.ImageModelGPTImage2, patchedStates)
	}

	// --- 8. PATCH /model-states/{model} 修改 gemini 状态为「禁用」
	stateBody := `{"status":"禁用","error_message":"manual disable"}`
	req = httptest.NewRequest(http.MethodPatch, "/api/openai-accounts/"+url.PathEscape(accountID)+"/model-states/"+url.PathEscape(util.ImageModelGeminiFlashImage), strings.NewReader(stateBody))
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("model-states patch status = %d body = %s", res.Code, res.Body.String())
	}
	var stateResp map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &stateResp); err != nil {
		t.Fatalf("model-states json: %v", err)
	}
	stateItem := util.StringMap(stateResp["item"])
	updatedStates := util.StringMap(stateItem["model_states"])
	geminiState := util.StringMap(updatedStates[util.ImageModelGeminiFlashImage])
	if util.Clean(geminiState["status"]) != "禁用" || util.Clean(geminiState["error_message"]) != "manual disable" {
		t.Fatalf("gemini state = %#v", geminiState)
	}
	gptState := util.StringMap(updatedStates[util.ImageModelGPTImage2])
	if util.Clean(gptState["status"]) != "正常" {
		t.Fatalf("gpt state contaminated = %#v", gptState)
	}

	// --- 9. PATCH /model-states/{model} 对 allowed_models 之外的模型应 400
	req = httptest.NewRequest(http.MethodPatch, "/api/openai-accounts/"+url.PathEscape(accountID)+"/model-states/unknown-model", strings.NewReader(`{"status":"正常"}`))
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("model-states unknown status = %d body = %s", res.Code, res.Body.String())
	}

	// --- 10. DELETE 不存在的 id 返回 404
	req = httptest.NewRequest(http.MethodDelete, "/api/openai-accounts/oa_does_not_exist", nil)
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d body = %s", res.Code, res.Body.String())
	}

	// --- 11. DELETE 现有 id 后列表清空
	req = httptest.NewRequest(http.MethodDelete, "/api/openai-accounts/"+url.PathEscape(accountID), nil)
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/openai-accounts", nil)
	req.Header.Set("Authorization", auth)
	res = httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("post-delete list status = %d body = %s", res.Code, res.Body.String())
	}
	if err := json.Unmarshal(res.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("post-delete list json: %v", err)
	}
	if items, _ := listResp["items"].([]any); len(items) != 0 {
		t.Fatalf("post-delete list items = %#v", items)
	}
}

// TestOpenAIAccountsRequiresAdminPermission 校验非 admin 身份访问
// /api/openai-accounts 集合返回 403，避免普通用户读取脱敏前的 api_key 引用线索。
//
// _Requirements: 1.4, 1.8
func TestOpenAIAccountsRequiresAdminPermission(t *testing.T) {
	app := newTestApp(t)
	defer app.Close()

	_, userKey, err := app.auth.CreateAPIKey(service.AuthRoleUser, "regular", service.AuthOwner{})
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/openai-accounts", nil)
	req.Header.Set("Authorization", "Bearer "+userKey)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("user-access status = %d body = %s", res.Code, res.Body.String())
	}
}

// TestProfileBillingReturnsDualBucketView 验证 task 10.3：
// GET /api/profile 返回的 billing 字段顶层同时包含 bucket_a 与 bucket_b
// 两组对象，不再回退到旧的扁平 standard / subscription 结构。
// 通过 ApplyAdjustment 给 bucket_b 注入余额，确认两桶在响应里互相独立。
//
// _Requirements: 9.1, 9.5
func TestProfileBillingReturnsDualBucketView(t *testing.T) {
	app := newTestAppWithBillingDefaults(t, service.BillingTypeStandard, "5", "0", service.BillingPeriodMonthly)
	defer app.Close()

	user, rawKey, err := app.auth.CreateAPIKey(service.AuthRoleUser, "dual-bucket-profile", service.AuthOwner{})
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	// CreateAPIKey 返回的是凭据视图；当 owner.ID 为空时，billing 维度的
	// 用户 ID 等于凭据 id（managedAuthUserID 的回退路径）。
	userID := util.Clean(user["owner_id"])
	if userID == "" {
		userID = util.Clean(user["id"])
	}
	operator := service.Identity{ID: "admin", Role: service.AuthRoleAdmin, Name: "Admin"}
	if _, err := app.billing.ApplyAdjustment(userID, operator, map[string]any{
		"type":   "increase_balance",
		"bucket": util.ImageBucketB,
		"amount": 7,
		"reason": "seed bucket_b",
	}); err != nil {
		t.Fatalf("ApplyAdjustment(bucket_b) error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("profile status = %d body = %s", res.Code, res.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("profile json: %v", err)
	}

	billing := util.StringMap(payload["billing"])
	if billing == nil {
		t.Fatalf("profile billing missing: %s", res.Body.String())
	}
	bucketA := util.StringMap(billing[util.ImageBucketA])
	bucketB := util.StringMap(billing[util.ImageBucketB])
	if bucketA == nil || bucketB == nil {
		t.Fatalf("profile billing dual bucket missing: %#v", billing)
	}
	if util.ToInt(bucketA["available"], -1) != 5 {
		t.Fatalf("bucket_a available = %#v, want 5", bucketA["available"])
	}
	if util.ToInt(bucketB["available"], -1) != 7 {
		t.Fatalf("bucket_b available = %#v, want 7", bucketB["available"])
	}
	// 双桶切换后旧的扁平字段必须消失（AGENTS.md「No compatibility layers」原则）。
	if _, exists := billing["standard"]; exists {
		t.Fatalf("profile billing must not expose flat standard field: %#v", billing)
	}
	if _, exists := billing["subscription"]; exists {
		t.Fatalf("profile billing must not expose flat subscription field: %#v", billing)
	}
}

// TestCreationTaskImageGenerationsRejectsInvalidModel 校验 task 10.5：
// /api/creation-tasks/image-generations 与 /api/creation-tasks/image-edits
// 在收到非 External_Image_Model 集合元素的 model 时立即返回 400，
// 而不是把请求送进 ImageTaskService。
//
// _Requirements: 6.1, 6.2, 6.6
func TestCreationTaskImageGenerationsRejectsInvalidModel(t *testing.T) {
	app := newTestApp(t)
	defer app.Close()

	_, rawKey, err := app.auth.CreateAPIKey(service.AuthRoleUser, "invalid-model", service.AuthOwner{})
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}

	body := `{"client_task_id":"invalid-model-task","prompt":"draw","model":"dall-e-3"}`
	req := httptest.NewRequest(http.MethodPost, "/api/creation-tasks/image-generations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid model status = %d body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "is not a billable image model") {
		t.Fatalf("invalid model body = %s", res.Body.String())
	}
}

// TestCreationTaskImageGenerationsResponseExposesBucketAndResolvedModel
// 校验 task 8.3 + 10.4 在 HTTP 出口的契约：
//
//   - /api/creation-tasks/image-generations submit 后，响应 JSON 必含
//     `bucket` 与 `resolved_model` 字段。
//   - 显式 model = gpt-image-2 时，bucket = bucket_a、resolved_model = gpt-image-2。
//
// 该测试仅触发 submit 阶段（model 校验 + Auto 路由 + 预扣费 + 任务排队），
// 不进入实际上游执行；upstream_kind 此时仍为空，与 design.md 中
// 「任务尚未执行或失败前 upstream_kind 为空」一致。
//
// _Requirements: 6.1, 6.7, 9.4
func TestCreationTaskImageGenerationsResponseExposesBucketAndResolvedModel(t *testing.T) {
	app := newTestApp(t)
	defer app.Close()

	_, rawKey, err := app.auth.CreateAPIKey(service.AuthRoleUser, "bucket-fields", service.AuthOwner{})
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}

	body := `{"client_task_id":"bucket-fields-task","prompt":"draw","model":"gpt-image-2"}`
	req := httptest.NewRequest(http.MethodPost, "/api/creation-tasks/image-generations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("submit status = %d body = %s", res.Code, res.Body.String())
	}

	var task map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &task); err != nil {
		t.Fatalf("submit json: %v", err)
	}
	if util.Clean(task["bucket"]) != util.ImageBucketA {
		t.Fatalf("task bucket = %#v, want bucket_a; full body = %s", task["bucket"], res.Body.String())
	}
	if util.Clean(task["resolved_model"]) != util.ImageModelGPTImage2 {
		t.Fatalf("task resolved_model = %#v, want gpt-image-2; full body = %s", task["resolved_model"], res.Body.String())
	}
	if util.Clean(task["model"]) != util.ImageModelGPTImage2 {
		t.Fatalf("task model echoed back = %#v, want gpt-image-2", task["model"])
	}
	// upstream_kind 在执行后才有意义；此处不应出现非空值。
	if upstream := util.Clean(task["upstream_kind"]); upstream != "" {
		t.Fatalf("task upstream_kind = %#v, want empty before execution", upstream)
	}
}
