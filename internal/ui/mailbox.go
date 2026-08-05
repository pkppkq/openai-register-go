package ui

// 本文件提供邮箱管理窗口所需的只读 Wails 绑定。
//
// 邮箱网络连接仍统一复用 internal/mail.Reader；绑定只负责账号解析、代理链、
// 超时、资源回收和敏感信息脱敏，不会删除、移动或标记邮件。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// mailboxOperationTimeout 是一次“连接 + 读取”的总时限。邮箱 Reader 内部还
// 有各自的 HTTP/IMAP 超时；这里的总边界防止 Wails Promise 无限悬挂。
var mailboxOperationTimeout = 90 * time.Second

// mailboxReader 是邮箱管理实际使用的最小只读能力面。测试替身无需实现取码
// 轮询、封禁扫描等无关接口，因此更难意外触发真实邮箱访问。
type mailboxReader interface {
	Connect() error
	Close() error
	ListFolders() ([]string, error)
	ListRecentMessages(folder string, maxMessages int, query string) ([]mail.MailRecord, error)
	FetchMessage(folder, messageID string) (mail.MailRecord, error)
}

type mailboxReaderFactory func(*models.MailAccount, mail.Log, string) (mailboxReader, error)

var mailboxNewReader mailboxReaderFactory = func(account *models.MailAccount, log mail.Log, proxyURL string) (mailboxReader, error) {
	return mail.CreateMailReader(account, log, proxyURL)
}

// MailboxMessagesRequest 是邮箱列表的筛选条件。
type MailboxMessagesRequest struct {
	Email  string `json:"email"`
	Folder string `json:"folder"`
	Limit  int    `json:"limit"`
	Query  string `json:"query"`
}

// MailboxMessageRequest 唯一定位一封待读取正文的邮件。
type MailboxMessageRequest struct {
	Email  string `json:"email"`
	Folder string `json:"folder"`
	ID     string `json:"id"`
}

// MailboxMessage 是适合直接传给前端的标准化邮件。列表接口不会返回 Body，
// 只有 GetMailboxMessage 会携带完整正文。
type MailboxMessage struct {
	ID          string  `json:"id"`
	Folder      string  `json:"folder"`
	Kind        string  `json:"kind"`
	Code        string  `json:"code"`
	Subject     string  `json:"subject"`
	From        string  `json:"from"`
	To          string  `json:"to"`
	Date        string  `json:"date"`
	MailTime    float64 `json:"mailTime"`
	MailTimeISO string  `json:"mailTimeIso"`
	Snippet     string  `json:"snippet"`
	Body        string  `json:"body,omitempty"`
}

type mailboxOperationValue struct {
	folders  []string
	messages []MailboxMessage
	message  MailboxMessage
}

type mailboxOperationResult struct {
	value mailboxOperationValue
	err   error
}

type mailboxOperation func(mailboxReader) (mailboxOperationValue, error)

// mailboxResources 允许超时分支关闭尚在阻塞的 Reader/代理链。attach 与 close
// 互斥，保证即使超时发生在资源创建过程中，稍后创建出的资源也会立即回收。
type mailboxResources struct {
	mu     sync.Mutex
	closed bool
	reader mailboxReader
	proxy  *proxySession
}

func (r *mailboxResources) attachProxy(proxy *proxySession) bool {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		proxy.Close()
		return false
	}
	r.proxy = proxy
	r.mu.Unlock()
	return true
}

func (r *mailboxResources) attachReader(reader mailboxReader) bool {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = reader.Close()
		return false
	}
	r.reader = reader
	r.mu.Unlock()
	return true
}

func (r *mailboxResources) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	reader := r.reader
	proxy := r.proxy
	r.reader = nil
	r.proxy = nil
	r.mu.Unlock()

	if reader != nil {
		_ = reader.Close()
	}
	proxy.Close()
}

// ListMailboxFolders 连接指定账号的邮箱并返回可读文件夹。
func (a *App) ListMailboxFolders(email string) ([]string, error) {
	value, err := a.runMailboxOperation(email, func(reader mailboxReader) (mailboxOperationValue, error) {
		folders, err := reader.ListFolders()
		if err != nil {
			return mailboxOperationValue{}, err
		}
		if folders == nil {
			folders = []string{}
		}
		return mailboxOperationValue{folders: folders}, nil
	})
	return value.folders, err
}

// ListMailboxMessages 返回指定文件夹最近的邮件元数据，不传输完整正文。
func (a *App) ListMailboxMessages(req MailboxMessagesRequest) ([]MailboxMessage, error) {
	folder, limit, query, err := normalizeMailboxListRequest(req)
	if err != nil {
		return nil, err
	}
	value, err := a.runMailboxOperation(req.Email, func(reader mailboxReader) (mailboxOperationValue, error) {
		records, err := reader.ListRecentMessages(folder, limit, query)
		if err != nil {
			return mailboxOperationValue{}, err
		}
		messages := make([]MailboxMessage, 0, len(records))
		for _, record := range records {
			messages = append(messages, mailboxMessageFromRecord(record, false))
		}
		return mailboxOperationValue{messages: messages}, nil
	})
	return value.messages, err
}

// GetMailboxMessage 读取一封邮件的完整正文。
func (a *App) GetMailboxMessage(req MailboxMessageRequest) (MailboxMessage, error) {
	folder := strings.TrimSpace(req.Folder)
	if folder == "" {
		folder = "INBOX"
	}
	messageID := strings.TrimSpace(req.ID)
	if messageID == "" {
		return MailboxMessage{}, errors.New("邮件 ID 为空")
	}
	if len([]rune(folder)) > 512 {
		return MailboxMessage{}, errors.New("邮箱文件夹名称过长")
	}
	if len([]rune(messageID)) > 4096 {
		return MailboxMessage{}, errors.New("邮件 ID 过长")
	}

	value, err := a.runMailboxOperation(req.Email, func(reader mailboxReader) (mailboxOperationValue, error) {
		record, err := reader.FetchMessage(folder, messageID)
		if err != nil {
			return mailboxOperationValue{}, err
		}
		return mailboxOperationValue{message: mailboxMessageFromRecord(record, true)}, nil
	})
	return value.message, err
}

// ExtractMailboxCode 从已读取的邮件文本中离线提取验证码，不访问网络。
func (a *App) ExtractMailboxCode(text string) string {
	return mail.ExtractOpenAICode(text)
}

// ExtractMailboxInviteLink 从已读取的邮件文本中离线提取 Team 邀请链接。
func (a *App) ExtractMailboxInviteLink(text string) string {
	return mail.ExtractChatGPTTeamInviteURL(text)
}

func normalizeMailboxListRequest(req MailboxMessagesRequest) (string, int, string, error) {
	folder := strings.TrimSpace(req.Folder)
	if folder == "" {
		folder = "INBOX"
	}
	if len([]rune(folder)) > 512 {
		return "", 0, "", errors.New("邮箱文件夹名称过长")
	}
	limit := req.Limit
	if limit == 0 {
		limit = 80
	}
	if limit < 10 {
		limit = 10
	}
	if limit > 500 {
		limit = 500
	}
	query := strings.TrimSpace(req.Query)
	if len([]rune(query)) > 512 {
		return "", 0, "", errors.New("邮箱搜索内容过长")
	}
	return folder, limit, query, nil
}

func mailboxMessageFromRecord(record mail.MailRecord, includeBody bool) MailboxMessage {
	out := MailboxMessage{
		ID:          record.ID,
		Folder:      record.Folder,
		Kind:        record.Kind,
		Code:        record.Code,
		Subject:     record.Subject,
		From:        record.From,
		To:          record.To,
		Date:        record.Date,
		MailTime:    record.MailTime,
		MailTimeISO: record.MailTimeISO,
		Snippet:     record.Snippet,
	}
	if includeBody {
		out.Body = record.Body
	}
	return out
}

func (a *App) runMailboxOperation(email string, operation mailboxOperation) (mailboxOperationValue, error) {
	data, err := a.mailboxAccountData(email)
	if err != nil {
		return mailboxOperationValue{}, err
	}

	baseContext := a.ctx
	if baseContext == nil {
		baseContext = context.Background()
	}
	timeout := mailboxOperationTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(baseContext, timeout)
	defer cancel()

	resources := &mailboxResources{}
	resultCh := make(chan mailboxOperationResult, 1)
	go func() {
		value, runErr := a.runMailboxOperationNow(ctx, data, resources, operation)
		resultCh <- mailboxOperationResult{value: value, err: runErr}
	}()

	select {
	case result := <-resultCh:
		return result.value, result.err
	case <-ctx.Done():
		// Outlook 的优雅 LOGOUT 本身也依赖远端响应，不能让清理动作反过来
		// 穿透总超时。后台 close 会立即关闭可用连接；即使远端不回应，
		// Wails 调用也仍按上面的期限返回。
		go resources.close()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return mailboxOperationValue{}, fmt.Errorf("邮箱读取超时（%s）", timeout)
		}
		return mailboxOperationValue{}, errors.New("邮箱读取已取消")
	}
}

func (a *App) runMailboxOperationNow(
	ctx context.Context,
	data networkAccountData,
	resources *mailboxResources,
	operation mailboxOperation,
) (mailboxOperationValue, error) {
	account := data.Account
	originalRefreshToken := account.RefreshToken
	dynamicProxy := networkLoginDynamicProxy(data.Settings)
	safeLog := func(line string) {
		a.Log(mailboxRedact(line, mailboxSecrets(account, data.Settings, originalRefreshToken, dynamicProxy)...))
	}

	if err := ctx.Err(); err != nil {
		return mailboxOperationValue{}, err
	}
	proxySession, proxyURL, err := a.networkProxy(data.Settings, dynamicProxy, safeLog)
	if err != nil {
		return mailboxOperationValue{}, mailboxSafeError(err, mailboxSecrets(account, data.Settings, originalRefreshToken, dynamicProxy)...)
	}
	if !resources.attachProxy(proxySession) {
		return mailboxOperationValue{}, ctx.Err()
	}
	defer resources.close()

	reader, err := mailboxNewReader(&account, mail.Log(safeLog), proxyURL)
	if err != nil {
		return mailboxOperationValue{}, mailboxSafeError(err, mailboxSecrets(account, data.Settings, originalRefreshToken, dynamicProxy, proxyURL)...)
	}
	if !resources.attachReader(reader) {
		return mailboxOperationValue{}, ctx.Err()
	}
	if err := reader.Connect(); err != nil {
		secrets := mailboxSecrets(account, data.Settings, originalRefreshToken, dynamicProxy, proxyURL)
		connectErr := mailboxSafeError(err, secrets...)
		persistErr := a.persistMailboxRefreshToken(account.Email, originalRefreshToken, account.RefreshToken, account.Raw)
		if persistErr != nil {
			persistErr = mailboxSafeError(
				fmt.Errorf("保存轮换后的邮箱 Refresh Token 失败: %w", persistErr),
				secrets...,
			)
		}
		return mailboxOperationValue{}, errors.Join(connectErr, persistErr)
	}
	if err := ctx.Err(); err != nil {
		return mailboxOperationValue{}, err
	}

	value, operationErr := operation(reader)
	if err := ctx.Err(); err != nil {
		operationErr = err
	}
	persistErr := a.persistMailboxRefreshToken(account.Email, originalRefreshToken, account.RefreshToken, account.Raw)
	if persistErr == nil && account.RefreshToken != originalRefreshToken {
		safeLog("微软轮换后的邮箱 Refresh Token 已保存")
	}
	if operationErr != nil {
		operationErr = mailboxSafeError(operationErr, mailboxSecrets(account, data.Settings, originalRefreshToken, dynamicProxy, proxyURL)...)
	}
	if persistErr != nil {
		persistErr = mailboxSafeError(
			fmt.Errorf("保存轮换后的邮箱 Refresh Token 失败: %w", persistErr),
			mailboxSecrets(account, data.Settings, originalRefreshToken, dynamicProxy, proxyURL)...,
		)
	}
	return value, errors.Join(operationErr, persistErr)
}

func (a *App) mailboxAccountData(email string) (networkAccountData, error) {
	data, err := a.networkAccountData(email)
	if err != nil {
		return networkAccountData{}, err
	}
	account := data.Account
	usesCloudMail := alias.AccountUsesCloudMail(
		&account,
		data.Settings.CloudMailBase,
		data.Settings.CloudMailToken,
		data.Settings.CloudMailEnabled,
	)
	if !usesCloudMail &&
		(strings.TrimSpace(account.ClientID) == "" || strings.TrimSpace(account.RefreshToken) == "") {
		return networkAccountData{}, errors.New("这个账号没有可用的 Cloud Mail API 或 Outlook OAuth 收件配置")
	}
	data.Account = account
	return data, nil
}

// persistMailboxRefreshToken 只在微软确实轮换邮箱 Token 时更新对应账号，并在
// 发现另一个并发任务已经写入不同 Token 时拒绝覆盖。
func (a *App) persistMailboxRefreshToken(email, oldToken, newToken, raw string) error {
	newToken = strings.TrimSpace(newToken)
	if newToken == "" || newToken == strings.TrimSpace(oldToken) {
		return nil
	}
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		rows, _ := snapshot["accounts"].([]any)
		for _, row := range rows {
			record, ok := row.(map[string]any)
			if !ok || !strings.EqualFold(networkText(record["email"]), email) {
				continue
			}
			current := strings.TrimSpace(networkText(record["refresh_token"]))
			if current == newToken {
				return snapshot, map[string]bool{}, errNoStateChange
			}
			if current != strings.TrimSpace(oldToken) {
				return snapshot, nil, errors.New("账号邮箱 Refresh Token 已被其他任务更新，未覆盖新值")
			}
			record["refresh_token"] = newToken
			record["raw"] = raw
			return snapshot, map[string]bool{}, nil
		}
		return snapshot, nil, fmt.Errorf("账号不存在: %s", email)
	})
}

var (
	mailboxCredentialURLRE = regexp.MustCompile(`(?i)\b((?:https?|socks4a?|socks5h?)://)[^/\s@]+@`)
	mailboxNamedSecretRE   = regexp.MustCompile(`(?i)((?:authorization|password|refresh[_ -]?token|cloud[_ -]?mail[_ -]?token)\s*[:=]\s*(?:bearer\s+)?)[^,\s;]+`)
)

func mailboxSecrets(account models.MailAccount, st settings.Settings, extra ...string) []string {
	values := []string{
		account.Password,
		account.RefreshToken,
		account.CloudMailToken,
		account.Raw,
		st.CloudMailToken,
		st.LocalProxy,
	}
	values = append(values, extra...)
	seen := map[string]bool{}
	out := make([]string, 0, len(values)*2)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if encoded := url.QueryEscape(value); encoded != value && !seen[encoded] {
			seen[encoded] = true
			out = append(out, encoded)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func mailboxRedact(text string, secrets ...string) string {
	out := text
	for _, secret := range secrets {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, "***")
		}
	}
	out = mailboxCredentialURLRE.ReplaceAllString(out, `${1}***@`)
	out = mailboxNamedSecretRE.ReplaceAllString(out, `${1}***`)
	return out
}

func mailboxSafeError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("邮箱读取已取消")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("邮箱读取超时")
	}
	return errors.New(mailboxRedact(err.Error(), secrets...))
}
