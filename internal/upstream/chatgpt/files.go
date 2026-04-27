// files.go —— chatgpt.com 文件上传协议,图生图/图像编辑的前置步骤。
//
// 三步协议(对齐 chatgpt.com 浏览器真实抓包):
//
//  1. POST /backend-api/files
//     body: {file_name, file_size, use_case: "multimodal"}
//     resp: {file_id, upload_url, status: "success"}
//
//  2. PUT <upload_url>                 (Azure Blob SAS URL)
//     headers: Content-Type / x-ms-blob-type: BlockBlob / x-ms-version: 2020-04-08 / Origin
//     body: 原始字节
//
//  3. POST /backend-api/files/{file_id}/uploaded
//     body: {}
//     resp: {status: "success", download_url, ...}
//
// 上传完成后,在 f/conversation.messages 里:
//   - content 从 text 变 multimodal_text;parts 前面加上
//     {"asset_pointer": "file-service://<file_id>", "height":.., "width":.., "size_bytes":..}
//   - metadata.attachments 加一项 {id, mimeType, name, size, height?, width?}
//
// 注意:upload_url 指向 Azure,不要走同一个 chatgpt.com 代理/utls transport,
// 这里用单独的一个 http.Client(沿用 Client 内部的 Transport 走代理,但不带 Auth/Oai-* 头)。

package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register decoders
	_ "image/jpeg" //
	_ "image/png"  //
	"io"
	"net/http"
	"strings"
	"time"
)

// UploadedFile 是三步上传后沉淀的"可 attach 给 messages"的元数据。
// 字段命名对齐 chatgpt.com 的 attachment payload,序列化时直接当 map 用。
type UploadedFile struct {
	FileID      string `json:"file_id"`
	FileName    string `json:"file_name"`
	FileSize    int    `json:"file_size"`
	MimeType    string `json:"mime_type"`
	UseCase     string `json:"use_case"`         // 图片: multimodal, 文件: my_files
	Width       int    `json:"width,omitempty"`  // 仅图片
	Height      int    `json:"height,omitempty"` // 仅图片
	DownloadURL string `json:"download_url"`     // POST /uploaded 返回,通常不直接用
}

// UploadFile 执行完整三步上传。调用方传入原始字节 + 建议的文件名即可。
// 识别到 image/* 时会尝试 Decode 拿到宽高(Decode 失败不致命,按 0 处理)。
//
// 实践经验:步骤 1、3 走 chatgpt.com(uTLS / 代理 / auth 头),步骤 2 走 Azure,
// 使用独立标准 HTTP/TLS client;Azure 的 SAS URL 本身带鉴权,不需要 Oai/Auth 头。
func (c *Client) UploadFile(ctx context.Context, data []byte, fileName string) (*UploadedFile, error) {
	if len(data) == 0 {
		return nil, errors.New("empty file data")
	}
	mime, ext := sniffMime(data)
	useCase := "multimodal"
	if !strings.HasPrefix(mime, "image/") {
		useCase = "my_files"
	}
	if fileName == "" {
		fileName = fmt.Sprintf("file-%d%s", len(data), ext)
	}

	out := &UploadedFile{
		FileName: fileName,
		FileSize: len(data),
		MimeType: mime,
		UseCase:  useCase,
	}
	if strings.HasPrefix(mime, "image/") {
		if img, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			out.Width = img.Width
			out.Height = img.Height
		}
	}

	// ---- Step 1: POST /backend-api/files ----
	step1Body := map[string]interface{}{
		"file_name": fileName,
		"file_size": len(data),
		"use_case":  useCase,
	}
	if out.Width > 0 && out.Height > 0 {
		step1Body["height"] = out.Height
		step1Body["width"] = out.Width
	}
	b1, _ := json.Marshal(step1Body)
	step1Resp, err := c.createUploadFile(ctx, b1)
	if err != nil {
		return nil, err
	}
	out.FileID = step1Resp.FileID

	// chatgpt 浏览器行为:step1 和 step2 之间会 sleep 一小会儿,避免 Azure 那边
	// 还没完成 SAS 分发。参考实现是 1s,这里保守点给 500ms。
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// ---- Step 2: PUT upload_url (Azure Blob) ----
	if err := c.uploadFileBytes(ctx, step1Resp.UploadURL, mime, data); err != nil {
		return nil, err
	}

	downloadURL, err := c.registerUploadedFile(ctx, step1Resp.FileID)
	if err != nil {
		return nil, err
	}
	out.DownloadURL = downloadURL

	return out, nil
}

type createUploadFileResponse struct {
	FileID    string `json:"file_id"`
	UploadURL string `json:"upload_url"`
	Status    string `json:"status"`
}

func (c *Client) createUploadFile(ctx context.Context, body []byte) (createUploadFileResponse, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.opts.BaseURL+"/backend-api/files",
			bytes.NewReader(body))
		if err != nil {
			return createUploadFileResponse{}, err
		}
		c.commonHeaders(req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		res, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("create file: %w", err)
			if attempt < maxAttempts && isRetryableUploadError(ctx, err) {
				if err := sleepWithContext(ctx, uploadRetryDelay(attempt)); err != nil {
					return createUploadFileResponse{}, lastErr
				}
				continue
			}
			return createUploadFileResponse{}, lastErr
		}
		buf, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode >= 400 {
			lastErr = &UpstreamError{Status: res.StatusCode, Message: "create file failed", Body: string(buf)}
			if attempt < maxAttempts && isRetryableUploadStatus(res.StatusCode) {
				if err := sleepWithContext(ctx, uploadRetryDelay(attempt)); err != nil {
					return createUploadFileResponse{}, lastErr
				}
				continue
			}
			return createUploadFileResponse{}, lastErr
		}
		var step1Resp createUploadFileResponse
		if err := json.Unmarshal(buf, &step1Resp); err != nil {
			return createUploadFileResponse{}, fmt.Errorf("decode create-file resp: %w (body=%s)", err, truncateStr(string(buf), 200))
		}
		if step1Resp.FileID == "" || step1Resp.UploadURL == "" {
			return createUploadFileResponse{}, fmt.Errorf("create-file empty: %s", truncateStr(string(buf), 200))
		}
		return step1Resp, nil
	}
	return createUploadFileResponse{}, lastErr
}

func (c *Client) registerUploadedFile(ctx context.Context, fileID string) (string, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.opts.BaseURL+"/backend-api/files/"+fileID+"/uploaded",
			strings.NewReader("{}"))
		if err != nil {
			return "", err
		}
		c.commonHeaders(req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		res, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("register uploaded: %w", err)
			if attempt < maxAttempts && isRetryableUploadError(ctx, err) {
				if err := sleepWithContext(ctx, uploadRetryDelay(attempt)); err != nil {
					return "", lastErr
				}
				continue
			}
			return "", lastErr
		}
		buf, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode >= 400 {
			lastErr = &UpstreamError{Status: res.StatusCode, Message: "register uploaded failed", Body: string(buf)}
			if attempt < maxAttempts && isRetryableUploadStatus(res.StatusCode) {
				if err := sleepWithContext(ctx, uploadRetryDelay(attempt)); err != nil {
					return "", lastErr
				}
				continue
			}
			return "", lastErr
		}
		var step3Resp struct {
			Status      string `json:"status"`
			DownloadURL string `json:"download_url"`
		}
		_ = json.Unmarshal(buf, &step3Resp)
		return step3Resp.DownloadURL, nil
	}
	return "", lastErr
}

func (c *Client) uploadFileBytes(ctx context.Context, uploadURL, mime string, data []byte) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mime)
		req.Header.Set("x-ms-blob-type", "BlockBlob")
		req.Header.Set("x-ms-version", "2020-04-08")
		req.Header.Set("Origin", c.opts.BaseURL)
		req.Header.Set("User-Agent", c.opts.UserAgent)
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("Accept-Language", "en-US,en;q=0.8")
		req.Header.Set("Referer", c.opts.BaseURL+"/")

		res, err := c.uploadHTTPClient().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("upload PUT: %w", err)
			if attempt < maxAttempts && isRetryableUploadError(ctx, err) {
				if err := sleepWithContext(ctx, uploadRetryDelay(attempt)); err != nil {
					return lastErr
				}
				continue
			}
			return lastErr
		}

		if res.StatusCode >= 400 {
			buf, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			lastErr = &UpstreamError{Status: res.StatusCode, Message: "upload PUT failed", Body: string(buf)}
			if attempt < maxAttempts && isRetryableUploadStatus(res.StatusCode) {
				if err := sleepWithContext(ctx, uploadRetryDelay(attempt)); err != nil {
					return lastErr
				}
				continue
			}
			return lastErr
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return nil
	}
	return lastErr
}

func (c *Client) uploadHTTPClient() *http.Client {
	if c != nil && c.uploadHC != nil {
		return c.uploadHC
	}
	return c.hc
}

func isRetryableUploadStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func isRetryableUploadError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "unexpected end")
}

func uploadRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	return time.Duration(attempt) * 500 * time.Millisecond
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Attachment 是 messages[*].metadata.attachments[*] 的序列化对象。
type Attachment struct {
	ID       string `json:"id"`
	MimeType string `json:"mimeType"`
	Name     string `json:"name"`
	Size     int    `json:"size"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// ToAttachment 把一个已上传的 file 转成 messages.metadata.attachments 里的条目。
func (u *UploadedFile) ToAttachment() Attachment {
	a := Attachment{ID: u.FileID, MimeType: u.MimeType, Name: u.FileName, Size: u.FileSize}
	if u.UseCase == "multimodal" {
		a.Width = u.Width
		a.Height = u.Height
	}
	return a
}

// AssetPointerPart 是 messages[*].content.parts 里的一项(图片),
// 用于把 file-service:// 挂到多模态消息最前面。
type AssetPointerPart struct {
	ContentType  string `json:"content_type,omitempty"` // "image_asset_pointer"
	AssetPointer string `json:"asset_pointer"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	SizeBytes    int    `json:"size_bytes,omitempty"`
}

// ToAssetPointerPart 返回 multimodal_text.parts 里 insert 在 prompt 前的那一项。
func (u *UploadedFile) ToAssetPointerPart() AssetPointerPart {
	return AssetPointerPart{
		ContentType:  "image_asset_pointer",
		AssetPointer: "file-service://" + u.FileID,
		Width:        u.Width,
		Height:       u.Height,
		SizeBytes:    u.FileSize,
	}
}

// sniffMime 用前 512 字节识别 mime 和推荐扩展名。
// net/http 的 DetectContentType 已足够覆盖 png/jpg/gif/webp 的主流场景。
func sniffMime(data []byte) (mime, ext string) {
	n := 512
	if len(data) < n {
		n = len(data)
	}
	mime = http.DetectContentType(data[:n])
	// DetectContentType 可能附带 charset,去掉
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch {
	case strings.EqualFold(mime, "image/jpeg"):
		ext = ".jpg"
	case strings.EqualFold(mime, "image/png"):
		ext = ".png"
	case strings.EqualFold(mime, "image/gif"):
		ext = ".gif"
	case strings.EqualFold(mime, "image/webp"):
		ext = ".webp"
	case strings.EqualFold(mime, "application/pdf"):
		ext = ".pdf"
	default:
		ext = ""
	}
	return
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
