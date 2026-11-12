package i18n

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func TestSharedLocaleContract(t *testing.T) {
	data, err := os.ReadFile("../../i18n/locale-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Preference string
		System     []string
		Expected   string
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		if got := Resolve(v.Preference, v.System); got != v.Expected {
			t.Errorf("%+v: %s", v, got)
		}
	}
}
func TestEmbeddedMessages(t *testing.T) {
	if CommonCancel("zh-CN") != "取消" || CommonCopy("en") != "Copy" {
		t.Fatal("message contract")
	}
	for _, n := range []int{0, 1, 2, 1000} {
		expected := "images could not be attached. Tap an image for details."
		if n == 1 {
			expected = "image could not be attached. Tap it for details."
		}
		if got := ImageAttachFailed("en", n); got != fmtCount(n)+" "+expected {
			t.Error(got)
		}
		if got := ImageAttachFailed("zh-CN", n); got != fmtCount(n)+" 张图没加上，点图片看原因" {
			t.Error(got)
		}
	}
	if CommonCancel("unsupported") != "取消" {
		t.Fatal("fallback")
	}
}
func fmtCount(n int) string { return strconv.Itoa(n) }
