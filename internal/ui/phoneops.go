package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/phoneprovider"
)

// JobManualPhoneCode 是只读轮询已保存 sms_url 的手动取码任务。
const JobManualPhoneCode JobKind = "manual_phone_code"

// PhoneView 是手机号池的 Wails 传输结构。
type PhoneView struct {
	Number       string `json:"number"`
	SMSURL       string `json:"smsUrl"`
	ReceiveCount int    `json:"receiveCount"`
	Status       string `json:"status"`
	LastCode     string `json:"lastCode"`
	LastError    string `json:"lastError"`
}

// PhonesResult 返回手机号池操作后的完整状态。
type PhonesResult struct {
	Imported int         `json:"imported"`
	Updated  int         `json:"updated"`
	Total    int         `json:"total"`
	Errors   []string    `json:"errors"`
	Phones   []PhoneView `json:"phones"`
	Message  string      `json:"message"`
}

// ManualPhoneCodeResult 是手动取码任务的可读取结果。
type ManualPhoneCodeResult struct {
	Number string `json:"number"`
	Code   string `json:"code"`
}

var fetchManualPhoneCode = phoneprovider.FetchManualCode

func init() {
	// 复用远程任务的统一结果读取绑定；该类型仍然不属于会租号的 worker 入口。
	networkJobKinds[JobManualPhoneCode] = true
}

// ListPhones 只读返回当前手机号池。
func (a *App) ListPhones() (PhonesResult, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return PhonesResult{}, fmt.Errorf("读取 state.json 失败: %w", err)
	}
	phones := uiPhonesFromSnapshot(snapshot)
	return PhonesResult{
		Total:  len(phones),
		Errors: []string{},
		Phones: uiPhoneViews(phones),
	}, nil
}

// ImportPhones 解析并按手机号合并导入文本。已有号码会更新 sms_url；仅当其
// 当前为“不可用”时恢复为“可用”并清除最近错误，与 Python 基线一致。
func (a *App) ImportPhones(text string) (PhonesResult, error) {
	out := PhonesResult{Errors: []string{}}
	lines := uiPhoneInputLines(text)
	if len(lines) == 0 {
		return out, fmt.Errorf("请先粘贴手机号")
	}

	err := a.mutatePhoneState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		phones := uiPhonesFromSnapshot(snapshot)
		for lineIndex, line := range lines {
			phone, parseErr := models.ParsePhoneLine(line)
			if parseErr != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("第 %d 行: %v", lineIndex+1, parseErr))
				continue
			}
			found := -1
			for index := range phones {
				if phones[index].Number == phone.Number {
					found = index
					break
				}
			}
			if found >= 0 {
				phones[found].SMSURL = phone.SMSURL
				if phones[found].Status == "不可用" {
					phones[found].Status = "可用"
					phones[found].LastError = ""
				}
				out.Updated++
			} else {
				phones = append(phones, phone)
			}
			out.Imported++
		}
		snapshot["phones"] = uiPhonesToSnapshot(phones)
		out.Total = len(phones)
		out.Phones = uiPhoneViews(phones)
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}

	out.Message = fmt.Sprintf("已导入 %d 个手机号", out.Imported)
	if len(out.Errors) > 0 {
		out.Message += "；失败: " + strings.Join(out.Errors, "; ")
	}
	a.Log(out.Message)
	return out, nil
}

// ResetPhones 将所有号码恢复为可用并清空错误和接码次数；最近验证码保留，
// 便于操作者继续查看上一次成功结果。
func (a *App) ResetPhones() (PhonesResult, error) {
	out := PhonesResult{Errors: []string{}}
	err := a.mutatePhoneState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		phones := uiPhonesFromSnapshot(snapshot)
		for index := range phones {
			if phones[index].Status != "可用" || phones[index].LastError != "" || phones[index].ReceiveCount != 0 {
				out.Updated++
			}
			phones[index].Status = "可用"
			phones[index].LastError = ""
			phones[index].ReceiveCount = 0
		}
		snapshot["phones"] = uiPhonesToSnapshot(phones)
		out.Total = len(phones)
		out.Phones = uiPhoneViews(phones)
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	out.Message = "手机号池已重置为可用"
	a.Log(out.Message)
	return out, nil
}

// ClearPhones 只有 confirmed=true 且当前没有运行中任务时才清空手机号池。
func (a *App) ClearPhones(confirmed bool) (PhonesResult, error) {
	out := PhonesResult{Errors: []string{}}
	if !confirmed {
		return out, fmt.Errorf("清空手机号池需要明确确认")
	}
	unlockJobs, err := a.uiLockPhoneClear()
	if err != nil {
		return out, err
	}
	defer unlockJobs()

	err = a.mutatePhoneState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		phones := uiPhonesFromSnapshot(snapshot)
		if len(phones) == 0 {
			out.Phones = []PhoneView{}
			return snapshot, map[string]bool{}, errNoStateChange
		}
		out.Updated = len(phones)
		snapshot["phones"] = []any{}
		out.Phones = []PhoneView{}
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	if out.Updated == 0 {
		return out, nil
	}
	out.Message = "手机号池已清空"
	a.Log(out.Message)
	return out, nil
}

// StartManualPhoneCode 启动一个可取消的 30 秒任务。它只使用手机号池中已经
// 保存的 sms_url；不会调用 Provider.Next、SMSBower 客户端或任何租号接口。
func (a *App) StartManualPhoneCode(number string) (JobView, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return JobView{}, fmt.Errorf("请先选中手机号")
	}
	snapshot, err := a.snapshot()
	if err != nil {
		return JobView{}, err
	}
	phone, ok := uiPhoneByNumber(uiPhonesFromSnapshot(snapshot), number)
	if !ok {
		return JobView{}, fmt.Errorf("手机号不存在: %s", number)
	}
	if err := phoneprovider.ValidateManualSMSURL(phone.SMSURL); err != nil {
		return JobView{}, err
	}

	// 捕获本次启动时保存的 URL；即使 UI 随后编辑输入框，本任务也不会轮询
	// 未经手机号池读取的新地址。
	savedURL := phone.SMSURL
	return a.startNetworkJobWithLogEmail(JobManualPhoneCode, phone.Number, "", func(ctx context.Context, log func(string)) (any, error) {
		log(fmt.Sprintf("[手动取码] 开始读取 %s", phone.Number))
		code, fetchErr := fetchManualPhoneCode(ctx, phone.Number, savedURL)
		result := ManualPhoneCodeResult{Number: phone.Number, Code: code}
		if fetchErr != nil {
			// 用户取消只结束任务，不把 context.Canceled 写进手机号的错误栏。
			if ctx.Err() != nil || errors.Is(fetchErr, context.Canceled) {
				log(fmt.Sprintf("[手动取码] %s 已取消", phone.Number))
				return result, fetchErr
			}
			if saveErr := a.uiSaveManualPhoneResult(phone.Number, "", fetchErr.Error()); saveErr != nil {
				return result, fmt.Errorf("%v；保存失败状态失败: %w", fetchErr, saveErr)
			}
			log(fmt.Sprintf("[手动取码] %s 读取失败: %v", phone.Number, fetchErr))
			return result, fetchErr
		}
		if saveErr := a.uiSaveManualPhoneResult(phone.Number, code, ""); saveErr != nil {
			return result, fmt.Errorf("保存手动取码结果失败: %w", saveErr)
		}
		log(fmt.Sprintf("[手动取码] %s 读取到验证码: %s", phone.Number, code))
		return result, nil
	})
}

func (a *App) uiSaveManualPhoneResult(number, code, lastError string) error {
	return a.mutatePhoneState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		phones := uiPhonesFromSnapshot(snapshot)
		for index := range phones {
			if phones[index].Number != number {
				continue
			}
			if code != "" {
				phones[index].LastCode = code
				phones[index].LastError = ""
			} else {
				phones[index].LastError = lastError
			}
			// 手动查看不代表注册流程消费了一个接码次数，也不改变可用状态。
			snapshot["phones"] = uiPhonesToSnapshot(phones)
			return snapshot, map[string]bool{}, nil
		}
		return snapshot, nil, fmt.Errorf("手机号不存在: %s", number)
	})
}

// uiLockPhoneClear 在检查和落盘之间阻止新任务登记，避免并发调用绕过
// “任务运行中不可清空”的安全条件。
func (a *App) uiLockPhoneClear() (func(), error) {
	if a.jobs == nil {
		return func() {}, nil
	}
	a.jobs.mu.Lock()
	for _, running := range a.jobs.jobs {
		if running.view.Status == StatusRunning {
			a.jobs.mu.Unlock()
			return nil, fmt.Errorf("任务正在运行，不能清空手机号池")
		}
	}
	return a.jobs.mu.Unlock, nil
}

func uiPhoneInputLines(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := make([]string, 0)
	for _, raw := range strings.Split(normalized, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func uiPhonesFromSnapshot(snapshot map[string]any) []models.PhoneEntry {
	rows, _ := snapshot["phones"].([]any)
	phones := make([]models.PhoneEntry, 0, len(rows))
	for _, row := range rows {
		if value, ok := row.(map[string]any); ok {
			phones = append(phones, models.PhoneFromMap(value))
		}
	}
	return phones
}

func uiPhonesToSnapshot(phones []models.PhoneEntry) []any {
	rows := make([]any, 0, len(phones))
	for _, phone := range phones {
		rows = append(rows, models.PhoneToMap(phone))
	}
	return rows
}

func uiPhoneViews(phones []models.PhoneEntry) []PhoneView {
	out := make([]PhoneView, 0, len(phones))
	for _, phone := range phones {
		out = append(out, PhoneView{
			Number:       phone.Number,
			SMSURL:       phone.SMSURL,
			ReceiveCount: phone.ReceiveCount,
			Status:       phone.Status,
			LastCode:     phone.LastCode,
			LastError:    phone.LastError,
		})
	}
	return out
}

func uiPhoneByNumber(phones []models.PhoneEntry, number string) (models.PhoneEntry, bool) {
	for _, phone := range phones {
		if phone.Number == number {
			return phone, true
		}
	}
	return models.PhoneEntry{}, false
}
