package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// openaiAdapter 兼容 OpenAI /v1/chat/completions、/v1/images/generations。
//
// 许多第三方中转/聚合站(one-api、new-api、deepseek 官方、moonshot 官方、
// kimi 兼容端点等)都遵循 OpenAI 接口规范,差别只在 BaseURL 和 APIKey。
// 因此这个适配器同时适用:BaseURL 允许带或不带 /v1 后缀,我们做一次规整。
type openaiAdapter struct {
	baseURL                 string
	apiKey                  string
	client                  *http.Client
	apimartOfficialFallback *bool
}

// NewOpenAI 构造一个 OpenAI 兼容适配器。
func NewOpenAI(p Params) *openaiAdapter {
	base := strings.TrimRight(p.BaseURL, "/")
	// 自动去尾部的 /v1,底下拼接时再补;用户填 https://api.openai.com 和
	// https://api.openai.com/v1 都要能用。
	base = strings.TrimSuffix(base, "/v1")
	timeout := time.Duration(p.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	var extra struct {
		OfficialFallback *bool `json:"official_fallback"`
	}
	if strings.TrimSpace(p.Extra) != "" {
		_ = json.Unmarshal([]byte(p.Extra), &extra)
	}
	return &openaiAdapter{
		baseURL:                 base,
		apiKey:                  p.APIKey,
		client:                  &http.Client{Timeout: timeout},
		apimartOfficialFallback: extra.OfficialFallback,
	}
}

func (a *openaiAdapter) Type() string { return "openai" }

func (a *openaiAdapter) endpoint(path string) string {
	return a.baseURL + "/v1" + path
}

// Chat 发起 OpenAI /v1/chat/completions。流式和非流式都转成统一的 ChatStream。
func (a *openaiAdapter) Chat(ctx context.Context, upstreamModel string, req *ChatRequest) (ChatStream, error) {
	payload := map[string]any{
		"model":    upstreamModel,
		"messages": req.Messages,
		"stream":   req.Stream,
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if req.TopP > 0 {
		payload["top_p"] = req.TopP
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, upstreamErr(resp)
	}

	ch := make(chan ChatChunk, 16)
	if req.Stream {
		go parseOpenAISSE(resp.Body, ch)
	} else {
		go parseOpenAINonStream(resp.Body, ch)
	}
	return ch, nil
}

// parseOpenAISSE 解析 text/event-stream 响应,每行 data: {...}。
func parseOpenAISSE(body io.ReadCloser, ch chan<- ChatChunk) {
	defer body.Close()
	defer close(ch)

	sc := bufio.NewScanner(body)
	// SSE 单行可能很长,扩大 buffer。
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 4*1024*1024)

	var lastUsage *ChatUsage

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var obj struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			continue
		}
		if obj.Usage != nil {
			lastUsage = &ChatUsage{
				PromptTokens:     obj.Usage.PromptTokens,
				CompletionTokens: obj.Usage.CompletionTokens,
				TotalTokens:      obj.Usage.TotalTokens,
			}
		}
		for _, c := range obj.Choices {
			chunk := ChatChunk{Delta: c.Delta.Content}
			if c.FinishReason != nil {
				chunk.FinishReason = *c.FinishReason
			}
			ch <- chunk
		}
	}

	if lastUsage != nil {
		ch <- ChatChunk{Usage: lastUsage}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		ch <- ChatChunk{Err: err}
	}
}

// parseOpenAINonStream 读整个 JSON 响应,一次吐成 delta + finish_reason。
func parseOpenAINonStream(body io.ReadCloser, ch chan<- ChatChunk) {
	defer body.Close()
	defer close(ch)

	var obj struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(body).Decode(&obj); err != nil {
		ch <- ChatChunk{Err: fmt.Errorf("openai: decode non-stream: %w", err)}
		return
	}
	if len(obj.Choices) == 0 {
		ch <- ChatChunk{FinishReason: "stop"}
		return
	}
	c := obj.Choices[0]
	ch <- ChatChunk{Delta: c.Message.Content, FinishReason: c.FinishReason}
	ch <- ChatChunk{Usage: &ChatUsage{
		PromptTokens:     obj.Usage.PromptTokens,
		CompletionTokens: obj.Usage.CompletionTokens,
		TotalTokens:      obj.Usage.TotalTokens,
	}}
}

// ImageGenerate 调用 /v1/images/generations(DALL·E 3 / gpt-image-1 等)。
func (a *openaiAdapter) ImageGenerate(ctx context.Context, upstreamModel string, req *ImageRequest) (*ImageResult, error) {
	n := req.N
	if n <= 0 {
		n = 1
	}
	size := req.Size
	if size == "" {
		size = "1024x1024"
	}
	payload := map[string]any{
		"model":  upstreamModel,
		"prompt": req.Prompt,
		"n":      n,
		"size":   size,
	}
	isAPIMart := isAPIMartImageEndpoint(a.baseURL)
	if isAPIMart {
		if ratio := strings.TrimSpace(req.AspectRatio); ratio != "" {
			payload["size"] = ratio
		}
		if resolution := normalizeAPIMartResolution(req.Resolution); resolution != "" {
			payload["resolution"] = resolution
		}
		if a.apimartOfficialFallback != nil {
			payload["official_fallback"] = *a.apimartOfficialFallback
		}
	}
	path := "/images/generations"
	if len(req.Images) > 0 {
		if isAPIMart {
			imageURLs := make([]string, 0, len(req.Images))
			for _, imageURL := range req.Images {
				if strings.TrimSpace(imageURL) == "" {
					continue
				}
				imageURLs = append(imageURLs, imageURL)
			}
			payload["image_urls"] = imageURLs
		} else {
			path = "/images/edits"
			images := make([]map[string]string, 0, len(req.Images))
			for _, imageURL := range req.Images {
				if strings.TrimSpace(imageURL) == "" {
					continue
				}
				images = append(images, map[string]string{"image_url": imageURL})
			}
			payload["images"] = images
		}
	}
	if req.Quality != "" {
		payload["quality"] = req.Quality
	}
	if req.Style != "" {
		payload["style"] = req.Style
	}
	if req.Format != "" && supportsImageResponseFormat(upstreamModel) {
		payload["response_format"] = req.Format
	}
	if req.OutputFormat != "" {
		payload["output_format"] = req.OutputFormat
	}
	if req.OutputCompression != nil {
		payload["output_compression"] = *req.OutputCompression
	}
	if req.Background != "" {
		payload["background"] = req.Background
	}
	if req.Moderation != "" {
		payload["moderation"] = req.Moderation
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: image request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, upstreamErr(resp)
	}
	bodyData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: image read: %w", err)
	}
	if taskID := parseAPIMartImageTaskID(bodyData); taskID != "" {
		return a.pollAPIMartImageTask(ctx, taskID)
	}
	var obj struct {
		Data []struct {
			URL string `json:"url"`
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyData, &obj); err != nil {
		return nil, fmt.Errorf("openai: image decode: %w", err)
	}
	r := &ImageResult{}
	for _, d := range obj.Data {
		if d.URL != "" {
			r.URLs = append(r.URLs, d.URL)
		}
		if d.B64 != "" {
			r.B64s = append(r.B64s, d.B64)
		}
	}
	if len(r.URLs) == 0 && len(r.B64s) == 0 {
		return nil, errors.New("openai: empty image response")
	}
	return r, nil
}

func isAPIMartImageEndpoint(baseURL string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(baseURL)), "apimart.ai")
}

func normalizeAPIMartResolution(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	switch normalized {
	case "1k", "2k", "4k":
		return normalized
	default:
		return ""
	}
}

func parseAPIMartImageTaskID(body []byte) string {
	var payload struct {
		Code any `json:"code"`
		Data []struct {
			Status string `json:"status"`
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Data) == 0 {
		return ""
	}
	if valueString(payload.Code) != "200" {
		return ""
	}
	first := payload.Data[0]
	status := strings.ToLower(strings.TrimSpace(first.Status))
	if first.TaskID == "" || (status != "" && status != "submitted") {
		return ""
	}
	return first.TaskID
}

func (a *openaiAdapter) pollAPIMartImageTask(ctx context.Context, taskID string) (*ImageResult, error) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return a.finalizeAPIMartImageTaskAfterTimeout(taskID, ctx.Err())
		case <-timer.C:
		}
		result, done, err := a.fetchAPIMartImageTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if done {
			return result, nil
		}
		timer.Reset(2 * time.Second)
	}
}

func (a *openaiAdapter) finalizeAPIMartImageTaskAfterTimeout(taskID string, originalErr error) (*ImageResult, error) {
	if strings.TrimSpace(taskID) == "" || originalErr == nil {
		return nil, originalErr
	}
	finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, done, err := a.fetchAPIMartImageTask(finalCtx, taskID)
	if err == nil {
		if done {
			return result, nil
		}
		return nil, originalErr
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return nil, originalErr
	}
	return nil, err
}

func (a *openaiAdapter) fetchAPIMartImageTask(ctx context.Context, taskID string) (*ImageResult, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.endpoint("/tasks/"+url.PathEscape(taskID)+"?language=en"), nil)
	if err != nil {
		return nil, false, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, false, fmt.Errorf("openai: task poll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, false, upstreamErr(resp)
	}
	bodyData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("openai: task poll read: %w", err)
	}
	var payload struct {
		Code any `json:"code"`
		Data struct {
			Status       string `json:"status"`
			Message      string `json:"message"`
			ErrorMessage string `json:"error_message"`
			Error        struct {
				Code    any    `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
			Result struct {
				Images []struct {
					URL any `json:"url"`
				} `json:"images"`
			} `json:"result"`
		} `json:"data"`
		Error struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bodyData, &payload); err != nil {
		return nil, false, fmt.Errorf("openai: task poll decode: %w", err)
	}
	status := strings.ToLower(strings.TrimSpace(payload.Data.Status))
	switch status {
	case "pending", "processing", "":
		return nil, false, nil
	case "completed":
		result := &ImageResult{}
		for _, image := range payload.Data.Result.Images {
			result.URLs = append(result.URLs, nonEmptyStrings(image.URL)...)
		}
		if len(result.URLs) == 0 && len(result.B64s) == 0 {
			return nil, false, errors.New("openai: empty apimart task result")
		}
		return result, true, nil
	case "failed", "cancelled":
		return nil, false, apimartTaskError(payload, bodyData)
	default:
		return nil, false, fmt.Errorf("openai: unexpected apimart task status %q", payload.Data.Status)
	}
}

func apimartTaskError(payload struct {
	Code any `json:"code"`
	Data struct {
		Status       string `json:"status"`
		Message      string `json:"message"`
		ErrorMessage string `json:"error_message"`
		Error        struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Images []struct {
				URL any `json:"url"`
			} `json:"images"`
		} `json:"result"`
	} `json:"data"`
	Error struct {
		Code    any    `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}, body []byte) error {
	statusCode := http.StatusBadGateway
	if strings.EqualFold(strings.TrimSpace(payload.Data.Status), "failed") {
		statusCode = http.StatusInternalServerError
	}
	errCode := valueString(payload.Data.Error.Code)
	if errCode == "" {
		errCode = valueString(payload.Error.Code)
	}
	errType := strings.TrimSpace(payload.Data.Error.Type)
	if errType == "" {
		errType = strings.TrimSpace(payload.Error.Type)
	}
	message := strings.TrimSpace(payload.Data.Error.Message)
	if message == "" {
		message = strings.TrimSpace(payload.Data.ErrorMessage)
	}
	if message == "" {
		message = strings.TrimSpace(payload.Data.Message)
	}
	if message == "" {
		message = strings.TrimSpace(payload.Error.Message)
	}
	if message == "" {
		message = "apimart task " + strings.TrimSpace(payload.Data.Status)
	}
	return &UpstreamHTTPError{
		Status:  statusCode,
		Code:    errCode,
		Type:    errType,
		Message: message,
		Body:    strings.TrimSpace(string(body)),
	}
}

func nonEmptyStrings(v any) []string {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, nonEmptyStrings(item)...)
		}
		return out
	default:
		return nil
	}
}

func supportsImageResponseFormat(model string) bool {
	return !strings.HasPrefix(strings.ToLower(model), "gpt-image-")
}

// Ping 发一次 /v1/models 探活。大部分兼容站都实现了这个端点。
func (a *openaiAdapter) Ping(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.endpoint("/models"), nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return upstreamErr(resp)
	}
	return nil
}

// upstreamErr 读取响应 body 做简要错误归纳。
func upstreamErr(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	return newUpstreamHTTPError(resp, data)
}
