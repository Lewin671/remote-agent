package corebind

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/imageparse"
)

func TestImageBindSmoke(t *testing.T) {
	if got := SniffImageFormat([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}); got != int32(imageparse.FormatPNG) {
		t.Fatalf("SniffImageFormat(png) = %d, want %d", got, imageparse.FormatPNG)
	}
	if got := TiffOrientation([]byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0}); got != 6 {
		t.Fatalf("TiffOrientation = %d, want 6", got)
	}
	var nilPlan *ImagePlan
	if _, err := nilPlan.Step(0, nil); err == nil {
		t.Fatalf("expected error on nil ImagePlan")
	}
	p := NewImagePlan(int32(imageparse.ModeInspect), 0, 13)
	res, err := p.Step(0, []byte("GIF89a\x04\x00\x03\x00\x00\x00\x00"))
	if err != nil {
		t.Fatalf("Step error: %v", err)
	}
	if len(res) < 16 || res[0] != imageparse.StatusDone || res[1] != byte(imageparse.FormatGIF) || res[3] != 1 {
		t.Fatalf("unexpected inspect response: %x", res)
	}
}
