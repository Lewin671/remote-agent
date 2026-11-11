package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"connectrpc.com/connect"
)

type blockVectors struct {
	Version    int    `json:"version"`
	BlockBytes string `json:"blockBytes"`
	Cases      []struct {
		Name           string   `json:"name"`
		SizeBytes      string   `json:"sizeBytes"`
		Offsets        []string `json:"offsets"`
		LastBlockBytes string   `json:"lastBlockFileBytes"`
		InvalidOffsets []string `json:"invalidOffsets"`
		SHA256         string   `json:"sha256"`
	} `json:"cases"`
}

func atoi64(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The daemon serves exactly what testdata/shared-files/blocks.json tells
// clients to expect.
func TestSharedFileBlockVectors(t *testing.T) {
	b, err := os.ReadFile("../../testdata/shared-files/blocks.json")
	if err != nil {
		t.Fatal(err)
	}
	var v blockVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || atoi64(t, v.BlockBytes) != sharedChunkBytes {
		t.Fatalf("vectors: version %d, block %s", v.Version, v.BlockBytes)
	}
	h := newSendHarness(t)
	for _, c := range v.Cases {
		size := atoi64(t, c.SizeBytes)
		path := filepath.Join(h.temp, "vector.bin")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if c.SHA256 != "" {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i % 251)
			}
			_, err = f.Write(data)
		} else {
			err = f.Truncate(size)
		}
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		h.s.mu.Lock()
		h.s.turnID = c.Name // keep clear of the per-turn limit
		h.s.mu.Unlock()
		offer, err := h.send(path)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		var got []byte
		for i, off := range c.Offsets {
			res, err := h.d.uploads.sharedChunk(h.id(), offer.FileId, atoi64(t, off))
			if err != nil {
				t.Fatalf("%s at %s: %v", c.Name, off, err)
			}
			if len(res.Data) != sharedChunkBytes || res.GetTotalBytes() != uint64(size) {
				t.Fatalf("%s at %s: %d bytes", c.Name, off, len(res.Data))
			}
			keep := int64(sharedChunkBytes)
			if i == len(c.Offsets)-1 {
				keep = atoi64(t, c.LastBlockBytes)
				if !bytes.Equal(res.Data[keep:], make([]byte, int64(sharedChunkBytes)-keep)) {
					t.Fatalf("%s: padding is not zeros", c.Name)
				}
			}
			if c.SHA256 != "" {
				got = append(got, res.Data[:keep]...)
			}
		}
		if c.SHA256 != "" {
			sum := sha256.Sum256(got)
			if hex.EncodeToString(sum[:]) != c.SHA256 || !bytes.Equal(sum[:], offer.Sha256) {
				t.Fatalf("%s: hash", c.Name)
			}
		}
		for _, off := range c.InvalidOffsets {
			if _, err := h.d.uploads.sharedChunk(h.id(), offer.FileId, atoi64(t, off)); connect.CodeOf(err) != connect.CodeInvalidArgument && connect.CodeOf(err) != connect.CodeOutOfRange {
				t.Fatalf("%s: offset %s accepted: %v", c.Name, off, err)
			}
		}
	}
}
