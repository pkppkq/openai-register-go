package ui

import (
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/phoneprovider"
)

// sharedPhonePool 返回 App 生命周期内唯一的手工号码池。
//
// SMSBowerProvider 仍按任务创建和关闭，因为它持有可计费激活；手工号码池则
// 必须跨任务共享，批量并发才能在同一把锁下完成“查找 + 标记使用中”。
func (a *App) sharedPhonePool() *phoneprovider.MemoryPool {
	a.phonePoolOnce.Do(func() {
		pool := phoneprovider.NewMemoryPool(nil, nil)
		pool.OnStateUpdated = func(update phoneprovider.PoolUpdate) {
			if err := a.persistPhonePoolUpdate(update); err != nil && a.logs != nil {
				a.Log("保存手机号池状态失败: " + err.Error())
			}
		}
		a.phonePool = pool
	})
	return a.phonePool
}

// refreshPhonePool 在号码池锁内读取最新 state，防止“先读旧快照、后覆盖新
// 占用”的竞态。任务开始、账号导入和设置保存后都可安全调用。
func (a *App) refreshPhonePool() error {
	return a.sharedPhonePool().Refresh(func() ([]*models.MailAccount, []*models.PhoneEntry, error) {
		snapshot, err := a.snapshot()
		if err != nil {
			return nil, nil, err
		}
		accounts, phones := phonePoolPointers(snapshot)
		return accounts, phones, nil
	})
}

// refreshPhonePoolFromSnapshot 只用于最新读取失败时的启动快照回退。传入
// 数据仍在号码池锁内复制，任务之间不会得到不同的 MemoryPool 实例。
func (a *App) refreshPhonePoolFromSnapshot(snapshot map[string]any) {
	_ = a.sharedPhonePool().Refresh(func() ([]*models.MailAccount, []*models.PhoneEntry, error) {
		accounts, phones := phonePoolPointers(snapshot)
		return accounts, phones, nil
	})
}

func phonePoolPointers(snapshot map[string]any) ([]*models.MailAccount, []*models.PhoneEntry) {
	values := accountsFromSnapshot(snapshot)
	accounts := make([]*models.MailAccount, len(values))
	for index := range values {
		accounts[index] = &values[index]
	}
	phoneValues := uiPhonesFromSnapshot(snapshot)
	phones := make([]*models.PhoneEntry, len(phoneValues))
	for index := range phoneValues {
		phones[index] = &phoneValues[index]
	}
	return accounts, phones
}

// persistPhonePoolUpdate 把一次池内事务写入最新 state。完整 phones 与本次
// auth_phone 绑定在同一个 mutateState 中落盘，避免状态已经“使用中”而账号
// 仍未绑定，或账号已绑定但号码仍显示“可用”的半完成文件。
func (a *App) persistPhonePoolUpdate(update phoneprovider.PoolUpdate) error {
	return a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		snapshot["phones"] = uiPhonesToSnapshot(update.Phones)
		rows, _ := snapshot["accounts"].([]any)
		for _, binding := range update.Accounts {
			for _, row := range rows {
				accountMap, ok := row.(map[string]any)
				if !ok {
					continue
				}
				account := models.AccountFromMap(accountMap)
				if !strings.EqualFold(account.Email, binding.Email) {
					continue
				}
				accountMap["auth_phone_number"] = binding.Number
				accountMap["auth_phone_sms_url"] = binding.SMSURL
				break
			}
		}
		return snapshot, map[string]bool{}, nil
	})
}

// mutatePhoneState 将 UI 对 phones 的直接编辑也纳入号码池同一把锁。若只在
// mutateState 返回后再刷新，运行任务可能恰好在两者之间保存旧内存快照，
// 从而覆盖刚导入、重置或手动取码写入的数据。
func (a *App) mutatePhoneState(
	flush bool,
	fn func(map[string]any) (map[string]any, map[string]bool, error),
) error {
	if fn == nil {
		return fmt.Errorf("手机号池状态变更函数为空")
	}
	return a.sharedPhonePool().Refresh(func() ([]*models.MailAccount, []*models.PhoneEntry, error) {
		var synchronized map[string]any
		err := a.mutateState(flush, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
			next, dirty, mutateErr := fn(snapshot)
			if next == nil {
				next = snapshot
			}
			synchronized = next
			return next, dirty, mutateErr
		})
		if err != nil {
			return nil, nil, err
		}
		accounts, phones := phonePoolPointers(synchronized)
		return accounts, phones, nil
	})
}
