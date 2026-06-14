package service

import (
	"strings"
	"testing"

	"chatgpt2api/internal/util"
)

// newTestOpenAIAccountService 构造一个空的 OpenAIAccountService 测试夹具，
// 共享一个新的 storage backend 与 LogService。
func newTestOpenAIAccountService(t *testing.T) *OpenAIAccountService {
	t.Helper()
	backend := newTestStorageBackend(t)
	return NewOpenAIAccountService(backend, NewLogService(backend))
}

// createTestOpenAIAccount 调用 Create 并把视图返回给调用方；遇到错误直接 t.Fatalf。
func createTestOpenAIAccount(t *testing.T, s *OpenAIAccountService, input map[string]any) map[string]any {
	t.Helper()
	view, err := s.Create(input)
	if err != nil {
		t.Fatalf("Create(%v) error = %v", input, err)
	}
	if view == nil {
		t.Fatalf("Create(%v) returned nil view", input)
	}
	return view
}

// modelState 从 publicOpenAIAccount 视图中读取 model_states[model] 子结构，
// 缺失或类型错误时调用 t.Fatalf。
func modelState(t *testing.T, view map[string]any, model string) map[string]any {
	t.Helper()
	states, ok := view["model_states"].(map[string]any)
	if !ok || states == nil {
		t.Fatalf("model_states missing on view: %#v", view)
	}
	state, ok := states[model].(map[string]any)
	if !ok || state == nil {
		t.Fatalf("model_states[%q] missing on view: %#v", model, states)
	}
	return state
}

// findOpenAIAccountInternal 通过 List 之外的内部 items 字段查找原始（未脱敏）记录。
// 由于测试位于同一个 service 包中，可以直接访问未导出字段。
func findOpenAIAccountInternal(t *testing.T, s *OpenAIAccountService, id string) map[string]any {
	t.Helper()
	for _, item := range s.items {
		if util.Clean(item["id"]) == id {
			return item
		}
	}
	t.Fatalf("internal account %s not found", id)
	return nil
}

// TestOpenAIAccountServiceCreate 验证 Create 在合法输入下：
//   - 返回的视图 api_key 已脱敏为 sk-***{last4}
//   - 自动生成的 id 以 oa_ 前缀开头
//   - 默认 priority=0 / concurrency=1
//   - allowed_models 中每个模型在 model_states 中初始化为默认值
//   - created_at / updated_at 非空
//
// _Requirements: 1.2
func TestOpenAIAccountServiceCreate(t *testing.T) {
	s := newTestOpenAIAccountService(t)
	view := createTestOpenAIAccount(t, s, map[string]any{
		"name":     "primary",
		"api_key":  "sk-test-abcdwxyz",
		"base_url": "https://api.example.com/v1",
		"allowed_models": []any{
			util.ImageModelGPTImage2,
			util.ImageModelGeminiFlashImage,
		},
	})

	if got := util.Clean(view["api_key"]); got != "sk-***wxyz" {
		t.Fatalf("view.api_key = %q, want sk-***wxyz", got)
	}

	id := util.Clean(view["id"])
	if !strings.HasPrefix(id, "oa_") {
		t.Fatalf("view.id = %q, want oa_ prefix", id)
	}
	if got := util.ToInt(view["priority"], -1); got != 0 {
		t.Fatalf("view.priority = %d, want 0", got)
	}
	if got := util.ToInt(view["concurrency"], -1); got != 1 {
		t.Fatalf("view.concurrency = %d, want 1", got)
	}

	allowed := util.AsStringSlice(view["allowed_models"])
	if len(allowed) != 2 {
		t.Fatalf("view.allowed_models = %v, want 2 entries", allowed)
	}
	for _, model := range []string{util.ImageModelGPTImage2, util.ImageModelGeminiFlashImage} {
		state := modelState(t, view, model)
		if got := util.Clean(state["status"]); got != openAIAccountModelStatusOK {
			t.Fatalf("model_states[%q].status = %q, want %q", model, got, openAIAccountModelStatusOK)
		}
		if got := util.ToInt(state["success"], -1); got != 0 {
			t.Fatalf("model_states[%q].success = %d, want 0", model, got)
		}
		if got := util.ToInt(state["fail"], -1); got != 0 {
			t.Fatalf("model_states[%q].fail = %d, want 0", model, got)
		}
		if got := util.Clean(state["error_message"]); got != "" {
			t.Fatalf("model_states[%q].error_message = %q, want empty", model, got)
		}
		if _, ok := state["last_used_at"]; !ok {
			t.Fatalf("model_states[%q] missing last_used_at field", model)
		}
	}

	if got := util.Clean(view["created_at"]); got == "" {
		t.Fatalf("view.created_at is empty, want non-empty ISO timestamp")
	}
	if got := util.Clean(view["updated_at"]); got == "" {
		t.Fatalf("view.updated_at is empty, want non-empty ISO timestamp")
	}

	// 内部 items 中 api_key 必须保持原值，不能被脱敏污染。
	internal := findOpenAIAccountInternal(t, s, id)
	if got := util.Clean(internal["api_key"]); got != "sk-test-abcdwxyz" {
		t.Fatalf("internal api_key = %q, want sk-test-abcdwxyz (raw)", got)
	}
}

// TestOpenAIAccountServiceCreateValidationFailures 表驱动覆盖 Create 阶段
// 校验失败的全部分支，每个 case 验证错误信息文本与不写入持久化。
//
// _Requirements: 1.5, 1.6, 1.7
func TestOpenAIAccountServiceCreateValidationFailures(t *testing.T) {
	cases := []struct {
		name    string
		input   map[string]any
		wantErr string
	}{
		{
			name: "missing api_key",
			input: map[string]any{
				"base_url":       "https://api.example.com/v1",
				"allowed_models": []any{util.ImageModelGPTImage2},
			},
			wantErr: "api_key is required",
		},
		{
			name: "whitespace api_key",
			input: map[string]any{
				"api_key":        "   ",
				"base_url":       "https://api.example.com/v1",
				"allowed_models": []any{util.ImageModelGPTImage2},
			},
			wantErr: "api_key is required",
		},
		{
			name: "missing base_url",
			input: map[string]any{
				"api_key":        "sk-test-1234",
				"allowed_models": []any{util.ImageModelGPTImage2},
			},
			wantErr: "base_url must be a valid http(s) absolute URL",
		},
		{
			name: "malformed base_url",
			input: map[string]any{
				"api_key":        "sk-test-1234",
				"base_url":       "not-a-url",
				"allowed_models": []any{util.ImageModelGPTImage2},
			},
			wantErr: "base_url must be a valid http(s) absolute URL",
		},
		{
			name: "non-http scheme base_url",
			input: map[string]any{
				"api_key":        "sk-test-1234",
				"base_url":       "ftp://example.com",
				"allowed_models": []any{util.ImageModelGPTImage2},
			},
			wantErr: "base_url must be a valid http(s) absolute URL",
		},
		{
			name: "empty allowed_models",
			input: map[string]any{
				"api_key":        "sk-test-1234",
				"base_url":       "https://api.example.com/v1",
				"allowed_models": []any{},
			},
			wantErr: "allowed_models must be a non-empty subset of {gpt-image-2, gemini-3.1-flash-image}",
		},
		{
			name: "unknown allowed_models codex",
			input: map[string]any{
				"api_key":        "sk-test-1234",
				"base_url":       "https://api.example.com/v1",
				"allowed_models": []any{util.ImageModelCodexGPTImage2},
			},
			wantErr: "allowed_models must be a non-empty subset of {gpt-image-2, gemini-3.1-flash-image}",
		},
		{
			name: "unknown allowed_models dall-e-3",
			input: map[string]any{
				"api_key":        "sk-test-1234",
				"base_url":       "https://api.example.com/v1",
				"allowed_models": []any{"dall-e-3"},
			},
			wantErr: "allowed_models must be a non-empty subset of {gpt-image-2, gemini-3.1-flash-image}",
		},
		{
			name: "concurrency zero",
			input: map[string]any{
				"api_key":        "sk-test-1234",
				"base_url":       "https://api.example.com/v1",
				"allowed_models": []any{util.ImageModelGPTImage2},
				"concurrency":    0,
			},
			wantErr: "concurrency must be >= 1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestOpenAIAccountService(t)
			view, err := s.Create(tc.input)
			if err == nil {
				t.Fatalf("Create(%v) expected error %q, got view = %#v", tc.input, tc.wantErr, view)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Create error = %q, want contain %q", err.Error(), tc.wantErr)
			}
			if len(s.items) != 0 {
				t.Fatalf("Create failure must not persist; items = %#v", s.items)
			}
		})
	}
}

// TestOpenAIAccountServiceUpdate 验证 Update 的关键行为：
//   - 空白 api_key 保留原值，不会清空
//   - allowed_models 收缩时移除多余的 model_states，保留剩余模型的计数
//   - allowed_models 重新扩张时新增模型用默认值初始化
//
// _Requirements: 1.2, 1.5
func TestOpenAIAccountServiceUpdate(t *testing.T) {
	s := newTestOpenAIAccountService(t)
	created := createTestOpenAIAccount(t, s, map[string]any{
		"name":     "primary",
		"api_key":  "sk-orig-abcd1234",
		"base_url": "https://api.example.com/v1",
		"allowed_models": []any{
			util.ImageModelGPTImage2,
			util.ImageModelGeminiFlashImage,
		},
	})
	id := util.Clean(created["id"])

	// 在收缩 allowed_models 之前先在 gemini 上累积一次成功，验证计数能跨 update 保留。
	s.MarkModelResult(id, util.ImageModelGeminiFlashImage, true, "")

	// 1) 空白 api_key 保留原值
	view, err := s.Update(id, map[string]any{
		"api_key": "  ",
		"name":    "primary-renamed",
	})
	if err != nil {
		t.Fatalf("Update(empty api_key): %v", err)
	}
	if got := util.Clean(view["api_key"]); got != "sk-***1234" {
		t.Fatalf("view.api_key after empty-patch update = %q, want sk-***1234", got)
	}
	internal := findOpenAIAccountInternal(t, s, id)
	if got := util.Clean(internal["api_key"]); got != "sk-orig-abcd1234" {
		t.Fatalf("internal api_key after empty-patch update = %q, want sk-orig-abcd1234", got)
	}
	if got := util.Clean(view["name"]); got != "primary-renamed" {
		t.Fatalf("view.name after update = %q, want primary-renamed", got)
	}

	// 2) 收缩 allowed_models：移除 gpt-image-2，保留 gemini 及其计数
	view, err = s.Update(id, map[string]any{
		"allowed_models": []any{util.ImageModelGeminiFlashImage},
	})
	if err != nil {
		t.Fatalf("Update(shrink allowed_models): %v", err)
	}
	if got := util.AsStringSlice(view["allowed_models"]); len(got) != 1 || got[0] != util.ImageModelGeminiFlashImage {
		t.Fatalf("allowed_models after shrink = %v, want [%s]", got, util.ImageModelGeminiFlashImage)
	}
	states, _ := view["model_states"].(map[string]any)
	if states == nil {
		t.Fatalf("model_states missing after shrink: %#v", view)
	}
	if _, ok := states[util.ImageModelGPTImage2]; ok {
		t.Fatalf("model_states[%q] should be removed after shrink: %#v", util.ImageModelGPTImage2, states)
	}
	geminiState := modelState(t, view, util.ImageModelGeminiFlashImage)
	if got := util.ToInt(geminiState["success"], -1); got != 1 {
		t.Fatalf("gemini.success after shrink = %d, want 1 (preserved across update)", got)
	}

	// 3) 再次扩张：把 gpt-image-2 加回来，模型状态应初始化为默认值
	view, err = s.Update(id, map[string]any{
		"allowed_models": []any{
			util.ImageModelGeminiFlashImage,
			util.ImageModelGPTImage2,
		},
	})
	if err != nil {
		t.Fatalf("Update(expand allowed_models): %v", err)
	}
	gptState := modelState(t, view, util.ImageModelGPTImage2)
	if got := util.Clean(gptState["status"]); got != openAIAccountModelStatusOK {
		t.Fatalf("gpt-image-2.status after re-add = %q, want %q", got, openAIAccountModelStatusOK)
	}
	if got := util.ToInt(gptState["success"], -1); got != 0 {
		t.Fatalf("gpt-image-2.success after re-add = %d, want 0 (default initialized)", got)
	}
	if got := util.ToInt(gptState["fail"], -1); got != 0 {
		t.Fatalf("gpt-image-2.fail after re-add = %d, want 0 (default initialized)", got)
	}
	geminiState = modelState(t, view, util.ImageModelGeminiFlashImage)
	if got := util.ToInt(geminiState["success"], -1); got != 1 {
		t.Fatalf("gemini.success after expand = %d, want 1 (still preserved)", got)
	}
}

// TestOpenAIAccountServiceDeleteReleasesReservations 验证 Delete 同时清除
// in-memory 槽位计数；删除后 Reserve 找不到候选返回错误。
//
// _Requirements: 1.10, 1.12
func TestOpenAIAccountServiceDeleteReleasesReservations(t *testing.T) {
	s := newTestOpenAIAccountService(t)
	created := createTestOpenAIAccount(t, s, map[string]any{
		"api_key":        "sk-del-abcd",
		"base_url":       "https://api.example.com/v1",
		"allowed_models": []any{util.ImageModelGPTImage2},
		"concurrency":    2,
	})
	id := util.Clean(created["id"])

	first, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	if first.AccountID != id {
		t.Fatalf("first reservation account = %q, want %q", first.AccountID, id)
	}
	second, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil)
	if err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	if second.AccountID != id {
		t.Fatalf("second reservation account = %q, want %q", second.AccountID, id)
	}
	if got := s.reservations[id]; got != 2 {
		t.Fatalf("reservations[%q] before delete = %d, want 2", id, got)
	}

	if !s.Delete(id) {
		t.Fatalf("Delete(%q) returned false", id)
	}
	if _, ok := s.reservations[id]; ok {
		t.Fatalf("reservations[%q] still present after Delete: %v", id, s.reservations)
	}

	if _, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil); err == nil {
		t.Fatalf("Reserve after Delete expected error, got nil")
	} else if !strings.Contains(err.Error(), "no available openai-protocol account") {
		t.Fatalf("Reserve error = %q, want contain `no available openai-protocol account`", err.Error())
	}
}

// TestOpenAIAccountServiceReservePriorityAndSlots 验证 ReserveForUpstreamModel
// 的 priority 升序选择、并发槽位耗尽切换、Release 后重新可用、跨 upstream
// 模型隔离、exclude 集合作用、HasAvailableForUpstreamModel 在初始/耗尽两态
// 下的返回值。
//
// _Requirements: 1.10
func TestOpenAIAccountServiceReservePriorityAndSlots(t *testing.T) {
	s := newTestOpenAIAccountService(t)
	acc1View := createTestOpenAIAccount(t, s, map[string]any{
		"name":           "acc1-high-priority",
		"api_key":        "sk-acc1-aaaa",
		"base_url":       "https://api.example.com/v1",
		"allowed_models": []any{util.ImageModelGPTImage2},
		"priority":       10,
		"concurrency":    1,
	})
	acc2View := createTestOpenAIAccount(t, s, map[string]any{
		"name":           "acc2-low-priority",
		"api_key":        "sk-acc2-bbbb",
		"base_url":       "https://api.example.com/v1",
		"allowed_models": []any{util.ImageModelGPTImage2},
		"priority":       0,
		"concurrency":    2,
	})
	acc3View := createTestOpenAIAccount(t, s, map[string]any{
		"name":           "acc3-gemini-only",
		"api_key":        "sk-acc3-cccc",
		"base_url":       "https://api.example.com/v1",
		"allowed_models": []any{util.ImageModelGeminiFlashImage},
		"priority":       0,
		"concurrency":    1,
	})
	acc1ID := util.Clean(acc1View["id"])
	acc2ID := util.Clean(acc2View["id"])
	acc3ID := util.Clean(acc3View["id"])

	// 初始：gpt-image-2 至少有 acc1+acc2 两个候选可用。
	if !s.HasAvailableForUpstreamModel(util.ImageModelGPTImage2) {
		t.Fatalf("HasAvailableForUpstreamModel(gpt-image-2) before any reserve = false, want true")
	}

	// 第一次预留：acc2（priority=0 优先于 acc1 的 priority=10）。
	first, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil)
	if err != nil {
		t.Fatalf("first Reserve(gpt-image-2): %v", err)
	}
	if first.AccountID != acc2ID {
		t.Fatalf("first reservation = %q, want acc2 %q (lower priority wins)", first.AccountID, acc2ID)
	}

	// 第二次预留：acc2 仍未耗尽（concurrency=2），继续返回 acc2。
	second, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil)
	if err != nil {
		t.Fatalf("second Reserve(gpt-image-2): %v", err)
	}
	if second.AccountID != acc2ID {
		t.Fatalf("second reservation = %q, want acc2 %q (concurrency=2 not yet exhausted)", second.AccountID, acc2ID)
	}

	// 第三次预留：acc2 耗尽，必须切到 acc1。
	third, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil)
	if err != nil {
		t.Fatalf("third Reserve(gpt-image-2): %v", err)
	}
	if third.AccountID != acc1ID {
		t.Fatalf("third reservation = %q, want acc1 %q (acc2 exhausted)", third.AccountID, acc1ID)
	}

	// 第四次预留：acc1 与 acc2 都已满，应当失败。
	if _, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil); err == nil {
		t.Fatalf("fourth Reserve expected error, got nil")
	}
	if s.HasAvailableForUpstreamModel(util.ImageModelGPTImage2) {
		t.Fatalf("HasAvailableForUpstreamModel(gpt-image-2) after exhausting both = true, want false")
	}

	// 释放 acc2 一个槽位 → 再次预留：再次拿到 acc2。
	s.Release(acc2ID)
	fifth, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, nil)
	if err != nil {
		t.Fatalf("fifth Reserve after release acc2: %v", err)
	}
	if fifth.AccountID != acc2ID {
		t.Fatalf("fifth reservation = %q, want acc2 %q (released slot reusable)", fifth.AccountID, acc2ID)
	}

	// 跨 upstream 模型隔离：gemini 通路只能命中 acc3。
	gemini, err := s.ReserveForUpstreamModel(util.ImageModelGeminiFlashImage, nil)
	if err != nil {
		t.Fatalf("Reserve(gemini): %v", err)
	}
	if gemini.AccountID != acc3ID {
		t.Fatalf("gemini reservation = %q, want acc3 %q", gemini.AccountID, acc3ID)
	}

	// 释放 acc1，使其在 exclude={acc2} 场景下成为唯一可用候选；验证 exclude 生效。
	s.Release(acc1ID)
	excludeView, err := s.ReserveForUpstreamModel(util.ImageModelGPTImage2, map[string]struct{}{acc2ID: {}})
	if err != nil {
		t.Fatalf("Reserve(gpt-image-2, exclude=acc2): %v", err)
	}
	if excludeView.AccountID != acc1ID {
		t.Fatalf("exclude reservation = %q, want acc1 %q (acc2 excluded)", excludeView.AccountID, acc1ID)
	}
}

// TestOpenAIAccountServiceMarkModelResultIsolation 验证 MarkModelResult 仅
// 作用于指定模型，不影响其他模型；未知模型静默忽略，不 panic 也不变更状态。
//
// _Requirements: 1.10
func TestOpenAIAccountServiceMarkModelResultIsolation(t *testing.T) {
	s := newTestOpenAIAccountService(t)
	created := createTestOpenAIAccount(t, s, map[string]any{
		"api_key":  "sk-mark-abcd",
		"base_url": "https://api.example.com/v1",
		"allowed_models": []any{
			util.ImageModelGPTImage2,
			util.ImageModelGeminiFlashImage,
		},
	})
	id := util.Clean(created["id"])

	// 1) 失败 → 仅 gpt-image-2 计数与 error_message 更新；gemini 不变。
	s.MarkModelResult(id, util.ImageModelGPTImage2, false, "boom")
	view := snapshotView(t, s, id)
	gpt := modelState(t, view, util.ImageModelGPTImage2)
	if got := util.ToInt(gpt["fail"], -1); got != 1 {
		t.Fatalf("gpt-image-2.fail after failure = %d, want 1", got)
	}
	if got := util.Clean(gpt["error_message"]); got != "boom" {
		t.Fatalf("gpt-image-2.error_message after failure = %q, want %q", got, "boom")
	}
	gemini := modelState(t, view, util.ImageModelGeminiFlashImage)
	if got := util.ToInt(gemini["success"], -1); got != 0 {
		t.Fatalf("gemini.success after gpt-only failure = %d, want 0", got)
	}
	if got := util.ToInt(gemini["fail"], -1); got != 0 {
		t.Fatalf("gemini.fail after gpt-only failure = %d, want 0", got)
	}
	if got := util.Clean(gemini["status"]); got != openAIAccountModelStatusOK {
		t.Fatalf("gemini.status after gpt-only failure = %q, want %q", got, openAIAccountModelStatusOK)
	}
	if got := util.Clean(gemini["error_message"]); got != "" {
		t.Fatalf("gemini.error_message after gpt-only failure = %q, want empty", got)
	}

	// 2) 成功 → gpt-image-2 success+1，error_message 清空、status 重置；fail 不动。
	s.MarkModelResult(id, util.ImageModelGPTImage2, true, "")
	view = snapshotView(t, s, id)
	gpt = modelState(t, view, util.ImageModelGPTImage2)
	if got := util.ToInt(gpt["fail"], -1); got != 1 {
		t.Fatalf("gpt-image-2.fail after success = %d, want 1 (unchanged)", got)
	}
	if got := util.ToInt(gpt["success"], -1); got != 1 {
		t.Fatalf("gpt-image-2.success after success = %d, want 1", got)
	}
	if got := util.Clean(gpt["status"]); got != openAIAccountModelStatusOK {
		t.Fatalf("gpt-image-2.status after success = %q, want %q", got, openAIAccountModelStatusOK)
	}
	if got := util.Clean(gpt["error_message"]); got != "" {
		t.Fatalf("gpt-image-2.error_message after success = %q, want empty", got)
	}

	// 3) 未知模型 → 静默忽略；不 panic 且所有状态保持上一步的值。
	s.MarkModelResult(id, "unknown-model", true, "")
	view = snapshotView(t, s, id)
	gpt = modelState(t, view, util.ImageModelGPTImage2)
	if got := util.ToInt(gpt["success"], -1); got != 1 {
		t.Fatalf("gpt-image-2.success after unknown-model call = %d, want 1 (unchanged)", got)
	}
	if got := util.ToInt(gpt["fail"], -1); got != 1 {
		t.Fatalf("gpt-image-2.fail after unknown-model call = %d, want 1 (unchanged)", got)
	}
	gemini = modelState(t, view, util.ImageModelGeminiFlashImage)
	if got := util.ToInt(gemini["success"], -1); got != 0 {
		t.Fatalf("gemini.success after unknown-model call = %d, want 0", got)
	}
	if got := util.ToInt(gemini["fail"], -1); got != 0 {
		t.Fatalf("gemini.fail after unknown-model call = %d, want 0", got)
	}
}

// TestOpenAIAccountServiceUpdateModelStateOnlyAffectsTarget 验证
// UpdateModelState 仅修改指定模型的状态字段，不影响其他模型；
// 模型不在 allowed_models 内时返回错误。
//
// _Requirements: 1.10
func TestOpenAIAccountServiceUpdateModelStateOnlyAffectsTarget(t *testing.T) {
	s := newTestOpenAIAccountService(t)
	created := createTestOpenAIAccount(t, s, map[string]any{
		"api_key":  "sk-state-abcd",
		"base_url": "https://api.example.com/v1",
		"allowed_models": []any{
			util.ImageModelGPTImage2,
			util.ImageModelGeminiFlashImage,
		},
	})
	id := util.Clean(created["id"])

	view, err := s.UpdateModelState(id, util.ImageModelGPTImage2, map[string]any{
		"status":        "限流",
		"error_message": "rate",
	})
	if err != nil {
		t.Fatalf("UpdateModelState(gpt-image-2): %v", err)
	}
	gpt := modelState(t, view, util.ImageModelGPTImage2)
	if got := util.Clean(gpt["status"]); got != "限流" {
		t.Fatalf("gpt-image-2.status = %q, want 限流", got)
	}
	if got := util.Clean(gpt["error_message"]); got != "rate" {
		t.Fatalf("gpt-image-2.error_message = %q, want rate", got)
	}
	gemini := modelState(t, view, util.ImageModelGeminiFlashImage)
	if got := util.Clean(gemini["status"]); got != openAIAccountModelStatusOK {
		t.Fatalf("gemini.status after gpt-only patch = %q, want %q", got, openAIAccountModelStatusOK)
	}
	if got := util.Clean(gemini["error_message"]); got != "" {
		t.Fatalf("gemini.error_message after gpt-only patch = %q, want empty", got)
	}

	if _, err := s.UpdateModelState(id, "unknown-model", map[string]any{"status": "限流"}); err == nil {
		t.Fatalf("UpdateModelState(unknown-model) expected error, got nil")
	} else if !strings.Contains(err.Error(), "is not in allowed_models") {
		t.Fatalf("UpdateModelState(unknown-model) error = %q, want contain `is not in allowed_models`", err.Error())
	}
}

// snapshotView 取出指定 id 的脱敏视图；找不到时调用 t.Fatalf。
func snapshotView(t *testing.T, s *OpenAIAccountService, id string) map[string]any {
	t.Helper()
	for _, view := range s.List() {
		if util.Clean(view["id"]) == id {
			return view
		}
	}
	t.Fatalf("view for %q not found via List()", id)
	return nil
}
