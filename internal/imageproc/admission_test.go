package imageproc

import (
	"testing"
	"testing/synctest"
)

func TestAdmissionCoversDecodedImageAndNestedMetadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := Acquire(), Acquire()
		defer second.Release()
		data := exifPNG(t)
		img, _, err := first.Decode(data, DefaultLimits)
		if err != nil {
			first.Release()
			t.Fatal(err)
		}
		// Cleaning an oriented image decodes again under the same admission.
		// No third permit is available, so accidental nested acquisition deadlocks.
		clean, err := first.StripMetadata(data)
		if err != nil || Orientation(clean) != 1 {
			first.Release()
			t.Fatalf("clean: %v", err)
		}
		admitted := make(chan struct{})
		go func() { p := Acquire(); close(admitted); p.Release() }()
		synctest.Wait()
		select {
		case <-admitted:
			first.Release()
			t.Fatal("Decode released the caller's admission")
		default:
		}
		if img.Bounds().Dx() != 20 || img.Bounds().Dy() != 40 {
			first.Release()
			t.Fatal("lost oriented dimensions")
		}
		first.Release()
		<-admitted
	})
}
