package imageparse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type vectorFile struct {
	Cases []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name                   string   `json:"name"`
	InputBase64            string   `json:"inputBase64"`
	Format                 string   `json:"format"`
	Orientation            int32    `json:"orientation"`
	Width                  *uint32  `json:"width"`
	Height                 *uint32  `json:"height"`
	ExpectedStrippedBase64 string   `json:"expectedStrippedBase64"`
	StripError             string   `json:"stripError"`
	ForbiddenSubstrings    []string `json:"forbiddenSubstrings"`
}

func formatName(f Format) string {
	switch f {
	case FormatJPEG:
		return "jpeg"
	case FormatPNG:
		return "png"
	case FormatWebP:
		return "webp"
	case FormatGIF:
		return "gif"
	case FormatHEIC:
		return "heic"
	case FormatAVIF:
		return "avif"
	default:
		return "unknown"
	}
}

func formatFromName(s string) Format {
	switch s {
	case "jpeg":
		return FormatJPEG
	case "png":
		return FormatPNG
	case "webp":
		return FormatWebP
	case "gif":
		return FormatGIF
	case "heic":
		return FormatHEIC
	case "avif":
		return FormatAVIF
	default:
		return FormatUnknown
	}
}

func scanJpegEntropyGo(b []byte, pos int) int {
	for i := pos; i+1 < len(b); i++ {
		if b[i] != 0xff {
			continue
		}
		k := i
		for k+1 < len(b) && b[k+1] == 0xff {
			k++
		}
		if k+1 >= len(b) {
			return -1
		}
		next := b[k+1]
		if next == 0x00 || (next >= 0xd0 && next <= 0xd7) {
			i = k + 1
			continue
		}
		return i
	}
	return -1
}

func skipGifSubBlocksGo(b []byte, pos int) int {
	p := pos
	for {
		if p >= len(b) {
			return -1
		}
		n := int(b[p])
		p += 1 + n
		if p > len(b) {
			return -1
		}
		if n == 0 {
			return p
		}
	}
}

func runPlanWithWindow(t *testing.T, data []byte, mode Mode, expected Format, maxWin int) (done []byte, errCode byte, errDetail string) {
	t.Helper()
	p := NewPlan(mode, expected, int64(len(data)))
	off := int64(0)
	initEnd := len(data)
	if initEnd > maxWin {
		initEnd = maxWin
	}
	slice := data[:initEnd]
	for steps := 0; steps < 100000; steps++ {
		res, err := p.Step(off, slice)
		if err != nil {
			t.Fatalf("Step error: %v", err)
		}
		switch res[0] {
		case StatusDone:
			return res, 0, ""
		case StatusError:
			return nil, res[1], string(res[2:])
		case StatusNeedRead:
			uoff := readU64le(res, 1)
			need := uint64(u32le(res, 9))
			if int(need) > maxWin && need > 65536 {
				// Respect large requested EXIF/segment reads, otherwise cap to maxWin only when larger than needed.
			}
			end := uoff + need
			if end > uint64(len(data)) {
				end = uint64(len(data))
			}
			off = int64(uoff)
			slice = data[uoff:end]
		case StatusNeedScanJpegEntropy:
			from := int(readU64le(res, 1))
			found := scanJpegEntropyGo(data, from)
			off = int64(found)
			if found >= 0 {
				end := found + maxWin
				if end > len(data) {
					end = len(data)
				}
				slice = data[found:end]
			} else {
				slice = nil
			}
		case StatusNeedSkipGifSubBlocks:
			from := int(readU64le(res, 1))
			endPos := skipGifSubBlocksGo(data, from)
			off = int64(endPos)
			if endPos >= 0 {
				end := endPos + maxWin
				if end > len(data) {
					end = len(data)
				}
				slice = data[endPos:end]
			} else {
				slice = nil
			}
		default:
			t.Fatalf("unexpected status %d", res[0])
		}
	}
	t.Fatalf("plan exceeded step budget")
	return nil, 0, ""
}

func spliceDone(src, done []byte) []byte {
	count := int(u32le(done, 12))
	at := 16
	var out []byte
	for i := 0; i < count; i++ {
		kind := done[at]
		at++
		if kind == OpKeepRange {
			s := readU64le(done, at)
			e := readU64le(done, at+8)
			out = append(out, src[s:e]...)
			at += 16
		} else {
			n := int(u32le(done, at))
			out = append(out, done[at+4:at+4+n]...)
			at += 4 + n
		}
	}
	return out
}

func readU64le(b []byte, at int) uint64 {
	return uint64(u32le(b, at)) | uint64(u32le(b, at+4))<<32
}

func TestSharedImageVectors(t *testing.T) {
	vecPath := filepath.Join("..", "..", "..", "testdata", "images", "vectors.json")
	raw, err := os.ReadFile(vecPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vf vectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	if len(vf.Cases) < 30 {
		t.Fatalf("expected >= 30 vector cases, got %d", len(vf.Cases))
	}

	// Run every case with both normal 64 KB initial window and tiny 19-byte window to stress NeedRead boundary splits.
	for _, maxWin := range []int{64 * 1024, 19} {
		for _, tc := range vf.Cases {
			input, err := base64.StdEncoding.DecodeString(tc.InputBase64)
			if err != nil {
				t.Fatalf("%s: decode input: %v", tc.Name, err)
			}

			// 1. SniffFormat (<= 256 byte header)
			head := input
			if len(head) > 256 {
				head = head[:256]
			}
			gotFmt := formatName(SniffFormat(head))
			if gotFmt != tc.Format {
				t.Errorf("%s (win=%d): SniffFormat = %q, want %q", tc.Name, maxWin, gotFmt, tc.Format)
			}

			// 2. ModeInspect (format, orientation, dimensions)
			done, errCode, errDetail := runPlanWithWindow(t, input, ModeInspect, FormatUnknown, maxWin)
			if errCode != 0 {
				t.Errorf("%s (win=%d): ModeInspect unexpected error %d: %s", tc.Name, maxWin, errCode, errDetail)
				continue
			}
			if got := formatName(Format(done[1])); got != tc.Format {
				t.Errorf("%s (win=%d): Inspect format = %q, want %q", tc.Name, maxWin, got, tc.Format)
			}
			if got := int32(done[2]); got != tc.Orientation {
				t.Errorf("%s (win=%d): Inspect orientation = %d, want %d", tc.Name, maxWin, got, tc.Orientation)
			}
			if tc.Width != nil && tc.Height != nil {
				if done[3] == 0 {
					t.Errorf("%s (win=%d): expected size %dx%d, got hasSize=0", tc.Name, maxWin, *tc.Width, *tc.Height)
				} else {
					w, h := u32le(done, 4), u32le(done, 8)
					if w != *tc.Width || h != *tc.Height {
						t.Errorf("%s (win=%d): size = %dx%d, want %dx%d", tc.Name, maxWin, w, h, *tc.Width, *tc.Height)
					}
				}
			}

			// 3. ModeStrip
			expectedFmt := formatFromName(tc.Format)
			if tc.StripError != "" {
				_, code, detail := runPlanWithWindow(t, input, ModeStrip, expectedFmt, maxWin)
				if code == 0 {
					t.Errorf("%s (win=%d): expected strip error %q, got success", tc.Name, maxWin, tc.StripError)
				} else if tc.StripError == "unsupported" && code != ErrCodeUnsupported && expectedFmt != FormatUnknown {
					t.Errorf("%s (win=%d): expected ErrCodeUnsupported, got %d (%s)", tc.Name, maxWin, code, detail)
				} else if tc.StripError == "malformed" && code != ErrCodeMalformed {
					t.Errorf("%s (win=%d): expected ErrCodeMalformed, got %d (%s)", tc.Name, maxWin, code, detail)
				}
			} else if tc.ExpectedStrippedBase64 != "" {
				wantStripped, err := base64.StdEncoding.DecodeString(tc.ExpectedStrippedBase64)
				if err != nil {
					t.Fatalf("%s: decode expectedStripped: %v", tc.Name, err)
				}
				stripDone, code, detail := runPlanWithWindow(t, input, ModeStrip, expectedFmt, maxWin)
				if code != 0 {
					t.Errorf("%s (win=%d): strip failed (%d: %s)", tc.Name, maxWin, code, detail)
					continue
				}
				gotStripped := spliceDone(input, stripDone)
				if !bytes.Equal(gotStripped, wantStripped) {
					t.Errorf("%s (win=%d): stripped bytes mismatch (got %d B, want %d B)", tc.Name, maxWin, len(gotStripped), len(wantStripped))
				}
				for _, bad := range tc.ForbiddenSubstrings {
					if bytes.Contains(gotStripped, []byte(bad)) {
						t.Errorf("%s (win=%d): stripped output still contains forbidden substring %q", tc.Name, maxWin, bad)
					}
				}
				// Idempotence: stripping already-stripped output produces identical bytes and orientation=1.
				idempDone, code2, detail2 := runPlanWithWindow(t, gotStripped, ModeStrip, expectedFmt, maxWin)
				if code2 != 0 {
					t.Errorf("%s (win=%d): idempotent strip failed (%d: %s)", tc.Name, maxWin, code2, detail2)
				} else if got2 := spliceDone(gotStripped, idempDone); !bytes.Equal(got2, gotStripped) {
					t.Errorf("%s (win=%d): strip is not idempotent", tc.Name, maxWin)
				}
				inspectStripped, code3, detail3 := runPlanWithWindow(t, gotStripped, ModeInspect, FormatUnknown, maxWin)
				if code3 != 0 {
					t.Errorf("%s (win=%d): inspect stripped failed (%d: %s)", tc.Name, maxWin, code3, detail3)
				} else if inspectStripped[2] != 1 {
					t.Errorf("%s (win=%d): stripped output orientation = %d, want 1", tc.Name, maxWin, inspectStripped[2])
				}
			}
		}
	}
}

func TestParseTiffOrientationEdgeCases(t *testing.T) {
	// Malformed / boundary TIFF blocks never panic and return 1.
	badCases := [][]byte{
		nil,
		{},
		[]byte("II"),
		{0x49, 0x49, 42, 0, 0xff, 0xff, 0xff, 0xff},
		{0x4d, 0x4d, 0, 42, 0, 0, 0, 8, 0, 9},
		[]byte("Exif\x00\x00II"),
	}
	for i, c := range badCases {
		if got := ParseTiffOrientation(c); got != 1 {
			t.Errorf("case %d: got %d, want 1", i, got)
		}
	}
}

func makeTiffOrient(orient byte, totalSize int) []byte {
	if totalSize < 32 {
		totalSize = 32
	}
	b := make([]byte, totalSize)
	copy(b, []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orient, 0, 0})
	return b
}

func makePngChunk(typ string, payload []byte) []byte {
	out := make([]byte, 12+len(payload))
	out[0] = byte(len(payload) >> 24)
	out[1] = byte(len(payload) >> 16)
	out[2] = byte(len(payload) >> 8)
	out[3] = byte(len(payload))
	copy(out[4:8], typ)
	copy(out[8:8+len(payload)], payload)
	// CRC bytes left 0 (container parser does not verify CRC).
	return out
}

func makeRiffChunk(fourcc string, payload []byte) []byte {
	pad := len(payload) & 1
	out := make([]byte, 8+len(payload)+pad)
	copy(out[0:4], fourcc)
	putU32le(out, 4, uint32(len(payload)))
	copy(out[8:], payload)
	return out
}

func makeWebpFile(chunks ...[]byte) []byte {
	bodyLen := 4
	for _, c := range chunks {
		bodyLen += len(c)
	}
	out := make([]byte, 0, 8+bodyLen)
	out = append(out, 'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P')
	putU32le(out, 4, uint32(bodyLen))
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func makeMinimalJpegBody() []byte {
	// SOF0 (16x12) + SOS + 2 bytes entropy + EOI
	return []byte{
		0xff, 0xc0, 0, 11, 8, 0, 12, 0, 16, 1, 1, 0x11, 0,
		0xff, 0xda, 0, 8, 1, 1, 0, 0, 63, 0,
		0x12, 0x34,
		0xff, 0xd9,
	}
}

func TestNoInfiniteLoopRegressions(t *testing.T) {
	// Trigger 1: PNG eXIf chunk > 64 KB (70,000 B and 200,000 B).
	ihdrPayload := []byte{0, 0, 0, 10, 0, 0, 0, 20, 8, 6, 0, 0, 0}
	for _, exifLen := range []int{70_000, 200_000} {
		png := append([]byte{}, pngSignature[:]...)
		png = append(png, makePngChunk("IHDR", ihdrPayload)...)
		png = append(png, makePngChunk("eXIf", makeTiffOrient(6, exifLen))...)
		png = append(png, makePngChunk("IDAT", []byte{1, 2, 3, 4})...)
		png = append(png, makePngChunk("IEND", nil)...)

		done, code, detail := runPlanWithWindow(t, png, ModeInspect, FormatPNG, 64*1024)
		if code != 0 {
			t.Fatalf("PNG eXIf=%d inspect failed: %d %s", exifLen, code, detail)
		}
		if done[2] != 6 {
			t.Fatalf("PNG eXIf=%d orientation = %d, want 6", exifLen, done[2])
		}
		stripDone, code, detail := runPlanWithWindow(t, png, ModeStrip, FormatPNG, 64*1024)
		if code != 0 {
			t.Fatalf("PNG eXIf=%d strip failed: %d %s", exifLen, code, detail)
		}
		stripped := spliceDone(png, stripDone)
		if bytes.Contains(stripped, []byte("eXIf")) {
			t.Fatalf("PNG eXIf=%d was not stripped", exifLen)
		}
	}

	// Trigger 2: WebP EXIF chunk > 64 KB (70,000 B and 200,000 B).
	vp8x := make([]byte, 10)
	vp8x[0] = 0x08
	vp8x[4] = 9  // width 10
	vp8x[7] = 19 // height 20
	vp8l := []byte{0x2f, 0, 0, 0, 0, 0}
	for _, exifLen := range []int{70_000, 200_000} {
		webp := makeWebpFile(
			makeRiffChunk("VP8X", vp8x),
			makeRiffChunk("VP8L", vp8l),
			makeRiffChunk("EXIF", makeTiffOrient(8, exifLen)),
		)
		done, code, detail := runPlanWithWindow(t, webp, ModeInspect, FormatWebP, 64*1024)
		if code != 0 {
			t.Fatalf("WebP EXIF=%d inspect failed: %d %s", exifLen, code, detail)
		}
		if done[2] != 8 {
			t.Fatalf("WebP EXIF=%d orientation = %d, want 8", exifLen, done[2])
		}
		stripDone, code, detail := runPlanWithWindow(t, webp, ModeStrip, FormatWebP, 64*1024)
		if code != 0 {
			t.Fatalf("WebP EXIF=%d strip failed: %d %s", exifLen, code, detail)
		}
		stripped := spliceDone(webp, stripDone)
		if bytes.Contains(stripped, []byte("EXIF")) {
			t.Fatalf("WebP EXIF=%d was not stripped", exifLen)
		}
	}

	// Trigger 3: JPEG with > 64 KB consecutive 0xFF fill bytes (70,000 B and 200,000 B).
	body := makeMinimalJpegBody()
	for _, fillLen := range []int{70_000, 200_000} {
		jpeg := make([]byte, 0, 2+fillLen+len(body))
		jpeg = append(jpeg, 0xff, 0xd8)
		jpeg = append(jpeg, bytes.Repeat([]byte{0xff}, fillLen)...)
		jpeg = append(jpeg, body...)

		done, code, detail := runPlanWithWindow(t, jpeg, ModeInspect, FormatJPEG, 64*1024)
		if code != 0 {
			t.Fatalf("JPEG fill=%d inspect failed: %d %s", fillLen, code, detail)
		}
		if done[3] != 1 || u32le(done, 4) != 16 || u32le(done, 8) != 12 {
			t.Fatalf("JPEG fill=%d size mismatch: hasSize=%d %dx%d", fillLen, done[3], u32le(done, 4), u32le(done, 8))
		}
		stripDone, code, detail := runPlanWithWindow(t, jpeg, ModeStrip, FormatJPEG, 64*1024)
		if code != 0 {
			t.Fatalf("JPEG fill=%d strip failed: %d %s", fillLen, code, detail)
		}
		stripped := spliceDone(jpeg, stripDone)
		want := append([]byte{0xff, 0xd8}, body...)
		if !bytes.Equal(stripped, want) {
			t.Fatalf("JPEG fill=%d stripped len=%d want=%d", fillLen, len(stripped), len(want))
		}
	}

	// Trigger 4: Full-length JPEG APP1 (segLen=65535) preceded by 1 B and 5 B of 0xFF fill bytes.
	app1Payload := append([]byte("Exif\x00\x00"), makeTiffOrient(3, 65535-2-6)...)
	for _, fillLen := range []int{1, 5} {
		jpeg := make([]byte, 0, 2+fillLen+4+len(app1Payload)+len(body))
		jpeg = append(jpeg, 0xff, 0xd8)
		jpeg = append(jpeg, bytes.Repeat([]byte{0xff}, fillLen)...)
		jpeg = append(jpeg, 0xff, 0xe1, 0xff, 0xff)
		jpeg = append(jpeg, app1Payload...)
		jpeg = append(jpeg, body...)

		done, code, detail := runPlanWithWindow(t, jpeg, ModeInspect, FormatJPEG, 64*1024)
		if code != 0 {
			t.Fatalf("JPEG full APP1 fill=%d inspect failed: %d %s", fillLen, code, detail)
		}
		if done[2] != 3 {
			t.Fatalf("JPEG full APP1 fill=%d orientation = %d, want 3", fillLen, done[2])
		}
		stripDone, code, detail := runPlanWithWindow(t, jpeg, ModeStrip, FormatJPEG, 64*1024)
		if code != 0 {
			t.Fatalf("JPEG full APP1 fill=%d strip failed: %d %s", fillLen, code, detail)
		}
		stripped := spliceDone(jpeg, stripDone)
		want := append([]byte{0xff, 0xd8}, body...)
		if !bytes.Equal(stripped, want) {
			t.Fatalf("JPEG full APP1 fill=%d stripped mismatch", fillLen)
		}
	}
}

func TestStalledNeedReadFailsDeterministically(t *testing.T) {
	// If a buggy caller repeatedly returns an empty buffer for NeedRead at the same offset,
	// Plan must fail with stalled_read instead of looping forever.
	p := NewPlan(ModeStrip, FormatPNG, 1024)
	for i := 0; i < 10; i++ {
		res, err := p.Step(0, nil)
		if err != nil {
			t.Fatalf("step %d unexpected err: %v", i, err)
		}
		if res[0] == StatusError {
			return
		}
	}
	t.Fatalf("expected Plan to terminate with StatusError on stalled reads")
}

func runPlanCountingBytes(t *testing.T, data []byte, mode Mode, expected Format, initWin int) (done []byte, steps int, bytesRead int64) {
	t.Helper()
	p := NewPlan(mode, expected, int64(len(data)))
	maxSteps := int(MaxPlanSteps(int64(len(data))))
	off := int64(0)
	initEnd := len(data)
	if initEnd > initWin {
		initEnd = initWin
	}
	slice := data[:initEnd]
	bytesRead += int64(len(slice))
	for steps = 0; steps < maxSteps; steps++ {
		res, err := p.Step(off, slice)
		if err != nil {
			t.Fatalf("Step error: %v", err)
		}
		switch res[0] {
		case StatusDone:
			return res, steps + 1, bytesRead
		case StatusError:
			t.Fatalf("unexpected plan error %d: %s", res[1], string(res[2:]))
		case StatusNeedRead:
			uoff := readU64le(res, 1)
			need := uint64(u32le(res, 9))
			end := uoff + need
			if end > uint64(len(data)) {
				end = uint64(len(data))
			}
			off = int64(uoff)
			slice = data[uoff:end]
			bytesRead += int64(len(slice))
		default:
			t.Fatalf("unexpected status %d", res[0])
		}
	}
	t.Fatalf("exceeded maxSteps=%d", maxSteps)
	return nil, steps, bytesRead
}

func TestPng20MB8KBIdatCrossBoundaryBytes(t *testing.T) {
	// 20 MiB PNG split into 2,560 x 8 KiB IDAT chunks (typical libpng chunking) + tEXt + post-IDAT eXIf.
	const idatSize = 8 * 1024
	const idatCount = (20 * 1024 * 1024) / idatSize
	ihdr := makePngChunk("IHDR", []byte{0, 0, 0x0f, 0xa0, 0, 0, 0x0b, 0xb8, 8, 6, 0, 0, 0}) // 4000x3000
	textChunk := makePngChunk("tEXt", []byte("Comment\x00SECRET-TEXT"))
	idatChunk := makePngChunk("IDAT", make([]byte, idatSize))
	exifChunk := makePngChunk("eXIf", makeTiffOrient(6, 128))
	iend := makePngChunk("IEND", nil)

	totalSize := len(pngSignature) + len(ihdr) + len(textChunk) + idatCount*len(idatChunk) + len(exifChunk) + len(iend)
	png := make([]byte, 0, totalSize)
	png = append(png, pngSignature[:]...)
	png = append(png, ihdr...)
	png = append(png, textChunk...)
	for i := 0; i < idatCount; i++ {
		png = append(png, idatChunk...)
	}
	png = append(png, exifChunk...)
	png = append(png, iend...)

	// Inspect (initial 64 KB window): must read chunk headers only (~86 KB total, NOT 20 MB!).
	doneInspect, stepsInspect, bytesInspect := runPlanCountingBytes(t, png, ModeInspect, FormatPNG, 64*1024)
	if doneInspect[2] != 6 || doneInspect[3] != 1 || u32le(doneInspect, 4) != 4000 || u32le(doneInspect, 8) != 3000 {
		t.Fatalf("inspect mismatch: orient=%d hasSize=%d %dx%d", doneInspect[2], doneInspect[3], u32le(doneInspect, 4), u32le(doneInspect, 8))
	}
	if bytesInspect > 128*1024 {
		t.Fatalf("20 MB PNG Inspect crossed %d bytes (want <= 128 KiB, steps=%d)", bytesInspect, stepsInspect)
	}

	// Strip (initial 64 KB window): must read chunk headers only (~86 KB total, NOT 20 MB!),
	// and merge all 2,560 contiguous IDAT chunks into a single KeepRange!
	doneStrip, stepsStrip, bytesStrip := runPlanCountingBytes(t, png, ModeStrip, FormatPNG, 64*1024)
	if bytesStrip > 128*1024 {
		t.Fatalf("20 MB PNG Strip crossed %d bytes (want <= 128 KiB, steps=%d)", bytesStrip, stepsStrip)
	}
	opCount := u32le(doneStrip, 12)
	if opCount > 4 {
		t.Fatalf("expected merged KeepRanges (<= 4 ops), got %d", opCount)
	}
	stripped := spliceDone(png, doneStrip)
	wantLen := totalSize - len(textChunk) - len(exifChunk)
	if len(stripped) != wantLen {
		t.Fatalf("stripped len = %d, want %d", len(stripped), wantLen)
	}
	if bytes.Contains(stripped, []byte("SECRET-TEXT")) || bytes.Contains(stripped, []byte("eXIf")) {
		t.Fatalf("metadata leaked in stripped 20 MB PNG")
	}
	t.Logf("20 MiB PNG (8 KiB IDAT x %d): Inspect=%d B (%d steps), Strip=%d B (%d steps, %d ops)",
		idatCount, bytesInspect, stepsInspect, bytesStrip, stepsStrip, opCount)
}

func TestMemoryAmplificationGuards(t *testing.T) {
	// 1. Alternating kept/dropped JPEG segments exceeding maxSpliceOps / maxJpegSegs must be rejected cleanly.
	const pairs = maxSpliceOps + 100
	body := makeMinimalJpegBody()
	evil := make([]byte, 0, 2+pairs*8+len(body))
	evil = append(evil, 0xff, 0xd8)
	for i := 0; i < pairs; i++ {
		evil = append(evil, 0xff, 0xdb, 0x00, 0x02) // DQT (kept)
		evil = append(evil, 0xff, 0xe1, 0x00, 0x02) // APP1 (dropped)
	}
	evil = append(evil, body...)

	_, stripCode, _ := runPlanWithWindow(t, evil, ModeStrip, FormatJPEG, 64*1024)
	if stripCode == 0 {
		t.Fatalf("expected alternating JPEG segments > maxSpliceOps to fail in ModeStrip")
	}
	_, segCode, _ := runPlanWithWindow(t, evil, ModeJpegSegments, FormatJPEG, 64*1024)
	if segCode == 0 {
		t.Fatalf("expected alternating JPEG segments > maxJpegSegs to fail in ModeJpegSegments")
	}

	// 2. Oversized 2 MiB VP8X chunk must patch only 1 flags byte as literal (tiny Done frame, no 2 MiB literal copy).
	bigVp8x := make([]byte, 2*1024*1024)
	bigVp8x[0] = 0x1c // alpha + EXIF + XMP
	bigVp8x[4] = 9
	bigVp8x[7] = 19
	webp := makeWebpFile(
		makeRiffChunk("VP8X", bigVp8x),
		makeRiffChunk("VP8L", []byte{0x2f, 0, 0, 0, 0, 0}),
		makeRiffChunk("EXIF", makeTiffOrient(1, 64)),
	)
	done, _, bytesCrossed := runPlanCountingBytes(t, webp, ModeStrip, FormatWebP, 64*1024)
	if len(done) > 256 {
		t.Fatalf("oversized VP8X Done frame = %d B, want <= 256 B", len(done))
	}
	if bytesCrossed > 128*1024 {
		t.Fatalf("oversized VP8X crossed %d B, want <= 128 KiB", bytesCrossed)
	}
	stripped := spliceDone(webp, done)
	if stripped[20] != 0x10 {
		t.Fatalf("VP8X patched flags = 0x%02x, want 0x10", stripped[20])
	}
	if len(stripped) != 12+8+len(bigVp8x)+8+6 {
		t.Fatalf("VP8X stripped len = %d, want %d", len(stripped), 12+8+len(bigVp8x)+8+6)
	}
}
