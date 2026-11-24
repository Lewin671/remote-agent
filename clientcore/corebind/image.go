package corebind

import (
	"errors"

	"github.com/Lewin671/remote-agent/clientcore/domain/imageparse"
)

var errNilImagePlan = errors.New("corebind:nil_image_plan")

// SniffImageFormat returns the imageparse.Format enum code from header bytes (first <= 256 bytes).
func SniffImageFormat(header []byte) int32 {
	return int32(imageparse.SniffFormat(header))
}

// TiffOrientation extracts EXIF orientation (1..8) from a TIFF/EXIF block; returns 1 when absent or invalid.
func TiffOrientation(tiff []byte) int32 {
	return imageparse.ParseTiffOrientation(tiff)
}

// ImagePlanMaxSteps returns the upper bound on Step iterations shells should enforce for totalLen bytes.
func ImagePlanMaxSteps(totalLen int64) int64 {
	return imageparse.MaxPlanSteps(totalLen)
}

// ImagePlan holds a sans-IO image container parser / strip-planner behind a gomobile-safe handle.
type ImagePlan struct {
	inner *imageparse.Plan
}

// NewImagePlan creates a new image container inspection or metadata-stripping plan.
func NewImagePlan(mode int32, expectedFormat int32, totalLen int64) *ImagePlan {
	return &ImagePlan{
		inner: imageparse.NewPlan(imageparse.Mode(mode), imageparse.Format(expectedFormat), totalLen),
	}
}

// Step feeds a small byte slice read at offset (or offset < 0 for EOF from a skip primitive)
// and returns the packed response bytes.
func (p *ImagePlan) Step(offset int64, buf []byte) ([]byte, error) {
	if p == nil || p.inner == nil {
		return nil, errNilImagePlan
	}
	return p.inner.Step(offset, buf)
}
