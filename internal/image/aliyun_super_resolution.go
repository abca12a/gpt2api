package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	gimage "image"
	"image/png"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	aliyunimage "github.com/alibabacloud-go/imageenhan-20190930/v3/client"
	"github.com/alibabacloud-go/tea/dara"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const (
	aliyunDefaultEndpoint      = "imageenhan.cn-shanghai.aliyuncs.com"
	aliyunDefaultRegionID      = "cn-shanghai"
	aliyunDefaultOutputFormat  = "png"
	aliyunDefaultOutputQuality = int32(95)
	aliyunMaxInputBytes        = 20 * 1024 * 1024
	aliyunMaxOutputBytes       = 64 * 1024 * 1024
	aliyunMaxLongSide          = 5000
	aliyunMinShortSide         = 64
	aliyunMaxAspectRatio       = 2.0
)

var (
	ErrSuperResolutionDisabled = errors.New("image super resolution: disabled")
	ErrSuperResolutionNoop     = errors.New("image super resolution: source already reaches target")
	ErrSuperResolutionInput    = errors.New("image super resolution: invalid input")
	ErrSuperResolutionTimeout  = errors.New("image super resolution: poll timeout")
)

type AliyunSuperResolutionConfig struct {
	Enabled         bool
	AccessKeyID     string
	AccessKeySecret string
	RegionID        string
	Endpoint        string
	OutputFormat    string
	OutputQuality   int32
	PollInterval    time.Duration
	PollTimeout     time.Duration
}

type SuperResolutionResult struct {
	Data        []byte
	ContentType string
	Noop        bool
	JobID       string
}

type aliyunSuperResolutionAPI interface {
	GenerateSuperResolutionImageAdvance(request *aliyunimage.GenerateSuperResolutionImageAdvanceRequest, runtime *dara.RuntimeOptions) (*aliyunimage.GenerateSuperResolutionImageResponse, error)
	GetAsyncJobResult(request *aliyunimage.GetAsyncJobResultRequest) (*aliyunimage.GetAsyncJobResultResponse, error)
}

type AliyunSuperResolutionClient struct {
	api        aliyunSuperResolutionAPI
	cfg        AliyunSuperResolutionConfig
	httpClient *http.Client
	sleep      func(context.Context, time.Duration) error
}

func NewAliyunSuperResolutionClient(cfg AliyunSuperResolutionConfig) (*AliyunSuperResolutionClient, error) {
	cfg = normalizeAliyunSuperResolutionConfig(cfg)
	if !cfg.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.AccessKeySecret) == "" {
		return nil, fmt.Errorf("%w: access key is empty", ErrSuperResolutionDisabled)
	}
	api, err := aliyunimage.NewClient(&openapi.Config{
		AccessKeyId:     dara.String(cfg.AccessKeyID),
		AccessKeySecret: dara.String(cfg.AccessKeySecret),
		Endpoint:        dara.String(cfg.Endpoint),
		RegionId:        dara.String(cfg.RegionID),
		Protocol:        dara.String("HTTPS"),
		ConnectTimeout:  dara.Int(10 * 1000),
		ReadTimeout:     dara.Int(60 * 1000),
	})
	if err != nil {
		return nil, fmt.Errorf("image super resolution: aliyun client: %w", err)
	}
	return newAliyunSuperResolutionClient(api, cfg, nil), nil
}

func newAliyunSuperResolutionClient(api aliyunSuperResolutionAPI, cfg AliyunSuperResolutionConfig, httpClient *http.Client) *AliyunSuperResolutionClient {
	cfg = normalizeAliyunSuperResolutionConfig(cfg)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &AliyunSuperResolutionClient{
		api:        api,
		cfg:        cfg,
		httpClient: httpClient,
		sleep:      sleepContext,
	}
}

func (c *AliyunSuperResolutionClient) RequestTimeout() time.Duration {
	if c == nil {
		return 3 * time.Minute
	}
	timeout := c.cfg.PollTimeout + 60*time.Second
	if timeout < 3*time.Minute {
		return 3 * time.Minute
	}
	return timeout
}

func normalizeAliyunSuperResolutionConfig(cfg AliyunSuperResolutionConfig) AliyunSuperResolutionConfig {
	if strings.TrimSpace(cfg.RegionID) == "" {
		cfg.RegionID = aliyunDefaultRegionID
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		cfg.Endpoint = aliyunDefaultEndpoint
	}
	cfg.OutputFormat = strings.ToLower(strings.TrimSpace(cfg.OutputFormat))
	if cfg.OutputFormat == "" {
		cfg.OutputFormat = aliyunDefaultOutputFormat
	}
	if cfg.OutputQuality <= 0 {
		cfg.OutputQuality = aliyunDefaultOutputQuality
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 120 * time.Second
	}
	return cfg
}

func (c *AliyunSuperResolutionClient) Upscale(ctx context.Context, src []byte, sourceContentType, scale, userData string) (SuperResolutionResult, error) {
	if c == nil || c.api == nil {
		return SuperResolutionResult{}, ErrSuperResolutionDisabled
	}
	input, err := prepareAliyunSuperResolutionInput(src, scale)
	if err != nil {
		if errors.Is(err, ErrSuperResolutionNoop) {
			return SuperResolutionResult{Data: src, ContentType: sourceContentType, Noop: true}, nil
		}
		return SuperResolutionResult{}, err
	}

	request := &aliyunimage.GenerateSuperResolutionImageAdvanceRequest{
		ImageUrlObject: bytes.NewReader(input.data),
		OutputFormat:   dara.String(c.cfg.OutputFormat),
		OutputQuality:  dara.Int32(c.cfg.OutputQuality),
		Scale:          dara.Int32(input.scale),
	}
	if strings.TrimSpace(userData) != "" {
		request.UserData = dara.String(userData)
	}
	runtime := &dara.RuntimeOptions{
		Autoretry:      dara.Bool(true),
		MaxAttempts:    dara.Int(2),
		ConnectTimeout: dara.Int(10 * 1000),
		ReadTimeout:    dara.Int(60 * 1000),
	}

	if err := ctx.Err(); err != nil {
		return SuperResolutionResult{}, err
	}
	response, err := c.api.GenerateSuperResolutionImageAdvance(request, runtime)
	if err != nil {
		return SuperResolutionResult{}, fmt.Errorf("image super resolution: submit: %w", err)
	}
	if resultURL := directAliyunResultURL(response); resultURL != "" {
		data, contentType, err := c.downloadResult(ctx, resultURL)
		if err != nil {
			return SuperResolutionResult{}, err
		}
		return SuperResolutionResult{Data: data, ContentType: contentType}, nil
	}

	jobID := aliyunSubmitJobID(response)
	if jobID == "" {
		return SuperResolutionResult{}, errors.New("image super resolution: empty aliyun job id")
	}
	resultURL, err := c.pollResultURL(ctx, jobID)
	if err != nil {
		return SuperResolutionResult{JobID: jobID}, err
	}
	data, contentType, err := c.downloadResult(ctx, resultURL)
	if err != nil {
		return SuperResolutionResult{JobID: jobID}, err
	}
	return SuperResolutionResult{Data: data, ContentType: contentType, JobID: jobID}, nil
}

func (c *AliyunSuperResolutionClient) pollResultURL(ctx context.Context, jobID string) (string, error) {
	deadline := time.Now().Add(c.cfg.PollTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		response, err := c.api.GetAsyncJobResult(&aliyunimage.GetAsyncJobResultRequest{JobId: dara.String(jobID)})
		if err != nil {
			return "", fmt.Errorf("image super resolution: poll: %w", err)
		}
		var data *aliyunimage.GetAsyncJobResultResponseBodyData
		if response != nil && response.GetBody() != nil {
			data = response.GetBody().GetData()
		}
		if data != nil {
			status := strings.ToUpper(strings.TrimSpace(dara.StringValue(data.GetStatus())))
			switch {
			case status == "PROCESS_SUCCESS":
				resultURL := parseAliyunResultURL(dara.StringValue(data.GetResult()))
				if resultURL == "" {
					return "", errors.New("image super resolution: empty result url")
				}
				return resultURL, nil
			case strings.Contains(status, "FAILED") || strings.Contains(status, "FAIL") || strings.Contains(status, "ERROR"):
				return "", fmt.Errorf("image super resolution: job failed: %s %s", dara.StringValue(data.GetErrorCode()), dara.StringValue(data.GetErrorMessage()))
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w: %s", ErrSuperResolutionTimeout, jobID)
		}
		if err := c.sleep(ctx, c.cfg.PollInterval); err != nil {
			return "", err
		}
	}
}

func (c *AliyunSuperResolutionClient) downloadResult(ctx context.Context, resultURL string) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("image super resolution: result request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("image super resolution: result fetch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("image super resolution: result http %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, aliyunMaxOutputBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("image super resolution: result read: %w", err)
	}
	if len(body) > aliyunMaxOutputBytes {
		return nil, "", fmt.Errorf("%w: result too large", ErrSuperResolutionInput)
	}
	contentType := response.Header.Get("Content-Type")
	if len(body) > 0 && (contentType == "" || !strings.HasPrefix(strings.ToLower(contentType), "image/")) {
		detected := http.DetectContentType(body)
		if strings.HasPrefix(strings.ToLower(detected), "image/") {
			contentType = detected
		}
	}
	if contentType == "" {
		contentType = "image/" + c.cfg.OutputFormat
	}
	return body, contentType, nil
}

type aliyunPreparedInput struct {
	data   []byte
	format string
	width  int
	height int
	scale  int32
}

func prepareAliyunSuperResolutionInput(src []byte, scale string) (aliyunPreparedInput, error) {
	if len(src) == 0 {
		return aliyunPreparedInput{}, fmt.Errorf("%w: empty image", ErrSuperResolutionInput)
	}
	target := longSideOf(ValidateUpscale(scale))
	if target == 0 {
		return aliyunPreparedInput{}, ErrSuperResolutionNoop
	}
	config, format, err := gimage.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return aliyunPreparedInput{}, fmt.Errorf("%w: decode config: %v", ErrSuperResolutionInput, err)
	}
	aliyunScale, noop := aliyunScaleForTarget(config.Width, config.Height, target)
	if noop {
		return aliyunPreparedInput{}, ErrSuperResolutionNoop
	}
	if len(src) > aliyunMaxInputBytes {
		return aliyunPreparedInput{}, fmt.Errorf("%w: image larger than 20MB", ErrSuperResolutionInput)
	}
	if err := validateAliyunImageDimensions(config.Width, config.Height); err != nil {
		return aliyunPreparedInput{}, err
	}

	format = strings.ToLower(format)
	prepared := aliyunPreparedInput{data: src, format: format, width: config.Width, height: config.Height, scale: aliyunScale}
	if isAliyunDirectUploadFormat(format) {
		return prepared, nil
	}
	decoded, _, err := gimage.Decode(bytes.NewReader(src))
	if err != nil {
		return aliyunPreparedInput{}, fmt.Errorf("%w: decode image: %v", ErrSuperResolutionInput, err)
	}
	var buffer bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&buffer, decoded); err != nil {
		return aliyunPreparedInput{}, fmt.Errorf("image super resolution: png encode: %w", err)
	}
	if buffer.Len() > aliyunMaxInputBytes {
		return aliyunPreparedInput{}, fmt.Errorf("%w: converted image larger than 20MB", ErrSuperResolutionInput)
	}
	prepared.data = buffer.Bytes()
	prepared.format = "png"
	return prepared, nil
}

func validateAliyunImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("%w: invalid dimensions", ErrSuperResolutionInput)
	}
	longSide := width
	shortSide := height
	if height > longSide {
		longSide = height
		shortSide = width
	}
	if longSide > aliyunMaxLongSide {
		return fmt.Errorf("%w: long side larger than 5000", ErrSuperResolutionInput)
	}
	if shortSide < aliyunMinShortSide {
		return fmt.Errorf("%w: short side smaller than 64", ErrSuperResolutionInput)
	}
	if float64(longSide)/float64(shortSide) > aliyunMaxAspectRatio {
		return fmt.Errorf("%w: aspect ratio larger than 2:1", ErrSuperResolutionInput)
	}
	return nil
}

func isAliyunDirectUploadFormat(format string) bool {
	switch strings.ToLower(format) {
	case "jpeg", "jpg", "png", "bmp":
		return true
	default:
		return false
	}
}

func aliyunScaleForTarget(width, height, targetLongSide int) (int32, bool) {
	longSide := width
	if height > longSide {
		longSide = height
	}
	if longSide <= 0 || targetLongSide <= 0 || longSide >= targetLongSide {
		return 0, true
	}
	scale := int32(math.Ceil(float64(targetLongSide) / float64(longSide)))
	if scale < 1 {
		scale = 1
	}
	if scale > 4 {
		scale = 4
	}
	return scale, false
}

func aliyunSubmitJobID(response *aliyunimage.GenerateSuperResolutionImageResponse) string {
	if response == nil || response.Body == nil {
		return ""
	}
	return strings.TrimSpace(dara.StringValue(response.Body.GetRequestId()))
}

func directAliyunResultURL(response *aliyunimage.GenerateSuperResolutionImageResponse) string {
	if response == nil || response.Body == nil || response.Body.GetData() == nil {
		return ""
	}
	return sanitizeAliyunResultURL(dara.StringValue(response.Body.GetData().GetResultUrl()))
}

func parseAliyunResultURL(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return ""
	}
	if strings.HasPrefix(result, "http://") || strings.HasPrefix(result, "https://") {
		return sanitizeAliyunResultURL(result)
	}
	var object map[string]interface{}
	if err := json.Unmarshal([]byte(result), &object); err != nil {
		return ""
	}
	for _, key := range []string{"ResultUrl", "resultUrl", "result_url", "URL", "url"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return sanitizeAliyunResultURL(value)
		}
	}
	return ""
}

func sanitizeAliyunResultURL(raw string) string {
	return strings.ReplaceAll(strings.TrimSpace(raw), "&amp;", "&")
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
