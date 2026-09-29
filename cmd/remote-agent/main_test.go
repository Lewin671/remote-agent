package main

import (
	"os"
	"testing"
)

func TestEnsureUTF8(t *testing.T) {
	for _, c := range []struct {
		name                 string
		lcAll, lcCtype, lang string
		wantAll, wantCtype   string
	}{
		{"none", "", "", "", "", utf8Locale()},
		{"utf-8 LANG", "", "", "zh_CN.UTF-8", "", ""},
		{"ascii LANG", "", "", "C", "", utf8Locale()},
		{"utf-8 LC_CTYPE", "", "en_US.utf8", "C", "", "en_US.utf8"},
		{"ascii LC_ALL", "C", "", "en_US.UTF-8", utf8Locale(), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LC_ALL", c.lcAll)
			t.Setenv("LC_CTYPE", c.lcCtype)
			t.Setenv("LANG", c.lang)
			ensureUTF8()
			if got := os.Getenv("LC_ALL"); got != c.wantAll {
				t.Errorf("LC_ALL = %q, want %q", got, c.wantAll)
			}
			if got := os.Getenv("LC_CTYPE"); got != c.wantCtype {
				t.Errorf("LC_CTYPE = %q, want %q", got, c.wantCtype)
			}
		})
	}
}
