package openai

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestEmbeddedJSSyntax syntax-checks the Cloudflare interstitial detector. An
// invalid injected script fails silently in the browser, so it would report
// "no challenge" forever. See internal/worker's sibling test for the two real
// bugs of this class found in the Python original.
func TestEmbeddedJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping embedded-JS syntax check")
	}
	path := filepath.Join(t.TempDir(), "cf_detect.js")
	if err := os.WriteFile(path, []byte("(\n"+CFInterstitialDetectJS+"\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("CFInterstitialDetectJS is not valid JavaScript:\n%s", out)
	}
}
