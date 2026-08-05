package worker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedJSSyntax parses every JavaScript payload this package injects into
// the browser. A malformed injected script fails SILENTLY at runtime (the
// browser just rejects it), which is exactly how two bugs survived in the Python
// original:
//
//   - _install_fingerprint's script had a stray '}' — Playwright dropped it, so
//     the worker's anti-detection spoofs never actually ran.
//   - click_trial_claim_button_on_page's scoring fallback lives in a NON-raw
//     triple-quoted string, so `\t\n\r` inside `[\t\n\r ]*` became real control
//     characters inside a JS regex literal — a SyntaxError, so that fallback
//     never executed either.
//
// These are the real runtime values (Go consts), not a source scrape, so what is
// checked here is exactly what reaches Chrome.
func TestEmbeddedJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping embedded-JS syntax check")
	}

	payloads := map[string]string{
		"aboutYouBodyTextJS":                 aboutYouBodyTextJS,
		"aboutYouVisibleControlCountJS":      aboutYouVisibleControlCountJS,
		"aboutYouElementRectJS":              aboutYouElementRectJS,
		"aboutYouFinishFormSubmitJS":         aboutYouFinishFormSubmitJS,
		"aboutYouFieldMetaJS":                aboutYouFieldMetaJS,
		"aboutYouDOMFillJS":                  aboutYouDOMFillJS,
		"aboutYouKeyboardBoxJS":              aboutYouKeyboardBoxJS,
		"aboutYouKeyboardCommitJS":           aboutYouKeyboardCommitJS,
		"aboutYouVisibleInputValuesJS":       aboutYouVisibleInputValuesJS,
		"aboutYouFocusSubmitOrBodyJS":        aboutYouFocusSubmitOrBodyJS,
		"aboutYouBlurActiveJS":               aboutYouBlurActiveJS,
		"authBodyInnerTextJS":                authBodyInnerTextJS,
		"authLocateClickTargetJS":            authLocateClickTargetJS,
		"emailOTPValidateJS":                 emailOTPValidateJS,
		"payLinkSessionProbeJS":              payLinkSessionProbeJS,
		"trialClaimRoleButtonJS":             trialClaimRoleButtonJS,
		"trialClaimScoreJS":                  trialClaimScoreJS,
		"phoneInputMetaJS":                   phoneInputMetaJS,
		"phoneClickUsePhoneNumberContinueJS": phoneClickUsePhoneNumberContinueJS,
		"phoneSelectUSCountryJS":             phoneSelectUSCountryJS,
		"phoneSelectUSOptionJS":              phoneSelectUSOptionJS,
		"phoneCodeInputMetaJS":               phoneCodeInputMetaJS,
		"phoneClickContinueLadderJS":         phoneClickContinueLadderJS,
		"selectTeamWorkspaceJS":              selectTeamWorkspaceJS,
		"completeTeamOnboardingJS":           completeTeamOnboardingJS,
		"approveSSOLoginJS":                  approveSSOLoginJS,
		"clickCodexConsentJS":                clickCodexConsentJS,
	}

	dir := t.TempDir()
	for name, src := range payloads {
		t.Run(name, func(t *testing.T) {
			if strings.TrimSpace(src) == "" {
				t.Fatalf("%s is empty", name)
			}
			// A JS regex literal cannot contain a raw line terminator; catching
			// this explicitly gives a far clearer failure than node's parser.
			if i := strings.IndexAny(src, "\t\r"); i >= 0 && strings.Contains(src[:i], "/") {
				if strings.Count(src[:i], "/")%2 == 1 {
					t.Fatalf("%s: raw control character inside what looks like a regex literal at offset %d", name, i)
				}
			}
			// Wrap the bare function/arrow expression so it stands alone.
			path := filepath.Join(dir, name+".js")
			if err := os.WriteFile(path, []byte("(\n"+src+"\n)\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
				t.Fatalf("%s is not valid JavaScript:\n%s", name, out)
			}
		})
	}
}
