package imageproc

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"sync"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func noise(w, h int, alpha bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.IntN(256))
		if i%4 == 3 && !alpha {
			img.Pix[i] = 255
		}
	}
	return img
}

func encPNG(t *testing.T, img image.Image) []byte {
	var buf bytes.Buffer
	// These fixtures exercise decoding, resizing and metadata handling, not
	// PNG's compression search. Keep every pixel and the oversized payload.
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// exifJPEG is a 40x20 JPEG with an EXIF segment saying orientation 6 (turn
// right) and carrying a marker standing for GPS data.
func exifJPEG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 20; x++ { // left half red, right half blue
		for y := 0; y < 20; y++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
			img.Set(x+20, y, color.RGBA{0, 0, 255, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	tiff = binary.BigEndian.AppendUint16(tiff, 1)
	tiff = append(tiff, 0x01, 0x12, 0x00, 0x03, 0, 0, 0, 1, 0x00, 0x06, 0, 0)
	tiff = append(tiff, 0, 0, 0, 0)
	tiff = append(tiff, "GPS-LATITUDE-31.2"...)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	seg = append(seg, payload...)
	b := buf.Bytes()
	return append(append(append([]byte(nil), b[:2]...), seg...), b[2:]...)
}

func TestOrientationAndStripJPEG(t *testing.T) {
	permit := Acquire()
	defer permit.Release()
	b := exifJPEG(t)
	if o := Orientation(b); o != 6 {
		t.Fatalf("orientation %d", o)
	}
	img, f, err := permit.Decode(b, DefaultLimits)
	if err != nil || f != pb.ImageFormat_IMAGE_FORMAT_JPEG {
		t.Fatal(err)
	}
	if bd := img.Bounds(); bd.Dx() != 20 || bd.Dy() != 40 {
		t.Fatalf("not turned: %v", bd)
	}
	// Turned right, the red left half ends up on top.
	if r, _, bl, _ := img.At(10, 5).RGBA(); r < 0x8000 || bl > 0x8000 {
		t.Fatal("turned the wrong way")
	}
	clean, err := permit.StripMetadata(b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(clean, []byte("GPS-LATITUDE")) || bytes.Contains(clean, []byte("Exif")) {
		t.Fatal("metadata left")
	}
	if _, w, h, _ := Config(clean); w != 20 || h != 40 {
		t.Fatalf("stripped copy is %dx%d, want it upright", w, h)
	}

	// Without a turn, the pixels are kept as they are.
	plain := bytes.Replace(b, []byte{0x00, 0x06, 0, 0, 0, 0, 0, 0}, []byte{0x00, 0x01, 0, 0, 0, 0, 0, 0}, 1)
	if Orientation(plain) != 1 {
		t.Fatal("orientation not reset in the test image")
	}
	clean, err = permit.StripMetadata(plain)
	if err != nil || bytes.Contains(clean, []byte("GPS-LATITUDE")) {
		t.Fatalf("strip: %v", err)
	}
	if !bytes.Equal(clean[len(clean)-100:], plain[len(plain)-100:]) {
		t.Fatal("image data changed")
	}
}

func pngChunk(typ string, data []byte) []byte {
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(c, typ...)
	c = append(c, data...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(append([]byte(typ), data...)))
}

func TestStripPNGAndWebP(t *testing.T) {
	b := encPNG(t, noise(8, 8, false))
	withText := append(append(append([]byte(nil), b[:33]...), pngChunk("tEXt", []byte("GPS\x00here"))...), b[33:]...)
	withText = append(withText[:len(withText)-12], append(pngChunk("eXIf", []byte("MMxx")), withText[len(withText)-12:]...)...)
	clean, err := StripMetadata(withText)
	if err != nil || bytes.Contains(clean, []byte("here")) || bytes.Contains(clean, []byte("eXIf")) {
		t.Fatalf("png: %v", err)
	}
	if _, _, _, err := Config(clean); err != nil {
		t.Fatal(err)
	}

	// A made-up extended WebP: VP8X with the EXIF flag, an EXIF chunk.
	vp8x := append([]byte("VP8X"), 10, 0, 0, 0, 0x08, 0, 0, 0, 7, 0, 0, 7, 0, 0)
	exif := append([]byte("EXIF"), 4, 0, 0, 0, 'G', 'P', 'S', '!')
	body := append(append([]byte("WEBP"), vp8x...), exif...)
	webp := append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...)
	webp = append(webp, body...)
	clean, err = StripMetadata(webp)
	if err != nil || bytes.Contains(clean, []byte("GPS!")) || clean[20]&0x08 != 0 || int(binary.LittleEndian.Uint32(clean[4:])) != len(clean)-8 {
		t.Fatalf("webp: %v %x", err, clean)
	}
}

func TestPreviewAndDisplay(t *testing.T) {
	permit := Acquire()
	defer permit.Release()
	big := encPNG(t, noise(3000, 2000, false))
	if len(big) < 3<<20 {
		t.Fatalf("test image only %d bytes", len(big))
	}
	img, _, err := permit.Decode(big, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Preview(img)
	if err != nil || p.Format != pb.ImageFormat_IMAGE_FORMAT_JPEG || p.Width != 320 || p.Height != 213 || len(p.Data) > 40<<10 {
		t.Fatalf("preview %v %dx%d %d bytes", err, p.Width, p.Height, len(p.Data))
	}
	const limit = 1<<20 - 4096
	d, err := Display(img, func(e Encoded) bool { return len(e.Data) <= limit })
	if err != nil || d.Width > DisplaySide || len(d.Data) > limit || d.Format != pb.ImageFormat_IMAGE_FORMAT_JPEG {
		t.Fatalf("display %v %dx%d %d bytes", err, d.Width, d.Height, len(d.Data))
	}

	// Transparency keeps PNG, shrinking until it fits.
	img, _, _ = permit.Decode(encPNG(t, noise(1500, 1500, true)), DefaultLimits)
	d, err = Display(img, func(e Encoded) bool { return len(e.Data) <= limit })
	if err != nil || d.Format != pb.ImageFormat_IMAGE_FORMAT_PNG || len(d.Data) > limit {
		t.Fatalf("transparent display %v %v %d", err, d.Format, len(d.Data))
	}
	if p, _ := Preview(img); p.Format != pb.ImageFormat_IMAGE_FORMAT_PNG {
		t.Fatal("transparent preview not PNG")
	}
}

func TestLimits(t *testing.T) {
	permit := Acquire()
	defer permit.Release()
	b := encPNG(t, noise(100, 100, false))
	if _, _, err := permit.Decode(b, Limits{MaxBytes: 10, MaxPixels: 1 << 30}); err != ErrTooLarge {
		t.Fatalf("bytes: %v", err)
	}
	if _, _, err := permit.Decode(b, Limits{MaxBytes: 1 << 30, MaxPixels: 9999}); err != ErrTooLarge {
		t.Fatalf("pixels: %v", err)
	}
	if _, _, err := permit.Decode([]byte("<svg/>"), DefaultLimits); err == nil {
		t.Fatal("svg accepted")
	}
	if _, _, err := permit.Decode(b[:len(b)/2], DefaultLimits); err == nil {
		t.Fatal("truncated image accepted")
	}
}

// G9: a GIF's comment and application extensions (XMP) go; frames and the
// looping extension stay.
func TestStripGIF(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	var buf bytes.Buffer
	g := &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), pal), image.NewPaletted(image.Rect(0, 0, 4, 4), pal)}, Delay: []int{1, 1}}
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// After the header, screen descriptor and global color table.
	at := 13
	if b[10]&0x80 != 0 {
		at += 3 << (b[10]&7 + 1)
	}
	comment := append([]byte{0x21, 0xFE, 8}, "GPS here"...)
	comment = append(comment, 0)
	xmp := append([]byte{0x21, 0xFF, 11}, "XMP DataXMP"...)
	xmp = append(xmp, 5, 'w', 'h', 'e', 'r', 'e', 0)
	withMeta := append(append(append(append([]byte(nil), b[:at]...), comment...), xmp...), b[at:]...)
	clean, err := StripMetadata(withMeta)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(clean, []byte("GPS here")) || bytes.Contains(clean, []byte("XMP DataXMP")) {
		t.Fatal("metadata left")
	}
	if !bytes.Contains(clean, []byte("NETSCAPE2.0")) {
		t.Fatal("looping extension dropped")
	}
	got, err := gif.DecodeAll(bytes.NewReader(clean))
	if err != nil || len(got.Image) != 2 {
		t.Fatalf("frames: %v", err)
	}
}

// G11: at most two images are held decoded at a time, through encoding.
func TestProcessIsBounded(t *testing.T) {
	b := encPNG(t, noise(200, 200, false))
	var mu sync.Mutex
	active, most := 0, 0
	release := make(chan struct{})
	entered := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			Process(b, DefaultLimits, func(Encoded) bool {
				mu.Lock()
				active++
				most = max(most, active)
				mu.Unlock()
				if first {
					first = false
					entered <- struct{}{}
				}
				<-release
				mu.Lock()
				active--
				mu.Unlock()
				return true
			})
		}()
	}
	<-entered
	<-entered
	close(release)
	wg.Wait()
	if most != 2 {
		t.Fatalf("%d images encoded at once", most)
	}
}

// seg is a JPEG marker segment with its length.
func seg(marker byte, payload []byte) []byte {
	b := []byte{0xFF, marker}
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload)+2))
	return append(b, payload...)
}

// Metadata after the first scan goes too: a comment before EOI, an APP1
// between scans, anything after EOI. Scans, tables and restart markers of
// a multi-scan (progressive-like) stream stay as they are.
func TestStripJPEGAfterScans(t *testing.T) {
	var buf bytes.Buffer
	jpeg.Encode(&buf, noise(64, 48, false), &jpeg.Options{Quality: 80})
	b := buf.Bytes()
	eoi := len(b) - 2
	// A second scan: a table, a scan header and entropy data with a
	// stuffed 0xFF and a restart marker, as progressive JPEGs have.
	scan2 := append(seg(0xC4, []byte{0x10, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5}), seg(0xDA, []byte{1, 1, 0x11, 1, 63, 0x10})...)
	scan2 = append(scan2, 0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD3, 0x56)
	gps := seg(0xE1, append([]byte("Exif\x00\x00"), "GPS-AFTER-SCAN"...))
	com := seg(0xFE, []byte("GPS-IN-COMMENT"))
	in := append(append(append(append(append([]byte(nil), b[:eoi]...), gps...), scan2...), com...), 0xFF, 0xD9)
	in = append(in, "TRAILING-GPS"...)
	clean, err := StripMetadata(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"GPS-AFTER-SCAN", "GPS-IN-COMMENT", "TRAILING-GPS"} {
		if bytes.Contains(clean, []byte(s)) {
			t.Fatalf("%s left", s)
		}
	}
	want := append(append(append([]byte(nil), b[:eoi]...), scan2...), 0xFF, 0xD9)
	if !bytes.Equal(clean, want) {
		t.Fatal("scans or tables changed")
	}
	// A plain JPEG comes out unchanged and decodable.
	if clean, _ := StripMetadata(b); !bytes.Equal(clean, b) {
		t.Fatal("clean JPEG changed")
	}
}

// V4: a restart marker after fill bytes (FF FF D0) is part of the scan.
func TestStripJPEGFilledRestart(t *testing.T) {
	var buf bytes.Buffer
	jpeg.Encode(&buf, noise(16, 16, false), &jpeg.Options{Quality: 80})
	b := buf.Bytes()
	eoi := len(b) - 2
	scan := append(seg(0xDA, []byte{1, 1, 0x11, 1, 63, 0x10}), 0x12, 0xFF, 0xFF, 0xD0, 0x56, 0xFF, 0x00, 0x78)
	in := append(append(append([]byte(nil), b[:eoi]...), scan...), 0xFF, 0xD9)
	clean, err := StripMetadata(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(clean, in) {
		t.Fatal("scan changed")
	}
}

// bits writes a VP8L bitstream, least significant bit first.
type bits struct {
	b []byte
	n int
}

func (w *bits) put(v uint32, n int) {
	for i := range n {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		w.b[w.n/8] |= byte(v>>i&1) << (w.n % 8)
		w.n++
	}
}

func riffChunk(typ string, data []byte) []byte {
	c := binary.LittleEndian.AppendUint32([]byte(typ), uint32(len(data)))
	c = append(c, data...)
	if len(data)%2 == 1 {
		c = append(c, 0)
	}
	return c
}

// exifWebP is a 2x1 lossless WebP, red then blue, in the extended format
// with an EXIF chunk saying orientation o. There is no encoder to make
// one with, so its few bits are written out here.
func exifWebP(o byte, exifHeader bool) []byte {
	var w bits
	w.put(0x2f, 8)   // signature
	w.put(2-1, 14)   // width
	w.put(1-1, 14)   // height
	w.put(0, 1+3)    // no alpha, version 0
	w.put(0, 1+1+1)  // no transform, no colour cache, one group of codes
	w.put(0b0001, 4) // green: always 0
	w.put(0b111, 3)  // red: 0 or 255, a bit each
	w.put(0, 8)      //
	w.put(255, 8)    //
	w.put(0b111, 3)  // blue: the same
	w.put(0, 8)      //
	w.put(255, 8)    //
	w.put(0b101, 3)  // alpha: always 255
	w.put(255, 8)    //
	w.put(0b0001, 4) // distance: unused
	w.put(0b01, 2)   // first pixel: red 255, blue 0
	w.put(0b10, 2)   // second pixel: red 0, blue 255
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00")
	tiff = append(tiff, o, 0, 0, 0, 0, 0, 0)
	if exifHeader {
		tiff = append([]byte("Exif\x00\x00"), tiff...)
	}
	body := []byte("WEBP")
	body = append(body, riffChunk("VP8X", []byte{0x08, 0, 0, 0, 2 - 1, 0, 0, 1 - 1, 0, 0})...)
	body = append(body, riffChunk("VP8L", w.b)...)
	body = append(body, riffChunk("EXIF", tiff)...)
	return append(binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body))), body...)
}

// exifPNG is the 40x20 picture of exifJPEG as a PNG whose eXIf chunk says
// orientation 6.
func exifPNG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 20; x++ {
		for y := 0; y < 20; y++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
			img.Set(x+20, y, color.RGBA{0, 0, 255, 255})
		}
	}
	b := encPNG(t, img)
	tiff := []byte("II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	return append(append(append([]byte(nil), b[:33]...), pngChunk("eXIf", tiff)...), b[33:]...)
}

// The CLIs record pasted images as they are. A viewer shows a PNG or a WebP
// turned by its orientation tag as much as a JPEG, and so do the clients'
// own decoders; the copies made here carry no tag, so the pixels must be
// turned.
func TestOrientationOfPNGAndWebP(t *testing.T) {
	permit := Acquire()
	defer permit.Release()
	red := func(img image.Image, x, y int) bool {
		r, _, b, _ := img.At(x, y).RGBA()
		return r > 0x8000 && b < 0x8000
	}
	b := exifPNG(t)
	if o := Orientation(b); o != 6 {
		t.Fatalf("png orientation %d", o)
	}
	img, f, err := permit.Decode(b, DefaultLimits)
	if err != nil || f != pb.ImageFormat_IMAGE_FORMAT_PNG {
		t.Fatal(err, f)
	}
	// Turned right, the red left half ends up on top.
	if bd := img.Bounds(); bd.Dx() != 20 || bd.Dy() != 40 || !red(img, 10, 5) || red(img, 10, 35) {
		t.Fatalf("png not turned upright: %v", bd)
	}
	p, err := permit.Process(b, DefaultLimits, func(Encoded) bool { return true })
	if err != nil || p.Width != 20 || p.Height != 40 || p.Preview.Width != 20 || p.Display.Height != 40 {
		t.Fatalf("processed png: %v %+v", err, p)
	}

	for _, header := range []bool{false, true} {
		b = exifWebP(6, header)
		if o := Orientation(b); o != 6 {
			t.Fatalf("webp orientation %d (Exif header %v)", o, header)
		}
		img, f, err = permit.Decode(b, DefaultLimits)
		if err != nil || f != pb.ImageFormat_IMAGE_FORMAT_WEBP {
			t.Fatal(err, f)
		}
		if bd := img.Bounds(); bd.Dx() != 1 || bd.Dy() != 2 || !red(img, 0, 0) || red(img, 0, 1) {
			t.Fatalf("webp not turned upright: %v", bd)
		}
	}
	// Stripping the tag alone would leave them on their side.
	for name, b := range map[string][]byte{"png": exifPNG(t), "webp": exifWebP(6, false)} {
		clean, err := permit.StripMetadata(b)
		if err != nil || Orientation(clean) != 1 || bytes.Contains(clean, []byte("eXIf")) {
			t.Fatalf("%s: strip: %v", name, err)
		}
		_, w, h, _ := Config(b)
		if f, cw, ch, err := Config(clean); err != nil || f != pb.ImageFormat_IMAGE_FORMAT_PNG || cw != h || ch != w {
			t.Fatalf("%s: stripped copy is %v %dx%d, want an upright PNG: %v", name, f, cw, ch, err)
		}
	}
	// Orientation 1 and a WebP without EXIF are left alone.
	img, _, err = permit.Decode(exifWebP(1, false), DefaultLimits)
	if bd := img.Bounds(); err != nil || bd.Dx() != 2 || bd.Dy() != 1 || !red(img, 0, 0) || red(img, 1, 0) {
		t.Fatalf("upright webp: %v %v", err, bd)
	}
	if Orientation(encPNG(t, noise(8, 8, false))) != 1 {
		t.Fatal("a PNG without eXIf has an orientation")
	}
}
