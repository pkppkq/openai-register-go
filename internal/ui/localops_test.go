package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/accounts"
	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

func TestDeleteAccountsRejectsRunningJobWithoutStateChange(t *testing.T) {
	app, stateFile := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{{Email: "running@example.com", AccountType: "free"}},
		map[string]any{"running@example.com": map[string]any{"access_token": "must-stay"}},
	))

	const jobID = "delete-guard-running"
	app.startStubJob(jobID, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	t.Cleanup(func() {
		view, ok := app.jobView(jobID)
		if ok && view.Status == StatusRunning {
			if err := app.CancelJob(jobID); err != nil {
				t.Errorf("取消删除门禁测试任务: %v", err)
				return
			}
		}
		waitStatus(t, app, jobID, StatusCancelled)
	})

	before, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("读取删除前状态: %v", err)
	}
	if _, err := app.DeleteAccounts([]string{"running@example.com"}, true); err == nil {
		t.Fatal("存在运行中任务时 DeleteAccounts 应拒绝")
	} else if !strings.Contains(err.Error(), "任务正在运行") {
		t.Fatalf("删除拒绝原因=%q，期望提示任务正在运行", err)
	}
	after, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("读取删除后状态: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("运行中任务拒绝删除后 state.json 字节发生变化")
	}
}

func TestLocalOpsDeleteAndClearPreserveSessions(t *testing.T) {
	app, stateFile := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{
			{Email: "A@example.com", AccountType: "free", Group: "组A"},
			{Email: "B@example.com", AccountType: "plus", Group: "组B"},
		},
		map[string]any{
			"A@example.com": map[string]any{"access_token": "token-a"},
			"B@example.com": map[string]any{"access_token": "token-b"},
		},
	))
	seed := readLocalOpsRawState(t, stateFile)
	seed["results"] = map[string]any{"A@example.com": "link-a", "B@example.com": "link-b"}
	seed["link_attempt_counts"] = map[string]any{"A@example.com": 1, "B@example.com": 2}
	writeLocalOpsState(t, stateFile, seed)

	deleted, err := app.DeleteAccounts([]string{"b@EXAMPLE.com"}, true)
	if err != nil {
		t.Fatalf("DeleteAccounts: %v", err)
	}
	if deleted.Deleted != 1 || deleted.Total != 1 || deleted.Emails[0] != "B@example.com" {
		t.Fatalf("删除结果异常: %#v", deleted)
	}
	raw := readLocalOpsRawState(t, stateFile)
	if _, ok := raw["results"].(map[string]any)["B@example.com"]; ok {
		t.Fatal("已删除账号的长链结果仍在")
	}
	if _, ok := raw["link_attempt_counts"].(map[string]any)["B@example.com"]; ok {
		t.Fatal("已删除账号的撞链次数仍在")
	}
	if _, ok := raw["session_results"].(map[string]any)["B@example.com"]; !ok {
		t.Fatal("删除账号不应删除拆分 Session 索引")
	}

	if _, err := app.ClearAccounts(false); err == nil {
		t.Fatal("未确认时 ClearAccounts 应拒绝")
	}
	cleared, err := app.ClearAccounts(true)
	if err != nil {
		t.Fatalf("ClearAccounts: %v", err)
	}
	if cleared.Deleted != 1 || cleared.Total != 0 {
		t.Fatalf("清空结果异常: %#v", cleared)
	}
	snapshot, err := app.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got := len(accountsFromSnapshot(snapshot)); got != 0 {
		t.Fatalf("清空后仍有 %d 个账号", got)
	}
	if got := len(sessionResultsFromSnapshot(snapshot)); got != 2 {
		t.Fatalf("清空后 Session 数量=%d，期望 2", got)
	}
	st := settings.FromSnapshot(snapshot)
	if len(st.AccountGroups) != 1 || st.AccountGroups[0] != settings.AccountDefaultGroup {
		t.Fatalf("分组未重置: %#v", st.AccountGroups)
	}
	if st.AccountGroupFilter != settings.AccountAllGroup {
		t.Fatalf("分组筛选=%q，期望 全部", st.AccountGroupFilter)
	}
}

func TestLocalOpsGroupsMoveAndAccountTypes(t *testing.T) {
	app, _ := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{
			{Email: "a@example.com", AccountType: "free", Group: "Alpha"},
			{Email: "b@example.com", AccountType: "team", Status: "处理中", OpenaiRT: "rt-b", Group: models.AccountDefaultGroup},
		},
		nil,
	))

	created, err := app.CreateAccountGroup("  Beta  ")
	if err != nil || created.Group != "Beta" {
		t.Fatalf("CreateAccountGroup = %#v, %v", created, err)
	}
	if _, err := app.CreateAccountGroup("beta"); err == nil {
		t.Fatal("大小写不同的同名分组应拒绝")
	}
	moved, err := app.MoveAccountsToGroup([]string{"A@EXAMPLE.COM"}, "beta")
	if err != nil || moved.Group != "Beta" || moved.Updated != 1 {
		t.Fatalf("MoveAccountsToGroup = %#v, %v", moved, err)
	}
	renamed, err := app.RenameAccountGroup("Beta", "Gamma")
	if err != nil || renamed.Updated != 1 || renamed.Group != "Gamma" {
		t.Fatalf("RenameAccountGroup = %#v, %v", renamed, err)
	}

	if _, err := app.SetAccountType([]string{"a@example.com"}, "plus", false); err != nil {
		t.Fatalf("SetAccountType plus: %v", err)
	}
	if _, err := app.SetAccountType([]string{"b@example.com"}, "free", true); err != nil {
		t.Fatalf("SetAccountType free: %v", err)
	}
	page, err := app.ListAccounts(AccountFilter{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	rows := localRowsByKey(page.Rows)
	if rows["a@example.com"].AccountType != "plus" || rows["a@example.com"].Status != "Plus" {
		t.Fatalf("Plus 账号字段异常: %#v", rows["a@example.com"])
	}
	if row := rows["b@example.com"]; row.AccountType != "free" || row.Status != "" || row.OpenaiRT != "" {
		t.Fatalf("Free 未清空状态/RT: %#v", row)
	}

	if _, err := app.DeleteAccountGroup("Gamma", false); err == nil {
		t.Fatal("未确认时 DeleteAccountGroup 应拒绝")
	}
	deleted, err := app.DeleteAccountGroup("gamma", true)
	if err != nil || deleted.Updated != 1 {
		t.Fatalf("DeleteAccountGroup = %#v, %v", deleted, err)
	}
	page, _ = app.ListAccounts(AccountFilter{})
	rows = localRowsByKey(page.Rows)
	if rows["a@example.com"].Group != models.AccountDefaultGroup {
		t.Fatalf("删除分组后账号分组=%q", rows["a@example.com"].Group)
	}
}

func TestLocalOpsAutoClassifyScopesAndModes(t *testing.T) {
	t.Run("当前可见长链分类", func(t *testing.T) {
		app, stateFile := newLocalOpsTestApp(t, localOpsSnapshot(
			[]models.MailAccount{
				{Email: "attempt@example.com", AccountType: "free", Group: "当前"},
				{Email: "linked@example.com", AccountType: "plus", Group: "当前"},
				{Email: "other@example.com", AccountType: "team", Group: "其他"},
			},
			map[string]any{
				"attempt@example.com": map[string]any{"access_token": "keep-attempt"},
			},
		))
		seed := readLocalOpsRawState(t, stateFile)
		seed["results"] = map[string]any{"linked@example.com": "https://pay.example/link"}
		seed["link_attempt_counts"] = map[string]any{"attempt@example.com": 2}
		writeLocalOpsState(t, stateFile, seed)

		result, err := app.AutoClassifyAccounts(AutoClassifyRequest{
			Mode: "link", Scope: "current", CurrentGroup: "当前",
		})
		if err != nil {
			t.Fatalf("AutoClassifyAccounts: %v", err)
		}
		if result.Updated != 2 || result.Counts["提链未成功"] != 1 || result.Counts["提链成功"] != 1 {
			t.Fatalf("分类计数异常: %#v", result)
		}
		page, _ := app.ListAccounts(AccountFilter{})
		rows := localRowsByKey(page.Rows)
		if rows["other@example.com"].Group != "其他" {
			t.Fatalf("current 范围改动了不可见账号: %#v", rows["other@example.com"])
		}
		snapshot, _ := app.snapshot()
		payload := sessionResultsFromSnapshot(snapshot)["attempt@example.com"].(map[string]any)
		if payload["access_token"] != "keep-attempt" {
			t.Fatal("自动分类修改了 Session token")
		}
	})

	t.Run("选中试用分类", func(t *testing.T) {
		app, _ := newLocalOpsTestApp(t, localOpsSnapshot(
			[]models.MailAccount{
				{Email: "trial@example.com", AccountType: "free", Group: "原组"},
				{Email: "untouched@example.com", AccountType: "free", Group: "原组"},
			},
			map[string]any{
				"trial@example.com": map[string]any{
					"access_token": "keep", "plus_trial_eligible": true,
				},
			},
		))
		result, err := app.AutoClassifyAccounts(AutoClassifyRequest{
			Mode: "trial", Scope: "selected", SelectedEmails: []string{"TRIAL@example.com"},
		})
		if err != nil {
			t.Fatalf("AutoClassifyAccounts: %v", err)
		}
		if result.Counts["有Plus试用"] != 1 || result.GroupFilter != "有Plus试用" {
			t.Fatalf("试用分类异常: %#v", result)
		}
		page, _ := app.ListAccounts(AccountFilter{})
		rows := localRowsByKey(page.Rows)
		if rows["untouched@example.com"].Group != "原组" {
			t.Fatal("selected 范围改动了未选账号")
		}
	})

	t.Run("全部套餐分类", func(t *testing.T) {
		app, _ := newLocalOpsTestApp(t, localOpsSnapshot(
			[]models.MailAccount{
				{Email: "free@example.com", AccountType: "free"},
				{Email: "plus@example.com", AccountType: "plus"},
				{Email: "team@example.com", AccountType: "team"},
				{Email: "unknown@example.com", AccountType: "strange"},
				{Email: "fallback@example.com", AccountType: "strange"},
			},
			map[string]any{
				"fallback@example.com": map[string]any{"plan_type": "k12"},
			},
		))
		result, err := app.AutoClassifyAccounts(AutoClassifyRequest{Mode: "plan", Scope: "all"})
		if err != nil {
			t.Fatalf("AutoClassifyAccounts: %v", err)
		}
		for group, want := range map[string]int{"free": 1, "plus": 1, "team": 1, "k12": 1, "类型未知": 1} {
			if result.Counts[group] != want {
				t.Errorf("%s 数量=%d，期望 %d", group, result.Counts[group], want)
			}
		}
	})
}

func TestLocalOpsGeneratePlusAliases(t *testing.T) {
	app, _ := newLocalOpsTestApp(t, localOpsSnapshot(
		[]models.MailAccount{{
			Email: "mother@example.com", Password: "pw", ClientID: "cid", RefreshToken: "rt",
			AccountType: "free", Group: "母组",
		}},
		nil,
	))
	result, err := app.CreatePlusAliasAccounts(PlusAliasRequest{
		Emails: []string{"MOTHER@example.com"}, Count: 2,
	})
	if err != nil {
		t.Fatalf("CreatePlusAliasAccounts: %v", err)
	}
	if result.Created != 2 || result.Total != 3 {
		t.Fatalf("生成结果异常: %#v", result)
	}
	page, _ := app.ListAccounts(AccountFilter{})
	aliases := 0
	for _, row := range page.Rows {
		if !alias.IsPlusAliasEmail(row.Email) {
			continue
		}
		aliases++
		if row.ClientID != "cid" || row.RefreshToken != "rt" || row.Group != "母组" {
			t.Errorf("别名未继承母账号本地字段: %#v", row)
		}
		if row.AccountType != "free" || row.Status != alias.PlusAliasPendingStatus {
			t.Errorf("别名初始状态异常: %#v", row)
		}
	}
	if aliases != 2 {
		t.Fatalf("实际别名数量=%d", aliases)
	}
}

func TestLocalOpsGenerateDomainMailCloudAndForwarding(t *testing.T) {
	t.Run("Cloud Mail", func(t *testing.T) {
		snapshot := localOpsSnapshot(nil, nil)
		snapshot["settings"] = map[string]any{
			"cloud_mail_enabled": true,
			"cloud_mail_base":    "https://mail.example.test/",
			"cloud_mail_token":   "token",
		}
		app, _ := newLocalOpsTestApp(t, snapshot)
		result, err := app.CreateDomainMailAccounts(DomainMailRequest{Count: 2})
		if err != nil {
			t.Fatalf("CreateDomainMailAccounts: %v", err)
		}
		if !result.CloudMode || result.Created != 2 || result.Group != alias.AccountDomainMailChildGroup {
			t.Fatalf("Cloud Mail 结果异常: %#v", result)
		}
		page, _ := app.ListAccounts(AccountFilter{})
		for _, row := range page.Rows {
			if !strings.HasSuffix(row.Email, "@"+models.DefaultDomainMailDomain) {
				t.Errorf("域名邮箱=%q", row.Email)
			}
			if row.MailProvider != "cloudmail" || row.Status != alias.DomainAliasPendingStatus {
				t.Errorf("Cloud Mail 账号字段异常: %#v", row)
			}
		}
	})

	t.Run("Outlook 转发", func(t *testing.T) {
		snapshot := localOpsSnapshot([]models.MailAccount{{
			Email: "owner@corp.example", Password: "pw", ClientID: "cid", RefreshToken: "rt",
			AccountType: "free", Group: alias.AccountDomainMailMainGroup,
		}}, nil)
		snapshot["settings"] = map[string]any{
			"cloud_mail_enabled": false,
			"cloud_mail_base":    "https://mail.example.test",
		}
		app, _ := newLocalOpsTestApp(t, snapshot)
		result, err := app.CreateDomainMailAccounts(DomainMailRequest{
			SelectedEmails: []string{"OWNER@corp.example"},
			Count:          1,
			ReceiveMailboxes: map[string]string{
				"owner@corp.example": "receive@outlook.com",
			},
		})
		if err != nil {
			t.Fatalf("CreateDomainMailAccounts: %v", err)
		}
		if result.CloudMode || result.Created != 1 {
			t.Fatalf("转发模式结果异常: %#v", result)
		}
		page, _ := app.ListAccounts(AccountFilter{})
		rows := localRowsByKey(page.Rows)
		if rows["owner@corp.example"].ReceiveMailbox != "receive@outlook.com" {
			t.Fatalf("母账号未保存接收邮箱: %#v", rows["owner@corp.example"])
		}
		child := rows[accounts.KeyOf(result.Emails[0])]
		if child.MailProvider != "outlook" || child.ReceiveMailbox != "receive@outlook.com" {
			t.Fatalf("转发子账号字段异常: %#v", child)
		}
	})
}

func newLocalOpsTestApp(t *testing.T, snapshot map[string]any) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	dataDir := filepath.Join(dir, "state_data")
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	snapshot["schema_version"] = 1
	writeLocalOpsState(t, stateFile, snapshot)
	t.Setenv("STATE_FILE", stateFile)
	t.Setenv("STATE_DATA_DIR", dataDir)
	return New(), stateFile
}

func localOpsSnapshot(all []models.MailAccount, sessions map[string]any) map[string]any {
	rows := make([]any, 0, len(all))
	groups := []any{models.AccountDefaultGroup}
	seenGroups := map[string]bool{models.AccountDefaultGroup: true}
	for _, account := range all {
		if account.AccountType == "" {
			account.AccountType = "free"
		}
		if account.Group == "" {
			account.Group = models.AccountDefaultGroup
		}
		rows = append(rows, models.AccountToMap(account))
		if !seenGroups[account.Group] {
			seenGroups[account.Group] = true
			groups = append(groups, account.Group)
		}
	}
	if sessions == nil {
		sessions = map[string]any{}
	}
	return map[string]any{
		"accounts":            rows,
		"session_results":     sessions,
		"results":             map[string]any{},
		"link_attempt_counts": map[string]any{},
		"settings": map[string]any{
			"account_groups":       groups,
			"account_group_filter": settings.AccountAllGroup,
		},
	}
}

func writeLocalOpsState(t *testing.T, path string, value map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("编码测试状态: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建测试目录: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入测试状态: %v", err)
	}
}

func readLocalOpsRawState(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取测试状态: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("解码测试状态: %v", err)
	}
	return value
}

func localRowsByKey(rows []AccountRow) map[string]AccountRow {
	out := make(map[string]AccountRow, len(rows))
	for _, row := range rows {
		out[row.Key] = row
	}
	return out
}
