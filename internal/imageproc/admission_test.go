package imageproc

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
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

func TestImagePermitWaitRespectsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := Acquire(), Acquire()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			p, err := AcquireContext(ctx)
			if p != nil {
				p.Release()
			}
			done <- err
		}()
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			if err != context.DeadlineExceeded {
				t.Errorf("permit wait=%v", err)
			}
		default:
			t.Error("expired permit waiter stayed blocked")
		}
		first.Release()
		second.Release()
	})
}
