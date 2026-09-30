// Package imageproc turns images found in a CLI's record into what
// clients show: a small preview embedded in events and a display copy
// uploaded on its own (architecture §17). It also checks and cleans images
// clients upload before they are pasted into a CLI.
//
// Everything is decoded under limits (bytes, pixels, one frame of an
// animation) and at most a few images are processed at a time, so that a
// large or hostile image cannot take the daemon's memory. Output is JPEG,
// or PNG for images with transparency; WebP is only read.
package imageproc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Limits bound what is decoded.
type Limits struct {
	// MaxBytes bounds the encoded image.
	MaxBytes int64
	// MaxPixels bounds width times height.
	MaxPixels int64
}

// DefaultLimits cover the originals CLIs record and clients send (up to
// 20 MB) with room to spare.
var DefaultLimits = Limits{MaxBytes: 48 << 20, MaxPixels: 60_000_000}

// Sizes of what clients get (architecture §17).
const (
	PreviewSide = 320
	DisplaySide = 2048
)

// Errors. They never include content.
var (
	ErrFormat   = errors.New("imageproc: not a PNG, JPEG, WebP or GIF image")
	ErrTooLarge = errors.New("imageproc: image over the size limits")
	ErrNoFit    = errors.New("imageproc: the display copy does not fit")
)

// sem bounds how many images are decoded at once, across sessions.
var sem = make(chan struct{}, 2)

// Sniff tells an image's format by its first bytes.
func Sniff(b []byte) pb.ImageFormat {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return pb.ImageFormat_IMAGE_FORMAT_PNG
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return pb.ImageFormat_IMAGE_FORMAT_JPEG
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return pb.ImageFormat_IMAGE_FORMAT_WEBP
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return pb.ImageFormat_IMAGE_FORMAT_GIF
	}
	return pb.ImageFormat_IMAGE_FORMAT_UNSPECIFIED
}

// FormatOfMediaType maps a MIME type such as "image/png".
func FormatOfMediaType(mt string) pb.ImageFormat {
	switch mt {
	case "image/png":
		return pb.ImageFormat_IMAGE_FORMAT_PNG
	case "image/jpeg", "image/jpg":
		return pb.ImageFormat_IMAGE_FORMAT_JPEG
	case "image/webp":
		return pb.ImageFormat_IMAGE_FORMAT_WEBP
	case "image/gif":
		return pb.ImageFormat_IMAGE_FORMAT_GIF
	}
	return pb.ImageFormat_IMAGE_FORMAT_UNSPECIFIED
}

// Ext is a file extension for the format, without the dot.
func Ext(f pb.ImageFormat) string {
	switch f {
	case pb.ImageFormat_IMAGE_FORMAT_PNG:
		return "png"
	case pb.ImageFormat_IMAGE_FORMAT_JPEG:
		return "jpg"
	case pb.ImageFormat_IMAGE_FORMAT_WEBP:
		return "webp"
	case pb.ImageFormat_IMAGE_FORMAT_GIF:
		return "gif"
	}
	return ""
}

// Config reads an image's format and size without decoding it.
func Config(b []byte) (pb.ImageFormat, int, int, error) {
	f := Sniff(b)
	var cfg image.Config
	var err error
	r := bytes.NewReader(b)
	switch f {
	case pb.ImageFormat_IMAGE_FORMAT_PNG:
		cfg, err = png.DecodeConfig(r)
	case pb.ImageFormat_IMAGE_FORMAT_JPEG:
		cfg, err = jpeg.DecodeConfig(r)
	case pb.ImageFormat_IMAGE_FORMAT_WEBP:
		cfg, err = webp.DecodeConfig(r)
	case pb.ImageFormat_IMAGE_FORMAT_GIF:
		cfg, err = gif.DecodeConfig(r)
	default:
		return f, 0, 0, ErrFormat
	}
	if err != nil {
		return f, 0, 0, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	return f, cfg.Width, cfg.Height, nil
}

// Decode decodes one image (the first frame of an animation) within lim,
// turned upright by a JPEG's EXIF orientation.
func Decode(b []byte, lim Limits) (image.Image, pb.ImageFormat, error) {
	if int64(len(b)) > lim.MaxBytes {
		return nil, 0, ErrTooLarge
	}
	f, w, h, err := Config(b)
	if err != nil {
		return nil, f, err
	}
	if w <= 0 || h <= 0 || int64(w)*int64(h) > lim.MaxPixels {
		return nil, f, ErrTooLarge
	}
	sem <- struct{}{}
	defer func() { <-sem }()
	r := bytes.NewReader(b)
	var img image.Image
	switch f {
	case pb.ImageFormat_IMAGE_FORMAT_PNG:
		img, err = png.Decode(r)
	case pb.ImageFormat_IMAGE_FORMAT_JPEG:
		img, err = jpeg.Decode(r)
		if err == nil {
			img = orient(img, Orientation(b))
		}
	case pb.ImageFormat_IMAGE_FORMAT_WEBP:
		img, err = webp.Decode(r)
	case pb.ImageFormat_IMAGE_FORMAT_GIF:
		img, err = gif.Decode(r) // the first frame only
	}
	if err != nil {
		return nil, f, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	return img, f, nil
}

// Encoded is an encoded image.
type Encoded struct {
	Format        pb.ImageFormat
	Width, Height int
	Data          []byte
}

// scale returns img shrunk so its long side is at most side.
func scale(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return img
	}
	if w >= h {
		h, w = max(1, h*side/w), side
	} else {
		w, h = max(1, w*side/h), side
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// opaque reports whether every pixel is fully opaque.
func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

func encode(img image.Image, transparent bool, quality int) (Encoded, error) {
	var buf bytes.Buffer
	b := img.Bounds()
	e := Encoded{Width: b.Dx(), Height: b.Dy()}
	if transparent {
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return e, err
		}
		e.Format = pb.ImageFormat_IMAGE_FORMAT_PNG
	} else {
		if err := jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: quality}); err != nil {
			return e, err
		}
		e.Format = pb.ImageFormat_IMAGE_FORMAT_JPEG
	}
	e.Data = buf.Bytes()
	return e, nil
}

// flatten draws img over white, so that an opaque image with an alpha
// channel encodes as it looks.
func flatten(img image.Image) image.Image {
	if _, ok := img.(*image.YCbCr); ok {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	return dst
}

// Preview makes the small copy embedded in events: long side at most
// PreviewSide, about 20 KB.
func Preview(img image.Image) (Encoded, error) {
	small := scale(img, PreviewSide)
	transparent := !opaque(small)
	e, err := encode(small, transparent, 70)
	if err == nil && !transparent && len(e.Data) > 40<<10 {
		e, err = encode(small, false, 45)
	}
	return e, err
}

// Display makes the copy clients open: long side at most DisplaySide,
// shrunk and compressed step by step until fits accepts it (the caller
// checks the sealed envelope's size).
func Display(img image.Image, fits func(Encoded) bool) (Encoded, error) {
	transparent := !opaque(img)
	side := DisplaySide
	for side >= 256 {
		scaled := scale(img, side)
		qualities := []int{85, 75, 65, 55}
		if transparent {
			qualities = []int{0}
		}
		for _, q := range qualities {
			e, err := encode(scaled, transparent, q)
			if err != nil {
				return Encoded{}, err
			}
			if fits(e) {
				return e, nil
			}
		}
		side = side * 4 / 5
	}
	return Encoded{}, ErrNoFit
}
