package service

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"chatgpt2api/internal/storage"
	"chatgpt2api/internal/util"
)

const (
	industryPromptPresetDocName = "industry_prompt_presets.json"
	industryPromptUserDocDir    = "industry_prompts"
	industryPromptMaxLength     = 4000
	industryPromptFinalMaxLen   = 8000

	IndustrySourceUserOverride = "user_override"
	IndustrySourcePublicPreset = "public_preset"
	IndustrySourceNone         = "none"
)

type IndustryPromptService struct {
	mu      sync.Mutex
	store   storage.JSONDocumentBackend
	presets []map[string]any
	users   map[string]map[string]any
}

func NewIndustryPromptService(backend ...storage.Backend) *IndustryPromptService {
	s := &IndustryPromptService{
		store: firstJSONDocumentStore(backend),
		users: map[string]map[string]any{},
	}
	s.presets = s.loadPresetsLocked()
	s.seedPresetsLocked()
	return s
}

// ---------------- presets ----------------

func (s *IndustryPromptService) ListPresets(search, status string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := copyMaps(s.presets)
	search = strings.ToLower(strings.TrimSpace(search))
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if status == "enabled" && !util.ToBool(item["enabled"]) {
			continue
		}
		if status == "disabled" && util.ToBool(item["enabled"]) {
			continue
		}
		if search != "" {
			key := strings.ToLower(util.Clean(item["industry_key"]))
			label := strings.ToLower(util.Clean(item["label"]))
			prompt := strings.ToLower(util.Clean(item["prompt"]))
			if !strings.Contains(key, search) && !strings.Contains(label, search) && !strings.Contains(prompt, search) {
				continue
			}
		}
		out = append(out, item)
	}
	sortIndustryPresets(out)
	return out
}

func (s *IndustryPromptService) ListEnabledPresets() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]map[string]any, 0, len(s.presets))
	for _, item := range s.presets {
		if !util.ToBool(item["enabled"]) {
			continue
		}
		items = append(items, util.CopyMap(item))
	}
	sortIndustryPresets(items)
	return items
}

func (s *IndustryPromptService) GetPresetByKey(industryKey string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item := s.findPresetByKeyLocked(industryKey); item != nil {
		return util.CopyMap(item)
	}
	return nil
}

func (s *IndustryPromptService) CreatePreset(body map[string]any, operator string) (map[string]any, error) {
	industryKey := normalizeIndustryKey(util.Clean(body["industry_key"]))
	if industryKey == "" {
		return nil, fmt.Errorf("industry_key is required")
	}
	label := util.Clean(body["label"])
	if label == "" {
		return nil, fmt.Errorf("label is required")
	}
	prompt := util.Clean(body["prompt"])
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len([]rune(prompt)) > industryPromptMaxLength {
		return nil, fmt.Errorf("industry_prompt_too_long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findPresetByKeyLocked(industryKey) != nil {
		return nil, fmt.Errorf("industry_key already exists")
	}
	now := util.NowISO()
	item := map[string]any{
		"id":           "ip_" + util.NewHex(12),
		"industry_key": industryKey,
		"label":        label,
		"description":  util.Clean(body["description"]),
		"prompt":       prompt,
		"sort_order":   util.ToInt(body["sort_order"], len(s.presets)+1),
		"enabled":      util.ToBool(util.ValueOr(body["enabled"], true)),
		"version":      1,
		"created_at":   now,
		"updated_at":   now,
		"created_by":   operator,
		"updated_by":   operator,
	}
	s.presets = append(s.presets, item)
	sortIndustryPresets(s.presets)
	if err := s.savePresetsLocked(); err != nil {
		return nil, err
	}
	return util.CopyMap(item), nil
}

func (s *IndustryPromptService) UpdatePreset(id string, body map[string]any, operator string) (map[string]any, error) {
	id = util.Clean(id)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, item := range s.presets {
		if util.Clean(item["id"]) != id {
			continue
		}
		next := util.CopyMap(item)
		if value, ok := body["label"]; ok {
			if v := util.Clean(value); v != "" {
				next["label"] = v
			}
		}
		if value, ok := body["description"]; ok {
			next["description"] = util.Clean(value)
		}
		if value, ok := body["prompt"]; ok {
			prompt := util.Clean(value)
			if prompt == "" {
				return nil, fmt.Errorf("prompt is required")
			}
			if len([]rune(prompt)) > industryPromptMaxLength {
				return nil, fmt.Errorf("industry_prompt_too_long")
			}
			next["prompt"] = prompt
		}
		if value, ok := body["sort_order"]; ok {
			next["sort_order"] = util.ToInt(value, util.ToInt(item["sort_order"], 0))
		}
		if value, ok := body["enabled"]; ok {
			next["enabled"] = util.ToBool(value)
		}
		next["version"] = util.ToInt(item["version"], 1) + 1
		next["updated_at"] = util.NowISO()
		next["updated_by"] = operator
		s.presets[index] = next
		sortIndustryPresets(s.presets)
		if err := s.savePresetsLocked(); err != nil {
			return nil, err
		}
		return util.CopyMap(next), nil
	}
	return nil, fmt.Errorf("industry_prompt_not_found")
}

func (s *IndustryPromptService) DeletePreset(id string) bool {
	id = util.Clean(id)
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.presets[:0]
	removed := false
	for _, item := range s.presets {
		if util.Clean(item["id"]) == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return false
	}
	s.presets = append([]map[string]any(nil), next...)
	_ = s.savePresetsLocked()
	return true
}

func (s *IndustryPromptService) ImportPresets(items []map[string]any, operator string) (int, int, error) {
	if len(items) == 0 {
		return 0, 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	created, updated := 0, 0
	now := util.NowISO()
	for _, raw := range items {
		industryKey := normalizeIndustryKey(util.Clean(raw["industry_key"]))
		if industryKey == "" {
			continue
		}
		label := util.Clean(raw["label"])
		prompt := util.Clean(raw["prompt"])
		if label == "" || prompt == "" {
			continue
		}
		if len([]rune(prompt)) > industryPromptMaxLength {
			continue
		}
		existing := s.findPresetByKeyLocked(industryKey)
		if existing == nil {
			s.presets = append(s.presets, map[string]any{
				"id":           "ip_" + util.NewHex(12),
				"industry_key": industryKey,
				"label":        label,
				"description":  util.Clean(raw["description"]),
				"prompt":       prompt,
				"sort_order":   util.ToInt(raw["sort_order"], len(s.presets)+1),
				"enabled":      util.ToBool(util.ValueOr(raw["enabled"], true)),
				"version":      1,
				"created_at":   now,
				"updated_at":   now,
				"created_by":   operator,
				"updated_by":   operator,
			})
			created++
			continue
		}
		for i, item := range s.presets {
			if util.Clean(item["id"]) != util.Clean(existing["id"]) {
				continue
			}
			next := util.CopyMap(item)
			next["label"] = label
			next["description"] = util.Clean(raw["description"])
			next["prompt"] = prompt
			if value, ok := raw["sort_order"]; ok {
				next["sort_order"] = util.ToInt(value, util.ToInt(item["sort_order"], 0))
			}
			if value, ok := raw["enabled"]; ok {
				next["enabled"] = util.ToBool(value)
			}
			next["version"] = util.ToInt(item["version"], 1) + 1
			next["updated_at"] = now
			next["updated_by"] = operator
			s.presets[i] = next
			updated++
			break
		}
	}
	sortIndustryPresets(s.presets)
	if err := s.savePresetsLocked(); err != nil {
		return created, updated, err
	}
	return created, updated, nil
}

func (s *IndustryPromptService) ExportPresets() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyMaps(s.presets)
}

// ---------------- user overrides ----------------

func (s *IndustryPromptService) ListForUser(ownerID string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.loadUserDocLocked(ownerID)
	overrides := util.StringMap(doc["overrides"])
	out := make([]map[string]any, 0, len(s.presets))
	for _, preset := range s.presets {
		if !util.ToBool(preset["enabled"]) {
			continue
		}
		industryKey := util.Clean(preset["industry_key"])
		item := map[string]any{
			"industry_key": industryKey,
			"label":        util.Clean(preset["label"]),
			"description":  util.Clean(preset["description"]),
			"version":      util.ToInt(preset["version"], 1),
			"sort_order":   util.ToInt(preset["sort_order"], 0),
		}
		hasOverride := false
		resolvedPrompt := util.Clean(preset["prompt"])
		if override, ok := overrides[industryKey].(map[string]any); ok {
			hasOverride = true
			resolvedPrompt = util.Clean(override["prompt"])
		}
		item["has_override"] = hasOverride
		item["resolved_prompt"] = resolvedPrompt
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return util.ToInt(out[i]["sort_order"], 0) < util.ToInt(out[j]["sort_order"], 0)
	})
	return out
}

func (s *IndustryPromptService) GetForUser(ownerID, industryKey string) (map[string]any, bool) {
	industryKey = normalizeIndustryKey(industryKey)
	if industryKey == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preset := s.findPresetByKeyLocked(industryKey)
	if preset == nil {
		return nil, false
	}
	doc := s.loadUserDocLocked(ownerID)
	overrides := util.StringMap(doc["overrides"])
	item := map[string]any{
		"industry_key":  industryKey,
		"label":         util.Clean(preset["label"]),
		"description":   util.Clean(preset["description"]),
		"version":       util.ToInt(preset["version"], 1),
		"public_prompt": util.Clean(preset["prompt"]),
		"enabled":       util.ToBool(preset["enabled"]),
	}
	if override, ok := overrides[industryKey].(map[string]any); ok {
		item["has_override"] = true
		item["user_prompt"] = util.Clean(override["prompt"])
		if util.ToBool(preset["enabled"]) {
			item["resolved_prompt"] = util.Clean(override["prompt"])
		} else {
			item["resolved_prompt"] = util.Clean(override["prompt"])
		}
	} else {
		item["has_override"] = false
		item["user_prompt"] = ""
		if util.ToBool(preset["enabled"]) {
			item["resolved_prompt"] = util.Clean(preset["prompt"])
		} else {
			item["resolved_prompt"] = ""
		}
	}
	return item, true
}

func (s *IndustryPromptService) PutOverride(ownerID, industryKey, prompt string) (map[string]any, error) {
	industryKey = normalizeIndustryKey(industryKey)
	if industryKey == "" {
		return nil, fmt.Errorf("industry_key is required")
	}
	prompt = strings.TrimSpace(prompt)
	if len([]rune(prompt)) > industryPromptMaxLength {
		return nil, fmt.Errorf("industry_prompt_too_long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preset := s.findPresetByKeyLocked(industryKey)
	if preset == nil {
		return nil, fmt.Errorf("industry_prompt_not_found")
	}
	doc := s.loadUserDocLocked(ownerID)
	overrides := util.StringMap(doc["overrides"])
	if overrides == nil {
		overrides = map[string]any{}
	}
	overrides[industryKey] = map[string]any{
		"prompt":           prompt,
		"based_on_version": util.ToInt(preset["version"], 1),
		"updated_at":       util.NowISO(),
	}
	doc["overrides"] = overrides
	if err := s.saveUserDocLocked(ownerID, doc); err != nil {
		return nil, err
	}
	return util.CopyMap(overrides[industryKey].(map[string]any)), nil
}

func (s *IndustryPromptService) DeleteOverride(ownerID, industryKey string) bool {
	industryKey = normalizeIndustryKey(industryKey)
	if industryKey == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.loadUserDocLocked(ownerID)
	overrides := util.StringMap(doc["overrides"])
	if _, ok := overrides[industryKey]; !ok {
		return false
	}
	delete(overrides, industryKey)
	doc["overrides"] = overrides
	_ = s.saveUserDocLocked(ownerID, doc)
	return true
}

func (s *IndustryPromptService) GetCurrentIndustry(ownerID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.loadUserDocLocked(ownerID)
	key := util.Clean(doc["current_industry_key"])
	if key == "" {
		return "", false
	}
	preset := s.findPresetByKeyLocked(key)
	if preset == nil {
		return key, false
	}
	return key, util.ToBool(preset["enabled"])
}

func (s *IndustryPromptService) SetCurrentIndustry(ownerID, industryKey string) error {
	industryKey = normalizeIndustryKey(industryKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if industryKey != "" {
		if preset := s.findPresetByKeyLocked(industryKey); preset == nil {
			return fmt.Errorf("industry_prompt_not_found")
		}
	}
	doc := s.loadUserDocLocked(ownerID)
	doc["current_industry_key"] = industryKey
	return s.saveUserDocLocked(ownerID, doc)
}

// CountOverrides returns how many users have set overrides for a given industry_key.
// Loads all user docs from storage (best-effort).
func (s *IndustryPromptService) CountOverrides(industryKey string) int {
	industryKey = normalizeIndustryKey(industryKey)
	if industryKey == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, doc := range s.users {
		if _, ok := util.StringMap(doc["overrides"])[industryKey]; ok {
			count++
		}
	}
	return count
}

// ---------------- Resolve ----------------

// ResolveForUser returns the finalPrompt to prepend to userPrompt, and the source.
// When industryKey is empty, returns ("", IndustrySourceNone, nil).
func (s *IndustryPromptService) ResolveForUser(ownerID, industryKey string) (string, string, error) {
	industryKey = normalizeIndustryKey(industryKey)
	if industryKey == "" {
		return "", IndustrySourceNone, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preset := s.findPresetByKeyLocked(industryKey)
	presetPrompt := ""
	presetEnabled := false
	if preset != nil {
		presetPrompt = util.Clean(preset["prompt"])
		presetEnabled = util.ToBool(preset["enabled"])
	}
	doc := s.loadUserDocLocked(ownerID)
	overrides := util.StringMap(doc["overrides"])
	if override, ok := overrides[industryKey].(map[string]any); ok {
		return strings.TrimSpace(util.Clean(override["prompt"])), IndustrySourceUserOverride, nil
	}
	if preset == nil || !presetEnabled {
		return "", IndustrySourceNone, nil
	}
	return strings.TrimSpace(presetPrompt), IndustrySourcePublicPreset, nil
}

// ComposeFinalPrompt returns the composed prompt and the industry snippet used
// (already truncated to industryPromptFinalMaxLen if the composition overflows).
func ComposeIndustryFinalPrompt(industryPrompt, userPrompt string) (string, string) {
	industryPrompt = strings.TrimSpace(industryPrompt)
	userPrompt = strings.TrimSpace(userPrompt)
	if industryPrompt == "" {
		return userPrompt, ""
	}
	final := industryPrompt + "\n\n" + userPrompt
	runes := []rune(final)
	if len(runes) > industryPromptFinalMaxLen {
		final = string(runes[:industryPromptFinalMaxLen])
	}
	return final, industryPrompt
}

// ---------------- internal ----------------

func (s *IndustryPromptService) findPresetByKeyLocked(industryKey string) map[string]any {
	industryKey = normalizeIndustryKey(industryKey)
	if industryKey == "" {
		return nil
	}
	for _, item := range s.presets {
		if util.Clean(item["industry_key"]) == industryKey {
			return item
		}
	}
	return nil
}

func (s *IndustryPromptService) loadPresetsLocked() []map[string]any {
	raw := loadStoredJSON(s.store, industryPromptPresetDocName)
	items := make([]map[string]any, 0)
	for _, entry := range util.AsMapSlice(util.StringMap(raw)["items"]) {
		if key := normalizeIndustryKey(util.Clean(entry["industry_key"])); key != "" {
			entry["industry_key"] = key
			items = append(items, entry)
		}
	}
	// fallback: array root
	if len(items) == 0 {
		for _, entry := range util.AsMapSlice(raw) {
			if key := normalizeIndustryKey(util.Clean(entry["industry_key"])); key != "" {
				entry["industry_key"] = key
				items = append(items, entry)
			}
		}
	}
	sortIndustryPresets(items)
	return items
}

func (s *IndustryPromptService) savePresetsLocked() error {
	return saveStoredJSON(s.store, industryPromptPresetDocName, map[string]any{"items": s.presets})
}

func (s *IndustryPromptService) loadUserDocLocked(ownerID string) map[string]any {
	ownerID = util.Clean(ownerID)
	if ownerID == "" {
		return map[string]any{}
	}
	if doc, ok := s.users[ownerID]; ok {
		return doc
	}
	name := industryUserDocName(ownerID)
	raw := util.StringMap(loadStoredJSON(s.store, name))
	if raw == nil {
		raw = map[string]any{}
	}
	if raw["overrides"] == nil {
		raw["overrides"] = map[string]any{}
	}
	s.users[ownerID] = raw
	return raw
}

func (s *IndustryPromptService) saveUserDocLocked(ownerID string, doc map[string]any) error {
	ownerID = util.Clean(ownerID)
	if ownerID == "" {
		return fmt.Errorf("owner_id is required")
	}
	s.users[ownerID] = doc
	return saveStoredJSON(s.store, industryUserDocName(ownerID), doc)
}

func industryUserDocName(ownerID string) string {
	return industryPromptUserDocDir + "/" + util.SHA256Hex(ownerID) + ".json"
}

func normalizeIndustryKey(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	// allow a-z0-9 and dash/underscore; strip others
	out := make([]rune, 0, len(v))
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		}
	}
	return string(out)
}

func sortIndustryPresets(items []map[string]any) {
	sort.SliceStable(items, func(i, j int) bool {
		a := util.ToInt(items[i]["sort_order"], 0)
		b := util.ToInt(items[j]["sort_order"], 0)
		if a != b {
			return a < b
		}
		return util.Clean(items[i]["industry_key"]) < util.Clean(items[j]["industry_key"])
	})
}

// ---------------- seed ----------------

var industryPromptDefaultSeed = []map[string]any{
	{"industry_key": "ecommerce", "label": "电商零售", "description": "商品图 / 详情页视觉", "prompt": "面向电商零售场景生成图像：干净白底或场景化背景、突出商品主体、真实材质与光泽、避免过度滤镜，构图便于详情页与广告位使用。"},
	{"industry_key": "education", "label": "教育培训", "description": "课程封面 / 讲义配图", "prompt": "面向教育培训场景生成图像：清晰主题、正面积极、贴近课堂/学习情境，避免暗色或惊悚元素，色彩明快，适合海报与讲义。"},
	{"industry_key": "food", "label": "餐饮美食", "description": "菜品 / 品牌海报", "prompt": "面向餐饮美食场景生成图像：突出食材色泽与新鲜感、诱人构图、暖色调打光、避免劣质塑料感道具。"},
	{"industry_key": "travel", "label": "旅游出行", "description": "目的地宣传物料", "prompt": "面向旅游出行场景生成图像：真实自然的目的地风光或城市地标，光线通透、构图开阔、突出季节与在地文化。"},
	{"industry_key": "real-estate", "label": "地产家居", "description": "样板房 / 户型宣传", "prompt": "面向地产家居场景生成图像：干净整洁的空间、真实材质、自然光或柔和暖光、比例合理、体现居住品质。"},
	{"industry_key": "gaming", "label": "游戏动漫", "description": "角色 / 场景概念", "prompt": "面向游戏与动漫场景生成图像：清晰角色轮廓、动感构图、鲜明风格化色彩，保持画面结构与光影一致。"},
	{"industry_key": "finance", "label": "金融科技", "description": "banner / 产品视觉", "prompt": "面向金融与科技场景生成图像：现代感、简洁构图、蓝金/冷色调为主、避免夸张情绪，强调可信与专业。"},
	{"industry_key": "healthcare", "label": "医疗健康", "description": "科普 / 服务宣传", "prompt": "面向医疗健康场景生成图像：干净、专业、正向的画面语言、避免血腥或不适元素，柔和明亮的光线。"},
	{"industry_key": "legal", "label": "法律政务", "description": "普法宣传 / 服务", "prompt": "面向法律与政务场景生成图像：庄重、稳重、正式，避免夸张情绪或戏剧性冲突，构图对称、色调克制。"},
	{"industry_key": "corporate", "label": "企业官网", "description": "官网首屏 / 品牌形象", "prompt": "面向企业官网场景生成图像：现代商务感、真实办公或团队协作画面、避免过度渲染、强调专业与可信。"},
}

func (s *IndustryPromptService) seedPresetsLocked() {
	if len(s.presets) > 0 {
		return
	}
	if s.store == nil {
		return
	}
	now := util.NowISO()
	items := make([]map[string]any, 0, len(industryPromptDefaultSeed))
	for i, seed := range industryPromptDefaultSeed {
		items = append(items, map[string]any{
			"id":           "ip_" + util.NewHex(12),
			"industry_key": util.Clean(seed["industry_key"]),
			"label":        util.Clean(seed["label"]),
			"description":  util.Clean(seed["description"]),
			"prompt":       util.Clean(seed["prompt"]),
			"sort_order":   (i + 1) * 10,
			"enabled":      true,
			"version":      1,
			"created_at":   now,
			"updated_at":   now,
			"created_by":   "system",
			"updated_by":   "system",
		})
	}
	s.presets = items
	_ = s.savePresetsLocked()
}

// LoadAllUserDocsForBoot placeholder removed.
