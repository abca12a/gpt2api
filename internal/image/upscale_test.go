package image

import "testing"

func TestUpscaleAllowedForPlanOnlyFree(t *testing.T) {
	tests := []struct {
		name     string
		scale    string
		planType string
		want     bool
	}{
		{name: "free 2k", scale: Upscale2K, planType: "free", want: true},
		{name: "free 4k uppercase", scale: " 4K ", planType: " FREE ", want: true},
		{name: "plus denied", scale: Upscale4K, planType: "plus", want: false},
		{name: "team denied", scale: Upscale2K, planType: "team", want: false},
		{name: "empty scale denied", scale: "", planType: "free", want: false},
		{name: "invalid scale denied", scale: "8k", planType: "free", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UpscaleAllowedForPlan(tt.scale, tt.planType); got != tt.want {
				t.Fatalf("UpscaleAllowedForPlan(%q, %q) = %v, want %v", tt.scale, tt.planType, got, tt.want)
			}
		})
	}
}
