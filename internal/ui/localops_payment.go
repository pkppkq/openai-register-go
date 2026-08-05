package ui

import (
	"fmt"
	"strings"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/settings"
)

// PaymentCardView 是支付卡池的 Wails 传输结构。
type PaymentCardView struct {
	Card   string `json:"card"`
	Month  string `json:"month"`
	Year   string `json:"year"`
	CVV    string `json:"cvv"`
	Status string `json:"status"`
}

// PaymentCardsResult 返回卡池导入、重置或读取后的完整状态。
type PaymentCardsResult struct {
	Imported int               `json:"imported"`
	Updated  int               `json:"updated"`
	Total    int               `json:"total"`
	Errors   []string          `json:"errors"`
	Cards    []PaymentCardView `json:"cards"`
	Message  string            `json:"message"`
}

// PaymentCardConsumeResult 返回支付窗口应使用的完整 7 段卡资料。
type PaymentCardConsumeResult struct {
	CardText string          `json:"cardText"`
	Card     PaymentCardView `json:"card"`
	FromPool bool            `json:"fromPool"`
	Message  string          `json:"message"`
}

// ListPaymentCards 读取本地支付卡池，不修改状态。
func (a *App) ListPaymentCards() (PaymentCardsResult, error) {
	snapshot, err := a.snapshot()
	if err != nil {
		return PaymentCardsResult{}, fmt.Errorf("读取 state.json 失败: %w", err)
	}
	cards := localPaymentCardsFromSnapshot(snapshot)
	return PaymentCardsResult{
		Total: len(cards), Errors: []string{}, Cards: localPaymentCardViews(cards),
	}, nil
}

// ImportPaymentCards 从“卡号|月|年|CVV”文本导入卡池。
//
// 同卡号重新导入会更新卡资料，但保留原“未用/已用”状态。
func (a *App) ImportPaymentCards(text string) (PaymentCardsResult, error) {
	out := PaymentCardsResult{Errors: []string{}}
	lines := []string{}
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line := localStrip(raw); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return out, fmt.Errorf("请先粘贴支付卡")
	}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		cards := localPaymentCardsFromSnapshot(snapshot)
		for lineIndex, line := range lines {
			card, err := models.ParsePaymentCardLine(line)
			if err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("第 %d 行: %v", lineIndex+1, err))
				continue
			}
			found := -1
			for index := range cards {
				if cards[index].Card == card.Card {
					found = index
					break
				}
			}
			if found >= 0 {
				card.Status = cards[found].Status
				cards[found] = card
				out.Updated++
			} else {
				cards = append(cards, card)
			}
			out.Imported++
		}
		snapshot["payment_cards"] = localPaymentCardsToSnapshot(cards)
		out.Total = len(cards)
		out.Cards = localPaymentCardViews(cards)
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	out.Message = fmt.Sprintf("已导入 %d 张支付卡", out.Imported)
	if len(out.Errors) > 0 {
		out.Message += "；失败: " + strings.Join(out.Errors, "; ")
	}
	a.Log(out.Message)
	return out, nil
}

// ResetPaymentCards 把卡池内所有卡恢复为“未用”。
func (a *App) ResetPaymentCards() (PaymentCardsResult, error) {
	out := PaymentCardsResult{Errors: []string{}}
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		cards := localPaymentCardsFromSnapshot(snapshot)
		for index := range cards {
			if cards[index].Status != "未用" {
				out.Updated++
			}
			cards[index].Status = "未用"
		}
		snapshot["payment_cards"] = localPaymentCardsToSnapshot(cards)
		out.Total = len(cards)
		out.Cards = localPaymentCardViews(cards)
		return snapshot, map[string]bool{}, nil
	})
	if err != nil {
		return out, err
	}
	out.Message = "支付卡池已重置为未用"
	a.Log(out.Message)
	return out, nil
}

// ReplacePayPalCardHead 用卡池卡号、有效期和 CVV 替换 7 段 PayPal 卡资料头。
func (a *App) ReplacePayPalCardHead(paypalCard string, card PaymentCardView) (string, error) {
	return localReplacePayPalCardHead(paypalCard, localPaymentCardFromView(card))
}

// ConsumePaymentCard 取第一张“未用”卡并原子标记为“已用”。
//
// 本方法只准备本地字符串，不打开浏览器、不访问支付页面。基础卡资料为空时
// 返回空字符串且不消费卡；有卡池但已经耗尽时返回错误。
func (a *App) ConsumePaymentCard() (PaymentCardConsumeResult, error) {
	var out PaymentCardConsumeResult
	err := a.mutateState(true, func(snapshot map[string]any) (map[string]any, map[string]bool, error) {
		base := localStrip(settings.FromSnapshot(snapshot).PaypalCard)
		cards := localPaymentCardsFromSnapshot(snapshot)
		if base == "" {
			return snapshot, map[string]bool{}, errNoStateChange
		}
		for index := range cards {
			if cards[index].Status != "未用" {
				continue
			}
			value, err := localReplacePayPalCardHead(base, cards[index])
			if err != nil {
				return snapshot, nil, err
			}
			cards[index].Status = "已用"
			snapshot["payment_cards"] = localPaymentCardsToSnapshot(cards)
			out = PaymentCardConsumeResult{
				CardText: value,
				Card:     localPaymentCardView(cards[index]),
				FromPool: true,
				Message:  "本次支付使用卡: " + cards[index].Card,
			}
			return snapshot, map[string]bool{}, nil
		}
		if len(cards) > 0 {
			return snapshot, nil, fmt.Errorf("支付卡池没有未用卡，请导入新卡或重置卡池")
		}
		out.CardText = base
		return snapshot, map[string]bool{}, errNoStateChange
	})
	if err != nil {
		return out, err
	}
	if out.Message != "" {
		a.Log(out.Message)
	}
	return out, nil
}

func localReplacePayPalCardHead(paypalCard string, card models.PaymentCard) (string, error) {
	parts := strings.Split(paypalCard, "----")
	if len(parts) < 7 {
		return "", fmt.Errorf("PayPal 卡信息格式错误，需要至少 7 段 ---- 分隔")
	}
	parts[0] = card.Card
	parts[1] = card.Year + "/" + card.Month
	parts[2] = card.CVV
	return strings.Join(parts, "----"), nil
}

func localPaymentCardsFromSnapshot(snapshot map[string]any) []models.PaymentCard {
	rows, _ := snapshot["payment_cards"].([]any)
	out := make([]models.PaymentCard, 0, len(rows))
	for _, row := range rows {
		if item, ok := row.(map[string]any); ok && len(item) > 0 {
			out = append(out, models.CardFromMap(item))
		}
	}
	return out
}

func localPaymentCardsToSnapshot(cards []models.PaymentCard) []any {
	rows := make([]any, 0, len(cards))
	for _, card := range cards {
		rows = append(rows, models.CardToMap(card))
	}
	return rows
}

func localPaymentCardViews(cards []models.PaymentCard) []PaymentCardView {
	out := make([]PaymentCardView, 0, len(cards))
	for _, card := range cards {
		out = append(out, localPaymentCardView(card))
	}
	return out
}

func localPaymentCardView(card models.PaymentCard) PaymentCardView {
	return PaymentCardView{
		Card: card.Card, Month: card.Month, Year: card.Year, CVV: card.CVV, Status: card.Status,
	}
}

func localPaymentCardFromView(card PaymentCardView) models.PaymentCard {
	return models.PaymentCard{
		Card: card.Card, Month: card.Month, Year: card.Year, CVV: card.CVV, Status: card.Status,
	}
}
