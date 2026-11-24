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
				} else {
					if got2 := spliceDone(gotStripped, idempDone); !bytes.Equal(got2, gotStripped) {
						t.Errorf("%s (win=%d): strip is not idempotent", tc.Name, maxWin)
					}
					if idempDone[2] != 1 {
						t.Errorf("%s (win=%d): stripped output orientation = %d, want 1", tc.Name, maxWin, idempDone[2])
					}
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
