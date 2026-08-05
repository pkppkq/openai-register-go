package models

import "testing"

func TestProxyHealthSummary(t *testing.T) {
	ok := ProxyHealthResult{
		Success: true, IP: "1.2.3.4", Country: "JP", Region: "Tokyo", City: "Tokyo",
		Timezone: "Asia/Tokyo", Org: "AS123 X", ChatGPTStatus: 200, StripeStatus: 402,
	}
	want := "1.2.3.4 JP/Tokyo/Tokyo Asia/Tokyo AS123 X ChatGPT=200 Stripe=402"
	if got := ok.Summary(); got != want {
		t.Fatalf("success summary = %q, want %q", got, want)
	}

	// success with empty geo still carries the status parts
	bare := ProxyHealthResult{Success: true, ChatGPTStatus: 403}
	if got := bare.Summary(); got != "ChatGPT=403 Stripe=0" {
		t.Fatalf("bare summary = %q", got)
	}

	fail := ProxyHealthResult{Success: false, FailedStage: "出口", Error: "HTTP 429"}
	if got := fail.Summary(); got != "检测失败[出口]: HTTP 429" {
		t.Fatalf("fail summary = %q", got)
	}

	failNoErr := ProxyHealthResult{Success: false}
	if got := failNoErr.Summary(); got != "检测失败[unknown]" {
		t.Fatalf("fail-no-err summary = %q", got)
	}
}
