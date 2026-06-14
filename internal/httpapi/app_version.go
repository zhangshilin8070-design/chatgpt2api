package httpapi

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// AppVersionMetadata 描述 Android 客户端的最新可用版本元数据。
//
// 字段语义对齐 web-app-parity-iteration Requirement 5.2：响应 JSON 形态为
//
//	{ versionCode, versionName, downloadUrl, releaseNotes, minSupportedVersionCode }
//
// 数据来源严格固定为本文件中的默认常量 + settings.env override（通过
// internal/config 启动期注入的进程 ENV 读取），不引入新存储后端。
//
// "No compatibility layers" 约定下：override 字段任何一项非法即在进程启动期
// 报错（caller 通过 panic 终止启动），不回退到默认值，也不部分应用。
type AppVersionMetadata struct {
	VersionCode             int    `json:"versionCode"`
	VersionName             string `json:"versionName"`
	DownloadURL             string `json:"downloadUrl"`
	ReleaseNotes            string `json:"releaseNotes"`
	MinSupportedVersionCode int    `json:"minSupportedVersionCode"`
}

// 以下 5 个 settings.env override key 与 internal/config 设置项规范保持
// 同样的「`APP_LATEST_*`」前缀风格；当任一 key 在进程 ENV 中存在时，对应
// 字段被覆盖。空字符串视为「显式置空」并按字段规则校验——绝大多数字段
// 不允许显式空字符串（参见 loadAppVersionMetadata）。
const (
	envKeyAppLatestVersionCode             = "APP_LATEST_VERSION_CODE"
	envKeyAppLatestVersionName             = "APP_LATEST_VERSION_NAME"
	envKeyAppLatestDownloadURL             = "APP_LATEST_DOWNLOAD_URL"
	envKeyAppLatestReleaseNotes            = "APP_LATEST_RELEASE_NOTES"
	envKeyAppLatestMinSupportedVersionCode = "APP_LATEST_MIN_SUPPORTED_VERSION_CODE"
)

// defaultAppVersionMetadata 定义当前发布版本的元数据默认值。
//
// 与 android-image-app/app/build.gradle.kts 中的 versionCode / versionName
// 严格对齐：每次 Android 客户端发版需同步更新本常量，并随 backend 一起部署。
// downloadUrl 形态与 Requirement 3.3 / 部署脚本 zheye-v{versionName}.apk
// 命名规范保持一致。
//
// MinSupportedVersionCode 默认为 1：对存量 v3 用户不触发 Force_Update；
// 仅在出现破坏性协议变更时由运维通过 settings.env override 提升此值。
var defaultAppVersionMetadata = AppVersionMetadata{
	VersionCode:             3,
	VersionName:             "3.0.0",
	DownloadURL:             "https://github.com/zhangshilin8070-design/chatgpt2api/releases/latest/download/app.apk",
	ReleaseNotes:            "",
	MinSupportedVersionCode: 1,
}

// loadAppVersionMetadata 把默认常量与 settings.env override 合并为最终元数据。
//
// lookup 注入 os.LookupEnv 等价函数，便于单元测试在不污染全局 ENV 的前提
// 下覆盖各 override key。production 路径请使用 loadAppVersionMetadataFromEnv。
//
// 校验规则（任一不通过则返回 error，由 caller 在启动期 panic）：
//   - APP_LATEST_VERSION_CODE：必须可解析为正整数。
//   - APP_LATEST_VERSION_NAME：trim 后非空。
//   - APP_LATEST_DOWNLOAD_URL：trim 后非空，且为绝对 http(s) URL。
//   - APP_LATEST_RELEASE_NOTES：允许显式空字符串（运维表示"本次无变更说明"）。
//   - APP_LATEST_MIN_SUPPORTED_VERSION_CODE：必须可解析为正整数，且 ≤ VersionCode。
//
// "No compatibility layers"：override 非法时直接报错，绝不静默回退到默认。
func loadAppVersionMetadata(lookup func(key string) (string, bool)) (AppVersionMetadata, error) {
	if lookup == nil {
		return AppVersionMetadata{}, errors.New("app version metadata loader: lookup function is required")
	}
	metadata := defaultAppVersionMetadata

	if raw, ok := lookup(envKeyAppLatestVersionCode); ok {
		value, err := parsePositiveIntOverride(envKeyAppLatestVersionCode, raw)
		if err != nil {
			return AppVersionMetadata{}, err
		}
		metadata.VersionCode = value
	}
	if raw, ok := lookup(envKeyAppLatestVersionName); ok {
		value := strings.TrimSpace(raw)
		if value == "" {
			return AppVersionMetadata{}, fmt.Errorf("%s must not be empty", envKeyAppLatestVersionName)
		}
		metadata.VersionName = value
	}
	if raw, ok := lookup(envKeyAppLatestDownloadURL); ok {
		value, err := parseAbsoluteHTTPURLOverride(envKeyAppLatestDownloadURL, raw)
		if err != nil {
			return AppVersionMetadata{}, err
		}
		metadata.DownloadURL = value
	}
	if raw, ok := lookup(envKeyAppLatestReleaseNotes); ok {
		// release notes 允许显式空字符串：运维以此表达"本次无变更说明"。
		// 不做 trim 写回，保留运维原样换行/缩进。
		metadata.ReleaseNotes = raw
	}
	if raw, ok := lookup(envKeyAppLatestMinSupportedVersionCode); ok {
		value, err := parsePositiveIntOverride(envKeyAppLatestMinSupportedVersionCode, raw)
		if err != nil {
			return AppVersionMetadata{}, err
		}
		metadata.MinSupportedVersionCode = value
	}

	if metadata.MinSupportedVersionCode > metadata.VersionCode {
		return AppVersionMetadata{}, fmt.Errorf(
			"%s (%d) must not exceed latest version code (%d)",
			envKeyAppLatestMinSupportedVersionCode,
			metadata.MinSupportedVersionCode,
			metadata.VersionCode,
		)
	}
	return metadata, nil
}

// loadAppVersionMetadataFromEnv 是 production 路径包装：直接从进程 ENV 读取
// override。internal/config.NewStore 在启动期已把 settings.env 的所有键强制
// os.Setenv 覆盖到进程 ENV，本函数因此不需要再读 settings.env 文件。
func loadAppVersionMetadataFromEnv() (AppVersionMetadata, error) {
	return loadAppVersionMetadata(os.LookupEnv)
}

func parsePositiveIntOverride(key, raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("%s must not be empty", key)
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%s must be a base-10 integer (got %q)", key, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer (got %d)", key, value)
	}
	return value, nil
}

func parseAbsoluteHTTPURLOverride(key, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid URL: %w", key, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%s must use http(s) scheme (got %q)", key, parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%s must contain a host (got %q)", key, raw)
	}
	return trimmed, nil
}
