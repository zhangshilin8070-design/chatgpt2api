package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chatgpt2api/internal/cloudstorage"
	"chatgpt2api/internal/service"
	"chatgpt2api/internal/util"
)

func (a *App) handleImages(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		scope, status, message := imageListAccessScope(identity, r.URL.Query().Get("scope"))
		if status != 0 {
			util.WriteError(w, status, message)
			return
		}
		payload := a.images.ListImages(a.resolveImageBaseURL(r), strings.TrimSpace(r.URL.Query().Get("start_date")), strings.TrimSpace(r.URL.Query().Get("end_date")), scope)
		a.decorateImageList(payload)
		util.WriteJSON(w, http.StatusOK, payload)
	case http.MethodDelete:
		body, err := readJSONMap(r)
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		result, err := a.images.DeleteImages(util.AsStringSlice(body["paths"]), service.ImageAccessScope{All: true})
		if err != nil {
			util.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		util.WriteJSON(w, http.StatusOK, result)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) handleImageVisibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	identity, ok := a.requireIdentity(w, r, "")
	if !ok {
		return
	}
	body, err := readJSONMap(r)
	if err != nil {
		util.WriteError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	path := util.Clean(body["path"])
	if path == "" {
		util.WriteError(w, http.StatusBadRequest, "path is required")
		return
	}
	visibility := util.Clean(body["visibility"])
	sharePromptParams := util.ToBool(body["share_prompt_parameters"])
	shareReferences := sharePromptParams && util.ToBool(body["share_reference_images"])
	scope := service.ImageAccessScope{OwnerID: identityScope(identity)}
	if identity.Role == service.AuthRoleAdmin {
		scope = service.ImageAccessScope{All: true}
	}
	item, err := a.images.UpdateImageVisibility(path, visibility, scope, service.ImageVisibilityUpdateOptions{
		SharePromptParams: sharePromptParams,
		ShareReferences:   shareReferences,
	})
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "image not found" {
			status = http.StatusNotFound
		}
		util.WriteError(w, status, err.Error())
		return
	}
	a.decorateImageItem(item, a.imageOwnerDisplayNames())
	util.WriteJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (a *App) handleImageFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rel, err := imageFileRequestPath(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// When cloud storage is enabled, try cloud-first serving.
	// This handles the case where local file was deleted after successful cloud upload.
	if a.cloudStorage != nil && a.cloudStorage.Enabled() {
		if a.serveCloudImage(w, r, rel) {
			return
		}
	}

	// Fall back to local file serving with authorization check.
	ref, ok := a.authorizeImageFileRequest(w, r, rel)
	if !ok {
		return
	}
	http.ServeFile(w, r, ref.Path)
}

func (a *App) serveCloudImage(w http.ResponseWriter, r *http.Request, rel string) bool {
	record, err := a.cloudStorage.GetRecord(r.Context(), rel)
	if err != nil || record == nil {
		return false
	}

	// Direct URL mode (S3 with public bucket): redirect to the public URL.
	if record.DirectURL != "" {
		http.Redirect(w, r, record.DirectURL, http.StatusFound)
		return true
	}

	// If no encryption key, nothing to serve (shouldn't happen without DirectURL).
	if record.EncryptKey == "" {
		return false
	}

	// Fetch from cloud URL and decrypt
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, record.CloudURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := a.cloudStorage.GetHTTPClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}

	if len(body) <= record.HeadSize {
		return false
	}
	encryptedData := body[record.HeadSize:]

	aesKey := cloudstorage.Decode(record.EncryptKey)
	if aesKey == nil {
		return false
	}

	imageData, err := cloudstorage.DecryptAES(encryptedData, aesKey)
	if err != nil {
		return false
	}

	if record.ContentType != "" {
		w.Header().Set("Content-Type", record.ContentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeContent(w, r, "", time.Now(), bytes.NewReader(imageData))
	return true
}

func (a *App) handleImageReferenceFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rel, err := imageReferenceFileRequestPath(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ref, err := a.images.ImageReferenceFileAccess(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ref.Visibility == service.ImageVisibilityPublic && ref.Shared {
		if ref.ContentType != "" {
			w.Header().Set("Content-Type", ref.ContentType)
		}
		http.ServeFile(w, r, ref.Path)
		return
	}
	identity, ok := a.imageRequestIdentity(w, r)
	if !ok {
		return
	}
	if identity.Role != service.AuthRoleAdmin && (ref.OwnerID == "" || ref.OwnerID != identityScope(identity)) {
		http.NotFound(w, r)
		return
	}
	if ref.ContentType != "" {
		w.Header().Set("Content-Type", ref.ContentType)
	}
	http.ServeFile(w, r, ref.Path)
}

func (a *App) authorizeImageFileRequest(w http.ResponseWriter, r *http.Request, rel string) (service.ImageFileAccess, bool) {
	ref, err := a.images.ImageFileAccess(rel, service.ImageAccessScope{All: true})
	if err != nil {
		http.NotFound(w, r)
		return service.ImageFileAccess{}, false
	}
	if ref.Visibility == service.ImageVisibilityPublic {
		return ref, true
	}
	identity, ok := a.imageRequestIdentity(w, r)
	if !ok {
		return service.ImageFileAccess{}, false
	}
	if identity.Role == service.AuthRoleAdmin || (ref.OwnerID != "" && ref.OwnerID == identityScope(identity)) {
		return ref, true
	}
	http.NotFound(w, r)
	return service.ImageFileAccess{}, false
}

func (a *App) handleImageThumbnail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	thumbnailRel, err := imageThumbnailRequestPath(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sourceRel, sourceErr := a.images.SourceImageRelativePathFromThumbnail(thumbnailRel)
	if sourceErr != nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := a.authorizeImageFileRequest(w, r, sourceRel); !ok {
		return
	}
	_ = a.images.EnsureThumbnail(thumbnailRel)
	thumbPath := filepath.Join(a.config.ImageThumbnailsDir(), filepath.FromSlash(thumbnailRel))
	if info, err := os.Stat(thumbPath); err == nil && !info.IsDir() {
		w.Header().Set("Cache-Control", imageThumbnailCacheControl)
		http.ServeFile(w, r, thumbPath)
		return
	}
	sourcePath := filepath.Join(a.config.ImagesDir(), filepath.FromSlash(sourceRel))
	if info, err := os.Stat(sourcePath); err == nil && !info.IsDir() {
		http.ServeFile(w, r, sourcePath)
		return
	}
	http.NotFound(w, r)
}

func imageFileRequestPath(r *http.Request) (string, error) {
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/images/")
	if raw == "" || raw == r.URL.EscapedPath() {
		return "", errors.New("invalid image path")
	}
	rel, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	return rel, nil
}

func imageReferenceFileRequestPath(r *http.Request) (string, error) {
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/image-references/")
	if raw == "" || raw == r.URL.EscapedPath() {
		return "", errors.New("invalid image path")
	}
	rel, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	return rel, nil
}

func imageThumbnailRequestPath(r *http.Request) (string, error) {
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/image-thumbnails/")
	if raw == "" || raw == r.URL.EscapedPath() {
		return "", errors.New("invalid thumbnail path")
	}
	rel, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	return rel, nil
}
