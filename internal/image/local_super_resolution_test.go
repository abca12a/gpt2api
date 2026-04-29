package image

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

func TestLocalSuperResolutionUpscaleToTargetLongSide(t *testing.T) {
	client, err := NewLocalSuperResolutionClient(LocalSuperResolutionConfig{
		Enabled:       true,
		OutputFormat:  "png",
		OutputQuality: 95,
	})
	if err != nil {
		t.Fatalf("NewLocalSuperResolutionClient: %v", err)
	}

	result, err := client.Upscale(context.Background(), encodePNG(t, 128, 64), "image/png", Upscale2K, "task-1")
	if err != nil {
		t.Fatalf("Upscale: %v", err)
	}
	if result.Noop {
		t.Fatal("Upscale returned noop, want resized image")
	}
	if len(result.Data) == 0 {
		t.Fatal("Upscale returned empty data")
	}

	cfg, format := decodeImageConfig(t, result.Data)
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if cfg.Width != 2560 || cfg.Height != 1280 {
		t.Fatalf("size = %dx%d, want 2560x1280", cfg.Width, cfg.Height)
	}
}

func TestLocalSuperResolutionNoopWhenAlreadyTarget(t *testing.T) {
	client, err := NewLocalSuperResolutionClient(LocalSuperResolutionConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewLocalSuperResolutionClient: %v", err)
	}

	src := encodePNG(t, 3840, 2160)
	result, err := client.Upscale(context.Background(), src, "image/png", Upscale4K, "task-2")
	if err != nil {
		t.Fatalf("Upscale: %v", err)
	}
	if !result.Noop {
		t.Fatal("Upscale should noop for image already at target")
	}
	if !bytes.Equal(result.Data, src) {
		t.Fatal("noop result should preserve original bytes")
	}
}

func TestLocalSuperResolutionAllowsWideAspectRatio(t *testing.T) {
	client, err := NewLocalSuperResolutionClient(LocalSuperResolutionConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewLocalSuperResolutionClient: %v", err)
	}

	result, err := client.Upscale(context.Background(), encodePNG(t, 256, 64), "image/png", Upscale2K, "wide")
	if err != nil {
		t.Fatalf("Upscale wide image: %v", err)
	}
	if result.Noop {
		t.Fatal("wide image should still be resizable")
	}

	cfg, _ := decodeImageConfig(t, result.Data)
	if cfg.Width != 2560 || cfg.Height != 640 {
		t.Fatalf("wide size = %dx%d, want 2560x640", cfg.Width, cfg.Height)
	}
}

func TestLocalSuperResolutionConvertsGIFToPNG(t *testing.T) {
	client, err := NewLocalSuperResolutionClient(LocalSuperResolutionConfig{
		Enabled:      true,
		OutputFormat: "png",
	})
	if err != nil {
		t.Fatalf("NewLocalSuperResolutionClient: %v", err)
	}

	result, err := client.Upscale(context.Background(), encodeGIF(t, 128, 128), "image/gif", Upscale2K, "gif")
	if err != nil {
		t.Fatalf("Upscale gif: %v", err)
	}

	cfg, format := decodeImageConfig(t, result.Data)
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if cfg.Width != 2560 || cfg.Height != 2560 {
		t.Fatalf("size = %dx%d, want 2560x2560", cfg.Width, cfg.Height)
	}
}

func decodeImageConfig(t *testing.T, data []byte) (image.Config, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return cfg, format
}

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 127, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func encodeGIF(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, width, height), []color.Color{color.Black, color.White})
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if (x+y)%2 == 0 {
				img.SetColorIndex(x, y, 1)
			}
		}
	}
	var buffer bytes.Buffer
	if err := gif.Encode(&buffer, img, nil); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
