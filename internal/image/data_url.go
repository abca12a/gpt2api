package image

import (
	"encoding/base64"
	"errors"
	"strings"
)

var errInvalidInlineImageDataURL = errors.New("invalid inline image data url")

// IsInlineImageDataURL 判断结果 URL 是否是可直接嵌入的大块 base64 图片。
func IsInlineImageDataURL(raw string) bool {
	s := strings.TrimSpace(strings.ToLower(raw))
	return strings.HasPrefix(s, "data:image/") && strings.Contains(s, ";base64,")
}

// DecodeInlineImageDataURL 把 data:image/...;base64,... 解码成字节与 content-type。
func DecodeInlineImageDataURL(raw string) ([]byte, string, error) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(s), "data:") {
		return nil, "", errInvalidInlineImageDataURL
	}
	comma := strings.IndexByte(s, ',')
	if comma <= len("data:") {
		return nil, "", errInvalidInlineImageDataURL
	}
	meta := s[len("data:"):comma]
	payload := s[comma+1:]
	if payload == "" {
		return nil, "", errInvalidInlineImageDataURL
	}
	parts := strings.Split(meta, ";")
	contentType := strings.TrimSpace(parts[0])
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return nil, "", errInvalidInlineImageDataURL
	}
	hasBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			hasBase64 = true
			break
		}
	}
	if !hasBase64 {
		return nil, "", errInvalidInlineImageDataURL
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return nil, "", err
		}
	}
	return data, contentType, nil
}
