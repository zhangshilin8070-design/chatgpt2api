package protocol

import (
	"errors"
	"fmt"
	"testing"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

// AutoRoute 是 AutoRouteResolver 的生产实现，覆盖以下三类入口：
//
//   - 已知对外模型：直接经 util.BucketForModel 校验后透传；
//   - "auto" / 空字符串：进入双桶余额 + 桶内可调度性策略选取；
//   - 非法模型：返回 util.BucketForModel 的错误。
//
// 本文件按 Requirement 5.2 / 5.3 / 5.6 / 5.9 / 5.10 编排表驱动测试，
// 复用 conversation_test.go 中定义的 fakeBillingChecker / fakeChatGPTInspector /
// fakeOpenAIReserver 三个最简化测试桩。

func TestAutoRouteResolveExplicitModelBypassesAuto(t *testing.T) {
	cases := []struct {
		model      string
		wantModel  string
		wantBucket string
	}{
		{util.ImageModelGPTImage2, util.ImageModelGPTImage2, util.ImageBucketA},
		{util.ImageModelCodexGPTImage2, util.ImageModelCodexGPTImage2, util.ImageBucketB},
		{util.ImageModelGeminiFlashImage, util.ImageModelGeminiFlashImage, util.ImageBucketB},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			route := AutoRoute{
				Billing:        fakeBillingChecker{},
				Accounts:       fakeChatGPTInspector{},
				OpenAIAccounts: &fakeOpenAIReserver{},
				PreferBucketB:  AutoPreferBucketBGemini,
			}
			gotModel, gotBucket, err := route.Resolve(service.Identity{ID: "u1"}, tc.model, 1)
			if err != nil {
				t.Fatalf("Resolve(%q) err = %v", tc.model, err)
			}
			if gotModel != tc.wantModel || gotBucket != tc.wantBucket {
				t.Fatalf("Resolve(%q) = (%q, %q), want (%q, %q)", tc.model, gotModel, gotBucket, tc.wantModel, tc.wantBucket)
			}
		})
	}
}

func TestAutoRouteResolveBucketAReadyAlwaysWins(t *testing.T) {
	for _, prefer := range []string{"", AutoPreferBucketBCodex, AutoPreferBucketBGemini} {
		t.Run("prefer="+prefer, func(t *testing.T) {
			route := AutoRoute{
				Billing:        fakeBillingChecker{},
				Accounts:       fakeChatGPTInspector{available: true, paidAvailable: true},
				OpenAIAccounts: &fakeOpenAIReserver{available: map[string]bool{util.ImageModelGPTImage2: true, util.ImageModelGeminiFlashImage: true}},
				PreferBucketB:  prefer,
			}
			model, bucket, err := route.Resolve(service.Identity{ID: "u1"}, util.ImageModelAuto, 1)
			if err != nil {
				t.Fatalf("Resolve(auto) err = %v", err)
			}
			if model != util.ImageModelGPTImage2 || bucket != util.ImageBucketA {
				t.Fatalf("Resolve(auto) = (%q, %q), want (%q, %q)", model, bucket, util.ImageModelGPTImage2, util.ImageBucketA)
			}
		})
	}
}

func TestAutoRouteResolveBucketBPreference(t *testing.T) {
	cases := []struct {
		name       string
		prefer     string
		codex      bool
		gemini     bool
		wantModel  string
		wantBucket string
	}{
		{
			name:       "prefer codex with both dispatchable",
			prefer:     AutoPreferBucketBCodex,
			codex:      true,
			gemini:     true,
			wantModel:  util.ImageModelCodexGPTImage2,
			wantBucket: util.ImageBucketB,
		},
		{
			name:       "prefer gemini with both dispatchable",
			prefer:     AutoPreferBucketBGemini,
			codex:      true,
			gemini:     true,
			wantModel:  util.ImageModelGeminiFlashImage,
			wantBucket: util.ImageBucketB,
		},
		{
			name:       "empty preference falls back to codex",
			prefer:     "",
			codex:      true,
			gemini:     true,
			wantModel:  util.ImageModelCodexGPTImage2,
			wantBucket: util.ImageBucketB,
		},
		{
			name:       "only codex dispatchable",
			prefer:     AutoPreferBucketBGemini,
			codex:      true,
			gemini:     false,
			wantModel:  util.ImageModelCodexGPTImage2,
			wantBucket: util.ImageBucketB,
		},
		{
			name:       "only gemini dispatchable",
			prefer:     AutoPreferBucketBCodex,
			codex:      false,
			gemini:     true,
			wantModel:  util.ImageModelGeminiFlashImage,
			wantBucket: util.ImageBucketB,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			openai := &fakeOpenAIReserver{available: map[string]bool{}}
			if tc.codex {
				openai.available[util.ImageModelGPTImage2] = true
			}
			if tc.gemini {
				openai.available[util.ImageModelGeminiFlashImage] = true
			}
			// 桶 A 余额 OK 但没有 ChatGPT 账号 → 桶 A 不可调度，路由必须落到桶 B。
			route := AutoRoute{
				Billing:        fakeBillingChecker{},
				Accounts:       fakeChatGPTInspector{available: false, paidAvailable: false},
				OpenAIAccounts: openai,
				PreferBucketB:  tc.prefer,
			}
			model, bucket, err := route.Resolve(service.Identity{ID: "u1"}, util.ImageModelAuto, 1)
			if err != nil {
				t.Fatalf("Resolve(auto, prefer=%q) err = %v", tc.prefer, err)
			}
			if model != tc.wantModel || bucket != tc.wantBucket {
				t.Fatalf("Resolve(auto, prefer=%q) = (%q, %q), want (%q, %q)", tc.prefer, model, bucket, tc.wantModel, tc.wantBucket)
			}
		})
	}
}

func TestAutoRouteResolveBothBucketsOutOfBalance(t *testing.T) {
	bucketALimit := service.NewBillingLimitError(util.ImageBucketA, "")
	bucketBLimit := service.NewBillingLimitError(util.ImageBucketB, "")

	cases := []struct {
		name       string
		prefer     string
		codex      bool
		gemini     bool
		wantBucket string
	}{
		{
			name:       "prefer codex with codex dispatchable returns bucket_b",
			prefer:     AutoPreferBucketBCodex,
			codex:      true,
			gemini:     false,
			wantBucket: util.ImageBucketB,
		},
		{
			name:       "prefer gemini with gemini dispatchable returns bucket_b",
			prefer:     AutoPreferBucketBGemini,
			codex:      false,
			gemini:     true,
			wantBucket: util.ImageBucketB,
		},
		{
			name:       "empty preference returns bucket_a",
			prefer:     "",
			codex:      true,
			gemini:     true,
			wantBucket: util.ImageBucketA,
		},
		{
			name:       "prefer codex but codex not dispatchable returns bucket_a",
			prefer:     AutoPreferBucketBCodex,
			codex:      false,
			gemini:     false,
			wantBucket: util.ImageBucketA,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			openai := &fakeOpenAIReserver{available: map[string]bool{}}
			if tc.codex {
				openai.available[util.ImageModelGPTImage2] = true
			}
			if tc.gemini {
				openai.available[util.ImageModelGeminiFlashImage] = true
			}
			route := AutoRoute{
				Billing:        fakeBillingChecker{bucketAErr: bucketALimit, bucketBErr: bucketBLimit},
				Accounts:       fakeChatGPTInspector{available: false, paidAvailable: false},
				OpenAIAccounts: openai,
				PreferBucketB:  tc.prefer,
			}
			_, _, err := route.Resolve(service.Identity{ID: "u1"}, util.ImageModelAuto, 1)
			if err == nil {
				t.Fatal("Resolve(auto) err = nil, want BillingLimitError")
			}
			var limit service.BillingLimitError
			if !errors.As(err, &limit) {
				t.Fatalf("Resolve(auto) err = %T %v, want service.BillingLimitError", err, err)
			}
			if limit.Bucket != tc.wantBucket {
				t.Fatalf("BillingLimitError.Bucket = %q, want %q (err=%v)", limit.Bucket, tc.wantBucket, err)
			}
			wantCodePrefix := "user_balance_insufficient_"
			wantCode := wantCodePrefix + tc.wantBucket
			if limit.Code != wantCode {
				t.Fatalf("BillingLimitError.Code = %q, want %q", limit.Code, wantCode)
			}
		})
	}
}

func TestAutoRouteResolveNoUpstream(t *testing.T) {
	route := AutoRoute{
		Billing:        fakeBillingChecker{},
		Accounts:       fakeChatGPTInspector{available: false, paidAvailable: false},
		OpenAIAccounts: &fakeOpenAIReserver{available: map[string]bool{}},
		PreferBucketB:  AutoPreferBucketBCodex,
	}
	_, _, err := route.Resolve(service.Identity{ID: "u1"}, util.ImageModelAuto, 1)
	if !errors.Is(err, ErrNoUpstreamForAutoRoute) {
		t.Fatalf("Resolve(auto) err = %v, want ErrNoUpstreamForAutoRoute", err)
	}
}

func TestAutoRouteResolveUnknownModelReturnsError(t *testing.T) {
	route := AutoRoute{
		Billing:        fakeBillingChecker{},
		Accounts:       fakeChatGPTInspector{},
		OpenAIAccounts: &fakeOpenAIReserver{},
	}
	_, _, err := route.Resolve(service.Identity{ID: "u1"}, "definitely-not-a-model", 1)
	if err == nil {
		t.Fatal("Resolve(unknown) err = nil, want bucket-resolution error")
	}
	wantSubstr := fmt.Sprintf("model %s is not a billable image model", "definitely-not-a-model")
	if got := err.Error(); got != wantSubstr {
		t.Fatalf("Resolve(unknown) err = %q, want %q", got, wantSubstr)
	}
}
