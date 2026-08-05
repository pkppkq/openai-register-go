package phoneprovider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFetchManualCodePollsHTTPOnlyAndReusesExtractor(t *testing.T) {
	if ManualFetchTimeout != 30*time.Second {
		t.Fatalf("ManualFetchTimeout = %s, want 30s", ManualFetchTimeout)
	}
	var (
		mu     sync.Mutex
		method string
		path   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method, path = r.Method, r.URL.RequestURI()
		mu.Unlock()
		// Python 基线不因非 2xx 丢弃正文；正文仍交给共享提取器。
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("通知：OpenAI 登录验证码是 １２３４５６，请勿泄露"))
	}))
	defer server.Close()

	code, err := FetchManualCode(context.Background(), "+15550000", server.URL+"/saved?phone=1")
	if err != nil {
		t.Fatalf("FetchManualCode: %v", err)
	}
	if code != "１２３４５６" {
		t.Fatalf("code = %q", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if method != http.MethodGet || path != "/saved?phone=1" {
		t.Fatalf("请求 = %s %s", method, path)
	}
}

func TestFetchManualCodeCancellationInterruptsRequest(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := FetchManualCode(ctx, "+15550001", server.URL)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("请求未进入回环服务")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消未及时中断手动取码")
	}
}

func TestValidateManualSMSURLRejectsRentalAndRelativeSchemes(t *testing.T) {
	for _, test := range []struct {
		name string
		url  string
	}{
		{name: "empty", url: ""},
		{name: "relative", url: "/sms/1"},
		{name: "ftp", url: "ftp://example.com/sms"},
		{name: "smsbower", url: "smsbower://activation/123"},
		{name: "hostless", url: "https:///missing-host"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateManualSMSURL(test.url)
			if err == nil {
				t.Fatalf("ValidateManualSMSURL(%q) unexpectedly succeeded", test.url)
			}
			if test.url != "" && !strings.Contains(err.Error(), "http://") {
				t.Fatalf("错误没有说明协议约束: %v", err)
			}
		})
	}
	if err := ValidateManualSMSURL("https://127.0.0.1/sms"); err != nil {
		t.Fatalf("合法 HTTPS URL: %v", err)
	}
}
