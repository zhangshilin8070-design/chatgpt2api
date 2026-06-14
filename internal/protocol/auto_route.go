package protocol

import (
	"errors"
	"strings"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

// Auto_Mode 偏好桶 B 内部的对外模型取值集合。仅在程序启动时由配置项
// CHATGPT2API_AUTO_PREFER_BUCKET_B_MODEL 注入；运行期不可变。
const (
	AutoPreferBucketBCodex  = "codex"
	AutoPreferBucketBGemini = "gemini"
)

// ErrNoUpstreamForAutoRoute 表示尽管至少一个桶仍有余额，但该桶内没有
// 任何对外模型存在可调度的物理上游通路。HTTP 层应映射到 503。
var ErrNoUpstreamForAutoRoute = errors.New("no available upstream account for auto routing")

// AutoChatGPTAccountInspector 暴露 Auto 路由所需的 ChatGPT 账号池只读
// 视图。其唯一实现是 *service.AccountService（满足同名方法签名）。
//
// HasAvailableImageAccount: 桶 A 可调度判定 —— 至少一个 status==正常 且
// 还有图像槽位的账号（无论付费等级）。
// HasAvailablePaidImageAccount: 桶 B codex 通路 ChatGPT 侧可调度判定 ——
// 至少一个 Plus / ProLite / Pro / Team 账号 status==正常 且仍有图像槽位。
type AutoChatGPTAccountInspector interface {
	HasAvailableImageAccount() bool
	HasAvailablePaidImageAccount() bool
}

// chatgptAccountInspectorAdapter 把 *service.AccountService 适配为
// AutoChatGPTAccountInspector 接口。AccountService 历史方法名为
// HasAvailableAccount / HasAvailablePaidImageAccount，本类型仅做改名转发。
type chatgptAccountInspectorAdapter struct {
	accounts *service.AccountService
}

func (a chatgptAccountInspectorAdapter) HasAvailableImageAccount() bool {
	if a.accounts == nil {
		return false
	}
	return a.accounts.HasAvailableAccount()
}

func (a chatgptAccountInspectorAdapter) HasAvailablePaidImageAccount() bool {
	if a.accounts == nil {
		return false
	}
	return a.accounts.HasAvailablePaidImageAccount()
}

// NewChatGPTAccountInspector 把现有 AccountService 包装为 Auto 路由所需
// 的只读视图。main.go 在装配 AutoRoute 时调用本函数。
func NewChatGPTAccountInspector(accounts *service.AccountService) AutoChatGPTAccountInspector {
	return chatgptAccountInspectorAdapter{accounts: accounts}
}

// AutoRoute 是 AutoRouteResolver 的生产实现。装配点位于 main.go：
// 字段 Billing / Accounts / OpenAIAccounts 由对应服务接口实现注入；
// PreferBucketB 在启动期完成校验/默认化后传入。
type AutoRoute struct {
	Billing        BillingChecker
	Accounts       AutoChatGPTAccountInspector
	OpenAIAccounts OpenAIAccountReserver
	PreferBucketB  string
}

// Resolve 实现 AutoRouteResolver。
//
// 路径分支：
//   - originalModel = "auto" 或空字符串：按 Requirement 5 进入 Auto 解析。
//   - originalModel ∈ External_Image_Model：直接通过 util.BucketForModel
//     校验并返回 (originalModel, bucket, nil)。Auto 不参与，物理通路选择
//     仍由 Image_Engine 在 runSingleImageOutput 内基于桶完成。
//   - 其他取值：util.BucketForModel 返回 `model X is not a billable image model`
//     错误，由调用方映射为 HTTP 400。
func (r AutoRoute) Resolve(identity service.Identity, originalModel string, n int) (string, string, error) {
	model := strings.TrimSpace(originalModel)
	if model != "" && model != util.ImageModelAuto {
		bucket, err := util.BucketForModel(model)
		if err != nil {
			return "", "", err
		}
		return model, bucket, nil
	}
	return resolveAutoRoute(identity, r.Billing, r.Accounts, r.OpenAIAccounts, r.PreferBucketB, n)
}

// resolveAutoRoute 在 originalModel = "auto" / "" 时按桶余额优先级与
// 桶内对外模型可调度性挑选 External_Image_Model。
//
// 解析顺序（Requirement 5）：
//  1. 桶 A 余额充足 + 桶 A 至少 1 个可调度 ChatGPT 账号 → gpt-image-2 / bucket_a。
//  2. 否则进入桶 B：codex-gpt-image-2 与 gemini-3.1-flash-image 的可调度
//     性各自独立判定（见 isCodexDispatchable / isGeminiDispatchable）。
//     - 两者都可调度：按 preferBucketB 选；
//     - 只有一个：选那个；
//     - 都不可调度：返回 ErrNoUpstreamForAutoRoute（503）。
//  3. 两桶余额均不足：返回 BillingLimitError，bucket 字段填偏好优先桶
//     （preferBucketB 指向桶 B 的对外模型且其可调度时取桶 B，否则桶 A）。
//
// 偏好桶 B 但桶 B 无可调度模型时，BillingLimitError 仍指向桶 A，避免把
// 用户引导到一个根本无法承载的桶上。
func resolveAutoRoute(
	identity service.Identity,
	billing BillingChecker,
	accounts AutoChatGPTAccountInspector,
	openaiAccounts OpenAIAccountReserver,
	preferBucketB string,
	n int,
) (string, string, error) {
	if billing == nil {
		return "", "", errors.New("billing checker is not configured")
	}
	if n <= 0 {
		n = 1
	}

	bucketAErr := billing.CheckAvailable(identity, n, util.ImageBucketA)
	bucketBErr := billing.CheckAvailable(identity, n, util.ImageBucketB)

	bucketALimit, bucketAIsLimit := asBillingLimitError(bucketAErr)
	bucketBLimit, bucketBIsLimit := asBillingLimitError(bucketBErr)

	if bucketAErr != nil && !bucketAIsLimit {
		return "", "", bucketAErr
	}
	if bucketBErr != nil && !bucketBIsLimit {
		return "", "", bucketBErr
	}

	bucketAOK := bucketAErr == nil
	bucketBOK := bucketBErr == nil

	bucketAUpstreamReady := accounts != nil && accounts.HasAvailableImageAccount()
	codexDispatchable := isCodexDispatchable(accounts, openaiAccounts)
	geminiDispatchable := isGeminiDispatchable(openaiAccounts)
	bucketBUpstreamReady := codexDispatchable || geminiDispatchable

	if bucketAOK && bucketAUpstreamReady {
		return util.ImageModelGPTImage2, util.ImageBucketA, nil
	}

	if bucketBOK && bucketBUpstreamReady {
		resolved, err := pickBucketBExternalModel(preferBucketB, codexDispatchable, geminiDispatchable)
		if err != nil {
			return "", "", err
		}
		return resolved, util.ImageBucketB, nil
	}

	if !bucketAOK && !bucketBOK {
		preferred := preferredBillingLimit(preferBucketB, bucketBUpstreamReady, bucketALimit, bucketBLimit, bucketAIsLimit, bucketBIsLimit)
		return "", "", preferred
	}

	// 至少一个桶有余额，但对应桶没有可调度对外模型；另一个桶（如果有）
	// 也没有可调度对外模型。统一以「无可调度上游」作为对外信号，避免把
	// 用户引导到一个有余额却无法出图的桶。
	return "", "", ErrNoUpstreamForAutoRoute
}

// isCodexDispatchable 判断对外模型 codex-gpt-image-2 当前是否可被调度。
// 满足任一即可：
//   - ChatGPT 付费账号池有 Plus/ProLite/Pro/Team 且 status==正常 的账号；
//   - OpenAI 协议账号池有候选可承接 upstream gpt-image-2。
func isCodexDispatchable(accounts AutoChatGPTAccountInspector, openaiAccounts OpenAIAccountReserver) bool {
	if accounts != nil && accounts.HasAvailablePaidImageAccount() {
		return true
	}
	if openaiAccounts != nil && openaiAccounts.HasAvailableForUpstreamModel(util.ImageModelGPTImage2) {
		return true
	}
	return false
}

// isGeminiDispatchable 判断对外模型 gemini-3.1-flash-image 当前是否可被
// 调度。仅 OpenAI 协议账号池中存在 upstream gemini-3.1-flash-image 候选时
// 返回 true。
func isGeminiDispatchable(openaiAccounts OpenAIAccountReserver) bool {
	if openaiAccounts == nil {
		return false
	}
	return openaiAccounts.HasAvailableForUpstreamModel(util.ImageModelGeminiFlashImage)
}

// pickBucketBExternalModel 在桶 B 可调度的对外模型集合上按偏好选取一个
// External_Image_Model；调用前必须保证至少有一个候选可调度。
func pickBucketBExternalModel(preferBucketB string, codexDispatchable, geminiDispatchable bool) (string, error) {
	switch {
	case codexDispatchable && geminiDispatchable:
		if normalizeAutoPreferBucketB(preferBucketB) == AutoPreferBucketBGemini {
			return util.ImageModelGeminiFlashImage, nil
		}
		return util.ImageModelCodexGPTImage2, nil
	case codexDispatchable:
		return util.ImageModelCodexGPTImage2, nil
	case geminiDispatchable:
		return util.ImageModelGeminiFlashImage, nil
	default:
		return "", ErrNoUpstreamForAutoRoute
	}
}

// preferredBillingLimit 在两桶均限额时挑选要返回给客户端的 BillingLimitError。
// 优先桶 B 的条件是：preferBucketB 指向桶 B 的对外模型，且桶 B 至少有
// 一个可调度对外模型；否则一律以桶 A 为偏好。
func preferredBillingLimit(
	preferBucketB string,
	bucketBUpstreamReady bool,
	bucketALimit service.BillingLimitError,
	bucketBLimit service.BillingLimitError,
	bucketAIsLimit, bucketBIsLimit bool,
) error {
	preferB := bucketBUpstreamReady && (normalizeAutoPreferBucketB(preferBucketB) != "")

	if preferB && bucketBIsLimit {
		return bucketBLimit
	}
	if bucketAIsLimit {
		return bucketALimit
	}
	if bucketBIsLimit {
		return bucketBLimit
	}
	// 防御性：理论不可达 —— 调用方仅在 !bucketAOK && !bucketBOK 时进入。
	return service.NewBillingLimitError(util.ImageBucketA, "")
}

// asBillingLimitError 把任意错误尝试断言为 service.BillingLimitError；
// 同时支持 BillingLimitError 与 *BillingLimitError 两种装箱形式。
func asBillingLimitError(err error) (service.BillingLimitError, bool) {
	if err == nil {
		return service.BillingLimitError{}, false
	}
	var limit service.BillingLimitError
	if errors.As(err, &limit) {
		return limit, true
	}
	return service.BillingLimitError{}, false
}

// normalizeAutoPreferBucketB 把 PreferBucketB 字段规范化为合法枚举值。
// 取值不在 {codex, gemini} 时返回空串，由调用方决定回退（一般落到默认
// 的 codex 选择或桶 A 偏好）。
func normalizeAutoPreferBucketB(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AutoPreferBucketBCodex:
		return AutoPreferBucketBCodex
	case AutoPreferBucketBGemini:
		return AutoPreferBucketBGemini
	default:
		return ""
	}
}
