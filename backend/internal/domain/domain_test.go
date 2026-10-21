package domain

import (
	"strings"
	"testing"
	"time"
)

var testHeaders = []string{"Order ID", "Order Date", "Customer Name", "Phone Number", "Order Value", "Payment Type", "Order Status", "Dispatch Date", "Expected Delivery Date", "Assigned Employee"}

func orderRow(id, status, orderDate, dispatchDate, expectedDate, value string) []string {
	return []string{id, orderDate, "Test Customer", "9876501234", value, "Prepaid", status, dispatchDate, expectedDate, "Asha Menon"}
}

func TestEvaluateExceptions(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	testCases := []struct {
		name  string
		order Order
		rule  string
	}{
		{"unconfirmed", Order{Status: "Unconfirmed", OrderDate: now.Add(-3 * time.Hour), ExpectedDeliveryDate: now.Add(time.Hour)}, "unconfirmed"},
		{"dispatch delay", Order{Status: "Confirmed", DispatchDate: "Pending", OrderDate: now.Add(-25 * time.Hour), ExpectedDeliveryDate: now.Add(time.Hour)}, "dispatch-delay"},
		{"delivery delay", Order{Status: "Dispatched", OrderDate: now.Add(-48 * time.Hour), ExpectedDeliveryDate: now.Add(-time.Hour)}, "delivery-delay"},
		{"failed attempt", Order{Status: "Failed Delivery", OrderDate: now.Add(-48 * time.Hour), ExpectedDeliveryDate: now.Add(time.Hour)}, "failed-attempt"},
		{"high value", Order{Status: "Undelivered", OrderValue: 5001, OrderDate: now.Add(-48 * time.Hour), ExpectedDeliveryDate: now.Add(-time.Hour)}, "high-value-undelivered"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			exceptions := EvaluateExceptions(testCase.order, now, DefaultConfig)
			for _, exception := range exceptions {
				if exception.Rule == testCase.rule {
					if exception.Category == "" || exception.Reason == "" || exception.Priority == "" || exception.Action == "" || exception.Deadline.IsZero() {
						t.Fatalf("incomplete exception: %+v", exception)
					}
					return
				}
			}
			t.Fatalf("rule %q not evaluated: %+v", testCase.rule, exceptions)
		})
	}
}

func TestDispatchRuleUsesConfiguration(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	order := Order{Status: "Confirmed", DispatchDate: "Pending", OrderDate: now.Add(-18 * time.Hour), ExpectedDeliveryDate: now.Add(time.Hour)}
	if got := EvaluateExceptions(order, now, DefaultConfig); len(got) != 0 {
		t.Fatalf("default 24 hour rule should not trigger: %+v", got)
	}
	config := DefaultConfig
	config.DispatchHours = 12
	if got := EvaluateExceptions(order, now, config); len(got) != 1 || got[0].Rule != "dispatch-delay" {
		t.Fatalf("configured 12 hour rule should trigger: %+v", got)
	}
}

func TestValidateRowsReportsBadDataAndDuplicates(t *testing.T) {
	rows := [][]string{
		testHeaders,
		orderRow("valid", "Delivered", "2026-10-21", "2026-10-21", "2026-10-24", "1200"),
		orderRow("duplicate", "Delivered", "2026-10-21", "2026-10-21", "2026-10-24", "1200"),
		orderRow("duplicate", "Delivered", "2026-10-21", "2026-10-21", "2026-10-24", "1200"),
		{"bad", "2026-02-30", "", "123", "-2", "Cash", "Unknown", "bad", "2026-10-24", ""},
	}
	orders, rowErrors := ValidateRows(rows)
	if len(orders) != 2 || len(rowErrors) != 2 {
		t.Fatalf("orders=%d rowErrors=%d", len(orders), len(rowErrors))
	}
	if !strings.Contains(strings.Join(rowErrors[0].Errors, " "), "Duplicate order ID") {
		t.Fatalf("duplicate not reported: %+v", rowErrors[0])
	}
	joined := strings.Join(rowErrors[1].Errors, " ")
	for _, expected := range []string{"Customer name is required", "Order date is not a valid date", "Phone number", "non-negative number", "Prepaid or COD", "status is not supported"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("expected %q in errors: %s", expected, joined)
		}
	}
}

func TestInvalidRowsDoNotEvaluateExceptions(t *testing.T) {
	now := time.Now().UTC()
	order := Order{Status: "Unconfirmed", OrderDate: now.Add(-4 * time.Hour), ExpectedDeliveryDate: now.Add(time.Hour), DataErrors: []string{"bad order date"}}
	if got := EvaluateExceptions(order, time.Now(), DefaultConfig); len(got) != 0 {
		t.Fatalf("unsupported exception from invalid order: %+v", got)
	}
}

func TestRulesTriggerOnlyAfterConfiguredThresholds(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	unconfirmed := Order{Status: "Unconfirmed", OrderDate: now.Add(-2 * time.Hour), ExpectedDeliveryDate: now.Add(time.Hour)}
	if hasRule(EvaluateExceptions(unconfirmed, now, DefaultConfig), "unconfirmed") || !hasRule(EvaluateExceptions(unconfirmed, now.Add(time.Nanosecond), DefaultConfig), "unconfirmed") {
		t.Fatal("unconfirmed rule must trigger strictly after the 2 hour threshold")
	}
	dispatch := Order{Status: "Confirmed", DispatchDate: "Pending", OrderDate: now.Add(-24 * time.Hour), ExpectedDeliveryDate: now.Add(time.Hour)}
	if hasRule(EvaluateExceptions(dispatch, now, DefaultConfig), "dispatch-delay") || !hasRule(EvaluateExceptions(dispatch, now.Add(time.Nanosecond), DefaultConfig), "dispatch-delay") {
		t.Fatal("dispatch rule must trigger strictly after the 24 hour threshold")
	}
	highValue := Order{Status: "Undelivered", OrderDate: now.Add(-48 * time.Hour), ExpectedDeliveryDate: now.Add(-time.Hour), OrderValue: 5000}
	if hasRule(EvaluateExceptions(highValue, now, DefaultConfig), "high-value-undelivered") {
		t.Fatal("high-value rule must be strictly greater than its threshold")
	}
	highValue.OrderValue = 5000.01
	if !hasRule(EvaluateExceptions(highValue, now, DefaultConfig), "high-value-undelivered") {
		t.Fatal("high-value rule should trigger above its threshold")
	}
}

func TestDateOnlyExpectedDeliveryExpiresAfterCalendarDay(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	row := orderRow("due-today", "Dispatched", now.Add(-48*time.Hour).Format(time.RFC3339), "2026-10-20T12:00:00Z", "2026-10-21", "1200")
	orders, rowErrors := ValidateRows([][]string{testHeaders, row})
	if len(rowErrors) != 0 || len(orders) != 1 {
		t.Fatalf("date-only row invalid: orders=%d errors=%+v", len(orders), rowErrors)
	}
	if got := EvaluateExceptions(orders[0], now, DefaultConfig); hasRule(got, "delivery-delay") {
		t.Fatalf("date-only delivery date should remain valid through its day: %+v", got)
	}
	if got := EvaluateExceptions(orders[0], now.Add(12*time.Hour), DefaultConfig); !hasRule(got, "delivery-delay") {
		t.Fatalf("date-only delivery date should be overdue the next day: %+v", got)
	}
}

func TestParseDateAcceptsISODateTimeWithoutTimezone(t *testing.T) {
	parsed, err := ParseDate("2026-10-21T12:30:00")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Location() != time.UTC || parsed.Hour() != 12 || parsed.Minute() != 30 {
		t.Fatalf("naive ISO timestamp was not interpreted as UTC: %s", parsed.Format(time.RFC3339))
	}
}

func hasRule(exceptions []Exception, rule string) bool {
	for _, exception := range exceptions {
		if exception.Rule == rule {
			return true
		}
	}
	return false
}
