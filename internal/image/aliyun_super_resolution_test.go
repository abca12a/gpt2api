package image

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	aliyunimage "github.com/alibabacloud-go/imageenhan-20190930/v3/client"
	"github.com/alibabacloud-go/tea/dara"
)

func TestAliyunScaleForTarget(t *testing.T) {
	tests := []struct {
		name      string
		width     int
		height    int
		target    int
		wantScale int32
		wantNoop  bool
	}{
		{name: "1024 to 2k", width: 1024, height: 1024, target: 2560, wantScale: 3},
		{name: "1024 to 4k", width: 1024, height: 1024, target: 3840, wantScale: 4},
		{name: "1792 to 4k", width: 1792, height: 1024, target: 3840, wantScale: 3},
		{name: "already target", width: 3840, height: 2160, target: 3840, wantNoop: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotScale, gotNoop := aliyunScaleForTarget(tt.width, tt.height, tt.target)
			if gotScale != tt.wantScale || gotNoop != tt.wantNoop {
				t.Fatalf("aliyunScaleForTarget() = (%d, %v), want (%d, %v)", gotScale, gotNoop, tt.wantScale, tt.wantNoop)
			}
		})
	}
}

func TestPrepareAliyunSuperResolutionInput(t *testing.T) {
	pngBytes := encodePNG(t, 128, 128)
	prepared, err := prepareAliyunSuperResolutionInput(pngBytes, Upscale2K)
	if err != nil {
		t.Fatalf("prepare png: %v", err)
	}
	if prepared.format != "png" || !bytes.Equal(prepared.data, pngBytes) || prepared.scale != 4 {
		t.Fatalf("unexpected prepared png: format=%q scale=%d same=%v", prepared.format, prepared.scale, bytes.Equal(prepared.data, pngBytes))
	}

	gifBytes := encodeGIF(t, 128, 128)
	prepared, err = prepareAliyunSuperResolutionInput(gifBytes, Upscale2K)
	if err != nil {
		t.Fatalf("prepare gif: %v", err)
	}
	if prepared.format != "png" || bytes.Equal(prepared.data, gifBytes) || prepared.scale != 4 {
		t.Fatalf("gif should be converted to png, got format=%q scale=%d same=%v", prepared.format, prepared.scale, bytes.Equal(prepared.data, gifBytes))
	}
	if _, format, err := image.Decode(bytes.NewReader(prepared.data)); err != nil || format != "png" {
		t.Fatalf("converted gif decode = format %q err %v, want png", format, err)
	}
}

func TestPrepareAliyunSuperResolutionInputRejectsDimensions(t *testing.T) {
	if _, err := prepareAliyunSuperResolutionInput(encodePNG(t, 128, 32), Upscale2K); err == nil {
		t.Fatalf("short side below 64 should be rejected")
	}
	if _, err := prepareAliyunSuperResolutionInput(encodePNG(t, 256, 64), Upscale2K); err == nil {
		t.Fatalf("aspect ratio larger than 2:1 should be rejected")
	}
	if _, err := prepareAliyunSuperResolutionInput(encodePNG(t, 3840, 2160), Upscale4K); err != ErrSuperResolutionNoop {
		t.Fatalf("already target err = %v, want noop", err)
	}
}

func TestAliyunSuperResolutionPollSuccess(t *testing.T) {
	resultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("upscaled"))
	}))
	defer resultServer.Close()

	fakeAPI := &fakeAliyunSuperResolutionAPI{
		jobID: "job-1",
		statuses: []string{
			"PROCESSING",
			"PROCESS_SUCCESS",
		},
		resultURL: resultServer.URL + "/out.png?x=1&amp;y=2",
	}
	client := newAliyunSuperResolutionClient(fakeAPI, AliyunSuperResolutionConfig{
		Enabled:      true,
		PollInterval: time.Nanosecond,
		PollTimeout:  time.Second,
	}, resultServer.Client())
	client.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	result, err := client.Upscale(context.Background(), encodePNG(t, 128, 128), "image/png", Upscale2K, "task-1")
	if err != nil {
		t.Fatalf("Upscale: %v", err)
	}
	if string(result.Data) != "upscaled" || result.ContentType != "image/png" || result.JobID != "job-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if fakeAPI.pollCalls != 2 {
		t.Fatalf("poll calls = %d, want 2", fakeAPI.pollCalls)
	}
	if fakeAPI.submittedScale != 4 {
		t.Fatalf("submitted scale = %d, want 4", fakeAPI.submittedScale)
	}
}

func TestParseAliyunResultURL(t *testing.T) {
	if got := parseAliyunResultURL(`{"ResultUrl":"https://example.test/a.png?x=1&amp;y=2"}`); got != "https://example.test/a.png?x=1&y=2" {
		t.Fatalf("parse json result = %q", got)
	}
	if got := parseAliyunResultURL("https://example.test/a.png?x=1&amp;y=2"); got != "https://example.test/a.png?x=1&y=2" {
		t.Fatalf("parse direct result = %q", got)
	}
}

type fakeAliyunSuperResolutionAPI struct {
	jobID          string
	statuses       []string
	resultURL      string
	pollCalls      int
	submittedScale int32
}

func (f *fakeAliyunSuperResolutionAPI) GenerateSuperResolutionImageAdvance(request *aliyunimage.GenerateSuperResolutionImageAdvanceRequest, runtime *dara.RuntimeOptions) (*aliyunimage.GenerateSuperResolutionImageResponse, error) {
	f.submittedScale = dara.Int32Value(request.GetScale())
	return &aliyunimage.GenerateSuperResolutionImageResponse{
		Body: &aliyunimage.GenerateSuperResolutionImageResponseBody{
			RequestId: dara.String(f.jobID),
		},
	}, nil
}

func (f *fakeAliyunSuperResolutionAPI) GetAsyncJobResult(request *aliyunimage.GetAsyncJobResultRequest) (*aliyunimage.GetAsyncJobResultResponse, error) {
	status := "PROCESS_SUCCESS"
	if f.pollCalls < len(f.statuses) {
		status = f.statuses[f.pollCalls]
	}
	f.pollCalls++
	data := &aliyunimage.GetAsyncJobResultResponseBodyData{
		JobId:  request.GetJobId(),
		Status: dara.String(status),
	}
	if status == "PROCESS_SUCCESS" {
		data.Result = dara.String(`{"ResultUrl":"` + f.resultURL + `"}`)
	}
	return &aliyunimage.GetAsyncJobResultResponse{
		Body: &aliyunimage.GetAsyncJobResultResponseBody{Data: data},
	}, nil
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
