package service

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"chatgpt2api/internal/storage"
	"chatgpt2api/internal/util"
)

// openAIAccountsDocumentName 是 OpenAI 协议账号池的持久化文档名，与
// AccountService 使用的 accounts 存储完全独立。
const openAIAccountsDocumentName = "openai_accounts.json"

// openAIAccountAllowedUpstreamModels 列出 OpenAI 协议账号合法的
// allowed_models 取值集合（即 Upstream_Image_Model 集合）。
//
// 注意：codex-gpt-image-2 是仅在路由层出现的 External_Image_Model，
// 不进入此集合（参见 Requirement 3.12）。
var openAIAccountAllowedUpstreamModels = map[string]struct{}{
	util.ImageModelGPTImage2:        {},
	util.ImageModelGeminiFlashImage: {},
}

// openAIAccountModelStatusOK 是模型粒度状态字段 status 的「正常」枚举值，
// 仅 status==正常 的账号-模型对才会被 ReserveForUpstreamModel 选中。
const openAIAccountModelStatusOK = "正常"

// OpenAIAccountReservation 描述一次成功的账号槽位预留，承载 Image_Engine
// 发起 OpenAI 协议上游请求所需的全部输入。生命周期由调用方管理：使用完
// 后必须调用 OpenAIAccountService.Release(AccountID)。
type OpenAIAccountReservation struct {
	AccountID     string
	APIKey        string
	BaseURL       string
	UpstreamModel string
}

// OpenAIAccountService 管理 OpenAI 协议账号池（api_key + base_url 形态），
// 与现有 ChatGPT AccountService 完全平行。其内部 model_states 字典在账号
// -模型粒度维护可调度状态；reservations 用于内存中的并发槽位计数。
type OpenAIAccountService struct {
	mu           sync.Mutex
	store        storage.JSONDocumentBackend
	logs         *LogService
	items        []map[string]any
	reservations map[string]int // accountID -> in-flight slot count
}

// NewOpenAIAccountService 构造服务实例。logs 用于在 CRUD / 状态变更时写入
// 审计日志（与 AccountService 同 schema："module": "openai_accounts"）。
func NewOpenAIAccountService(backend storage.Backend, logs *LogService) *OpenAIAccountService {
	s := &OpenAIAccountService{
		store:        jsonDocumentStoreFromBackend(backend),
		logs:         logs,
		reservations: map[string]int{},
	}
	s.mu.Lock()
	s.loadLocked()
	s.mu.Unlock()
	return s
}

// loadLocked 从持久化文档加载 items；并对每条记录的 model_states 做
// 防御性规范化（缺失字段补默认、与 allowed_models 对齐）。
func (s *OpenAIAccountService) loadLocked() {
	raw := loadStoredJSON(s.store, openAIAccountsDocumentName)
	doc, _ := raw.(map[string]any)
	items := util.AsMapSlice(doc["items"])
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if normalized := normalizeOpenAIAccount(item); normalized != nil {
			out = append(out, normalized)
		}
	}
	s.items = out
}

// saveLocked 持久化 items；调用方必须持有 s.mu。
func (s *OpenAIAccountService) saveLocked() error {
	doc := map[string]any{
		"items":      s.items,
		"updated_at": util.NowISO(),
	}
	return saveStoredJSON(s.store, openAIAccountsDocumentName, doc)
}

// List 返回所有 OpenAI 协议账号的脱敏视图。api_key 被替换为
// `sk-***{last4}`；调用方拿到的是 deep-copy，不会影响内部 items。
func (s *OpenAIAccountService) List() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, publicOpenAIAccount(item))
	}
	return out
}

// Create 校验并创建一条新账号。返回脱敏后的账号视图。
//
// 字段约定：
//   - id 由服务端生成，形如 oa_xxxxxxxxxxxxxxxxxx
//   - priority 缺省 0；concurrency 缺省 1
//   - allowed_models 必填，每个元素都会在 model_states 中初始化为 {正常,空,0,0,空}
func (s *OpenAIAccountService) Create(input map[string]any) (map[string]any, error) {
	if input == nil {
		return nil, errors.New("input is required")
	}
	if err := validateOpenAIAccountInput(input, false); err != nil {
		return nil, err
	}
	now := util.NowISO()
	allowed := normalizeAllowedModels(input["allowed_models"])
	concurrency := util.ToInt(input["concurrency"], 1)
	if concurrency <= 0 {
		concurrency = 1
	}
	account := map[string]any{
		"id":             "oa_" + util.NewHex(18),
		"name":           strings.TrimSpace(util.Clean(input["name"])),
		"api_key":        strings.TrimSpace(util.Clean(input["api_key"])),
		"base_url":       strings.TrimSpace(util.Clean(input["base_url"])),
		"allowed_models": allowed,
		"priority":       util.ToInt(input["priority"], 0),
		"concurrency":    concurrency,
		"model_states":   buildModelStatesFor(allowed, nil),
		"created_at":     now,
		"updated_at":     now,
	}

	s.mu.Lock()
	s.items = append(s.items, account)
	if err := s.saveLocked(); err != nil {
		// 回滚内存视图避免与磁盘脱节（Fail-Fast）
		s.items = s.items[:len(s.items)-1]
		s.mu.Unlock()
		return nil, err
	}
	view := publicOpenAIAccount(account)
	s.mu.Unlock()

	if s.logs != nil {
		_ = s.logs.Add("新增 OpenAI 协议账号", map[string]any{
			"module":         "openai_accounts",
			"operation_type": "新增",
			"account_id":     account["id"],
			"name":           account["name"],
			"allowed_models": allowed,
		})
	}
	return view, nil
}

// Update 应用部分字段更新。允许更新 name / api_key / base_url /
// allowed_models / priority / concurrency 中的任意子集。
//
//   - api_key 缺失或空白：保留旧值
//   - allowed_models 变化：新增的模型在 model_states 中初始化为默认值；
//     不再保留的模型从 model_states 中删除
func (s *OpenAIAccountService) Update(id string, patch map[string]any) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("id is required")
	}
	if patch == nil {
		return nil, errors.New("patch is required")
	}
	if err := validateOpenAIAccountInput(patch, true); err != nil {
		return nil, err
	}

	s.mu.Lock()
	idx := s.findIndexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("openai account %s not found", id)
	}
	current := util.CopyMap(s.items[idx])

	if name, ok := patch["name"]; ok {
		current["name"] = strings.TrimSpace(util.Clean(name))
	}
	if rawKey, ok := patch["api_key"]; ok {
		if key := strings.TrimSpace(util.Clean(rawKey)); key != "" {
			current["api_key"] = key
		}
	}
	if baseURL, ok := patch["base_url"]; ok {
		current["base_url"] = strings.TrimSpace(util.Clean(baseURL))
	}
	if priority, ok := patch["priority"]; ok {
		current["priority"] = util.ToInt(priority, 0)
	}
	if concurrency, ok := patch["concurrency"]; ok {
		value := util.ToInt(concurrency, 1)
		if value <= 0 {
			value = 1
		}
		current["concurrency"] = value
	}
	if rawAllowed, ok := patch["allowed_models"]; ok {
		previousStates := stringMapOfMaps(current["model_states"])
		allowed := normalizeAllowedModels(rawAllowed)
		current["allowed_models"] = allowed
		current["model_states"] = buildModelStatesFor(allowed, previousStates)
	}
	current["updated_at"] = util.NowISO()
	if normalized := normalizeOpenAIAccount(current); normalized != nil {
		current = normalized
	}

	s.items[idx] = current
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	view := publicOpenAIAccount(current)
	s.mu.Unlock()

	if s.logs != nil {
		_ = s.logs.Add("更新 OpenAI 协议账号", map[string]any{
			"module":         "openai_accounts",
			"operation_type": "更新",
			"account_id":     id,
			"name":           current["name"],
			"allowed_models": current["allowed_models"],
		})
	}
	return view, nil
}

// Delete 按 id 移除账号；同时清除该账号上未结束的并发槽位预留。
// 返回是否真的删除了一条记录。
func (s *OpenAIAccountService) Delete(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	s.mu.Lock()
	idx := s.findIndexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return false
	}
	removed := s.items[idx]
	s.items = append(s.items[:idx], s.items[idx+1:]...)
	delete(s.reservations, id)
	_ = s.saveLocked()
	s.mu.Unlock()

	if s.logs != nil {
		_ = s.logs.Add("删除 OpenAI 协议账号", map[string]any{
			"module":         "openai_accounts",
			"operation_type": "删除",
			"account_id":     id,
			"name":           util.Clean(removed["name"]),
		})
	}
	return true
}

// UpdateModelState 仅作用于指定账号上指定 Upstream_Image_Model 的状态字段。
// 当前允许 patch 的键为 status 与 error_message；其它键被忽略。
//
// 校验失败（账号不存在 / model 不在 allowed_models 内）返回错误，不写盘。
func (s *OpenAIAccountService) UpdateModelState(id, model string, patch map[string]any) (map[string]any, error) {
	id = strings.TrimSpace(id)
	model = strings.TrimSpace(model)
	if id == "" {
		return nil, errors.New("id is required")
	}
	if model == "" {
		return nil, errors.New("model is required")
	}
	if patch == nil {
		return nil, errors.New("patch is required")
	}

	s.mu.Lock()
	idx := s.findIndexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("openai account %s not found", id)
	}
	current := util.CopyMap(s.items[idx])
	allowed := util.AsStringSlice(current["allowed_models"])
	if !containsString(allowed, model) {
		s.mu.Unlock()
		return nil, fmt.Errorf("model %s is not in allowed_models", model)
	}
	states := stringMapOfMaps(current["model_states"])
	if states == nil {
		states = map[string]map[string]any{}
	}
	state := states[model]
	if state == nil {
		state = defaultOpenAIAccountModelState()
	}
	if status, ok := patch["status"]; ok {
		state["status"] = strings.TrimSpace(util.Clean(status))
	}
	if errMessage, ok := patch["error_message"]; ok {
		state["error_message"] = strings.TrimSpace(util.Clean(errMessage))
	}
	states[model] = state
	current["model_states"] = mapOfMapsToAny(states)
	current["updated_at"] = util.NowISO()
	if normalized := normalizeOpenAIAccount(current); normalized != nil {
		current = normalized
	}
	s.items[idx] = current
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	view := publicOpenAIAccount(current)
	s.mu.Unlock()

	if s.logs != nil {
		_ = s.logs.Add("更新 OpenAI 协议账号模型状态", map[string]any{
			"module":         "openai_accounts",
			"operation_type": "状态变更",
			"account_id":     id,
			"model":          model,
			"status":         util.Clean(state["status"]),
		})
	}
	return view, nil
}

// MarkModelResult 在一次上游调用结束后更新计数与 last_used_at；
//
//   - success=true：success +1，status 重置为「正常」，error_message 清空
//   - success=false：fail +1；errMessage 非空则写入 error_message；status 不动
//     （由 Image_Engine 在专门的 401/429 路径上调用 UpdateModelState 调整）
//
// 该方法不参与槽位释放；释放由调用方在 Reserve 之后用 Release 完成。
func (s *OpenAIAccountService) MarkModelResult(id, upstreamModel string, success bool, errMessage string) {
	id = strings.TrimSpace(id)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if id == "" || upstreamModel == "" {
		return
	}
	s.mu.Lock()
	idx := s.findIndexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return
	}
	current := util.CopyMap(s.items[idx])
	allowed := util.AsStringSlice(current["allowed_models"])
	if !containsString(allowed, upstreamModel) {
		s.mu.Unlock()
		return
	}
	states := stringMapOfMaps(current["model_states"])
	if states == nil {
		states = map[string]map[string]any{}
	}
	state := states[upstreamModel]
	if state == nil {
		state = defaultOpenAIAccountModelState()
	}
	state["last_used_at"] = util.NowISO()
	if success {
		state["success"] = util.ToInt(state["success"], 0) + 1
		state["status"] = openAIAccountModelStatusOK
		state["error_message"] = ""
	} else {
		state["fail"] = util.ToInt(state["fail"], 0) + 1
		if trimmed := strings.TrimSpace(errMessage); trimmed != "" {
			state["error_message"] = trimmed
		}
	}
	states[upstreamModel] = state
	current["model_states"] = mapOfMapsToAny(states)
	current["updated_at"] = util.NowISO()
	if normalized := normalizeOpenAIAccount(current); normalized != nil {
		current = normalized
	}
	s.items[idx] = current
	_ = s.saveLocked()
	s.mu.Unlock()
}

// ReserveForUpstreamModel 在满足全部以下条件的账号集合中按 priority 升序
// 选出第一个候选并占用一个槽位：
//
//   - allowed_models 包含 upstreamModel
//   - model_states[upstreamModel].status == "正常"
//   - reservations[id] < concurrency
//   - id 不在 exclude 中
//
// 当排序键 priority 相同时按 id 字典序保证稳定性。返回的 reservation 必须
// 在使用结束后调用 Release(reservation.AccountID) 归还槽位。
//
// 如果没有候选，返回错误 `no available openai-protocol account for {upstreamModel}`。
func (s *OpenAIAccountService) ReserveForUpstreamModel(upstreamModel string, exclude map[string]struct{}) (OpenAIAccountReservation, error) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return OpenAIAccountReservation{}, errors.New("upstream model is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	candidates := s.eligibleAccountsLocked(upstreamModel, exclude)
	if len(candidates) == 0 {
		return OpenAIAccountReservation{}, fmt.Errorf("no available openai-protocol account for %s", upstreamModel)
	}
	pick := candidates[0]
	id := util.Clean(pick["id"])
	s.reservations[id] = s.reservations[id] + 1
	return OpenAIAccountReservation{
		AccountID:     id,
		APIKey:        util.Clean(pick["api_key"]),
		BaseURL:       util.Clean(pick["base_url"]),
		UpstreamModel: upstreamModel,
	}, nil
}

// Release 归还一个先前由 ReserveForUpstreamModel 占用的槽位。计数下溢时
// 删除该 entry 而非保留负值（Fail-Safe，但不掩盖调用错误：调用方多次释放
// 同一账号会被静默吸收）。
func (s *OpenAIAccountService) Release(accountID string) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := s.reservations[accountID]
	if count <= 1 {
		delete(s.reservations, accountID)
		return
	}
	s.reservations[accountID] = count - 1
}

// HasAvailableForUpstreamModel 用于 Auto 路由层快速判定某个 Upstream_Image_Model
// 是否还有可调度账号；判定条件与 ReserveForUpstreamModel 一致，但不占用槽位。
func (s *OpenAIAccountService) HasAvailableForUpstreamModel(upstreamModel string) bool {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.eligibleAccountsLocked(upstreamModel, nil)) > 0
}

// eligibleAccountsLocked 返回当前 items 中满足调度条件的账号视图，按
// priority 升序、id 字典序排序。调用方必须持有 s.mu。
func (s *OpenAIAccountService) eligibleAccountsLocked(upstreamModel string, exclude map[string]struct{}) []map[string]any {
	out := make([]map[string]any, 0, len(s.items))
	for _, item := range s.items {
		id := util.Clean(item["id"])
		if id == "" {
			continue
		}
		if _, skip := exclude[id]; skip {
			continue
		}
		allowed := util.AsStringSlice(item["allowed_models"])
		if !containsString(allowed, upstreamModel) {
			continue
		}
		states := stringMapOfMaps(item["model_states"])
		state := states[upstreamModel]
		if state == nil || util.Clean(state["status"]) != openAIAccountModelStatusOK {
			continue
		}
		concurrency := util.ToInt(item["concurrency"], 1)
		if concurrency <= 0 {
			concurrency = 1
		}
		if s.reservations[id] >= concurrency {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi := util.ToInt(out[i]["priority"], 0)
		pj := util.ToInt(out[j]["priority"], 0)
		if pi != pj {
			return pi < pj
		}
		return util.Clean(out[i]["id"]) < util.Clean(out[j]["id"])
	})
	return out
}

func (s *OpenAIAccountService) findIndexLocked(id string) int {
	for index, item := range s.items {
		if util.Clean(item["id"]) == id {
			return index
		}
	}
	return -1
}

// validateOpenAIAccountInput 校验 Create / Update 入参。
//
//   - isUpdate=false（Create）：api_key、base_url、allowed_models 全部强制；
//     concurrency 缺省由 Create 函数填默认值。
//   - isUpdate=true（Update）：api_key 缺失或空白视为「保留旧值」，由 Update
//     函数处理；其它字段一旦在 patch 中出现就必须合法（base_url 不允许清空）。
func validateOpenAIAccountInput(input map[string]any, isUpdate bool) error {
	if input == nil {
		return errors.New("input is required")
	}
	if !isUpdate {
		if strings.TrimSpace(util.Clean(input["api_key"])) == "" {
			return errors.New("api_key is required")
		}
	}
	rawBaseURL, hasBaseURL := input["base_url"]
	if !isUpdate || hasBaseURL {
		baseURL := strings.TrimSpace(util.Clean(rawBaseURL))
		if !isAbsoluteHTTPURL(baseURL) {
			return errors.New("base_url must be a valid http(s) absolute URL")
		}
	}
	rawAllowed, hasAllowed := input["allowed_models"]
	if !isUpdate || hasAllowed {
		allowed := normalizeAllowedModels(rawAllowed)
		if len(allowed) == 0 {
			return errors.New("allowed_models must be a non-empty subset of {gpt-image-2, gemini-3.1-flash-image}")
		}
		for _, model := range allowed {
			if _, ok := openAIAccountAllowedUpstreamModels[model]; !ok {
				return errors.New("allowed_models must be a non-empty subset of {gpt-image-2, gemini-3.1-flash-image}")
			}
		}
	}
	if rawConcurrency, ok := input["concurrency"]; ok {
		concurrency := util.ToInt(rawConcurrency, 0)
		if concurrency < 1 {
			return errors.New("concurrency must be >= 1")
		}
	}
	return nil
}

// redactAPIKey 把 api_key 脱敏为 `sk-***{last4}`。当原值短于 4 字符时
// 返回 `sk-***`，避免泄露任何尾部内容。
func redactAPIKey(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if len(apiKey) < 4 {
		return "sk-***"
	}
	return "sk-***" + apiKey[len(apiKey)-4:]
}

// publicOpenAIAccount 返回脱敏后的账号视图。调用方拿到的是 deep-copy。
func publicOpenAIAccount(item map[string]any) map[string]any {
	if item == nil {
		return nil
	}
	view := util.CopyMap(item)
	view["api_key"] = redactAPIKey(util.Clean(item["api_key"]))
	// 复制 allowed_models 与 model_states 以避免外部 mutate 影响内部状态
	if allowed := util.AsStringSlice(item["allowed_models"]); len(allowed) > 0 {
		view["allowed_models"] = append([]string(nil), allowed...)
	}
	if states := stringMapOfMaps(item["model_states"]); len(states) > 0 {
		copied := make(map[string]any, len(states))
		for model, state := range states {
			copied[model] = util.CopyMap(state)
		}
		view["model_states"] = copied
	}
	return view
}

// normalizeOpenAIAccount 把一条记录整理为规范形态（字段补齐、零值处理）。
// 返回 nil 表示该记录非法（缺 id 或 api_key）。
func normalizeOpenAIAccount(item map[string]any) map[string]any {
	if item == nil {
		return nil
	}
	id := strings.TrimSpace(util.Clean(item["id"]))
	apiKey := strings.TrimSpace(util.Clean(item["api_key"]))
	if id == "" || apiKey == "" {
		return nil
	}
	out := util.CopyMap(item)
	out["id"] = id
	out["name"] = strings.TrimSpace(util.Clean(out["name"]))
	out["api_key"] = apiKey
	out["base_url"] = strings.TrimSpace(util.Clean(out["base_url"]))
	out["priority"] = util.ToInt(out["priority"], 0)
	concurrency := util.ToInt(out["concurrency"], 1)
	if concurrency <= 0 {
		concurrency = 1
	}
	out["concurrency"] = concurrency
	allowed := normalizeAllowedModels(out["allowed_models"])
	out["allowed_models"] = allowed
	previousStates := stringMapOfMaps(out["model_states"])
	out["model_states"] = buildModelStatesFor(allowed, previousStates)
	if util.Clean(out["created_at"]) == "" {
		out["created_at"] = util.NowISO()
	}
	if util.Clean(out["updated_at"]) == "" {
		out["updated_at"] = util.NowISO()
	}
	return out
}

// normalizeAllowedModels 去重并过滤掉空白条目；保持稳定的输入顺序。
// 不强制要求模型属于合法集合（由 validateOpenAIAccountInput 负责校验）。
func normalizeAllowedModels(value any) []string {
	raw := util.AsStringSlice(value)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, model := range raw {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

// buildModelStatesFor 根据 allowed 模型集合构造 model_states 字典。
// 已存在于 previous 中的模型保持其计数与状态；新增模型用默认值初始化；
// 不在 allowed 中的旧模型直接丢弃。
func buildModelStatesFor(allowed []string, previous map[string]map[string]any) map[string]any {
	out := make(map[string]any, len(allowed))
	for _, model := range allowed {
		if existing, ok := previous[model]; ok && existing != nil {
			out[model] = util.CopyMap(existing)
			continue
		}
		out[model] = defaultOpenAIAccountModelState()
	}
	return out
}

// defaultOpenAIAccountModelState 返回模型粒度状态的初始值。
func defaultOpenAIAccountModelState() map[string]any {
	return map[string]any{
		"status":        openAIAccountModelStatusOK,
		"last_used_at":  "",
		"success":       0,
		"fail":          0,
		"error_message": "",
	}
}

// stringMapOfMaps 把 model_states 字段解读为 map[string]map[string]any 形态。
func stringMapOfMaps(value any) map[string]map[string]any {
	raw := util.StringMap(value)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]map[string]any, len(raw))
	for key, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out[key] = m
		}
	}
	return out
}

// mapOfMapsToAny 把 map[string]map[string]any 包装回 map[string]any，便于
// 写回 items 字段（持久化形态）。
func mapOfMapsToAny(in map[string]map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// containsString 用于 allowed_models 的成员判定。
func containsString(values []string, target string) bool {
	for _, item := range values {
		if item == target {
			return true
		}
	}
	return false
}

// isAbsoluteHTTPURL 校验 base_url 必须是 http/https 的绝对 URL。
func isAbsoluteHTTPURL(raw string) bool {
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !parsed.IsAbs() {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	return true
}
