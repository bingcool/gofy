package demoservice_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bingcool/gofy-interface-api/contracts/common/envelope"
	"github.com/bingcool/gofy-interface-api/contracts/common/errcode"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
	"github.com/yourorg/interface-service/appinterface/demoservice/productroute"
)

func TestEnvelopeRoundTripKeepsUint64Precision(t *testing.T) {
	payload := envelope.Envelope[orderdto.OrderDTO]{
		Code: 0,
		Msg:  "success",
		Data: orderdto.OrderDTO{
			ID:        18446744073709551615,
			UserID:    10001,
			Amount:    1999,
			Status:    orderenum.OrderStatusPaid,
			CreatedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		},
		ReqID: "req-1",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded envelope.Envelope[orderdto.OrderDTO]
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Data.ID != payload.Data.ID {
		t.Fatalf("id = %d, want %d", decoded.Data.ID, payload.Data.ID)
	}
	if decoded.ReqID != "req-1" || decoded.Msg != "success" {
		t.Fatalf("envelope = %+v", decoded)
	}
}

func TestUint64StringAcceptsStringAndNumber(t *testing.T) {
	var fromString types.Uint64String
	if err := json.Unmarshal([]byte(`"1234567890123456789"`), &fromString); err != nil {
		t.Fatalf("string: %v", err)
	}
	if fromString.String() != "1234567890123456789" {
		t.Fatalf("value = %s", fromString)
	}
	var fromNumber types.Uint64String
	if err := json.Unmarshal([]byte(`42`), &fromNumber); err != nil {
		t.Fatalf("number: %v", err)
	}
	if fromNumber.Uint64() != 42 {
		t.Fatalf("value = %d", fromNumber)
	}
	raw, err := json.Marshal(fromString)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `"1234567890123456789"` {
		t.Fatalf("marshaled = %s", raw)
	}
	if typ, format := fromString.OpenAPIType(); typ != "string" || format != "uint64" {
		t.Fatalf("openapi = %s/%s", typ, format)
	}
}

func TestDateJSONAndText(t *testing.T) {
	date, err := types.ParseDate("2026-11-04")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	raw, err := json.Marshal(date)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `"2026-11-04"` {
		t.Fatalf("json = %s", raw)
	}
	text, err := date.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(text) != "2026-11-04" {
		t.Fatalf("text = %s", text)
	}
}

func TestEnumHelpers(t *testing.T) {
	if !orderenum.OrderStatusPaid.Valid() {
		t.Fatal("paid should be valid")
	}
	if orderenum.OrderStatus("unknown").Valid() {
		t.Fatal("unknown should be invalid")
	}
	status, ok := orderenum.ParseOrderStatus("cancelled")
	if !ok || status != orderenum.OrderStatusCancelled {
		t.Fatalf("ParseOrderStatus = %v, %v", status, ok)
	}
	if orderenum.OrderStatusPaid.String() != "已支付" {
		t.Fatalf("label = %q", orderenum.OrderStatusPaid.String())
	}
}

func TestOrderCodesAreInTheirBand(t *testing.T) {
	for _, code := range []errcode.Code{demoservice.ErrCodeOrderNotFound, demoservice.ErrCodeOrderClosed} {
		if !errcode.InRange(code) {
			t.Fatalf("code %d is outside every declared range", code)
		}
	}
	for _, fault := range []errcode.Fault{demoservice.ErrOrderNotFound, demoservice.ErrOrderClosed} {
		if fault.Code != demoservice.ErrCodeOrderNotFound && fault.Code != demoservice.ErrCodeOrderClosed {
			t.Fatalf("fault = %+v", fault)
		}
		if fault.Message == "" || fault.Status == 0 {
			t.Fatalf("incomplete fault: %+v", fault)
		}
	}
	if err := demoservice.ErrOrderNotFound; err.Error() != "order not found" {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestProductCodesAreInTheirBand(t *testing.T) {
	if !errcode.InRange(demoservice.ErrCodeProductNotFound) {
		t.Fatalf("code %d is outside every declared range", demoservice.ErrCodeProductNotFound)
	}
	if got := demoservice.ErrCodeProductNotFound; got < 60000 || got > 60999 {
		t.Fatalf("product code %d is outside the 60000-60999 band", got)
	}
	fault := demoservice.ErrProductNotFound
	if fault.Code != demoservice.ErrCodeProductNotFound || fault.Message != "product not found" || fault.Status != 404 {
		t.Fatalf("fault = %+v", fault)
	}
}

func TestPageRequestOffset(t *testing.T) {
	page := paging.PageRequest{Page: 3, PageSize: 20}
	if got := page.Offset(); got != 40 {
		t.Fatalf("Offset = %d", got)
	}
}

func TestRouteMetadata(t *testing.T) {
	routes := demoservice.Routes()
	if len(routes) != 7 {
		t.Fatalf("routes = %d", len(routes))
	}
	seen := map[string]bool{}
	for _, desc := range routes {
		spec := desc.Spec()
		if spec.OperationID == "" || spec.Method == "" || spec.Path == "" {
			t.Fatalf("incomplete spec: %+v", spec)
		}
		if seen[spec.OperationID] {
			t.Fatalf("duplicate operation id %q", spec.OperationID)
		}
		seen[spec.OperationID] = true
		if spec.NoContent && spec.ResponseKind != "" {
			t.Fatalf("%s: NoContent and ResponseKind conflict", spec.OperationID)
		}
		if desc.RequestType() == nil || desc.ResponseType() == nil {
			t.Fatalf("%s: missing types", spec.OperationID)
		}
	}
	cancel := orderroute.CancelOrder.Spec()
	if !cancel.NoContent || cancel.Method != "DELETE" {
		t.Fatalf("cancel spec = %+v", cancel)
	}
	if got := orderroute.GetOrder.Spec().Method; got != "GET" {
		t.Fatalf("get order method = %q", got)
	}
	product := productroute.GetProduct.Spec()
	if product.Method != "GET" || product.Path != "/products/{id}" {
		t.Fatalf("get product spec = %+v", product)
	}
	if tags := product.Tags; len(tags) != 2 || tags[0] != productroute.ServiceTag || tags[1] != productroute.PrefixTag {
		t.Fatalf("get product tags = %v", tags)
	}
	if productroute.ListProducts.Spec().Path != "/products" {
		t.Fatalf("list products path = %q", productroute.ListProducts.Spec().Path)
	}
}
