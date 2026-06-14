package util

import "fmt"

// Image_Bucket constants. 用户生图配额按桶 A / 桶 B 独立计算。
const (
	ImageBucketA = "bucket_a"
	ImageBucketB = "bucket_b"
)

// Upstream_Kind constants. 物理上游通路标识，写入任务 payload 与日志用于审计。
const (
	UpstreamKindChatGPT   = "chatgpt"
	UpstreamKindOpenAIAPI = "openai_api"
)

// BucketForModel 把对外模型映射到所属计费桶。
//
// 映射规则：
//   - gpt-image-2 → bucket_a
//   - codex-gpt-image-2 / gemini-3.1-flash-image → bucket_b
//   - auto / 空串 → 错误：必须先经过 Auto_Mode 解析
//   - 其他取值 → 错误：非可计费图像模型
func BucketForModel(external string) (string, error) {
	switch external {
	case ImageModelGPTImage2:
		return ImageBucketA, nil
	case ImageModelCodexGPTImage2, ImageModelGeminiFlashImage:
		return ImageBucketB, nil
	case ImageModelAuto, "":
		return "", fmt.Errorf("bucket cannot be resolved before auto routing")
	default:
		return "", fmt.Errorf("model %s is not a billable image model", external)
	}
}

// UpstreamModelForExternal 把对外模型映射到上游真实模型。
//
// 映射规则：
//   - gpt-image-2 → gpt-image-2
//   - codex-gpt-image-2 → gpt-image-2
//   - gemini-3.1-flash-image → gemini-3.1-flash-image
//   - auto / 空串 / 未知值 → 错误：无法解析上游模型
func UpstreamModelForExternal(external string) (string, error) {
	switch external {
	case ImageModelGPTImage2, ImageModelCodexGPTImage2:
		return ImageModelGPTImage2, nil
	case ImageModelGeminiFlashImage:
		return ImageModelGeminiFlashImage, nil
	default:
		return "", fmt.Errorf("upstream model cannot be resolved for %s", external)
	}
}
