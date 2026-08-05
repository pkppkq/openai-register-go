package phoneprovider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ManualFetchTimeout 是“手动取码”的固定等待上限。该路径只读取调用方已经
// 保存的 HTTP(S) 接码链接，不构造 Provider，也不可能进入 SMSBower 租号分支。
const ManualFetchTimeout time.Duration = 30 * time.Second

// ValidateManualSMSURL 拒绝空链接、相对链接及所有非 HTTP(S) 协议。
func ValidateManualSMSURL(rawURL string) error {
	text := strings.TrimSpace(rawURL)
	if text == "" {
		return errors.New("该手机号未保存短信链接")
	}
	parsed, err := url.ParseRequestURI(text)
	if err != nil || parsed.Host == "" {
		return errors.New("短信链接格式无效，必须是 http:// 或 https:// 地址")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("短信链接格式无效，必须是 http:// 或 https:// 地址")
	}
	return nil
}

// FetchManualCode 最多轮询 30 秒并复用本包与注册流程相同的验证码提取器。
//
// 这里不复用 Provider.waitForPhoneCode：后者的 Sleep 无法被 context 立即
// 打断，且成功时会调用 Pool.RecordCode。手动查看必须可取消，也不得增加接码
// 次数，因此仅复用真正共享的 extractPhoneCode 规则。
func FetchManualCode(ctx context.Context, number, smsURL string) (string, error) {
	smsURL = strings.TrimSpace(smsURL)
	if err := ValidateManualSMSURL(smsURL); err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, cancel := context.WithTimeout(ctx, ManualFetchTimeout)
	defer cancel()

	lastText := ""
	for {
		if err := waitCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", manualFetchTimeoutError(number, lastText)
		}

		requestTimeout := manualRequestTimeout(waitCtx)
		text, err := defaultHTTPGet(waitCtx, smsURL, requestTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastText = err.Error()
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return "", manualFetchTimeoutError(number, lastText)
			}
		} else {
			text = strings.TrimSpace(text)
			lastText = truncRunes(text, 300)
			if code := extractPhoneCode(text); code != "" {
				return code, nil
			}
		}

		delay := manualPollInterval
		if deadline, ok := waitCtx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return "", manualFetchTimeoutError(number, lastText)
			}
			if remaining < delay {
				delay = remaining
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", manualFetchTimeoutError(number, lastText)
		case <-timer.C:
		}
	}
}

func manualRequestTimeout(ctx context.Context) time.Duration {
	timeout := 20 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		seconds := int(time.Until(deadline).Seconds())
		if seconds > 20 {
			seconds = 20
		}
		if seconds < 1 {
			seconds = 1
		}
		timeout = time.Duration(seconds) * time.Second
	}
	return timeout
}

func manualFetchTimeoutError(number, lastText string) error {
	return fmt.Errorf("等待手机号 %s 短信验证码超时，最后返回: %s", number, lastText)
}
