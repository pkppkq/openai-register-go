package ui

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// TestRiskyBindingsRequireConfirmationWithoutSideEffects 永久锁定 Wails 风险边界：
// 未确认请求必须在创建任务、修改临时状态或进入网络准备阶段前同步返回。
func TestRiskyBindingsRequireConfirmationWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name string
		call func(*App) error
	}{
		{
			name: "单账号注册",
			call: func(app *App) error {
				_, err := app.StartRegister(StartRegisterRequest{
					Email: "locked@example.com", CollectSession: true, Confirmed: false,
				})
				return err
			},
		},
		{
			name: "单账号重新提链",
			call: func(app *App) error {
				_, err := app.GenerateLinks(GenerateLinksRequest{
					Email: "locked@example.com", Confirmed: false,
				})
				return err
			},
		},
		{
			name: "批量注册",
			call: func(app *App) error {
				_, err := app.StartBatchRegister(StartBatchRequest{
					Emails: []string{"locked@example.com"}, Confirmed: false,
				})
				return err
			},
		},
		{
			name: "批量提链",
			call: func(app *App) error {
				// 空选择确保即使门禁未来意外后移，测试也不会触达真实支付流程。
				_, err := app.StartBatchGenerateLinks(StartLinkBatchRequest{Confirmed: false})
				return err
			},
		},
		{
			name: "应用提供商代理",
			call: func(app *App) error {
				_, err := app.ApplyProviderProxySettings(false)
				return err
			},
		},
		{
			name: "删除账号",
			call: func(app *App) error {
				_, err := app.DeleteAccounts([]string{"locked@example.com"}, false)
				return err
			},
		},
		{
			name: "切换为free",
			call: func(app *App) error {
				_, err := app.SetAccountType([]string{"locked@example.com"}, "free", false)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := localOpsSnapshot(
				[]models.MailAccount{{
					Email:       "locked@example.com",
					AccountType: "team",
					Status:      statusEmailLocked,
					OpenaiRT:    "rt-must-stay",
				}},
				map[string]any{
					"locked@example.com": map[string]any{"access_token": "token-must-stay"},
				},
			)
			app, stateFile := newLocalOpsTestApp(t, snapshot)
			before, err := os.ReadFile(stateFile)
			if err != nil {
				t.Fatalf("读取调用前临时状态: %v", err)
			}

			err = test.call(app)
			if err == nil {
				t.Fatal("未确认请求没有被拒绝")
			}
			if !strings.Contains(err.Error(), "确认") {
				t.Fatalf("拒绝原因=%q，期望明确提示确认", err)
			}
			if jobs := app.ListJobs(); len(jobs) != 0 {
				t.Fatalf("拒绝请求仍创建了 %d 个任务: %#v", len(jobs), jobs)
			}

			after, readErr := os.ReadFile(stateFile)
			if readErr != nil {
				t.Fatalf("读取调用后临时状态: %v", readErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("拒绝请求修改了临时状态文件")
			}
		})
	}
}

// Wails 只导出 App 的公开方法；该反射断言防止通用启动器再次暴露给 WebView。
func TestAppDoesNotExposeGenericStartJob(t *testing.T) {
	if _, ok := reflect.TypeOf(&App{}).MethodByName("StartJob"); ok {
		t.Fatal("App 仍公开 StartJob，WebView 可以绕过风险确认")
	}
}
