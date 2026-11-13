package codex

import (
	"context"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"testing"
	"testing/synctest"
)

type footerTerm struct{ footer string }

func (t *footerTerm) Context() context.Context { return context.Background() }
func (t *footerTerm) Write([]byte) error       { return nil }
func (t *footerTerm) Capture() (string, error) {
	return "\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n  " + t.footer + "\n", nil
}
func TestModelRejectionVerifiesRestoredOrRequestedFooter(t *testing.T) {
	for _, tc := range []struct {
		footer         string
		final, success bool
	}{{"GPT-6.1-Sol low · /tmp/work", true, false}, {"GPT-6-Luna high · /tmp/work", true, true}, {"GPT-6-Luna xhigh · /tmp/high", false, false}} {
		t.Run(tc.footer, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				term, w := agent.ObserveNative(&footerTerm{footer: tc.footer})
				_ = term.Write([]byte("3"))
				err := w.Failure(modelRejection(term, "GPT-6.1-Sol low · /tmp/work", "GPT-6-Luna", "high", agent.ErrUnsupported))
				if tc.success {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if err == nil || outcome.IsFinalError(err) != tc.final {
						t.Fatal(err)
					}
				}
			})
		})
	}
}

func TestModelFooterMatchesExactModelAndEffort(t *testing.T) {
	for _, tc := range []struct {
		footer string
		want   bool
	}{
		{"GPT-6-Luna high · /tmp/work", true},
		{"GPT-6-Luna xhigh · /tmp/high", false},
		{"GPT-6-Luna-mini high · /tmp/work", false},
		{"GPT-6.1-Sol low · /tmp/GPT-6-Luna high", false},
	} {
		if got := footerHasModel(tc.footer, "GPT-6-Luna", "high"); got != tc.want {
			t.Errorf("%q: %v", tc.footer, got)
		}
	}
}
