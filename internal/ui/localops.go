package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"

	"github.com/pkppkq/openai-register-go/internal/accounts"
	"github.com/pkppkq/openai-register-go/internal/alias"
	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

// AccountMutationResult 是账号增删改类本地操作的统一返回值。
type AccountMutationResult struct {
	Updated int      `json:"updated"`
	Deleted int      `json:"deleted"`
	Total   int      `json:"total"`
	Emails  []string `json:"emails"`
	Message string   `json:"message"`
}

// GroupMutationResult 返回分组操作后的规范名称和完整分组列表。
type GroupMutationResult struct {
	Group         string   `json:"group"`
	PreviousGroup string   `json:"previousGroup"`
	Updated       int      `json:"updated"`
	Groups        []string `json:"groups"`
}

// AutoClassifyRequest 描述自动分类对话框的全部输入。
type AutoClassifyRequest struct {
	Mode           string   `json:"mode"`
	Scope          string   `json:"scope"`
	SelectedEmails []string `json:"selectedEmails"`
	CurrentGroup   string   `json:"currentGroup"`
	CurrentStatus  string   `json:"currentStatus"`
	CurrentSearch  string   `json:"currentSearch"`
}

// AutoClassifyResult 返回分类数量以及分类后应显示的分组。
type AutoClassifyResult struct {
	Updated     int            `json:"updated"`
	Counts      map[string]int `json:"counts"`
	GroupFilter string         `json:"groupFilter"`
	Message     string         `json:"message"`
}

// PlusAliasRequest 是“别名注册”对话框提交的数据。
type PlusAliasRequest struct {
	Emails []string `json:"emails"`
	Count  int      `json:"count"`
}

// DomainMailRequest 是“生成域名邮箱”对话框提交的数据。
//
// ReceiveMailboxes 仅用于转发模式，键是母账号邮箱，值是实际接收邮件的
// Outlook 邮箱；已有 receive_mailbox 或微软个人邮箱可自动推导时可以省略。
type DomainMailRequest struct {
	SelectedEmails   []string          `json:"selectedEmails"`
	Count            int               `json:"count"`
	ReceiveMailboxes map[string]string `json:"receiveMailboxes"`
}

// GeneratedAccountsResult 返回别名或域名邮箱生成结果。
type GeneratedAccountsResult struct {
	Created   int      `json:"created"`
	Total     int      `json:"total"`
	Emails    []string `json:"emails"`
	Errors    []string `json:"errors"`
	Group     string   `json:"group"`
	CloudMode bool     `json:"cloudMode"`
	Message   string   `json:"message"`
}

// DeleteAccounts 删除稳定邮箱键对应的账号、长链结果和撞链次数。
//
// Session 结果故意保留，与 Python 的 delete_selected_account 一致，便于
// 将来重新导入同一邮箱时恢复拆分 Session 文件。删除前必须明确确认。
func (a *App) DeleteAccounts(emails []string, confirmed bool) (AccountMutationResult, error) {
	var out AccountMutationResult
	if !confirmed {
		return out, fmt.Errorf("删除账号前必须确认")
	}
	if a.localOpsHasRunningJob() {
		return out, fmt.Errorf("任务正在运行，不能删除邮箱")
	}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		indices, err := localResolveAccountIndices(all, emails)
		if err != nil {
			return snapshot, nil, err
		}
		deleted := make(map[int]bool, len(indices))
		out.Emails = make([]string, 0, len(indices))
		for _, index := range indices {
			deleted[index] = true
			out.Emails = append(out.Emails, all[index].Email)
		}

		kept := make([]models.MailAccount, 0, len(all)-len(deleted))
		for index, account := range all {
			if !deleted[index] {
				kept = append(kept, account)
			}
		}
		results := subMap(snapshot, "results")
		attempts := subMap(snapshot, "link_attempt_counts")
		for _, email := range out.Emails {
			delete(results, email)
			delete(attempts, email)
		}
		snapshot["accounts"] = accountsToSnapshot(kept)
		snapshot["results"] = results
		snapshot["link_attempt_counts"] = attempts
		out.Deleted = len(out.Emails)
		out.Total = len(kept)
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	out.Message = fmt.Sprintf("已删除邮箱 %d 个: %s", out.Deleted, strings.Join(localPreview(out.Emails, 10), ", "))
	if len(out.Emails) > 10 {
		out.Message += " 等"
	}
	a.Log(out.Message)
	return out, nil
}

// ClearAccounts 清空账号、长链结果和撞链次数，并重置分组筛选。
//
// confirmed 必须由前端确认框显式传入；这是 UI_SPEC 对原 Tk 无确认行为的
// 安全修正。Session 结果按原行为保留。
func (a *App) ClearAccounts(confirmed bool) (AccountMutationResult, error) {
	var out AccountMutationResult
	if !confirmed {
		return out, fmt.Errorf("清空账号列表前必须确认")
	}
	if a.localOpsHasRunningJob() {
		return out, fmt.Errorf("任务正在运行，不能清空列表")
	}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		out.Deleted = len(all)
		out.Total = 0
		out.Emails = make([]string, 0, len(all))
		for _, account := range all {
			out.Emails = append(out.Emails, account.Email)
		}
		snapshot["accounts"] = []any{}
		snapshot["results"] = map[string]any{}
		snapshot["link_attempt_counts"] = map[string]any{}
		st := settings.FromSnapshot(snapshot)
		st.AccountGroups = []string{settings.AccountDefaultGroup}
		st.AccountGroupFilter = settings.AccountAllGroup
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	out.Message = fmt.Sprintf("已清空账号列表，共移除 %d 个账号", out.Deleted)
	a.Log(out.Message)
	return out, nil
}

// CreateAccountGroup 新建分组并将当前筛选切换到新分组。
func (a *App) CreateAccountGroup(name string) (GroupMutationResult, error) {
	var out GroupMutationResult
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		group, err := localValidateGroupName(name, "", st.AccountGroups)
		if err != nil {
			return snapshot, nil, err
		}
		st.AccountGroups = append(st.AccountGroups, group)
		st.AccountGroupFilter = group
		out.Group = group
		out.Groups = append([]string{}, st.AccountGroups...)
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	return out, err
}

// RenameAccountGroup 重命名分组，并同步更新组内账号。
func (a *App) RenameAccountGroup(oldName, newName string) (GroupMutationResult, error) {
	var out GroupMutationResult
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		oldGroup, ok := localFindGroup(st.AccountGroups, oldName)
		if !ok || oldGroup == settings.AccountDefaultGroup {
			return snapshot, nil, fmt.Errorf("请选择一个自定义分组")
		}
		group, err := localValidateGroupName(newName, oldGroup, st.AccountGroups)
		if err != nil {
			return snapshot, nil, err
		}
		for index, existing := range st.AccountGroups {
			if existing == oldGroup {
				st.AccountGroups[index] = group
			}
		}
		all := accountsFromSnapshot(snapshot)
		for index := range all {
			if all[index].Group == oldGroup {
				all[index].Group = group
				out.Updated++
			}
		}
		st.AccountGroupFilter = group
		snapshot["accounts"] = accountsToSnapshot(all)
		out.Group = group
		out.PreviousGroup = oldGroup
		out.Groups = append([]string{}, st.AccountGroups...)
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	return out, err
}

// DeleteAccountGroup 删除自定义分组，并把组内账号移回“未分组”。
func (a *App) DeleteAccountGroup(group string, confirmed bool) (GroupMutationResult, error) {
	var out GroupMutationResult
	if !confirmed {
		return out, fmt.Errorf("删除分组前必须确认")
	}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		target, ok := localFindGroup(st.AccountGroups, group)
		if !ok || target == settings.AccountDefaultGroup {
			return snapshot, nil, fmt.Errorf("请选择一个自定义分组")
		}
		all := accountsFromSnapshot(snapshot)
		for index := range all {
			if all[index].Group == target {
				all[index].Group = settings.AccountDefaultGroup
				out.Updated++
			}
		}
		groups := make([]string, 0, len(st.AccountGroups)-1)
		for _, existing := range st.AccountGroups {
			if existing != target {
				groups = append(groups, existing)
			}
		}
		st.AccountGroups = groups
		st.AccountGroupFilter = settings.AccountDefaultGroup
		snapshot["accounts"] = accountsToSnapshot(all)
		out.PreviousGroup = target
		out.Group = settings.AccountDefaultGroup
		out.Groups = append([]string{}, groups...)
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	return out, err
}

// MoveAccountsToGroup 把稳定邮箱键对应的账号移动到已有分组。
func (a *App) MoveAccountsToGroup(emails []string, group string) (GroupMutationResult, error) {
	var out GroupMutationResult
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		st := settings.FromSnapshot(snapshot)
		target, ok := localFindGroup(st.AccountGroups, group)
		if !ok {
			return snapshot, nil, fmt.Errorf("分组不存在: %s", localStrip(group))
		}
		all := accountsFromSnapshot(snapshot)
		indices, err := localResolveAccountIndices(all, emails)
		if err != nil {
			return snapshot, nil, err
		}
		for _, index := range indices {
			all[index].Group = target
			out.Updated++
		}
		snapshot["accounts"] = accountsToSnapshot(all)
		out.Group = target
		out.Groups = append([]string{}, st.AccountGroups...)
		return snapshot, map[string]bool{}, nil
	})
	return out, err
}

// SetAccountType 设置 Free、Plus 或 Team 类型。
//
// Free 会按原实现清空状态和 openai_rt，因此必须明确确认；Plus/Team 仅在
// 原状态为空时补上“Plus”或“Team待注册”，不会覆盖工作流写入的更具体状态。
func (a *App) SetAccountType(emails []string, accountType string, confirmed bool) (AccountMutationResult, error) {
	var out AccountMutationResult
	accountType = strings.ToLower(localStrip(accountType))
	if accountType != "free" && accountType != "plus" && accountType != "team" {
		return out, fmt.Errorf("账号类型必须是 free、plus 或 team")
	}
	if accountType == "free" && !confirmed {
		return out, fmt.Errorf("切换为 free 会清空 OpenAI RT，必须先确认")
	}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		indices, err := localResolveAccountIndices(all, emails)
		if err != nil {
			return snapshot, nil, err
		}
		out.Emails = make([]string, 0, len(indices))
		for _, index := range indices {
			account := &all[index]
			account.AccountType = accountType
			switch accountType {
			case "plus":
				if account.Status == "" {
					account.Status = "Plus"
				}
			case "team":
				if account.Status == "" {
					account.Status = "Team待注册"
				}
			case "free":
				account.Status = ""
				account.OpenaiRT = ""
			}
			out.Emails = append(out.Emails, account.Email)
		}
		out.Updated = len(out.Emails)
		out.Total = len(all)
		snapshot["accounts"] = accountsToSnapshot(all)
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	out.Message = fmt.Sprintf("已将 %d 个邮箱类型改为 %s: %s", out.Updated, accountType, strings.Join(localPreview(out.Emails, 10), ", "))
	if len(out.Emails) > 10 {
		out.Message += " 等"
	}
	a.Log(out.Message)
	return out, nil
}

// AutoClassifyAccounts 按试用、长链或套餐类型批量移动账号分组。
//
// 此方法只改 account.group 和分组筛选，不改 Session、Token、RT、结果或状态。
func (a *App) AutoClassifyAccounts(req AutoClassifyRequest) (AutoClassifyResult, error) {
	out := AutoClassifyResult{Counts: map[string]int{}}
	req.Mode = strings.ToLower(localStrip(req.Mode))
	if req.Mode != "trial" && req.Mode != "link" && req.Mode != "plan" {
		return out, fmt.Errorf("请选择分类方式")
	}
	req.Scope = strings.ToLower(localStrip(req.Scope))
	if req.Scope != "selected" && req.Scope != "current" && req.Scope != "all" {
		req.Scope = "all"
	}

	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		st := settings.FromSnapshot(snapshot)
		lk := lookupsFromSnapshot(snapshot)
		var indices []int
		switch req.Scope {
		case "selected":
			var err error
			indices, err = localResolveAccountIndices(all, req.SelectedEmails)
			if err != nil {
				return snapshot, nil, err
			}
		case "current":
			group := req.CurrentGroup
			status := req.CurrentStatus
			if group == "" {
				group = st.AccountGroupFilter
			}
			if status == "" {
				status = st.AccountStatusFilter
			}
			indices = accounts.VisibleIndices(all, accounts.Filter{
				Group: group, Status: status, Search: req.CurrentSearch,
			}, lk)
		default:
			indices = make([]int, len(all))
			for index := range all {
				indices[index] = index
			}
		}
		if len(indices) == 0 {
			return snapshot, nil, fmt.Errorf("没有可分类的账号")
		}

		for _, index := range indices {
			group := localClassifyGroup(all[index], req.Mode, lk)
			group = localEnsureGroup(&st, group)
			all[index].Group = group
			out.Counts[group]++
		}
		out.Updated = len(indices)
		if req.Scope == "selected" {
			st.AccountGroupFilter = all[indices[0]].Group
		} else {
			st.AccountGroupFilter = settings.AccountAllGroup
		}
		out.GroupFilter = st.AccountGroupFilter
		snapshot["accounts"] = accountsToSnapshot(all)
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}

	modeLabels := map[string]string{"trial": "试用资格", "link": "长链结果", "plan": "账号类型"}
	scopeLabels := map[string]string{"all": "全部", "current": "当前分组可见", "selected": "选中"}
	groups := make([]string, 0, len(out.Counts))
	for group := range out.Counts {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		parts = append(parts, fmt.Sprintf("%s %d 个", group, out.Counts[group]))
	}
	out.Message = fmt.Sprintf("自动分类完成：%s / %s，共 %d 个账号：%s",
		modeLabels[req.Mode], scopeLabels[req.Scope], out.Updated, strings.Join(parts, "；"))
	a.Log(out.Message)
	return out, nil
}

// CreatePlusAliasAccounts 为选中的母邮箱生成 +随机数字 别名账号。
func (a *App) CreatePlusAliasAccounts(req PlusAliasRequest) (GeneratedAccountsResult, error) {
	out := GeneratedAccountsResult{Errors: []string{}}
	if a.localOpsHasRunningJob() {
		return out, fmt.Errorf("任务正在运行，不能生成别名账号")
	}
	if req.Count < 1 || req.Count > alias.MaxPlusAliasesPerMailbox {
		return out, fmt.Errorf("每个母邮箱生成数量必须为 1–%d", alias.MaxPlusAliasesPerMailbox)
	}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		indices, err := localResolveAccountIndices(all, req.Emails)
		if err != nil {
			return snapshot, nil, err
		}
		selected := make([]models.MailAccount, 0, len(indices))
		for _, index := range indices {
			selected = append(selected, all[index])
		}
		created, errs := alias.GeneratePlusAliases(all, selected, req.Count)
		out.Errors = append(out.Errors, errs...)
		out.Created = len(created)
		out.Total = len(all) + len(created)
		out.Emails = make([]string, 0, len(created))
		for _, account := range created {
			out.Emails = append(out.Emails, account.Email)
		}
		if len(created) == 0 {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		all = append(all, created...)
		snapshot["accounts"] = accountsToSnapshot(all)
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	if out.Created > 0 {
		out.Message = fmt.Sprintf("已生成别名注册账号 %d 个: %s", out.Created, strings.Join(localPreview(out.Emails, 8), ", "))
		if out.Created > 8 {
			out.Message += fmt.Sprintf(" 等 %d 个", out.Created)
		}
		a.Log(out.Message)
	}
	return out, nil
}

// CreateDomainMailAccounts 生成 Cloud Mail 或 Outlook 转发模式域名邮箱。
func (a *App) CreateDomainMailAccounts(req DomainMailRequest) (GeneratedAccountsResult, error) {
	out := GeneratedAccountsResult{Errors: []string{}}
	if a.localOpsHasRunningJob() {
		return out, fmt.Errorf("任务正在运行，不能生成域名邮箱")
	}
	if req.Count < 1 || req.Count > 500 {
		return out, fmt.Errorf("域名邮箱生成数量必须为 1–500")
	}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		all := accountsFromSnapshot(snapshot)
		st := settings.FromSnapshot(snapshot)
		cloudSettings, err := alias.CloudMailSettingsFrom(st.CloudMailBase, st.CloudMailToken, st.CloudMailEnabled)
		if err != nil {
			return snapshot, nil, fmt.Errorf("Cloud Mail 设置不可用：%w", err)
		}
		cloudMode := cloudSettings.Enabled
		sourceIndices := []int{}
		if !cloudMode {
			if len(req.SelectedEmails) > 0 {
				selected, err := localResolveAccountIndices(all, req.SelectedEmails)
				if err != nil {
					return snapshot, nil, err
				}
				for _, index := range selected {
					if localFold(all[index].Group) == localFold(alias.AccountDomainMailMainGroup) {
						sourceIndices = append(sourceIndices, index)
					}
				}
			}
			if len(sourceIndices) == 0 {
				for index := range all {
					if localFold(all[index].Group) == localFold(alias.AccountDomainMailMainGroup) {
						sourceIndices = append(sourceIndices, index)
					}
				}
			}
			if len(sourceIndices) == 0 {
				cloudMode = true
				st.CloudMailEnabled = true
			}
		}
		out.CloudMode = cloudMode
		childGroup := localEnsureGroup(&st, alias.AccountDomainMailChildGroup)
		out.Group = childGroup

		existing := make(map[string]bool, len(all))
		for _, account := range all {
			existing[account.Email] = true
		}
		created := []models.MailAccount{}
		if cloudMode {
			for index := 0; index < req.Count; index++ {
				email, err := alias.RandomDomainAliasEmail(models.DefaultDomainMailDomain, existing, 0)
				if err != nil {
					out.Errors = append(out.Errors, err.Error())
					break
				}
				account := alias.NewCloudMailDomainAccount(
					email,
					worker.GeneratePassword(),
					cloudSettings.BaseURL,
					cloudSettings.Token,
					childGroup,
				)
				existing[email] = true
				created = append(created, account)
			}
		} else {
			for _, sourceIndex := range sourceIndices {
				source := all[sourceIndex]
				if source.ClientID == "" || source.RefreshToken == "" {
					out.Errors = append(out.Errors, source.Email+": 缺少主 Outlook 的 client_id/refresh_token")
					continue
				}
				receiveMailbox := alias.DomainAliasReceiveMailbox(source)
				if receiveMailbox == "" {
					receiveMailbox = models.NormalizeEmailAddress(localReceiveMailbox(req.ReceiveMailboxes, source.Email))
				}
				if !strings.Contains(receiveMailbox, "@") {
					out.Errors = append(out.Errors, source.Email+": 未设置有效的接收主 Outlook 邮箱")
					continue
				}
				all[sourceIndex].ReceiveMailbox = receiveMailbox
				source.ReceiveMailbox = receiveMailbox
				for index := 0; index < req.Count; index++ {
					email, err := alias.RandomDomainAliasEmail(models.DefaultDomainMailDomain, existing, 0)
					if err != nil {
						out.Errors = append(out.Errors, source.Email+": "+err.Error())
						break
					}
					account, err := alias.CloneAccountForDomainAlias(source, email, receiveMailbox, "outlook")
					if err != nil {
						out.Errors = append(out.Errors, source.Email+": "+err.Error())
						break
					}
					account.Group = childGroup
					existing[email] = true
					created = append(created, account)
				}
			}
		}

		out.Created = len(created)
		out.Total = len(all) + len(created)
		out.Emails = make([]string, 0, len(created))
		for _, account := range created {
			out.Emails = append(out.Emails, account.Email)
		}
		if len(created) == 0 {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		all = append(all, created...)
		st.AccountGroupFilter = childGroup
		snapshot["accounts"] = accountsToSnapshot(all)
		return settings.ToSnapshot(st, snapshot), map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	if out.Created > 0 {
		mode := "转发到主 Outlook"
		if out.CloudMode {
			mode = "Cloud Mail API"
		}
		out.Message = fmt.Sprintf("已生成域名邮箱 %d 个并归入“%s”；目标域名=%s，收件方式=%s",
			out.Created, out.Group, models.DefaultDomainMailDomain, mode)
		a.Log(out.Message)
	}
	return out, nil
}

func (a *App) localOpsHasRunningJob() bool {
	if a.jobs == nil {
		return false
	}
	a.jobs.mu.Lock()
	defer a.jobs.mu.Unlock()
	for _, item := range a.jobs.jobs {
		if item != nil && item.view.Status == StatusRunning {
			return true
		}
	}
	return false
}

func localResolveAccountIndices(all []models.MailAccount, requested []string) ([]int, error) {
	if len(requested) == 0 {
		return nil, fmt.Errorf("请先选中邮箱")
	}
	byKey := make(map[string]int, len(all))
	ambiguous := map[string]bool{}
	for index, account := range all {
		key := accounts.Key(account)
		if _, exists := byKey[key]; exists {
			ambiguous[key] = true
			continue
		}
		byKey[key] = index
	}
	indices := make([]int, 0, len(requested))
	seen := map[string]bool{}
	for _, email := range requested {
		key := accounts.KeyOf(email)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if ambiguous[key] {
			return nil, fmt.Errorf("邮箱键不唯一，无法安全操作: %s", email)
		}
		index, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("未找到邮箱: %s", localStrip(email))
		}
		indices = append(indices, index)
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("请先选中邮箱")
	}
	return indices, nil
}

func localValidateGroupName(value, oldName string, groups []string) (string, error) {
	name := localStrip(value)
	if name == "" || utf8.RuneCountInString(name) > 32 {
		return "", fmt.Errorf("分组名称长度必须为 1–32 个字符")
	}
	if name == settings.AccountAllGroup || name == settings.AccountDefaultGroup {
		return "", fmt.Errorf("“%s”是保留名称", name)
	}
	for _, group := range groups {
		if localFold(group) == localFold(name) && group != oldName {
			return "", fmt.Errorf("已有同名分组")
		}
	}
	return name, nil
}

func localFindGroup(groups []string, requested string) (string, bool) {
	key := localFold(localStrip(requested))
	if key == localFold(settings.AccountAllGroup) {
		return "", false
	}
	for _, group := range groups {
		if localFold(group) == key {
			return group, true
		}
	}
	return "", false
}

func localEnsureGroup(st *settings.Settings, requested string) string {
	name := localStrip(requested)
	if name == "" || name == settings.AccountAllGroup {
		name = settings.AccountDefaultGroup
	}
	if existing, ok := localFindGroup(st.AccountGroups, name); ok {
		return existing
	}
	st.AccountGroups = append(st.AccountGroups, name)
	return name
}

func localClassifyGroup(account models.MailAccount, mode string, lk accounts.Lookups) string {
	switch mode {
	case "trial":
		payload, _ := lk.SessionResults[account.Email].(map[string]any)
		trialValue := strings.ToLower(localValueText(payload["plus_trial_eligible"]))
		trialStatus := strings.ToLower(localValueText(payload["plus_trial_status"]))
		status := accounts.StatusText(account, lk)
		foldedStatus := localFold(status)
		if trialValue == "true" || trialStatus == "eligible" || strings.Contains(foldedStatus, localFold("有plus试用")) {
			return "有Plus试用"
		}
		if trialValue == "false" || trialStatus == "not_eligible" || strings.Contains(foldedStatus, localFold("无plus试用")) {
			return "无Plus试用"
		}
		if strings.Contains(status, "试用检测失败") {
			return "试用检测失败"
		}
		return "试用未检测"
	case "link":
		if lk.HasLink(account.Email) {
			return "提链成功"
		}
		status := accounts.StatusText(account, lk)
		for _, keyword := range []string{"失败", "代理耗尽", "不可自动重试", "代理检测失败", "代理非日本"} {
			if strings.Contains(status, keyword) {
				return "提链失败"
			}
		}
		if lk.AttemptCount(account.Email) > 0 {
			return "提链未成功"
		}
		return "未提链"
	case "plan":
		switch localAccountPlanType(account, lk) {
		case "plus":
			return "plus"
		case "free":
			return "free"
		case "team":
			return "team"
		case "k12":
			return "k12"
		case "pro":
			return "pro"
		default:
			return "类型未知"
		}
	default:
		panic("localClassifyGroup 收到未经校验的分类方式")
	}
}

func localAccountPlanType(account models.MailAccount, lk accounts.Lookups) string {
	payload, _ := lk.SessionResults[account.Email].(map[string]any)
	summary, _ := payload["access_summary"].(map[string]any)
	candidates := []any{account.AccountType, payload["plan_type"], payload["chatgpt_plan_type"], summary["plan_type"]}
	for _, value := range candidates {
		plan := strings.ToLower(localValueText(value))
		switch plan {
		case "chatgptplusplan", "plus_plan":
			return "plus"
		case "free", "plus", "team", "k12", "pro":
			return plan
		}
	}
	return "unknown"
}

func localValueText(value any) string {
	if !settings.PyTruthy(value) {
		return ""
	}
	return localStrip(settings.PyStr(value))
}

func localReceiveMailbox(values map[string]string, email string) string {
	key := accounts.KeyOf(email)
	for candidate, value := range values {
		if accounts.KeyOf(candidate) == key {
			return value
		}
	}
	return ""
}

func localPreview(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func localFold(value string) string {
	return cases.Fold().String(value)
}
