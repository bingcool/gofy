package support

import (
	"errors"
	"testing"
)

type validateItem struct {
	SkuID uint64 `json:"sku_id" validate:"required" message:"required:SKU ID不能为空"`
}

type validateRequest struct {
	UserID uint64         `json:"user_id" validate:"required" message:"required:用户ID不能为空"`
	Amount int64          `json:"amount" validate:"required,min=1" message:"required:金额不能为空,min:金额必须大于0"`
	Items  []validateItem `json:"items" validate:"dive"`
}

type plainValidateRequest struct {
	Remark string `json:"remark" validate:"max=3"`
}

type pageFields struct {
	Page int `json:"page" validate:"min=1" message:"min:页码必须大于0"`
}

type pagedValidateRequest struct {
	pageFields

	UserID uint64 `json:"user_id" validate:"required" message:"required:用户ID不能为空"`
}

func TestValidationErrorPromotesMessageTagToMsg(t *testing.T) {
	v := NewValidator()
	req := &validateRequest{Items: []validateItem{{}}}
	err := v.Struct(req)
	if err == nil {
		t.Fatal("validate: want error, got nil")
	}

	e := validationError(req, err)
	want := "用户ID不能为空; 金额不能为空; SKU ID不能为空"
	if e.Message != want {
		t.Fatalf("msg = %q, want %q", e.Message, want)
	}
	if e.Details["user_id"] != "用户ID不能为空" {
		t.Fatalf("details.user_id = %q", e.Details["user_id"])
	}
	if e.Details["sku_id"] != "SKU ID不能为空" {
		t.Fatalf("details.sku_id = %q", e.Details["sku_id"])
	}
}

func TestValidationErrorResolvesEmbeddedMessageTag(t *testing.T) {
	v := NewValidator()
	req := &pagedValidateRequest{UserID: 10001}
	err := v.Struct(req)
	if err == nil {
		t.Fatal("validate: want error, got nil")
	}

	e := validationError(req, err)
	if e.Message != "页码必须大于0" {
		t.Fatalf("msg = %q, want 页码必须大于0", e.Message)
	}
	if e.Details["page"] != "页码必须大于0" {
		t.Fatalf("details.page = %q", e.Details["page"])
	}
}

func TestValidationErrorFallsBackToDefaultMessage(t *testing.T) {
	v := NewValidator()
	req := &plainValidateRequest{Remark: "abcdef"}
	err := v.Struct(req)
	if err == nil {
		t.Fatal("validate: want error, got nil")
	}

	e := validationError(req, err)
	if e.Message != defaultValidationMessage {
		t.Fatalf("msg = %q, want %q", e.Message, defaultValidationMessage)
	}
	if want := "remark failed on the 'max' rule (3)"; e.Details["remark"] != want {
		t.Fatalf("details.remark = %q, want %q", e.Details["remark"], want)
	}
}

func TestValidationErrorIgnoresNonValidationError(t *testing.T) {
	e := validationError(&plainValidateRequest{}, errors.New("boom"))
	if e.Message != defaultValidationMessage {
		t.Fatalf("msg = %q, want %q", e.Message, defaultValidationMessage)
	}
	if len(e.Details) != 0 {
		t.Fatalf("details = %v, want empty", e.Details)
	}
}
