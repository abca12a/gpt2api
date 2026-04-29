package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdimage "image"
	"image/color"
	stddraw "image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const (
	localDefaultOutputFormat  = "png"
	localDefaultOutputQuality = int32(95)
	localMaxOutputBytes       = 64 * 1024 * 1024
	localRequestTimeout       = 45 * time.Second
)

var (
	ErrSuperResolutionDisabled = errors.New("image super resolution: disabled")
	ErrSuperResolutionNoop     = errors.New("image super resolution: source already reaches target")
	ErrSuperResolutionInput    = errors.New("image super resolution: invalid input")
)

type LocalSuperResolutionConfig struct {
	Enabled       bool
	OutputFormat  string
	OutputQuality int32
}

type SuperResolutionResult struct {
	Data        []byte
	ContentType string
	Noop        bool
	JobID       string
}

type LocalSuperResolutionClient struct {
	cfg LocalSuperResolutionConfig
}

func NewLocalSuperResolutionClient(cfg LocalSuperResolutionConfig) (*LocalSuperResolutionClient, error) {
	cfg = normalizeLocalSuperResolutionConfig(cfg)
	if !cfg.Enabled {
		return nil, nil
	}
	return &LocalSuperResolutionClient{cfg: cfg}, nil
}

func (c *LocalSuperResolutionClient) RequestTimeout() time.Duration {
	return localRequestTimeout
}

func (c *LocalSuperResolutionClient) Upscale(ctx context.Context, src []byte, sourceContentType, scale, userData string) (SuperResolutionResult, error) {
	_ = userData
	if c == nil || !c.cfg.Enabled {
		return SuperResolutionResult{}, ErrSuperResolutionDisabled
	}
	prepared, err := prepareLocalSuperResolutionSource(src, scale)
	if err != nil {
		if errors.Is(err, ErrSuperResolutionNoop) {
			return SuperResolutionResult{
				Data:        src,
				ContentType: detectSourceContentType(src, sourceContentType),
				Noop:        true,
			}, nil
		}
		return SuperResolutionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return SuperResolutionResult{}, err
	}

	decoded, _, err := stdimage.Decode(bytes.NewReader(src))
	if err != nil {
		return SuperResolutionResult{}, fmt.Errorf("%w: decode image: %v", ErrSuperResolutionInput, err)
	}
	targetWidth, targetHeight := targetDimensions(prepared.width, prepared.height, prepared.targetLongSide)
	if targetWidth == prepared.width && targetHeight == prepared.height {
		return SuperResolutionResult{
			Data:        src,
			ContentType: detectSourceContentType(src, sourceContentType),
			Noop:        true,
		}, nil
	}

	dst := stdimage.NewRGBA(stdimage.Rect(0, 0, targetWidth, targetHeight))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), decoded, decoded.Bounds(), xdraw.Over, nil)

	if err := ctx.Err(); err != nil {
		return SuperResolutionResult{}, err
	}

	data, contentType, err := encodeLocalUpscaled(dst, c.cfg.OutputFormat, c.cfg.OutputQuality)
	if err != nil {
		return SuperResolutionResult{}, err
	}
	return SuperResolutionResult{
		Data:        data,
		ContentType: contentType,
	}, nil
}

type localPreparedSource struct {
	width          int
	height         int
	targetLongSide int
}

func normalizeLocalSuperResolutionConfig(cfg LocalSuperResolutionConfig) LocalSuperResolutionConfig {
	cfg.OutputFormat = normalizeLocalOutputFormat(cfg.OutputFormat)
	if cfg.OutputQuality <= 0 || cfg.OutputQuality > 100 {
		cfg.OutputQuality = localDefaultOutputQuality
	}
	return cfg
}

func normalizeLocalOutputFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpg", "jpeg":
		return "jpeg"
	default:
		return localDefaultOutputFormat
	}
}

func prepareLocalSuperResolutionSource(src []byte, scale string) (localPreparedSource, error) {
	if len(src) == 0 {
		return localPreparedSource{}, fmt.Errorf("%w: empty image", ErrSuperResolutionInput)
	}
	target := longSideOf(ValidateUpscale(scale))
	if target == 0 {
		return localPreparedSource{}, ErrSuperResolutionNoop
	}
	cfg, _, err := stdimage.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return localPreparedSource{}, fmt.Errorf("%w: decode config: %v", ErrSuperResolutionInput, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return localPreparedSource{}, fmt.Errorf("%w: invalid dimensions", ErrSuperResolutionInput)
	}
	if max(cfg.Width, cfg.Height) >= target {
		return localPreparedSource{}, ErrSuperResolutionNoop
	}
	return localPreparedSource{
		width:          cfg.Width,
		height:         cfg.Height,
		targetLongSide: target,
	}, nil
}

func targetDimensions(width, height, targetLongSide int) (int, int) {
	if width <= 0 || height <= 0 || targetLongSide <= 0 {
		return width, height
	}
	if width >= height {
		targetWidth := targetLongSide
		targetHeight := max(1, int(math.Round(float64(height)*float64(targetLongSide)/float64(width))))
		return targetWidth, targetHeight
	}
	targetHeight := targetLongSide
	targetWidth := max(1, int(math.Round(float64(width)*float64(targetLongSide)/float64(height))))
	return targetWidth, targetHeight
}

func encodeLocalUpscaled(img stdimage.Image, outputFormat string, outputQuality int32) ([]byte, string, error) {
	outputFormat = normalizeLocalOutputFormat(outputFormat)
	var buffer bytes.Buffer
	switch outputFormat {
	case "jpeg":
		if err := jpeg.Encode(&buffer, flattenOnWhite(img), &jpeg.Options{Quality: int(outputQuality)}); err != nil {
			return nil, "", fmt.Errorf("image super resolution: jpeg encode: %w", err)
		}
		if buffer.Len() > localMaxOutputBytes {
			return nil, "", fmt.Errorf("%w: result too large", ErrSuperResolutionInput)
		}
		return buffer.Bytes(), "image/jpeg", nil
	default:
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := encoder.Encode(&buffer, img); err != nil {
			return nil, "", fmt.Errorf("image super resolution: png encode: %w", err)
		}
		if buffer.Len() > localMaxOutputBytes {
			return nil, "", fmt.Errorf("%w: result too large", ErrSuperResolutionInput)
		}
		return buffer.Bytes(), "image/png", nil
	}
}

func flattenOnWhite(src stdimage.Image) *stdimage.RGBA {
	bounds := src.Bounds()
	dst := stdimage.NewRGBA(bounds)
	stddraw.Draw(dst, bounds, &stdimage.Uniform{C: color.White}, stdimage.Point{}, stddraw.Src)
	stddraw.Draw(dst, bounds, src, bounds.Min, stddraw.Over)
	return dst
}

func detectSourceContentType(src []byte, sourceContentType string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(sourceContentType)), "image/") {
		return sourceContentType
	}
	if detected := strings.ToLower(strings.TrimSpace(detectContentType(src))); strings.HasPrefix(detected, "image/") {
		return detected
	}
	return "image/png"
}

func detectContentType(src []byte) string {
	if len(src) == 0 {
		return ""
	}
	if len(src) > 512 {
		src = src[:512]
	}
	return http.DetectContentType(src)
}
