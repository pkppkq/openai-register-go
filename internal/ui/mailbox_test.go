package ui

// 本文件的邮箱 Reader 全部是内存替身；不会访问真实邮箱、邀请链接或代理。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
)

type fakeMailboxReader struct {
	connect      func() error
	close        func() error
	listFolders  func() ([]string, error)
	listMessages func(string, int, string) ([]mail.MailRecord, error)
	fetchMessage func(string, string) (mail.MailRecord, error)
	connectCalls atomic.Int32
	closeCalls   atomic.Int32
	folderCalls  atomic.Int32
	messageCalls atomic.Int32
	fetchCalls   atomic.Int32
}

func (f *fakeMailboxReader) Connect() error {
	f.connectCalls.Add(1)
	if f.connect != nil {
		return f.connect()
	}
	return nil
}

func (f *fakeMailboxReader) Close() error {
	f.closeCalls.Add(1)
	if f.close != nil {
		return f.close()
	}
	return nil
}

func (f *fakeMailboxReader) ListFolders() ([]string, error) {
	f.folderCalls.Add(1)
	if f.listFolders != nil {
		return f.listFolders()
	}
	return []string{}, nil
}

func (f *fakeMailboxReader) ListRecentMessages(folder string, limit int, query string) ([]mail.MailRecord, error) {
	f.messageCalls.Add(1)
	if f.listMessages != nil {
		return f.listMessages(folder, limit, query)
	}
	return []mail.MailRecord{}, nil
}

func (f *fakeMailboxReader) FetchMessage(folder, messageID string) (mail.MailRecord, error) {
	f.fetchCalls.Add(1)
	if f.fetchMessage != nil {
		return f.fetchMessage(folder, messageID)
	}
	return mail.MailRecord{}, nil
}

func installMailboxReaderFactory(t *testing.T, factory mailboxReaderFactory) {
	t.Helper()
	old := mailboxNewReader
	mailboxNewReader = factory
	t.Cleanup(func() {
		mailboxNewReader = old
	})
}

func mailboxOutlookFixture(email string) map[string]any {
	return map[string]any{
		"email":         email,
		"password":      "mail-password-secret",
		"client_id":     "mail-client-id",
		"refresh_token": "mail-refresh-secret",
		"raw":           email + "----mail-password-secret----mail-client-id----mail-refresh-secret",
		"account_type":  "free",
		"status":        "待处理",
		"group":         models.AccountDefaultGroup,
	}
}

func TestMailboxBindingsUseFakeReaderAndCloseEveryCall(t *testing.T) {
	const email = "reader@example.com"

	t.Run("列出文件夹并保存轮换Token", func(t *testing.T) {
		app := newNetworkOpsTestApp(t, []any{mailboxOutlookFixture(email)}, map[string]any{})
		reader := &fakeMailboxReader{
			listFolders: func() ([]string, error) {
				return []string{"INBOX", "Junk Email", "Archive"}, nil
			},
		}
		installMailboxReaderFactory(t, func(account *models.MailAccount, _ mail.Log, proxyURL string) (mailboxReader, error) {
			if account.Email != email || proxyURL != "" {
				t.Fatalf("Reader 参数不符: email=%q proxy=%q", account.Email, proxyURL)
			}
			reader.connect = func() error {
				account.RefreshToken = "mail-refresh-rotated"
				account.Raw = email + "----mail-password-secret----mail-client-id----mail-refresh-rotated"
				return nil
			}
			return reader, nil
		})

		folders, err := app.ListMailboxFolders(email)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(folders, "|"); got != "INBOX|Junk Email|Archive" {
			t.Fatalf("文件夹不符: %q", got)
		}
		if reader.connectCalls.Load() != 1 || reader.folderCalls.Load() != 1 || reader.closeCalls.Load() != 1 {
			t.Fatalf("Reader 生命周期不符: connect=%d list=%d close=%d",
				reader.connectCalls.Load(), reader.folderCalls.Load(), reader.closeCalls.Load())
		}
		account, _ := loadedNetworkAccount(t, app, email)
		if account.RefreshToken != "mail-refresh-rotated" ||
			!strings.HasSuffix(account.Raw, "----mail-refresh-rotated") {
			t.Fatalf("轮换 Token 未保存: %#v", account)
		}
	})

	t.Run("列出邮件会限制数量且不返回正文", func(t *testing.T) {
		app := newNetworkOpsTestApp(t, []any{mailboxOutlookFixture(email)}, map[string]any{})
		reader := &fakeMailboxReader{
			listMessages: func(folder string, limit int, query string) ([]mail.MailRecord, error) {
				if folder != "INBOX" || limit != 10 || query != "OpenAI" {
					t.Fatalf("列表参数不符: folder=%q limit=%d query=%q", folder, limit, query)
				}
				return []mail.MailRecord{{
					ID:          "m-1",
					Folder:      "INBOX",
					Kind:        "验证码",
					Code:        "048213",
					Subject:     "OpenAI verification",
					From:        "noreply@openai.com",
					To:          email,
					Date:        "Mon, 27 Jul 2026 12:00:00 +0000",
					MailTime:    1_785_153_600,
					MailTimeISO: "2026-07-27T20:00:00",
					Snippet:     "Your verification code is 048213",
					Body:        "这段完整正文不应由列表接口返回",
				}}, nil
			},
		}
		installMailboxReaderFactory(t, func(*models.MailAccount, mail.Log, string) (mailboxReader, error) {
			return reader, nil
		})

		messages, err := app.ListMailboxMessages(MailboxMessagesRequest{
			Email: email, Limit: 1, Query: "  OpenAI  ",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 1 || messages[0].ID != "m-1" || messages[0].Code != "048213" ||
			messages[0].MailTimeISO != "2026-07-27T20:00:00" || messages[0].Body != "" {
			t.Fatalf("邮件列表结果不符: %#v", messages)
		}
		if reader.connectCalls.Load() != 1 || reader.messageCalls.Load() != 1 || reader.closeCalls.Load() != 1 {
			t.Fatalf("Reader 生命周期不符: connect=%d list=%d close=%d",
				reader.connectCalls.Load(), reader.messageCalls.Load(), reader.closeCalls.Load())
		}
	})

	t.Run("读取正文返回完整邮件", func(t *testing.T) {
		app := newNetworkOpsTestApp(t, []any{mailboxOutlookFixture(email)}, map[string]any{})
		reader := &fakeMailboxReader{
			fetchMessage: func(folder, messageID string) (mail.MailRecord, error) {
				if folder != "Archive" || messageID != "graph-id/1" {
					t.Fatalf("正文参数不符: folder=%q id=%q", folder, messageID)
				}
				return mail.MailRecord{
					ID: "graph-id/1", Folder: folder, Subject: "Workspace invitation",
					Body: "Accept: https://chatgpt.com/admin/invite/accept?token=fake-only",
				}, nil
			},
		}
		installMailboxReaderFactory(t, func(*models.MailAccount, mail.Log, string) (mailboxReader, error) {
			return reader, nil
		})

		message, err := app.GetMailboxMessage(MailboxMessageRequest{
			Email: email, Folder: " Archive ", ID: " graph-id/1 ",
		})
		if err != nil {
			t.Fatal(err)
		}
		if message.ID != "graph-id/1" || !strings.Contains(message.Body, "fake-only") {
			t.Fatalf("正文结果不符: %#v", message)
		}
		if reader.connectCalls.Load() != 1 || reader.fetchCalls.Load() != 1 || reader.closeCalls.Load() != 1 {
			t.Fatalf("Reader 生命周期不符: connect=%d fetch=%d close=%d",
				reader.connectCalls.Load(), reader.fetchCalls.Load(), reader.closeCalls.Load())
		}
	})
}

func TestMailboxExtractionBindingsAreOffline(t *testing.T) {
	app := &App{}
	if got := app.ExtractMailboxCode("Your ChatGPT verification code is 739210."); got != "739210" {
		t.Fatalf("验证码=%q", got)
	}
	invite := "https://chatgpt.com/admin/invite/accept?token=fake-team-token"
	if got := app.ExtractMailboxInviteLink("Accept invitation: " + invite); got != invite {
		t.Fatalf("邀请链接=%q", got)
	}
	for _, text := range []string{
		"https://chatgpt.com/k12-invite?wId=fake-school",
		"https://example.com/team/invite?token=fake",
	} {
		if got := app.ExtractMailboxInviteLink(text); got != "" {
			t.Fatalf("不可信链接不应通过: %q -> %q", text, got)
		}
	}
}

func TestMailboxBindingValidationRefusesBeforeReaderCreation(t *testing.T) {
	app := newNetworkOpsTestApp(t, []any{map[string]any{
		"email": "missing@example.com", "password": "pw",
	}}, map[string]any{})
	var factoryCalls atomic.Int32
	installMailboxReaderFactory(t, func(*models.MailAccount, mail.Log, string) (mailboxReader, error) {
		factoryCalls.Add(1)
		return &fakeMailboxReader{}, nil
	})

	if _, err := app.ListMailboxFolders("missing@example.com"); err == nil ||
		!strings.Contains(err.Error(), "没有可用") {
		t.Fatalf("缺少配置应拒绝，实际错误: %v", err)
	}
	if _, err := app.ListMailboxMessages(MailboxMessagesRequest{
		Email: "missing@example.com", Folder: strings.Repeat("x", 513),
	}); err == nil || !strings.Contains(err.Error(), "文件夹名称过长") {
		t.Fatalf("过长文件夹应拒绝，实际错误: %v", err)
	}
	if _, err := app.GetMailboxMessage(MailboxMessageRequest{
		Email: "missing@example.com", ID: "",
	}); err == nil || !strings.Contains(err.Error(), "邮件 ID 为空") {
		t.Fatalf("空 ID 应拒绝，实际错误: %v", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("拒绝路径创建了 Reader: %d", factoryCalls.Load())
	}
}

func TestMailboxBindingTimeoutClosesBlockingFakeReader(t *testing.T) {
	const email = "timeout@example.com"
	app := newNetworkOpsTestApp(t, []any{mailboxOutlookFixture(email)}, map[string]any{})
	started := make(chan struct{})
	closed := make(chan struct{})
	var startOnce sync.Once
	var closeOnce sync.Once
	reader := &fakeMailboxReader{
		connect: func() error {
			startOnce.Do(func() { close(started) })
			<-closed
			return context.Canceled
		},
		close: func() error {
			closeOnce.Do(func() { close(closed) })
			return nil
		},
	}
	installMailboxReaderFactory(t, func(*models.MailAccount, mail.Log, string) (mailboxReader, error) {
		return reader, nil
	})
	oldTimeout := mailboxOperationTimeout
	mailboxOperationTimeout = 25 * time.Millisecond
	t.Cleanup(func() {
		mailboxOperationTimeout = oldTimeout
	})

	_, err := app.ListMailboxFolders(email)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("阻塞 Reader 应超时，实际错误: %v", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("Reader 未进入阻塞 Connect")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("超时后未关闭 Reader")
	}
	if reader.closeCalls.Load() != 1 {
		t.Fatalf("Reader Close 次数=%d，期望 1", reader.closeCalls.Load())
	}
}

func TestMailboxErrorsAndLogsRedactSecrets(t *testing.T) {
	const email = "secret@example.com"
	app := newNetworkOpsTestApp(t, []any{mailboxOutlookFixture(email)}, map[string]any{})
	var logs []string
	app.logSink = func(line string) {
		logs = append(logs, line)
	}
	installMailboxReaderFactory(t, func(account *models.MailAccount, log mail.Log, _ string) (mailboxReader, error) {
		log(fmt.Sprintf(
			"authorization: Bearer saved-token proxy=https://proxy-user:proxy-pass@proxy.invalid password=%s",
			account.Password,
		))
		return nil, errors.New(
			"refresh_token=mail-refresh-secret cloud_mail_token=saved-token " +
				"proxy=https://proxy-user:proxy-pass@proxy.invalid password=mail-password-secret",
		)
	})

	_, err := app.ListMailboxFolders(email)
	if err == nil {
		t.Fatal("fake Reader 创建错误应返回")
	}
	combined := err.Error() + "\n" + strings.Join(logs, "\n")
	for _, secret := range []string{
		"mail-refresh-secret",
		"saved-token",
		"mail-password-secret",
		"proxy-user",
		"proxy-pass",
	} {
		if strings.Contains(combined, secret) {
			t.Fatalf("错误或日志泄露敏感值 %q: %s", secret, combined)
		}
	}
	if !strings.Contains(combined, "***") {
		t.Fatalf("脱敏结果缺少占位符: %s", combined)
	}
}
