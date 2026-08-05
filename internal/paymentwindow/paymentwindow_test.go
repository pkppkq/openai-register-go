package paymentwindow

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestValidateLinkRequiresHTTPS(t *testing.T) {
	for _, valid := range []string{
		"https://pay.openai.com/c/pay/cs_fixture",
		"https://www.paypal.com/agreements/approve?ba_token=fixture",
		"https://external-provider.example/redirect",
	} {
		if err := ValidateLink(valid); err != nil {
			t.Errorf("ValidateLink(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"",
		"javascript:alert(1)",
		"http://pay.openai.com/c/pay/cs_fixture",
		"file:///C:/secret.txt",
		"https:///missing-host",
	} {
		if err := ValidateLink(invalid); err == nil {
			t.Errorf("ValidateLink(%q) 应拒绝", invalid)
		}
	}
}

func TestValidateExtensionAndSeedPreferences(t *testing.T) {
	dir := t.TempDir()
	if _, err := ValidateExtensionDir(dir); err == nil {
		t.Fatal("缺少 manifest.json 的扩展目录应拒绝")
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ValidateExtensionDir(dir)
	if err != nil || got == "" {
		t.Fatalf("ValidateExtensionDir=%q, %v", got, err)
	}

	profile := t.TempDir()
	if err := seedBrowserPreferences(profile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(profile, "Default", "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var prefs map[string]any
	if err := json.Unmarshal(raw, &prefs); err != nil {
		t.Fatalf("Preferences 不是 JSON: %v", err)
	}
	autofill, _ := prefs["autofill"].(map[string]any)
	if autofill["credit_card_enabled"] != false || prefs["credentials_enable_service"] != false {
		t.Fatalf("自动填充/密码管理未禁用: %#v", prefs)
	}
}

func TestPaymentFingerprintReusesSavedValue(t *testing.T) {
	saved := models.GenerateRegisterFingerprint()
	health := models.ProxyHealthResult{
		Success: true, IP: "203.0.113.1", Country: "JP", Timezone: "Asia/Tokyo",
	}
	got, reused, err := paymentFingerprint(&saved, health)
	if err != nil {
		t.Fatal(err)
	}
	if !reused || got.UserAgent != saved.UserAgent || got.Timezone != saved.Timezone {
		t.Fatalf("保存指纹未原样复用: reused=%v got=%#v", reused, got)
	}

	generated, reused, err := paymentFingerprint(nil, health)
	if err != nil {
		t.Fatal(err)
	}
	if reused || !models.ValidFingerprint(&generated) || generated.Timezone != "Asia/Tokyo" {
		t.Fatalf("出口指纹生成异常: reused=%v fp=%#v", reused, generated)
	}
}

func TestEmbeddedPaymentJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 未安装，跳过内嵌 JavaScript 语法检查")
	}
	dir := t.TempDir()
	seedRaw, _ := json.Marshal(paymentSeed{Phone: "+1", Card: "card", SMSURL: "https://sms.invalid"})
	seedProgram := strings.Replace(paymentSeedInitJS, "__PAYMENT_SEED__", string(seedRaw), 1)
	sources := map[string]string{
		"seed":      seedProgram,
		"autofill":  "(\n" + paymentAutofillJS + "\n)",
		"confirm":   "(\n" + openAIConfirmJS + "\n)",
		"agreement": "(\n" + payPalAgreementJS + "\n)",
	}
	for name, source := range sources {
		path := filepath.Join(dir, name+".js")
		if err := os.WriteFile(path, []byte(source+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
			t.Fatalf("%s JavaScript 语法错误: %v\n%s", name, err, output)
		}
	}
}
