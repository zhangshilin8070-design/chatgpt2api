package protocol

import (
	"chatgpt2api/internal/util"
)

// upstreamKind 标识一个生图请求实际走的物理上游通路。
//
// 该枚举仅在 protocol 包内部用于路由决策；写入 ImageOutput.Raw 时再转换为
// util.UpstreamKindChatGPT / util.UpstreamKindOpenAIAPI 字符串常量，避免把
// 字符串字面量散落到调度逻辑里。
type upstreamKind int

const (
	// upstreamKindChatGPT 表示走 ChatGPT 网页对话流账号池
	// （/backend-api/conversation 与 /backend-api/codex/responses）。
	upstreamKindChatGPT upstreamKind = iota
	// upstreamKindOpenAIAPI 表示走 OpenAI 协议 api_key + base_url 账号池
	// （/v1/images/generations 与 /v1/images/edits）。
	upstreamKindOpenAIAPI
)

// upstreamCandidate 描述一条物理上游通路候选。kind 决定使用哪个账号池与
// 后端实现；upstreamModel 仅在 kind == upstreamKindOpenAIAPI 时有意义，
// 用作 OpenAI 协议请求体里的 model 字段值（即 Upstream_Image_Model）。
type upstreamCandidate struct {
	kind          upstreamKind
	upstreamModel string
}

// buildUpstreamCandidates 按对外模型构造有序的物理上游通路候选列表。
//
// 候选顺序编码 Requirement 11 的回落策略：
//
//   - gpt-image-2:           [ChatGPT]
//   - gemini-3.1-flash-image:[OpenAIAPI(gemini-3.1-flash-image)]
//   - codex-gpt-image-2:     [ChatGPT, OpenAIAPI(gpt-image-2)]
//
// resolvedModel 必须是已经过 Auto_Mode 解析后的 External_Image_Model；
// auto / 空串 / 未知值返回空切片，由调用方负责在路由前完成校验
// （例如通过 util.BucketForModel）。
func buildUpstreamCandidates(resolvedModel string) []upstreamCandidate {
	switch resolvedModel {
	case util.ImageModelGPTImage2:
		return []upstreamCandidate{
			{kind: upstreamKindChatGPT},
		}
	case util.ImageModelCodexGPTImage2:
		return []upstreamCandidate{
			{kind: upstreamKindChatGPT},
			{kind: upstreamKindOpenAIAPI, upstreamModel: util.ImageModelGPTImage2},
		}
	case util.ImageModelGeminiFlashImage:
		return []upstreamCandidate{
			{kind: upstreamKindOpenAIAPI, upstreamModel: util.ImageModelGeminiFlashImage},
		}
	default:
		return nil
	}
}

// upstreamKindString 返回写入 ImageOutput.Raw["upstream_kind"] 的字符串值。
// 与 util 包的 UpstreamKindChatGPT / UpstreamKindOpenAIAPI 常量保持一致。
func upstreamKindString(kind upstreamKind) string {
	switch kind {
	case upstreamKindChatGPT:
		return util.UpstreamKindChatGPT
	case upstreamKindOpenAIAPI:
		return util.UpstreamKindOpenAIAPI
	default:
		return ""
	}
}
