package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// TestEmbeddedJSSyntax syntax-checks every JavaScript payload this package
// injects. See the sibling test in internal/worker for why this guard exists:
// an invalid injected script fails silently, and the Python original shipped
// two such dead blobs for a long time — including the fingerprint init script
// embedded here, whose stray '}' meant the worker's anti-detection spoofs never
// ran at all.
func TestEmbeddedJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping embedded-JS syntax check")
	}

	// Expressions: bare function/arrow literals, wrapped in parens to stand alone.
	exprs := map[string]string{
		"dumpStorageJS":        dumpStorageJS,
		"seedStorageJS":        seedStorageJS,
		"seedSessionStorageJS": seedSessionStorageJS,
		"clickButtonByTextJS":  clickButtonByTextJS,
		"reactFillJS":          reactFillJS,
		"clickSubmitByDOMJS":   clickSubmitByDOMJS,
		"sessionProbeJS":       sessionProbeJS,
	}
	// Statements: complete programs, checked as-is.
	stmts := map[string]string{
		// The real substituted script, not just the raw template — a payload
		// substitution that produced invalid JS would otherwise slip through.
		"FingerprintInitScript": FingerprintInitScript(models.GenerateRegisterFingerprint()),
	}

	dir := t.TempDir()
	check := func(t *testing.T, name, body string) {
		t.Helper()
		if strings.TrimSpace(body) == "" {
			t.Fatalf("%s is empty", name)
		}
		path := filepath.Join(dir, name+".js")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
			t.Fatalf("%s is not valid JavaScript:\n%s", name, out)
		}
	}
	for name, src := range exprs {
		t.Run(name, func(t *testing.T) { check(t, name, "(\n"+src+"\n)\n") })
	}
	for name, src := range stmts {
		t.Run(name, func(t *testing.T) { check(t, name, src+"\n") })
	}
}
