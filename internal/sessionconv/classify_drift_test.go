package sessionconv

import (
	"testing"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

// ClassifyPlanText is a hand-copy of openai.ClassifyChatGPTPlanText — both port
// classify_chatgpt_plan_text (app.py:5684-5696), kept separate so sessionconv
// carries no cross-gap dependency. The comment saying "keep the two in sync" is not
// a mechanism; this is.
//
// The classifier decides whether an account reads as team / k12 / plus / free, and
// that answer selects money paths (a Team invite creates a billable seat), so a
// silent drift between the two copies is not cosmetic.
//
// The production code still does NOT import openai — only this test does, so the
// dependency is test-only and the separation the comment describes is preserved.
func TestClassifyPlanTextMatchesTheCanonicalCopy(t *testing.T) {
	corpus := []string{
		// Branch 1: team, and the traps that must not reach a later branch.
		"team", "enterprise", "Business Plan", "school team", "chatgpt_team_plan", "free team",
		// Branch 2: k12, checked before plus.
		"k12", "K-12 Teacher", "school", "teacher plus",
		// Branch 3: plus, a SUBSTRING test that deliberately swallows pro/product.
		"plus", "ChatGPT Pro", "chatgptplusplan", "Professional", "product", "pro free",
		// Branch 4: free.
		"free", "chatgpt_free_plan", "no-plan", "None",
		// No match, plus whitespace and case handling.
		"", "   ", "unknown", "gpt-4", "TEAM", "  Plus  ", "\tK12\n",
		// Unicode whitespace: Python's strip() is wider than strings.TrimSpace, and
		// the two copies must at least agree with each other about it.
		" team ", "　plus　", "free",
	}
	for _, in := range corpus {
		want := openai.ClassifyChatGPTPlanText(in)
		if got := ClassifyPlanText(in); got != want {
			t.Errorf("ClassifyPlanText(%q) = %q, but openai.ClassifyChatGPTPlanText says %q",
				in, got, want)
		}
	}
}
