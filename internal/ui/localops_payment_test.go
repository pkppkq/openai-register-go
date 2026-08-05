package ui

import (
	"strings"
	"testing"

	"github.com/pkppkq/openai-register-go/internal/models"
)

func TestLocalOpsPaymentCardImportPreservesStatus(t *testing.T) {
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["payment_cards"] = []any{
		models.CardToMap(models.PaymentCard{Card: "4111111111111111", Month: "1", Year: "2030", CVV: "111", Status: "已用"}),
	}
	app, _ := newLocalOpsTestApp(t, snapshot)
	result, err := app.ImportPaymentCards(strings.Join([]string{
		"4111111111111111|02|31|222",
		"5555555555554444|12|2032|333",
		"bad-line",
	}, "\n"))
	if err != nil {
		t.Fatalf("ImportPaymentCards: %v", err)
	}
	if result.Imported != 2 || result.Updated != 1 || result.Total != 2 || len(result.Errors) != 1 {
		t.Fatalf("导入结果异常: %#v", result)
	}
	byNumber := map[string]PaymentCardView{}
	for _, card := range result.Cards {
		byNumber[card.Card] = card
	}
	if card := byNumber["4111111111111111"]; card.Status != "已用" || card.Month != "2" || card.Year != "2031" || card.CVV != "222" {
		t.Fatalf("同卡号更新未保留状态: %#v", card)
	}
	if byNumber["5555555555554444"].Status != "未用" {
		t.Fatalf("新卡状态异常: %#v", byNumber["5555555555554444"])
	}
}

func TestLocalOpsPaymentCardReplaceConsumeReset(t *testing.T) {
	base := "old-card----2029/1----999----phone----sms-token----name----address"
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["settings"] = map[string]any{"paypal_card": base}
	snapshot["payment_cards"] = []any{
		models.CardToMap(models.PaymentCard{Card: "4111111111111111", Month: "2", Year: "2031", CVV: "111", Status: "未用"}),
		models.CardToMap(models.PaymentCard{Card: "5555555555554444", Month: "12", Year: "2032", CVV: "222", Status: "未用"}),
	}
	app, _ := newLocalOpsTestApp(t, snapshot)

	replaced, err := app.ReplacePayPalCardHead(base, PaymentCardView{
		Card: "4000000000000002", Month: "7", Year: "2035", CVV: "321",
	})
	if err != nil || replaced != "4000000000000002----2035/7----321----phone----sms-token----name----address" {
		t.Fatalf("ReplacePayPalCardHead=%q, %v", replaced, err)
	}
	if _, err := app.ReplacePayPalCardHead("only----two", PaymentCardView{}); err == nil {
		t.Fatal("不足 7 段的 PayPal 卡资料应拒绝")
	}

	first, err := app.ConsumePaymentCard()
	if err != nil {
		t.Fatalf("第一次 ConsumePaymentCard: %v", err)
	}
	if !first.FromPool || first.Card.Card != "4111111111111111" ||
		first.CardText != "4111111111111111----2031/2----111----phone----sms-token----name----address" {
		t.Fatalf("第一次消费异常: %#v", first)
	}
	second, err := app.ConsumePaymentCard()
	if err != nil || second.Card.Card != "5555555555554444" {
		t.Fatalf("第二次消费异常: %#v, %v", second, err)
	}
	if _, err := app.ConsumePaymentCard(); err == nil {
		t.Fatal("卡池耗尽后应拒绝")
	}

	listed, err := app.ListPaymentCards()
	if err != nil {
		t.Fatalf("ListPaymentCards: %v", err)
	}
	for _, card := range listed.Cards {
		if card.Status != "已用" {
			t.Fatalf("消费后卡状态=%q", card.Status)
		}
	}
	reset, err := app.ResetPaymentCards()
	if err != nil || reset.Updated != 2 {
		t.Fatalf("ResetPaymentCards=%#v, %v", reset, err)
	}
	for _, card := range reset.Cards {
		if card.Status != "未用" {
			t.Fatalf("重置后卡状态=%q", card.Status)
		}
	}
}

func TestLocalOpsPaymentCardMalformedBaseDoesNotConsume(t *testing.T) {
	snapshot := localOpsSnapshot(nil, nil)
	snapshot["settings"] = map[string]any{"paypal_card": "bad----base"}
	snapshot["payment_cards"] = []any{
		models.CardToMap(models.PaymentCard{Card: "4111111111111111", Month: "1", Year: "2030", CVV: "111", Status: "未用"}),
	}
	app, _ := newLocalOpsTestApp(t, snapshot)
	if _, err := app.ConsumePaymentCard(); err == nil {
		t.Fatal("格式错误的基础卡资料应拒绝")
	}
	listed, err := app.ListPaymentCards()
	if err != nil {
		t.Fatalf("ListPaymentCards: %v", err)
	}
	if listed.Cards[0].Status != "未用" {
		t.Fatal("卡头替换失败时不应消费卡")
	}
}
