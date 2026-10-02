package imageproc

import "testing"

// Uploaded images reach metadata parsers before being pasted into a CLI.
// Malformed headers and length fields must fail without panicking.
func FuzzStripMetadata(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("\x89PNG\r\n\x1a\n"),
		{0xff, 0xd8, 0xff, 0xe1, 0, 8, 'E', 'x', 'i', 'f', 0, 0, 0xff, 0xd9},
		[]byte("RIFF\xff\xff\xff\xffWEBPVP8X\xff\xff\xff\xff"),
		[]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00\x21\xff\x00\x3b"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			t.Skip()
		}
		StripMetadata(b)
		Orientation(b)
	})
}
