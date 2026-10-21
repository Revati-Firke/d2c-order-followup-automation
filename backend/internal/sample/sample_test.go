package sample

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"d2c-order-followup-automation/internal/domain"
)

func TestCommittedCSVSampleValidatesAndCoversEveryRule(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "sample_data", "d2c_order_followup_sample_109.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 110 {
		t.Fatalf("expected header plus 109 sample rows, got %d rows", len(rows))
	}
	orders, invalid := domain.ValidateRows(rows)
	if len(orders) != 106 || len(invalid) != 3 {
		t.Fatalf("committed CSV validation got %d accepted and %d invalid rows: %+v", len(orders), len(invalid), invalid)
	}
	byID := make(map[string]domain.Order, len(orders))
	for _, order := range orders {
		byID[order.OrderID] = order
	}
	now := time.Date(2026, 10, 21, 14, 13, 0, 0, time.UTC)
	cases := map[string]string{
		"EXC-UNCONFIRMED":        "unconfirmed",
		"EXC-NOT-DISPATCHED-30H": "dispatch-delay",
		"EXC-DISPATCHED-LATE":    "delivery-delay",
		"EXC-FAILED-ATTEMPT":     "failed-attempt",
		"EXC-HIGH-VALUE-LATE":    "high-value-undelivered",
	}
	for orderID, rule := range cases {
		order, exists := byID[orderID]
		if !exists {
			t.Errorf("sample order %s was rejected or missing", orderID)
			continue
		}
		if !containsRule(domain.EvaluateExceptions(order, now, domain.DefaultConfig), rule) {
			t.Errorf("sample order %s did not produce rule %s", orderID, rule)
		}
	}
	thresholdOrder, exists := byID["EXC-18H-THRESHOLD"]
	if !exists {
		t.Fatal("18-hour threshold sample order was rejected or missing")
	}
	if containsRule(domain.EvaluateExceptions(thresholdOrder, now, domain.DefaultConfig), "dispatch-delay") {
		t.Fatal("18-hour sample should not trigger with the default 24-hour threshold")
	}
	config := domain.DefaultConfig
	config.DispatchHours = 12
	if !containsRule(domain.EvaluateExceptions(thresholdOrder, now, config), "dispatch-delay") {
		t.Fatal("18-hour sample should trigger after changing dispatch threshold to 12 hours")
	}
}

func containsRule(exceptions []domain.Exception, rule string) bool {
	for _, exception := range exceptions {
		if exception.Rule == rule {
			return true
		}
	}
	return false
}
