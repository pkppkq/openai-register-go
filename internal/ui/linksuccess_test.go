package ui

// 本文件只向任务注册表写入 fake 任务并直接提交假长链结果；不会打开浏览器、
// 创建 checkout、访问代理或调用任何外部服务。

import (
	"context"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

func linkSuccessTestApp(t *testing.T, pause bool) *App {
	t.Helper()
	return newTempApp(t, map[string]any{
		"schema_version": 1,
		"accounts": []any{
			accountMap("winner@example.com", "free", "", "未分组"),
			accountMap("other@example.com", "free", "", "未分组"),
		},
		"settings": map[string]any{
			"pause_others_on_link_success": pause,
			"success_sound_enabled":        false,
		},
	})
}

func registerLinkSuccessTestJob(
	t *testing.T,
	app *App,
	kind JobKind,
	email string,
) (string, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	id, err := app.registerJob(kind, email, "", cancel)
	if err != nil {
		cancel()
		t.Fatalf("registerJob: %v", err)
	}
	return id, ctx, cancel
}

func TestLinkSuccessCancelsOtherRunningJobsButKeepsWinnerSucceeded(t *testing.T) {
	app := linkSuccessTestApp(t, true)
	winnerID, _, cancelWinner := registerLinkSuccessTestJob(t, app, JobRelink, "winner@example.com")
	t.Cleanup(cancelWinner)
	otherID, otherCtx, cancelOther := registerLinkSuccessTestJob(t, app, JobRegister, "other@example.com")
	t.Cleanup(cancelOther)

	app.finishJob(
		winnerID,
		JobRelink,
		models.MailAccount{Email: "winner@example.com"},
		&worker.PayLinkResult{URL: "https://example.test/pay/fixture"},
		nil,
		false,
	)

	select {
	case <-otherCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("长链成功后其他任务没有被取消")
	}
	winner, ok := app.jobView(winnerID)
	if !ok || winner.Status != StatusSucceeded {
		t.Fatalf("成功任务状态异常: %#v", winner)
	}
	other, ok := app.jobView(otherID)
	if !ok || other.Status != StatusRunning {
		t.Fatalf("取消信号不应伪造终态，真实 worker 仍负责收尾: %#v", other)
	}
	app.markJobFinished(otherID, nil, otherCtx.Err(), true)
}

func TestLinkSuccessSettingOffLeavesOtherJobsRunning(t *testing.T) {
	app := linkSuccessTestApp(t, false)
	winnerID, _, cancelWinner := registerLinkSuccessTestJob(t, app, JobRelink, "winner@example.com")
	t.Cleanup(cancelWinner)
	otherID, otherCtx, cancelOther := registerLinkSuccessTestJob(t, app, JobRegister, "other@example.com")
	t.Cleanup(cancelOther)

	app.finishJob(
		winnerID,
		JobRelink,
		models.MailAccount{Email: "winner@example.com"},
		&worker.PayLinkResult{URL: "https://example.test/pay/fixture"},
		nil,
		false,
	)

	select {
	case <-otherCtx.Done():
		t.Fatal("关闭暂停设置后不应取消其他任务")
	default:
	}
	app.markJobFinished(otherID, nil, nil, false)
}

func TestNonLinkResultNeverPausesOtherJobs(t *testing.T) {
	app := linkSuccessTestApp(t, true)
	winnerID, _, cancelWinner := registerLinkSuccessTestJob(t, app, JobRegister, "winner@example.com")
	t.Cleanup(cancelWinner)
	otherID, otherCtx, cancelOther := registerLinkSuccessTestJob(t, app, JobRegister, "other@example.com")
	t.Cleanup(cancelOther)

	app.finishJob(
		winnerID,
		JobRegister,
		models.MailAccount{Email: "winner@example.com"},
		map[string]any{"access_token": "fixture-token"},
		nil,
		false,
	)

	select {
	case <-otherCtx.Done():
		t.Fatal("没有 URL 的 Session 成功不应暂停其他任务")
	default:
	}
	app.markJobFinished(otherID, nil, nil, false)
}

func TestBatchParentLinkSuccessCancelsItsOwnContext(t *testing.T) {
	app := linkSuccessTestApp(t, true)
	parentID, parentCtx, cancelParent := registerLinkSuccessTestJob(t, app, JobBatchLink, "")
	t.Cleanup(cancelParent)

	app.handleLinkSuccess(parentID, "winner@example.com")

	select {
	case <-parentCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("批量提链成功后父 context 未取消，其他并发尝试会继续")
	}
	app.markJobFinished(parentID, nil, parentCtx.Err(), true)
}
