package service

import (
	"path/filepath"
	"strconv"
	"strings"
)

func normalizeImageReferenceMetadata(value any) []imageReferenceMetadata {
	items := imageReferenceMetadataItems(value)
	if len(items) == 0 {
		return nil
	}
	refs := make([]imageReferenceMetadata, 0, len(items))
	for _, item := range items {
		rel, err := cleanImageReferenceRelativePath(toString(item["path"]))
		if err != nil {
			continue
		}
		refs = append(refs, imageReferenceMetadata{
			Path:        rel,
			Filename:    strings.TrimSpace(toString(item["filename"])),
			ContentType: strings.TrimSpace(toString(item["content_type"])),
			Size:        int64(numericMetaValue(item["size"])),
		})
	}
	return refs
}

func safeImageReferenceFilename(value string, index int) string {
	name := filepath.Base(filepath.ToSlash(strings.TrimSpace(value)))
	if name == "." || name == "/" || name == "" {
		name = "reference-" + strconv.Itoa(index+1) + ".png"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := strings.Trim(b.String(), ".- _")
	if clean == "" {
		clean = "reference-" + strconv.Itoa(index+1) + ".png"
	}
	if !strings.Contains(filepath.Base(clean), ".") {
		clean += ".png"
	}
	if len(clean) > 96 {
		ext := filepath.Ext(clean)
		stem := strings.TrimSuffix(clean, ext)
		limit := 96 - len(ext)
		if limit < 1 {
			return clean[:96]
		}
		if len(stem) > limit {
			stem = stem[:limit]
		}
		clean = stem + ext
	}
	return clean
}

func imageReferenceMetadataItems(value any) []map[string]any {
	switch v := value.(type) {
	case []map[string]any:
		return v
	case []any:
		items := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				items = append(items, m)
			}
		}
		return items
	default:
		return nil
	}
}
