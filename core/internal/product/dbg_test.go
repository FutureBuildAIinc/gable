package product

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDebugCreateParse(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/products", strings.NewReader(`{"sku":"X1","description":"d","stock_uom":"PCS","base_price_ten_thousandths":5250000,"reorder_point":"40"}`))
	p, err := ParseCreate(req)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Logf("parsed: %+v", p)
	_ = http.StatusOK
}
