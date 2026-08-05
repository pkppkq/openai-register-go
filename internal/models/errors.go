package models

import (
	"errors"
	"fmt"
)

// statusError is implemented by the typed registration errors that carry a
// user-facing status string (Python set exc.status).
type statusError interface{ ErrorStatus() string }

// ExceptionStatus mirrors exception_status: the error's status if non-empty,
// else the default. Works through wrapped errors via errors.As.
func ExceptionStatus(err error, def string) string {
	var se statusError
	if errors.As(err, &se) {
		if s := se.ErrorStatus(); s != "" {
			return s
		}
	}
	return def
}

// AmountMismatchError mirrors AmountMismatchError: the paylink amount check
// failed. StripeAmountSource is inspected by the paylink error formatter.
type AmountMismatchError struct {
	TargetAmount       string
	ActualAmount       string
	StripeAmountSource string
}

func (e *AmountMismatchError) Error() string {
	return fmt.Sprintf("金额不匹配: 目标 %s, 实际 %s", e.TargetAmount, e.ActualAmount)
}

// ProxyExitCheckError mirrors ProxyExitCheckError: a required proxy-exit
// condition (e.g. Japan) was not met. Must propagate, never be retried.
type ProxyExitCheckError struct {
	Msg    string
	Status string
}

func (e *ProxyExitCheckError) Error() string { return e.Msg }
func (e *ProxyExitCheckError) ErrorStatus() string {
	if e.Status == "" {
		return "代理检测失败"
	}
	return e.Status
}

// NewProxyExitCheckError builds a ProxyExitCheckError with the default status.
func NewProxyExitCheckError(msg string) *ProxyExitCheckError {
	return &ProxyExitCheckError{Msg: msg, Status: "代理检测失败"}
}

// AccountDeactivatedError mirrors AccountDeactivatedError: the OpenAI account is
// deactivated (surfaced from OTP validation).
type AccountDeactivatedError struct {
	Msg    string
	Status string
}

func (e *AccountDeactivatedError) Error() string {
	if e.Msg == "" {
		return "OpenAI 账号已停用"
	}
	return e.Msg
}
func (e *AccountDeactivatedError) ErrorStatus() string {
	if e.Status == "" {
		return "账号已停用"
	}
	return e.Status
}

// NewAccountDeactivatedError builds an AccountDeactivatedError with defaults.
func NewAccountDeactivatedError() *AccountDeactivatedError {
	return &AccountDeactivatedError{Msg: "OpenAI 账号已停用", Status: "账号已停用"}
}

// PhoneRequiredError mirrors PhoneRequiredError: OpenAI demands phone verification.
type PhoneRequiredError struct {
	Msg    string
	Status string
}

func (e *PhoneRequiredError) Error() string {
	if e.Msg == "" {
		return "OpenAI 要求手机号验证"
	}
	return e.Msg
}
func (e *PhoneRequiredError) ErrorStatus() string {
	if e.Status == "" {
		return "需要手机号"
	}
	return e.Status
}

// PhoneRejectedError mirrors PhoneRejectedError: a specific phone number was
// rejected; Status classifies why (from ClassifyPhoneRejection).
type PhoneRejectedError struct {
	Msg    string
	Status string
}

func (e *PhoneRejectedError) Error() string { return e.Msg }
func (e *PhoneRejectedError) ErrorStatus() string {
	if e.Status == "" {
		return "手机号不可用"
	}
	return e.Status
}

// NewPhoneRejectedError builds a PhoneRejectedError with the default status.
func NewPhoneRejectedError(msg string) *PhoneRejectedError {
	return &PhoneRejectedError{Msg: msg, Status: "手机号不可用"}
}
