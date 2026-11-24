// Package imageparse implements container-level image format sniffing,
// dimension/orientation inspection, and lossless metadata-stripping plans
// without touching pixel data or requiring full-file copies across FFI/WASM.
package imageparse

import "errors"

// Format identifies a recognized image container by its header signature.
type Format int32

const (
	FormatUnknown Format = 0
	FormatJPEG    Format = 1
	FormatPNG     Format = 2
	FormatWebP    Format = 3
	FormatGIF     Format = 4
	FormatHEIC    Format = 5
	FormatAVIF    Format = 6
)

// Mode selects what an ImagePlan computes.
type Mode int32

const (
	// ModeInspect sniffs format, reads stored pixel dimensions, and extracts EXIF orientation.
	ModeInspect Mode = 1
	// ModeStrip validates the container, reads metadata, and builds a keep-range/literal splice plan.
	ModeStrip Mode = 2
	// ModeJpegSegments walks all JPEG segments up to EOI with keep flags (used by lossless JPEG rotation).
	ModeJpegSegments Mode = 3
)

// Plan response status codes encoded at byte 0 of Step output.
const (
	StatusDone                 byte = 0
	StatusNeedRead             byte = 1
	StatusNeedScanJpegEntropy  byte = 2
	StatusNeedSkipGifSubBlocks byte = 3
	StatusError                byte = 4
)

// Plan error codes encoded at byte 1 when status == StatusError.
const (
	ErrCodeMalformed   byte = 1
	ErrCodeUnsupported byte = 2
)

// Splice operation kinds in ModeStrip Done payloads.
const (
	OpKeepRange byte = 0
	OpLiteral   byte = 1
)

const (
	defaultReadWindow = 64 * 1024
	maxExifReadBytes  = 256 * 1024
	maxStalledReads   = 3

	planStepBytesDivisor int64 = 256
	planStepBase         int64 = 4096
	planStepCap          int64 = 262144
)

// MaxPlanSteps returns the deterministic upper bound on Plan.Step iterations for a file of totalLen bytes.
// Shells enforce this limit in their runPlan loop as an additional safety guard against non-terminating loops.
func MaxPlanSteps(totalLen int64) int64 {
	if totalLen <= 0 {
		return planStepBase
	}
	steps := totalLen/planStepBytesDivisor + planStepBase
	if steps > planStepCap {
		return planStepCap
	}
	return steps
}

var pngSignature = [8]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// SniffFormat identifies the image format solely from header bytes (first <= 256 B).
func SniffFormat(b []byte) Format {
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return FormatJPEG
	}
	if len(b) >= 8 &&
		b[0] == pngSignature[0] && b[1] == pngSignature[1] &&
		b[2] == pngSignature[2] && b[3] == pngSignature[3] &&
		b[4] == pngSignature[4] && b[5] == pngSignature[5] &&
		b[6] == pngSignature[6] && b[7] == pngSignature[7] {
		return FormatPNG
	}
	if len(b) >= 6 && b[0] == 'G' && b[1] == 'I' && b[2] == 'F' && b[3] == '8' &&
		(b[4] == '7' || b[4] == '9') && b[5] == 'a' {
		return FormatGIF
	}
	if len(b) >= 12 &&
		b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F' &&
		b[8] == 'W' && b[9] == 'E' && b[10] == 'B' && b[11] == 'P' {
		return FormatWebP
	}
	if len(b) >= 16 && b[4] == 'f' && b[5] == 't' && b[6] == 'y' && b[7] == 'p' {
		boxSize := uint64(u32be(b, 0))
		end := uint64(len(b))
		if boxSize >= 16 && boxSize < end {
			end = boxSize
		}
		// Check AVIF brands first because AVIF files also list the generic HEIF brand "mif1".
		if ftypHasBrand(b, end, isAvifBrand) {
			return FormatAVIF
		}
		if ftypHasBrand(b, end, isHeicBrand) {
			return FormatHEIC
		}
	}
	return FormatUnknown
}

func ftypHasBrand(b []byte, end uint64, match func(a, c, d, e byte) bool) bool {
	if match(b[8], b[9], b[10], b[11]) {
		return true
	}
	for at := uint64(16); at+4 <= end; at += 4 {
		if match(b[at], b[at+1], b[at+2], b[at+3]) {
			return true
		}
	}
	return false
}

func isAvifBrand(a, b, c, d byte) bool {
	return a == 'a' && b == 'v' && c == 'i' && (d == 'f' || d == 's')
}

func isHeicBrand(a, b, c, d byte) bool {
	if a == 'h' && b == 'e' {
		return (c == 'i' && (d == 'c' || d == 'x' || d == 'm' || d == 's')) ||
			(c == 'v' && (d == 'c' || d == 'x'))
	}
	return a == 'm' && (b == 'i' || b == 's') && c == 'f' && d == '1'
}

// ParseTiffOrientation extracts EXIF orientation (1..8) from a TIFF/EXIF block.
// Accepts an optional leading "Exif\x00\x00" prefix. Returns 1 when absent or invalid.
func ParseTiffOrientation(t []byte) int32 {
	if len(t) >= 6 && hasASCII(t, 0, "Exif\x00\x00") {
		t = t[6:]
	}
	if len(t) < 8 {
		return 1
	}
	var le bool
	if t[0] == 'I' && t[1] == 'I' {
		le = true
	} else if t[0] == 'M' && t[1] == 'M' {
		le = false
	} else {
		return 1
	}
	if u16(t, 2, le) != 42 {
		return 1
	}
	ifd := uint64(u32(t, 4, le))
	if ifd+2 > uint64(len(t)) {
		return 1
	}
	n := uint64(u16(t, int(ifd), le))
	for i := uint64(0); i < n; i++ {
		e := ifd + 2 + i*12
		if e+12 > uint64(len(t)) {
			return 1
		}
		ei := int(e)
		if u16(t, ei, le) != 0x0112 {
			continue
		}
		// Orientation is one SHORT (type 3, count >= 1), stored in the first 2 bytes of value field.
		if u16(t, ei+2, le) != 3 || u32(t, ei+4, le) < 1 {
			return 1
		}
		v := u16(t, ei+8, le)
		if v >= 1 && v <= 8 {
			return int32(v)
		}
		return 1
	}
	return 1
}

type spliceOp struct {
	kind  byte
	start uint64
	end   uint64
	lit   []byte
}

type jpegSeg struct {
	marker  byte
	keep    bool
	start   uint64
	end     uint64
	dataEnd uint64
}

// Plan is a sans-IO, header/seek-driven parser for a single image file.
// Shells feed small byte windows (typically <= 64 KB) and run tiny in-place
// byte-skip primitives for JPEG entropy scans and GIF sub-blocks.
type Plan struct {
	mode           Mode
	expectedFormat Format
	totalLen       uint64

	format      Format
	orientation int32
	hasSize     bool
	width       uint32
	height      uint32

	ops  []spliceOp
	segs []jpegSeg

	// Cursor state across Step calls.
	stage     byte
	pos       uint64
	limit     uint64 // WebP RIFF end
	sawScan   bool   // JPEG saw SOS
	sawData   bool   // PNG saw IDAT
	gifFrames uint32 // GIF frame count
	haveExif  bool

	// Pending state while waiting for shell byte-skip primitive.
	pendingSegStart  uint64
	pendingSegEnd    uint64
	pendingSegMarker byte
	pendingSegKeep   bool
	pendingGifStart  uint64
	pendingGifKeep   bool
	pendingGifIsImg  bool

	// WebP header rewrite tracking (index in ops for 12-byte RIFF header).
	webpOutBodyLen uint64

	// Consecutive NeedRead stall detector.
	hasLastRead  bool
	lastReadPos  uint64
	stalledReads int

	done bool
}

const (
	stageInit byte = iota
	stageJpegWalk
	stageJpegAfterEntropy
	stagePngWalk
	stageWebpWalk
	stageGifHeader
	stageGifWalk
	stageGifAfterSubBlocks
)

// NewPlan creates a new sans-IO container parser/strip-planner for a file of totalLen bytes.
// expectedFormat == FormatUnknown means auto-detect from header.
func NewPlan(mode Mode, expectedFormat Format, totalLen int64) *Plan {
	if totalLen < 0 {
		totalLen = 0
	}
	return &Plan{
		mode:           mode,
		expectedFormat: expectedFormat,
		totalLen:       uint64(totalLen),
		orientation:    1,
	}
}

// Step advances the plan with bytes read at offset (or offset < 0 to signal EOF from a skip primitive).
func (p *Plan) Step(offset int64, buf []byte) ([]byte, error) {
	if p.done {
		return nil, errors.New("imageparse:plan_finished")
	}
	if p.stage == stageInit {
		return p.stepInit(offset, buf)
	}
	switch p.format {
	case FormatJPEG:
		return p.stepJpeg(offset, buf)
	case FormatPNG:
		return p.stepPng(offset, buf)
	case FormatWebP:
		return p.stepWebp(offset, buf)
	case FormatGIF:
		return p.stepGif(offset, buf)
	default:
		return p.fail(ErrCodeUnsupported, "unsupported image format"), nil
	}
}

func (p *Plan) stepInit(offset int64, buf []byte) ([]byte, error) {
	if offset != 0 {
		return p.needRead(0, minU32(p.totalLen, defaultReadWindow)), nil
	}
	if p.totalLen == 0 {
		if p.mode == ModeInspect && p.expectedFormat == FormatUnknown {
			p.format = FormatUnknown
			return p.finish(), nil
		}
		return p.fail(ErrCodeMalformed, "empty image"), nil
	}
	// Ensure we have enough header bytes for sniffing (at least 64 bytes or whole file).
	needSniff := minU32(p.totalLen, 64)
	if uint32(len(buf)) < needSniff {
		return p.needRead(0, minU32(p.totalLen, defaultReadWindow)), nil
	}
	// For ISO BMFF ftyp boxes larger than 64 bytes, fetch up to 256 bytes if needed.
	if len(buf) >= 16 && buf[4] == 'f' && buf[5] == 't' && buf[6] == 'y' && buf[7] == 'p' {
		boxSize := uint64(u32be(buf, 0))
		want := boxSize
		if want > 256 {
			want = 256
		}
		if want > p.totalLen {
			want = p.totalLen
		}
		if uint64(len(buf)) < want {
			return p.needRead(0, uint32(want)), nil
		}
	}

	p.format = SniffFormat(buf)
	if p.expectedFormat != FormatUnknown && p.format != p.expectedFormat {
		if p.mode == ModeInspect {
			// Mismatched format during inspect: return format and default orientation=1, no size.
			return p.finish(), nil
		}
		return p.fail(ErrCodeMalformed, "image signature does not match expected format"), nil
	}

	if p.mode == ModeJpegSegments {
		if p.format != FormatJPEG {
			return p.fail(ErrCodeMalformed, "not a JPEG"), nil
		}
		p.stage = stageJpegWalk
		p.pos = 2
		return p.stepJpeg(offset, buf)
	}

	switch p.format {
	case FormatJPEG:
		p.stage = stageJpegWalk
		p.pos = 2
		if p.mode == ModeStrip {
			p.addKeep(0, 2)
		}
		return p.stepJpeg(offset, buf)
	case FormatPNG:
		p.stage = stagePngWalk
		p.pos = 8
		if p.mode == ModeStrip {
			p.addKeep(0, 8)
		}
		return p.stepPng(offset, buf)
	case FormatWebP:
		if p.totalLen < 12 || len(buf) < 12 {
			return p.fail(ErrCodeMalformed, "truncated WebP header"), nil
		}
		riffSize := uint64(u32le(buf, 4))
		end := uint64(8) + riffSize
		if end > p.totalLen {
			if p.mode == ModeInspect {
				// Best-effort inspect: stop at file end rather than failing.
				end = p.totalLen
			} else {
				return p.fail(ErrCodeMalformed, "WebP RIFF size runs past the end"), nil
			}
		}
		if end <= 12 {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "WebP has no chunks"), nil
		}
		p.limit = end
		p.pos = 12
		p.stage = stageWebpWalk
		if p.mode == ModeStrip {
			// Reserve op 0 for the 12-byte RIFF header (patched on finish).
			p.ops = append(p.ops, spliceOp{kind: OpLiteral, lit: make([]byte, 12)})
			copy(p.ops[0].lit, buf[:12])
			p.webpOutBodyLen = 4 // "WEBP"
		}
		return p.stepWebp(offset, buf)
	case FormatGIF:
		p.stage = stageGifHeader
		return p.stepGif(offset, buf)
	default:
		if p.mode == ModeInspect {
			return p.finish(), nil
		}
		return p.fail(ErrCodeUnsupported, "unsupported image format"), nil
	}
}

// ---------------------------------------------------------------- JPEG

func (p *Plan) stepJpeg(offset int64, buf []byte) ([]byte, error) {
	if p.stage == stageJpegAfterEntropy {
		if offset < 0 || uint64(offset) >= p.totalLen {
			return p.fail(ErrCodeMalformed, "JPEG scan data runs past the end"), nil
		}
		dataEnd := uint64(offset)
		if p.mode == ModeStrip {
			p.addKeep(p.pendingSegStart, dataEnd)
		} else if p.mode == ModeJpegSegments {
			p.segs = append(p.segs, jpegSeg{
				marker:  p.pendingSegMarker,
				keep:    p.pendingSegKeep,
				start:   p.pendingSegStart,
				end:     p.pendingSegEnd,
				dataEnd: dataEnd,
			})
		}
		p.pos = dataEnd
		p.stage = stageJpegWalk
	}

	base := uint64(0)
	if offset > 0 {
		base = uint64(offset)
	}
	bufEnd := base + uint64(len(buf))

	for {
		if p.pos >= p.totalLen {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "JPEG ends before EOI"), nil
		}
		if p.pos < base || p.pos >= bufEnd {
			return p.needRead(p.pos, minU32(p.totalLen-p.pos, defaultReadWindow)), nil
		}
		rel := int(p.pos - base)
		if buf[rel] != 0xff {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "JPEG expected a marker"), nil
		}
		m := rel + 1
		for m < len(buf) && buf[m] == 0xff {
			m++
		}
		if m >= len(buf) {
			if bufEnd >= p.totalLen {
				if p.mode == ModeInspect {
					return p.finish(), nil
				}
				return p.fail(ErrCodeMalformed, "JPEG ends before EOI"), nil
			}
			// Advance p.pos to the last 0xFF so fill bytes across window boundaries make forward progress.
			p.pos = bufEnd - 1
			return p.needRead(p.pos, minU32(p.totalLen-p.pos, defaultReadWindow)), nil
		}
		marker := buf[m]
		start := base + uint64(m-1)
		// Advance cursor past any leading 0xFF fill bytes so subsequent NeedRead offsets equal p.pos.
		p.pos = start

		if marker == 0xd9 { // EOI
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			if p.mode == ModeStrip {
				if !p.sawScan {
					return p.fail(ErrCodeMalformed, "JPEG has no image data"), nil
				}
				p.addKeep(start, start+2)
				return p.finish(), nil
			}
			p.segs = append(p.segs, jpegSeg{
				marker:  0xd9,
				keep:    true,
				start:   start,
				end:     start + 2,
				dataEnd: start + 2,
			})
			return p.finish(), nil
		}

		if marker == 0x00 || marker == 0x01 || marker == 0xd8 || (marker >= 0xd0 && marker <= 0xd7) {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "JPEG unexpected marker"), nil
		}

		// Need 2-byte length field at start+2..start+4.
		if start+4 > bufEnd {
			if start+4 > p.totalLen {
				if p.mode == ModeInspect {
					return p.finish(), nil
				}
				return p.fail(ErrCodeMalformed, "JPEG truncated segment header"), nil
			}
			return p.needRead(start, minU32(p.totalLen-start, defaultReadWindow)), nil
		}
		relStart := int(start - base)
		segLen := uint64(u16be(buf, relStart+2))
		end := start + 2 + segLen
		if segLen < 2 || end > p.totalLen {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "JPEG segment runs past the end"), nil
		}

		// Inspect SOF dimensions before SOS.
		if !p.sawScan && !p.hasSize && isJpegSOF(marker) {
			if segLen >= 7 {
				if start+9 > bufEnd {
					return p.needRead(start, minU32(p.totalLen-start, defaultReadWindow)), nil
				}
				p.height = uint32(u16be(buf, relStart+5))
				p.width = uint32(u16be(buf, relStart+7))
				p.hasSize = true
			}
		}

		// Inspect EXIF orientation in APP1 before SOS (Inspect mode only).
		if p.mode == ModeInspect && !p.sawScan && !p.haveExif && marker == 0xe1 && segLen > 8 {
			if start+10 > bufEnd {
				return p.needRead(p.pos, uint32(minU64(end-start, defaultReadWindow))), nil
			}
			if hasASCII(buf, relStart+4, "Exif\x00\x00") {
				// JPEG segment max length is 65535, so end-start <= 65537.
				if end > bufEnd {
					return p.needRead(p.pos, uint32(end-start)), nil
				}
				p.orientation = ParseTiffOrientation(buf[relStart+10 : int(end-base)])
				p.haveExif = true
			}
		}

		if marker == 0xda { // SOS
			p.sawScan = true
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			p.pendingSegStart = start
			p.pendingSegEnd = end
			p.pendingSegMarker = marker
			p.pendingSegKeep = true
			p.stage = stageJpegAfterEntropy
			return p.needScanJpegEntropy(end), nil
		}

		// Determine keep flag for APPn / COM (needs at most 16 bytes past start+4).
		keep := true
		if marker == 0xfe {
			keep = false
		} else if marker >= 0xe0 && marker <= 0xef {
			if marker == 0xe0 || marker == 0xe2 || marker == 0xee {
				checkEnd := start + 16
				if checkEnd > end {
					checkEnd = end
				}
				if checkEnd > bufEnd {
					return p.needRead(start, minU32(p.totalLen-start, defaultReadWindow)), nil
				}
				keep = keepJpegApp(marker, buf[relStart+4:int(checkEnd-base)])
			} else {
				keep = false
			}
		}

		if p.mode == ModeStrip && keep {
			p.addKeep(start, end)
		} else if p.mode == ModeJpegSegments {
			p.segs = append(p.segs, jpegSeg{
				marker:  marker,
				keep:    keep,
				start:   start,
				end:     end,
				dataEnd: end,
			})
		}

		// In Inspect mode, once we have both SOF dimensions and EXIF orientation, stop early!
		if p.mode == ModeInspect && p.hasSize && p.haveExif {
			return p.finish(), nil
		}
		p.pos = end
	}
}

func isJpegSOF(m byte) bool {
	return m >= 0xc0 && m <= 0xcf && m != 0xc4 && m != 0xc8 && m != 0xcc
}

func keepJpegApp(marker byte, payloadPrefix []byte) bool {
	switch marker {
	case 0xe0:
		return len(payloadPrefix) >= 5 && hasASCII(payloadPrefix, 0, "JFIF\x00")
	case 0xe2:
		return len(payloadPrefix) >= 12 && hasASCII(payloadPrefix, 0, "ICC_PROFILE\x00")
	case 0xee:
		return len(payloadPrefix) >= 5 && hasASCII(payloadPrefix, 0, "Adobe")
	default:
		return false
	}
}

// ---------------------------------------------------------------- PNG

func (p *Plan) stepPng(offset int64, buf []byte) ([]byte, error) {
	base := uint64(0)
	if offset > 0 {
		base = uint64(offset)
	}
	bufEnd := base + uint64(len(buf))

	for {
		if p.pos+12 > p.totalLen {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "PNG chunk runs past the end"), nil
		}
		if p.pos < base || p.pos+8 > bufEnd {
			// Request only the chunk header (plus IHDR dimensions at pos==8) so IDAT payloads never cross the boundary.
			want := uint32(8)
			if p.pos == 8 {
				want = 16
			}
			return p.needRead(p.pos, minU32(p.totalLen-p.pos, want)), nil
		}
		rel := int(p.pos - base)
		chunkLen := uint64(u32be(buf, rel))
		end := p.pos + 12 + chunkLen
		if chunkLen > 0x7fffffff || end > p.totalLen {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "PNG chunk runs past the end"), nil
		}
		t0, t1, t2, t3 := buf[rel+4], buf[rel+5], buf[rel+6], buf[rel+7]
		isIHDR := t0 == 'I' && t1 == 'H' && t2 == 'D' && t3 == 'R'
		if p.pos == 8 && !isIHDR {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "PNG does not start with IHDR"), nil
		}

		if isIHDR && !p.hasSize && chunkLen >= 8 {
			if p.pos+16 > bufEnd {
				return p.needRead(p.pos, minU32(p.totalLen-p.pos, 32)), nil
			}
			p.width = u32be(buf, rel+8)
			p.height = u32be(buf, rel+12)
			p.hasSize = true
		}

		isIDAT := t0 == 'I' && t1 == 'D' && t2 == 'A' && t3 == 'T'
		if isIDAT {
			p.sawData = true
		}

		isEXIF := t0 == 'e' && t1 == 'X' && t2 == 'I' && t3 == 'f'
		if p.mode == ModeInspect && isEXIF && !p.haveExif && chunkLen > 0 {
			readLen := chunkLen
			if readLen > maxExifReadBytes {
				readLen = maxExifReadBytes
			}
			dataStart := p.pos + 8
			if dataStart+readLen > bufEnd {
				return p.needRead(p.pos, uint32(8+readLen)), nil
			}
			dRel := int(dataStart - base)
			p.orientation = ParseTiffOrientation(buf[dRel : dRel+int(readLen)])
			p.haveExif = true
		}

		if p.mode == ModeStrip && !isPngDropChunk(t0, t1, t2, t3) {
			p.addKeep(p.pos, end)
		}

		isIEND := t0 == 'I' && t1 == 'E' && t2 == 'N' && t3 == 'D'
		if isIEND {
			if p.mode == ModeStrip && !p.sawData {
				return p.fail(ErrCodeMalformed, "PNG has no IDAT"), nil
			}
			return p.finish(), nil
		}

		p.pos = end
	}
}

func isPngDropChunk(a, b, c, d byte) bool {
	return (a == 't' && b == 'E' && c == 'X' && d == 't') ||
		(a == 'z' && b == 'T' && c == 'X' && d == 't') ||
		(a == 'i' && b == 'T' && c == 'X' && d == 't') ||
		(a == 'e' && b == 'X' && c == 'I' && d == 'f') ||
		(a == 't' && b == 'I' && c == 'M' && d == 'E')
}

// ---------------------------------------------------------------- WebP

func (p *Plan) stepWebp(offset int64, buf []byte) ([]byte, error) {
	base := uint64(0)
	if offset > 0 {
		base = uint64(offset)
	}
	bufEnd := base + uint64(len(buf))

	for {
		if p.pos >= p.limit {
			if p.mode == ModeStrip {
				// Patch RIFF file size (body len including "WEBP" == total - 8).
				putU32le(p.ops[0].lit, 4, uint32(p.webpOutBodyLen))
			}
			return p.finish(), nil
		}
		if p.pos+8 > p.limit {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "WebP chunk header runs past the end"), nil
		}
		if p.pos < base || p.pos+8 > bufEnd {
			// Request only the 8-byte RIFF chunk header (18 bytes at pos==12 for VP8*/VP8X header) to skip image payloads.
			want := uint32(8)
			if p.pos == 12 {
				want = 18
			}
			return p.needRead(p.pos, minU32(p.limit-p.pos, want)), nil
		}
		rel := int(p.pos - base)
		t0, t1, t2, t3 := buf[rel], buf[rel+1], buf[rel+2], buf[rel+3]
		size := uint64(u32le(buf, rel+4))
		dataStart := p.pos + 8
		dataEnd := dataStart + size
		if dataEnd > p.limit {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "WebP chunk runs past the end"), nil
		}

		isVP8 := t0 == 'V' && t1 == 'P' && t2 == '8' && t3 == ' '
		isVP8L := t0 == 'V' && t1 == 'P' && t2 == '8' && t3 == 'L'
		isVP8X := t0 == 'V' && t1 == 'P' && t2 == '8' && t3 == 'X'
		if p.pos == 12 && !isVP8 && !isVP8L && !isVP8X {
			if p.mode == ModeStrip {
				return p.fail(ErrCodeMalformed, "WebP does not start with VP8, VP8L or VP8X"), nil
			}
		}

		// Read dimensions from VP8X / VP8L / VP8.
		if !p.hasSize && (isVP8X || isVP8L || isVP8) {
			need := uint64(10)
			if isVP8L {
				need = 5
			}
			if size >= need {
				if dataStart+need > bufEnd {
					return p.needRead(p.pos, uint32(8+need)), nil
				}
				d := int(dataStart - base)
				if isVP8X {
					p.width = 1 + (uint32(buf[d+4]) | uint32(buf[d+5])<<8 | uint32(buf[d+6])<<16)
					p.height = 1 + (uint32(buf[d+7]) | uint32(buf[d+8])<<8 | uint32(buf[d+9])<<16)
					p.hasSize = true
				} else if isVP8L && buf[d] == 0x2f {
					bits := u32le(buf, d+1)
					p.width = 1 + (bits & 0x3fff)
					p.height = 1 + ((bits >> 14) & 0x3fff)
					p.hasSize = true
				} else if isVP8 && buf[d+3] == 0x9d && buf[d+4] == 0x01 && buf[d+5] == 0x2a {
					p.width = uint32(u16le(buf, d+6)) & 0x3fff
					p.height = uint32(u16le(buf, d+8)) & 0x3fff
					p.hasSize = true
				}
			}
		}

		isEXIF := t0 == 'E' && t1 == 'X' && t2 == 'I' && t3 == 'F'
		isXMP := t0 == 'X' && t1 == 'M' && t2 == 'P' && t3 == ' '
		if p.mode == ModeInspect && isEXIF && !p.haveExif && size > 0 {
			readLen := size
			if readLen > maxExifReadBytes {
				readLen = maxExifReadBytes
			}
			if dataStart+readLen > bufEnd {
				return p.needRead(p.pos, uint32(8+readLen)), nil
			}
			d := int(dataStart - base)
			p.orientation = ParseTiffOrientation(buf[d : d+int(readLen)])
			p.haveExif = true
		}

		if p.mode == ModeStrip && !isEXIF && !isXMP {
			if isVP8X {
				if size < 10 {
					return p.fail(ErrCodeMalformed, "WebP VP8X chunk too short"), nil
				}
				if dataEnd > bufEnd {
					return p.needRead(p.pos, uint32(dataEnd-p.pos)), nil
				}
				chunk := make([]byte, int(dataEnd-p.pos))
				copy(chunk, buf[rel:int(dataEnd-base)])
				// Clear EXIF (0x08) and XMP (0x04) flag bits.
				chunk[8] &^= 0x0c
				p.ops = append(p.ops, spliceOp{kind: OpLiteral, lit: chunk})
				p.webpOutBodyLen += uint64(len(chunk))
				if size&1 != 0 {
					p.ops = append(p.ops, spliceOp{kind: OpLiteral, lit: []byte{0}})
					p.webpOutBodyLen++
				}
			} else {
				p.addKeep(p.pos, dataEnd)
				p.webpOutBodyLen += dataEnd - p.pos
				if size&1 != 0 {
					p.ops = append(p.ops, spliceOp{kind: OpLiteral, lit: []byte{0}})
					p.webpOutBodyLen++
				}
			}
		}

		p.pos = dataEnd + (size & 1)
	}
}

// ---------------------------------------------------------------- GIF

func (p *Plan) stepGif(offset int64, buf []byte) ([]byte, error) {
	if p.stage == stageGifHeader {
		if p.totalLen < 10 {
			if p.mode == ModeInspect {
				return p.finish(), nil
			}
			return p.fail(ErrCodeMalformed, "GIF too short"), nil
		}
		if offset != 0 || len(buf) < 10 {
			return p.needRead(0, minU32(p.totalLen, defaultReadWindow)), nil
		}
		p.width = uint32(u16le(buf, 6))
		p.height = uint32(u16le(buf, 8))
		p.hasSize = true
		if p.mode == ModeInspect {
			return p.finish(), nil
		}
		if p.totalLen < 13 || len(buf) < 13 {
			return p.fail(ErrCodeMalformed, "not a GIF"), nil
		}
		flags := buf[10]
		pos := uint64(13)
		if flags&0x80 != 0 {
			pos += uint64(3) << ((flags & 7) + 1)
		}
		if pos > p.totalLen {
			return p.fail(ErrCodeMalformed, "GIF color table runs past the end"), nil
		}
		p.addKeep(0, pos)
		p.pos = pos
		p.stage = stageGifWalk
	}

	if p.stage == stageGifAfterSubBlocks {
		if offset < 0 || uint64(offset) > p.totalLen {
			return p.fail(ErrCodeMalformed, "GIF data runs past the end"), nil
		}
		end := uint64(offset)
		if p.pendingGifKeep {
			p.addKeep(p.pendingGifStart, end)
		}
		if p.pendingGifIsImg {
			p.gifFrames++
		}
		p.pos = end
		p.stage = stageGifWalk
	}

	base := uint64(0)
	if offset > 0 {
		base = uint64(offset)
	}
	bufEnd := base + uint64(len(buf))

	for {
		if p.pos >= p.totalLen {
			return p.fail(ErrCodeMalformed, "GIF ends before the trailer"), nil
		}
		if p.pos < base || p.pos >= bufEnd {
			return p.needRead(p.pos, minU32(p.totalLen-p.pos, defaultReadWindow)), nil
		}
		rel := int(p.pos - base)
		intro := buf[rel]
		switch intro {
		case 0x3b: // Trailer
			if p.gifFrames == 0 {
				return p.fail(ErrCodeMalformed, "GIF has no image"), nil
			}
			p.addKeep(p.pos, p.pos+1)
			return p.finish(), nil
		case 0x21: // Extension
			if p.pos+2 > p.totalLen {
				return p.fail(ErrCodeMalformed, "GIF extension runs past the end"), nil
			}
			need := uint64(2)
			if p.pos+2 > bufEnd {
				return p.needRead(p.pos, minU32(p.totalLen-p.pos, defaultReadWindow)), nil
			}
			label := buf[rel+1]
			keep := label != 0xfe
			if label == 0xff {
				// Application extension: keep only 11-byte "NETSCAPE2.0" or "ANIMEXTS1.0".
				need = 14
				if p.pos+need <= p.totalLen && p.pos+need > bufEnd {
					return p.needRead(p.pos, minU32(p.totalLen-p.pos, defaultReadWindow)), nil
				}
				keep = p.pos+14 <= p.totalLen && buf[rel+2] == 11 &&
					(hasASCII(buf, rel+3, "NETSCAPE2.0") || hasASCII(buf, rel+3, "ANIMEXTS1.0"))
			}
			_ = need
			p.pendingGifStart = p.pos
			p.pendingGifKeep = keep
			p.pendingGifIsImg = false
			p.stage = stageGifAfterSubBlocks
			return p.needSkipGifSubBlocks(p.pos + 2), nil
		case 0x2c: // Image descriptor
			if p.pos+11 > p.totalLen {
				return p.fail(ErrCodeMalformed, "GIF image descriptor runs past the end"), nil
			}
			if p.pos+10 > bufEnd {
				return p.needRead(p.pos, minU32(p.totalLen-p.pos, defaultReadWindow)), nil
			}
			f := buf[rel+9]
			lzw := p.pos + 10
			if f&0x80 != 0 {
				lzw += uint64(3) << ((f & 7) + 1)
			}
			if lzw+1 > p.totalLen {
				return p.fail(ErrCodeMalformed, "GIF image descriptor runs past the end"), nil
			}
			p.pendingGifStart = p.pos
			p.pendingGifKeep = true
			p.pendingGifIsImg = true
			p.stage = stageGifAfterSubBlocks
			return p.needSkipGifSubBlocks(lzw + 1), nil
		default:
			return p.fail(ErrCodeMalformed, "GIF unknown block"), nil
		}
	}
}

// ---------------------------------------------------------------- Encoding

func (p *Plan) addKeep(start, end uint64) {
	if start >= end {
		return
	}
	if n := len(p.ops); n > 0 && p.ops[n-1].kind == OpKeepRange && p.ops[n-1].end == start {
		p.ops[n-1].end = end
		return
	}
	p.ops = append(p.ops, spliceOp{kind: OpKeepRange, start: start, end: end})
}

func (p *Plan) needRead(offset uint64, length uint32) []byte {
	if p.hasLastRead && offset <= p.lastReadPos {
		p.stalledReads++
		if p.stalledReads > maxStalledReads {
			return p.fail(ErrCodeMalformed, "stalled_read")
		}
	} else {
		p.hasLastRead = true
		p.lastReadPos = offset
		p.stalledReads = 0
	}
	out := make([]byte, 13)
	out[0] = StatusNeedRead
	putU64le(out, 1, offset)
	putU32le(out, 9, length)
	return out
}

func (p *Plan) needScanJpegEntropy(fromOffset uint64) []byte {
	out := make([]byte, 9)
	out[0] = StatusNeedScanJpegEntropy
	putU64le(out, 1, fromOffset)
	return out
}

func (p *Plan) needSkipGifSubBlocks(fromOffset uint64) []byte {
	out := make([]byte, 9)
	out[0] = StatusNeedSkipGifSubBlocks
	putU64le(out, 1, fromOffset)
	return out
}

func (p *Plan) fail(code byte, detail string) []byte {
	p.done = true
	out := make([]byte, 2+len(detail))
	out[0] = StatusError
	out[1] = code
	copy(out[2:], detail)
	return out
}

func (p *Plan) finish() []byte {
	p.done = true
	// Header: [status(1) | format(1) | orientation(1) | hasSize(1) | width(4) | height(4) | count(4)] = 16 bytes
	size := 16
	if p.mode == ModeStrip {
		for _, op := range p.ops {
			if op.kind == OpKeepRange {
				size += 1 + 8 + 8
			} else {
				size += 1 + 4 + len(op.lit)
			}
		}
	} else if p.mode == ModeJpegSegments {
		size += len(p.segs) * (1 + 1 + 8 + 8 + 8)
	}
	out := make([]byte, size)
	out[0] = StatusDone
	out[1] = byte(p.format)
	out[2] = byte(p.orientation)
	if p.hasSize {
		out[3] = 1
	}
	putU32le(out, 4, p.width)
	putU32le(out, 8, p.height)

	at := 16
	if p.mode == ModeStrip {
		putU32le(out, 12, uint32(len(p.ops)))
		for _, op := range p.ops {
			out[at] = op.kind
			at++
			if op.kind == OpKeepRange {
				putU64le(out, at, op.start)
				putU64le(out, at+8, op.end)
				at += 16
			} else {
				putU32le(out, at, uint32(len(op.lit)))
				copy(out[at+4:], op.lit)
				at += 4 + len(op.lit)
			}
		}
	} else if p.mode == ModeJpegSegments {
		putU32le(out, 12, uint32(len(p.segs)))
		for _, s := range p.segs {
			out[at] = s.marker
			if s.keep {
				out[at+1] = 1
			}
			putU64le(out, at+2, s.start)
			putU64le(out, at+10, s.end)
			putU64le(out, at+18, s.dataEnd)
			at += 26
		}
	}
	return out
}

// ---------------------------------------------------------------- Byte helpers

func hasASCII(b []byte, at int, s string) bool {
	if at < 0 || at+len(s) > len(b) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b[at+i] != s[i] {
			return false
		}
	}
	return true
}

func u16be(b []byte, at int) uint16 {
	return uint16(b[at])<<8 | uint16(b[at+1])
}

func u16le(b []byte, at int) uint16 {
	return uint16(b[at]) | uint16(b[at+1])<<8
}

func u16(b []byte, at int, le bool) uint16 {
	if le {
		return u16le(b, at)
	}
	return u16be(b, at)
}

func u32be(b []byte, at int) uint32 {
	return uint32(b[at])<<24 | uint32(b[at+1])<<16 | uint32(b[at+2])<<8 | uint32(b[at+3])
}

func u32le(b []byte, at int) uint32 {
	return uint32(b[at]) | uint32(b[at+1])<<8 | uint32(b[at+2])<<16 | uint32(b[at+3])<<24
}

func u32(b []byte, at int, le bool) uint32 {
	if le {
		return u32le(b, at)
	}
	return u32be(b, at)
}

func putU32le(b []byte, at int, v uint32) {
	b[at] = byte(v)
	b[at+1] = byte(v >> 8)
	b[at+2] = byte(v >> 16)
	b[at+3] = byte(v >> 24)
}

func putU64le(b []byte, at int, v uint64) {
	b[at] = byte(v)
	b[at+1] = byte(v >> 8)
	b[at+2] = byte(v >> 16)
	b[at+3] = byte(v >> 24)
	b[at+4] = byte(v >> 32)
	b[at+5] = byte(v >> 40)
	b[at+6] = byte(v >> 48)
	b[at+7] = byte(v >> 56)
}

func minU32(a uint64, b uint32) uint32 {
	if a < uint64(b) {
		return uint32(a)
	}
	return b
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
