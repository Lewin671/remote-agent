package agent

import (
	"encoding/json"
	"flag"
	"os"
	"testing"
)

var updateDigests = flag.Bool("update-digests", false, "rewrite testdata/digest-vectors.json")

// digestVector is one InputDigest case. Clients (web/src/model.test.ts,
// the Android app) compute the same digest from an approval's
// tool_input_json to tie it to its tool call; these vectors keep them in
// step with Go's JSON encoding.
type digestVector struct {
	Tool   string `json:"tool"`
	Input  string `json:"input"`
	Digest string `json:"digest"`
}

var digestInputs = []struct{ tool, input string }{
	{"Bash", `{"command":"mkdir -p internal/handler","description":"Make the package"}`},
	{"Bash", `{"description":"x","command":"echo '<a> & b' | wc -l"}`},
	{"Write", `{"file_path":"/tmp/a.txt","content":"hello"}`},
	{"Write", `{ "content" : "line 1\nline 2\ttab", "file_path": "/tmp/b.txt" }`},
	{"Edit", `{"file_path":"/tmp/c.go","old_string":"a < b && c > d","new_string":"x","replace_all":false}`},
	{"Edit", `{"z":1,"a":[1,2.5,-3e-7,1e21,100000000000000000000],"m":{"b":null,"a":true}}`},
	{"Write", `{"content":"中文 🦊   \u0008\u000c\u0001 \"q\" \\ /","file_path":"/tmp/d"}`},
	{"WebFetch", `{"url":"https://example.com/?a=1&b=2","prompt":"<summary>"}`},
	{"Read", `{"é":1,"e":2,"z":3,"Z":4,"éa":5}`},
	{"Bash", `{"command":""}`},
	{"Bash", `not json`},
}

func TestDigestVectors(t *testing.T) {
	var got []digestVector
	for _, c := range digestInputs {
		got = append(got, digestVector{Tool: c.tool, Input: c.input, Digest: InputDigest(c.tool, []byte(c.input))})
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/digest-vectors.json"
	if *updateDigests {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Fatalf("%s is out of date; run go test ./internal/agent -run TestDigestVectors -update-digests", path)
	}
}
