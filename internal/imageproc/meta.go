package imageproc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Orientation returns the EXIF orientation (1 to 8) of a JPEG, 1 if it
// has none.
func Orientation(b []byte) int {
	exif := jpegSegment(b, 0xE1, []byte("Exif\x00\x00"))
	if exif == nil {
		return 1
	}
	t := exif[6:]
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[ifd:]))
	for i := 0; i < n; i++ {
		e := ifd + 2 + 12*i
		if e+12 > len(t) {
			break
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if o := int(bo.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// jpegSegment returns the payload of the first segment with marker m whose
// payload starts with prefix, or nil.
func jpegSegment(b []byte, m byte, prefix []byte) []byte {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return nil
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return nil // image data starts: no more metadata
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return nil
		}
		payload := b[i+4 : i+2+n]
		if marker == m && bytes.HasPrefix(payload, prefix) {
			return payload
		}
		i += 2 + n
	}
	return nil
}

// orient turns img upright for an EXIF orientation.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	src := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			si := src.PixOffset(x, y)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// StripMetadata removes metadata that may reveal where and when a photo
// was taken (EXIF, XMP, text chunks) from an image a client sent, keeping
// its pixels. A JPEG whose EXIF turns it is re-encoded upright instead, at
// high quality, since dropping the orientation alone would leave it on
// its side. GIFs are left as they are.
func StripMetadata(b []byte) ([]byte, error) {
	switch Sniff(b) {
	case pb.ImageFormat_IMAGE_FORMAT_JPEG:
		if Orientation(b) != 1 {
			img, _, err := Decode(b, DefaultLimits)
			if err != nil {
				return nil, err
			}
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		}
		return stripJPEG(b)
	case pb.ImageFormat_IMAGE_FORMAT_PNG:
		return stripPNG(b)
	case pb.ImageFormat_IMAGE_FORMAT_WEBP:
		return stripWebP(b)
	case pb.ImageFormat_IMAGE_FORMAT_GIF:
		return b, nil
	}
	return nil, ErrFormat
}

var errCorrupt = errors.New("imageproc: malformed image")

// stripJPEG drops APP1 (EXIF, XMP), APP3 to APP13, APP15 and comments,
// and APP2 unless it is an ICC profile; JFIF, ICC and Adobe segments stay.
func stripJPEG(b []byte) ([]byte, error) {
	out := append([]byte(nil), b[:2]...)
	i := 2
	for {
		if i+4 > len(b) || b[i] != 0xFF {
			return nil, errCorrupt
		}
		marker := b[i+1]
		if marker == 0xDA {
			return append(out, b[i:]...), nil
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return nil, errCorrupt
		}
		seg := b[i : i+2+n]
		drop := marker == 0xFE || marker == 0xE1 || (marker >= 0xE3 && marker <= 0xED) || marker == 0xEF ||
			(marker == 0xE2 && !bytes.HasPrefix(seg[4:], []byte("ICC_PROFILE\x00")))
		if !drop {
			out = append(out, seg...)
		}
		i += 2 + n
	}
}

// stripPNG drops text, EXIF and time chunks.
func stripPNG(b []byte) ([]byte, error) {
	out := append([]byte(nil), b[:8]...)
	i := 8
	for i < len(b) {
		if i+12 > len(b) {
			return nil, errCorrupt
		}
		n := int(binary.BigEndian.Uint32(b[i:]))
		if n < 0 || i+12+n > len(b) {
			return nil, errCorrupt
		}
		typ := string(b[i+4 : i+8])
		switch typ {
		case "tEXt", "zTXt", "iTXt", "eXIf", "tIME":
		default:
			out = append(out, b[i:i+12+n]...)
		}
		i += 12 + n
		if typ == "IEND" {
			break
		}
	}
	return out, nil
}

// stripWebP drops the EXIF and XMP chunks of an extended WebP and clears
// their flags.
func stripWebP(b []byte) ([]byte, error) {
	if len(b) < 12 {
		return nil, errCorrupt
	}
	out := append([]byte(nil), b[:12]...)
	i := 12
	for i < len(b) {
		if i+8 > len(b) {
			return nil, errCorrupt
		}
		typ := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4:]))
		size := 8 + n + n%2
		if n < 0 || i+8+n > len(b) {
			return nil, errCorrupt
		}
		end := min(i+size, len(b))
		switch typ {
		case "EXIF", "XMP ":
		case "VP8X":
			c := append([]byte(nil), b[i:end]...)
			if len(c) > 8 {
				c[8] &^= 0x08 | 0x04
			}
			out = append(out, c...)
		default:
			out = append(out, b[i:end]...)
		}
		i = end
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out, nil
}
