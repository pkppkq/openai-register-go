package ui

import (
	"context"
	"strings"
	"testing"
)

func TestJobLoggerRoutesPlainLinesByRegisteredJobEmail(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts": []any{
			accountMap("Owner@Example.com", "free", "", "未分组"),
			accountMap("other@example.com", "free", "", "未分组"),
		},
	})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	id, err := app.registerJob(JobAuthOnly, "Owner@Example.com", "", cancel)
	if err != nil {
		t.Fatalf("registerJob: %v", err)
	}
	log := app.jobLogger(id)

	// 独立 worker 的收尾日志可能发生在任务状态已经标记完成之后；路由必须
	// 继续使用登记时的稳定映射，而不是依赖当前运行状态。
	app.markJobFinished(id, nil, nil, false)
	log("登录完成")
	// worker 偶尔仍携带旧式邮箱前缀。登记邮箱必须优先，且内部前缀不应泄漏
	// 到结构化消息正文。
	log("[other@example.com] 提取长链失败")

	records := app.logs.AccountRecords("owner@example.com")
	if len(records) != 2 {
		t.Fatalf("登记账户日志数 = %d, want 2: %#v", len(records), records)
	}
	for _, record := range records {
		if record.Email != "owner@example.com" || record.Scope != "account" {
			t.Fatalf("结构化路由错误: %#v", record)
		}
		if !strings.Contains(record.Message, "["+id+"]") {
			t.Fatalf("可见 job 前缀丢失: %q", record.Message)
		}
		if strings.Contains(record.Message, "other@example.com") {
			t.Fatalf("内部邮箱前缀泄漏到消息正文: %q", record.Message)
		}
	}
	if other := app.logs.AccountRecords("other@example.com"); len(other) != 0 {
		t.Fatalf("消息内邮箱覆盖了任务登记邮箱: %#v", other)
	}
	if global := app.logs.GlobalRecords(); len(global) != 0 {
		t.Fatalf("账户任务日志错误落入全局缓冲区: %#v", global)
	}
}

func TestBatchParentJobLoggerRoutesEachPrefixedLineIndependently(t *testing.T) {
	app := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts": []any{
			accountMap("a@example.com", "free", "", "未分组"),
			accountMap("b@example.com", "free", "", "未分组"),
		},
	})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	id, err := app.registerJob(JobBatchRegister, "", "", cancel)
	if err != nil {
		t.Fatalf("registerJob: %v", err)
	}
	log := app.jobLogger(id)

	log("[a@example.com] 第一个账户认证完成")
	log("[b@example.com] 第二个账户认证失败")
	log("批量任务结束")

	aRecords := app.logs.AccountRecords("a@example.com")
	bRecords := app.logs.AccountRecords("b@example.com")
	global := app.logs.GlobalRecords()
	if len(aRecords) != 1 || len(bRecords) != 1 || len(global) != 1 {
		t.Fatalf("批量父任务路由错误: a=%#v b=%#v global=%#v", aRecords, bRecords, global)
	}
	if !strings.Contains(aRecords[0].Message, "["+id+"]") ||
		!strings.Contains(bRecords[0].Message, "["+id+"]") ||
		!strings.Contains(global[0].Message, "["+id+"]") {
		t.Fatalf("job 前缀未统一保留: a=%#v b=%#v global=%#v", aRecords, bRecords, global)
	}
	if strings.Contains(aRecords[0].Message, "a@example.com") ||
		strings.Contains(bRecords[0].Message, "b@example.com") {
		t.Fatalf("批量内部邮箱前缀未剥离: a=%q b=%q", aRecords[0].Message, bRecords[0].Message)
	}
}

func TestJobLoggerSeparatesConflictIdentityFromLogEmail(t *testing.T) {
	app := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	id, err := app.registerJobWithLogEmail(JobManualPhoneCode, "+15550000", "", "", cancel)
	if err != nil {
		t.Fatalf("registerJobWithLogEmail: %v", err)
	}
	app.jobLogger(id)("手动取码开始")

	if records := app.logs.AccountRecords("+15550000"); len(records) != 0 {
		t.Fatalf("手机号被创建为伪账户日志分组: %#v", records)
	}
	global := app.logs.GlobalRecords()
	if len(global) != 1 || global[0].Email != "" || global[0].Scope != "global" {
		t.Fatalf("非账户任务未进入全局日志: %#v", global)
	}
}
