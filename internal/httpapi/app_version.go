package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

// AppVersionMetadata 描述 Android 客户端的最新可用版本元数据。
//
// 字段语义对齐 web-app-parity-iteration Requirement 5.2：
//
//	{ versionCode, versionName, downloadUrl, releaseNotes, minSupportedVersionCode }
//
// 唯一数据源是 DataDir/app-version.json；该文件由运维通过 PUT
// /api/admin/app-version 写入，也可以手工编辑后由 mtime 热重载感知。
// 没有任何编译期硬编码默认值——文件缺失或格式非法直接报错，避免
// 把过期版本号悄悄发布给客户端（No compatibility layers）。
type AppVersionMetadata struct {
	VersionCode             int    `json:"versionCode"`
	VersionName             string `json:"versionName"`
	DownloadURL             string `json:"downloadUrl"`
	ReleaseNotes            string `json:"releaseNotes"`
	MinSupportedVersionCode int    `json:"minSupportedVersionCode"`
}

// appVersionFileName 是 DataDir 下的元数据文件名。
const appVersionFileName = "app-version.json"

// appVersionStore 维护一份 mtime 感知的 AppVersionMetadata 缓存。
//
// 设计目标：
//   - 0 重启 / 0 重编译切换版本：发布新 APK 只需替换 JSON 文件或调用
//     PUT /api/admin/app-version，下一次客户端请求即生效；
//   - 读多写极少：HTTP handler 走 atomic.Value，仅 stat 一次，无锁；
//   - 热加载窗口期严格化：mtime 不变 → 复用缓存；mtime 变化 →
//     重新 parse + 验证，验证失败保留旧缓存并返回 error，让 handler
//     向客户端报 503，避免半截 JSON 被推给真实客户端。
type appVersionStore struct {
	path  string
	mu    sync.Mutex   // 仅保护 reload 路径
	cache atomic.Value // *appVersionSnapshot，永远非 nil（启动期已 seed）
}

type appVersionSnapshot struct {
	metadata AppVersionMetadata
	modTime  int64
	size     int64
}

// newAppVersionStore 在启动期尝试加载一次元数据。
//
// 文件不存在时返回的 store 处于"未初始化"状态，Current() 会返回
// errAppVersionUninitialized；handler 见状返回 503，提示管理员通过
// PUT /api/admin/app-version 完成首次配置。文件存在但内容非法属于
// 启动期硬错误，向上抛给 internal/main.go::log.Fatalf 终止启动——
// 不静默回退默认值，符合 No compatibility layers。
func newAppVersionStore(path string) (*appVersionStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("app version store: path must not be empty")
	}
	s := &appVersionStore{path: path}
	s.cache.Store((*appVersionSnapshot)(nil))
	if _, err := s.reload(); err != nil {
		if errors.Is(err, errAppVersionUninitialized) {
			return s, nil
		}
		return nil, err
	}
	return s, nil
}

// errAppVersionUninitialized 表示尚未配置 app-version.json。
// store / handler 共享该哨兵以区分"未配置"与"配置非法"。
var errAppVersionUninitialized = errors.New(
	"app version not configured: call PUT /api/admin/app-version to publish the first release",
)

// Current 返回缓存中的元数据快照；如文件 mtime 已变则触发重载。
//
// 几个状态：
//   - 文件不存在 → errAppVersionUninitialized（503）
//   - 文件存在但缓存 mtime 已过期 → 触发 reload；reload 失败保留旧缓存
//   - 文件存在且 mtime 与缓存一致 → 直接返回缓存
func (s *appVersionStore) Current() (AppVersionMetadata, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AppVersionMetadata{}, errAppVersionUninitialized
		}
		return AppVersionMetadata{}, fmt.Errorf("stat app version file: %w", err)
	}
	current, _ := s.cache.Load().(*appVersionSnapshot)
	if current != nil && info.ModTime().UnixNano() == current.modTime && info.Size() == current.size {
		return current.metadata, nil
	}
	return s.reload()
}

// Save 把新元数据原子写入磁盘并刷新缓存。
//
// 走 temp-file + rename 写入序列，保证读取方永远只看到完整 JSON；
// 校验失败时不触碰磁盘，缓存保持旧值。
func (s *appVersionStore) Save(metadata AppVersionMetadata) (AppVersionMetadata, error) {
	if err := validateAppVersionMetadata(metadata); err != nil {
		return AppVersionMetadata{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := writeAppVersionFile(s.path, metadata); err != nil {
		return AppVersionMetadata{}, err
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return AppVersionMetadata{}, fmt.Errorf("stat app version file after write: %w", err)
	}
	s.cache.Store(&appVersionSnapshot{
		metadata: metadata,
		modTime:  info.ModTime().UnixNano(),
		size:     info.Size(),
	})
	return metadata, nil
}

func (s *appVersionStore) reload() (AppVersionMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AppVersionMetadata{}, errAppVersionUninitialized
		}
		return AppVersionMetadata{}, fmt.Errorf("read app version file: %w", err)
	}
	metadata, err := parseAppVersionMetadata(raw)
	if err != nil {
		return AppVersionMetadata{}, fmt.Errorf("parse app version file %s: %w", s.path, err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return AppVersionMetadata{}, fmt.Errorf("stat app version file: %w", err)
	}
	s.cache.Store(&appVersionSnapshot{
		metadata: metadata,
		modTime:  info.ModTime().UnixNano(),
		size:     info.Size(),
	})
	return metadata, nil
}

// parseAppVersionMetadata 解析并校验 JSON 字节。strict json.Decoder
// 拒绝未知字段，避免运维拼错字段名却走默认值。
func parseAppVersionMetadata(raw []byte) (AppVersionMetadata, error) {
	if len(raw) == 0 {
		return AppVersionMetadata{}, errors.New("file is empty")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var metadata AppVersionMetadata
	if err := dec.Decode(&metadata); err != nil {
		return AppVersionMetadata{}, fmt.Errorf("decode json: %w", err)
	}
	if err := validateAppVersionMetadata(metadata); err != nil {
		return AppVersionMetadata{}, err
	}
	return metadata, nil
}

func validateAppVersionMetadata(metadata AppVersionMetadata) error {
	if metadata.VersionCode <= 0 {
		return fmt.Errorf("versionCode must be a positive integer (got %d)", metadata.VersionCode)
	}
	if strings.TrimSpace(metadata.VersionName) == "" {
		return errors.New("versionName must not be empty")
	}
	if err := validateAbsoluteHTTPURL("downloadUrl", metadata.DownloadURL); err != nil {
		return err
	}
	if metadata.MinSupportedVersionCode <= 0 {
		return fmt.Errorf("minSupportedVersionCode must be a positive integer (got %d)", metadata.MinSupportedVersionCode)
	}
	if metadata.MinSupportedVersionCode > metadata.VersionCode {
		return fmt.Errorf(
			"minSupportedVersionCode (%d) must not exceed versionCode (%d)",
			metadata.MinSupportedVersionCode,
			metadata.VersionCode,
		)
	}
	return nil
}

func validateAbsoluteHTTPURL(field, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", field, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http(s) scheme (got %q)", field, parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must contain a host (got %q)", field, raw)
	}
	return nil
}

func writeAppVersionFile(path string, metadata AppVersionMetadata) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir for app version file: %w", err)
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal app version metadata: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".app-version-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	return nil
}

// handleAppLatestVersion 公开返回当前 App 版本元数据。
//
// 任何身份均可访问（与 /api/announcements 同层），用于 App 启动时检查
// 是否有新版本。响应 Cache-Control: no-store 保证 minSupportedVersionCode
// 提升能被强制下发，不被任何中间缓存截留。
func (a *App) handleAppLatestVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	metadata, err := a.appVersion.Current()
	if err != nil {
		status := http.StatusServiceUnavailable
		util.WriteError(w, status, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	util.WriteJSON(w, http.StatusOK, metadata)
}

// handleAppDownloadLatest 是固定下载链接：
//
//	GET /api/app/download/app  →  302  Location: <metadata.downloadUrl>
//
// 客户端、网页和外部分享都可以挂这一个 URL，发布新版本只要替换
// JSON 里的 downloadUrl，下一次点击立即跳转新地址。
func (a *App) handleAppDownloadLatest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	metadata, err := a.appVersion.Current()
	if err != nil {
		util.WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, metadata.DownloadURL, http.StatusFound)
}

// handleAdminAppVersion 给管理员读写元数据。
//
//	GET  /api/admin/app-version  →  200  {当前 JSON}
//	PUT  /api/admin/app-version  →  200  {写入后 JSON}
//
// PUT body 必须是合法的 AppVersionMetadata，字段缺失/格式错全部 400。
// 写入成功后立即刷新内存缓存，无需重启。
func (a *App) handleAdminAppVersion(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	if identity.Role != service.AuthRoleAdmin {
		util.WriteError(w, http.StatusForbidden, "admin permission required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		metadata, err := a.appVersion.Current()
		if err != nil {
			util.WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		util.WriteJSON(w, http.StatusOK, metadata)
	case http.MethodPut:
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var metadata AppVersionMetadata
		if err := dec.Decode(&metadata); err != nil {
			util.WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid json body: %v", err))
			return
		}
		saved, err := a.appVersion.Save(metadata)
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		util.WriteJSON(w, http.StatusOK, saved)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
