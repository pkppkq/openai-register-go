package openai

import "testing"

func TestIsCloudflareChallengeText(t *testing.T) {
	yes := []string{
		"Just a moment...",
		"JUST A MOMENT",
		"Checking your browser before accessing",
		"Attention Required! | Cloudflare",
		"<script src='/cdn-cgi/challenge-platform/x'>",
		"https://challenges.cloudflare.com/turnstile/v0/api.js",
		"__cf_chl_tk=abc",
		"<div class='cf-turnstile'></div>",
		"Verify you are human",
		"needs to review the security of your connection",
		"cf-browser-verification",
		"cloudflare says: challenge required", // AND-combo
		"turnstile widget by cloudflare",      // AND-combo
	}
	for _, s := range yes {
		if !IsCloudflareChallengeText(s) {
			t.Fatalf("IsCloudflareChallengeText(%q) = false, want true", s)
		}
	}
	no := []string{
		"",
		"Sign up for ChatGPT",
		"cloudflare",  // alone, no 'challenge'/'turnstile'
		"a challenge", // alone, no 'cloudflare'
		"Welcome back",
	}
	for _, s := range no {
		if IsCloudflareChallengeText(s) {
			t.Fatalf("IsCloudflareChallengeText(%q) = true, want false", s)
		}
	}
}

func TestExtractCloudflareChallengeURL(t *testing.T) {
	// obfuscated var, relative -> absolute
	if got := ExtractCloudflareChallengeURL(`var x = {cUPMDTk: "\/cdn-cgi\/challenge?a=1"};`); got != AuthBaseURL+"/cdn-cgi/challenge?a=1" {
		t.Fatalf("obfuscated-var extract = %q", got)
	}
	// obfuscated var, absolute stays
	if got := ExtractCloudflareChallengeURL(`cUPMDTk: "https://auth.openai.com/x/y"`); got != "https://auth.openai.com/x/y" {
		t.Fatalf("absolute extract = %q", got)
	}
	// history.replaceState fallback
	if got := ExtractCloudflareChallengeURL(`history.replaceState(null,null,"\/foo\/bar")`); got != AuthBaseURL+"/foo/bar" {
		t.Fatalf("replaceState extract = %q", got)
	}
	// HTML-escaped input is unescaped first
	if got := ExtractCloudflareChallengeURL(`cUPMDTk: &quot;/esc&quot;`); got != AuthBaseURL+"/esc" {
		t.Fatalf("escaped extract = %q", got)
	}
	// direct challenge URL fallback
	if got := ExtractCloudflareChallengeURL(`please visit https://challenges.cloudflare.com/abc/def now`); got != "https://challenges.cloudflare.com/abc/def" {
		t.Fatalf("direct extract = %q", got)
	}
	// nothing
	if got := ExtractCloudflareChallengeURL("plain page"); got != "" {
		t.Fatalf("no-match extract = %q, want empty", got)
	}
}
