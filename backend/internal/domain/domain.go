package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DispatchHours      int     `json:"dispatchHours"`
	UnconfirmedHours   int     `json:"unconfirmedHours"`
	HighValueThreshold float64 `json:"highValueThreshold"`
}

var DefaultConfig = Config{DispatchHours: 24, UnconfirmedHours: 2, HighValueThreshold: 5000}

type Order struct {
	OrderID              string    `json:"orderId"`
	OrderDate            time.Time `json:"orderDate"`
	CustomerName         string    `json:"customerName"`
	Phone                string    `json:"phone"`
	OrderValue           float64   `json:"orderValue"`
	PaymentType          string    `json:"paymentType"`
	Status               string    `json:"status"`
	DispatchDate         string    `json:"dispatchDate"`
	ExpectedDeliveryDate time.Time `json:"expectedDeliveryDate"`
	AssignedEmployee     string    `json:"assignedEmployee"`
	RowNumber            int       `json:"rowNumber"`
	DataErrors           []string  `json:"dataErrors,omitempty"`
}

type RowError struct {
	RowNumber int      `json:"rowNumber"`
	OrderID   string   `json:"orderId"`
	Errors    []string `json:"errors"`
}

type Exception struct {
	Rule     string    `json:"rule"`
	Category string    `json:"category"`
	Reason   string    `json:"reason"`
	Priority string    `json:"priority"`
	Action   string    `json:"action"`
	Deadline time.Time `json:"deadline"`
}

type Task struct {
	ID           int64     `json:"id"`
	OrderID      string    `json:"orderId"`
	OrderDate    time.Time `json:"orderDate"`
	CustomerName string    `json:"customerName"`
	Phone        string    `json:"phone"`
	OrderValue   float64   `json:"orderValue"`
	Rule         string    `json:"rule"`
	Reason       string    `json:"reason"`
	Category     string    `json:"category"`
	Priority     string    `json:"priority"`
	Employee     string    `json:"employee"`
	Action       string    `json:"action"`
	Deadline     time.Time `json:"deadline"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type AuditEntry struct {
	ID            int64     `json:"id"`
	OrderID       string    `json:"orderId"`
	TaskID        int64     `json:"taskId"`
	Action        string    `json:"action"`
	Timestamp     time.Time `json:"timestamp"`
	Actor         string    `json:"actor"`
	PreviousValue string    `json:"previousValue,omitempty"`
	NewValue      string    `json:"newValue,omitempty"`
}

type ImportRecord struct {
	ID           int64      `json:"id"`
	Filename     string     `json:"filename"`
	UploadedAt   time.Time  `json:"uploadedAt"`
	ReceivedRows int        `json:"receivedRows"`
	AcceptedRows int        `json:"acceptedRows"`
	InvalidRows  []RowError `json:"invalidRows"`
}

var requiredColumns = []struct {
	key     string
	label   string
	aliases []string
}{
	{"orderId", "Order ID", []string{"orderid", "orderno"}},
	{"orderDate", "Order date", []string{"orderdate", "createdat"}},
	{"customerName", "Customer name", []string{"customername", "name"}},
	{"phone", "Phone number", []string{"phone", "phonenumber", "mobile"}},
	{"orderValue", "Order value", []string{"ordervalue", "amount", "total"}},
	{"paymentType", "Payment type", []string{"paymenttype", "payment"}},
	{"status", "Order status", []string{"status", "orderstatus"}},
	{"dispatchDate", "Dispatch date", []string{"dispatchdate", "shippeddate"}},
	{"expectedDeliveryDate", "Expected delivery date", []string{"expecteddeliverydate", "deliverydate", "eta"}},
	{"assignedEmployee", "Assigned employee", []string{"assignedemployee", "employee", "assignee"}},
}

var statusValues = map[string]string{
	"unconfirmed": "Unconfirmed", "pending confirmation": "Unconfirmed",
	"confirmed": "Confirmed", "dispatched": "Dispatched", "shipped": "Dispatched",
	"delivered": "Delivered", "undelivered": "Undelivered", "failed delivery": "Failed Delivery",
	"delivery failed": "Failed Delivery", "failed attempt": "Failed Delivery", "failed delivery attempt": "Failed Delivery",
}

func canonicalHeader(value string) string {
	var result strings.Builder
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func ParseDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "1/2/2006", "01/02/2006", "2/1/2006", "02/01/2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date")
}

func ParseExpectedDate(value string) (time.Time, error) {
	parsed, err := ParseDate(value)
	if err != nil {
		return time.Time{}, err
	}
	value = strings.TrimSpace(value)
	if !strings.ContainsAny(value, "Tt") && !strings.Contains(value, ":") {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return parsed, nil
}

func isPendingDate(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pending", "not dispatched", "n/a", "na":
		return true
	default:
		return false
	}
}

func ValidateRows(rows [][]string) ([]Order, []RowError) {
	if len(rows) == 0 {
		return nil, []RowError{{RowNumber: 1, Errors: []string{"The file is empty"}}}
	}
	columns := make(map[string]int)
	for index, header := range rows[0] {
		header = strings.TrimPrefix(header, "\ufeff")
		normalized := canonicalHeader(header)
		for _, column := range requiredColumns {
			for _, alias := range column.aliases {
				if normalized == alias {
					if _, exists := columns[column.key]; !exists {
						columns[column.key] = index
					}
				}
			}
		}
	}
	var missing []string
	for _, column := range requiredColumns {
		if _, exists := columns[column.key]; !exists {
			missing = append(missing, column.label)
		}
	}
	if len(missing) > 0 {
		return nil, []RowError{{RowNumber: 1, Errors: []string{"Missing columns: " + strings.Join(missing, ", ")}}}
	}

	var orders []Order
	var errors []RowError
	seen := make(map[string]bool)
	for index, values := range rows[1:] {
		blank := true
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		value := func(key string) string {
			column := columns[key]
			if column >= len(values) {
				return ""
			}
			return strings.TrimSpace(values[column])
		}
		order := Order{
			OrderID: value("orderId"), CustomerName: value("customerName"), Phone: value("phone"),
			PaymentType: value("paymentType"), Status: value("status"), DispatchDate: value("dispatchDate"),
			AssignedEmployee: value("assignedEmployee"), RowNumber: index + 2,
		}
		var rowErrors []string
		for _, column := range requiredColumns {
			if value(column.key) == "" {
				rowErrors = append(rowErrors, column.label+" is required")
			}
		}
		if order.OrderID != "" && seen[order.OrderID] {
			rowErrors = append(rowErrors, "Duplicate order ID in this upload")
		}
		if order.OrderID != "" {
			seen[order.OrderID] = true
		}
		if orderDate, err := ParseDate(value("orderDate")); err != nil {
			if value("orderDate") != "" {
				rowErrors = append(rowErrors, "Order date is not a valid date")
			}
		} else {
			order.OrderDate = orderDate
		}
		if expected, err := ParseExpectedDate(value("expectedDeliveryDate")); err != nil {
			if value("expectedDeliveryDate") != "" {
				rowErrors = append(rowErrors, "Expected delivery date is not a valid date")
			}
		} else {
			order.ExpectedDeliveryDate = expected
		}
		if order.DispatchDate != "" && !isPendingDate(order.DispatchDate) {
			if dispatch, err := ParseDate(order.DispatchDate); err != nil {
				rowErrors = append(rowErrors, "Dispatch date is not a valid date")
			} else {
				order.DispatchDate = dispatch.Format(time.RFC3339)
			}
		} else if isPendingDate(order.DispatchDate) {
			order.DispatchDate = "Pending"
		}
		payment := strings.ToLower(order.PaymentType)
		if payment == "prepaid" {
			order.PaymentType = "Prepaid"
		} else if payment == "cod" {
			order.PaymentType = "COD"
		} else if payment != "" {
			rowErrors = append(rowErrors, "Payment type must be Prepaid or COD")
		}
		amount := strings.NewReplacer("₹", "", ",", "", " ", "").Replace(value("orderValue"))
		if parsed, err := strconv.ParseFloat(amount, 64); err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			if value("orderValue") != "" {
				rowErrors = append(rowErrors, "Order value must be a non-negative number")
			}
		} else {
			order.OrderValue = parsed
		}
		digits := 0
		for _, char := range order.Phone {
			if char >= '0' && char <= '9' {
				digits++
			}
		}
		if order.Phone != "" && (digits < 10 || digits > 15) {
			rowErrors = append(rowErrors, "Phone number must contain 10 to 15 digits")
		}
		if normalized, exists := statusValues[strings.ToLower(order.Status)]; exists {
			order.Status = normalized
		} else if order.Status != "" {
			rowErrors = append(rowErrors, "Order status is not supported")
		}
		if len(rowErrors) > 0 {
			errors = append(errors, RowError{RowNumber: order.RowNumber, OrderID: order.OrderID, Errors: rowErrors})
			continue
		}
		orders = append(orders, order)
	}
	return orders, errors
}

func EvaluateExceptions(order Order, now time.Time, config Config) []Exception {
	if len(order.DataErrors) > 0 || order.OrderDate.IsZero() || order.ExpectedDeliveryDate.IsZero() {
		return nil
	}
	if config.UnconfirmedHours <= 0 {
		config.UnconfirmedHours = DefaultConfig.UnconfirmedHours
	}
	if config.DispatchHours <= 0 {
		config.DispatchHours = DefaultConfig.DispatchHours
	}
	if config.HighValueThreshold < 0 {
		config.HighValueThreshold = DefaultConfig.HighValueThreshold
	}
	var exceptions []Exception
	add := func(rule, category, reason, priority, action string, deadline time.Time) {
		exceptions = append(exceptions, Exception{Rule: rule, Category: category, Reason: reason, Priority: priority, Action: action, Deadline: deadline})
	}
	if order.Status == "Unconfirmed" && now.Sub(order.OrderDate) > time.Duration(config.UnconfirmedHours)*time.Hour {
		add("unconfirmed", "Order confirmation", "Order is still unconfirmed after the allowed confirmation window.", "High", "Call the customer to confirm or cancel the order.", now.Add(2*time.Hour))
	}
	if order.Status == "Confirmed" && strings.EqualFold(order.DispatchDate, "Pending") && now.Sub(order.OrderDate) > time.Duration(config.DispatchHours)*time.Hour {
		add("dispatch-delay", "Dispatch delay", fmt.Sprintf("Confirmed order has not been dispatched within %d hours.", config.DispatchHours), "High", "Check fulfillment and dispatch the order.", now.Add(4*time.Hour))
	}
	if (order.Status == "Dispatched" || order.Status == "Undelivered") && order.ExpectedDeliveryDate.Before(now) {
		add("delivery-delay", "Delivery delay", "Expected delivery date has passed and the order is not delivered.", "Medium", "Contact the carrier and update the customer.", now.Add(4*time.Hour))
	}
	if order.Status == "Failed Delivery" {
		add("failed-attempt", "Failed delivery", "A delivery attempt failed and requires immediate follow-up.", "High", "Contact the customer and arrange another delivery attempt.", now)
	}
	if order.Status == "Undelivered" && order.OrderValue > config.HighValueThreshold && order.ExpectedDeliveryDate.Before(now) {
		add("high-value-undelivered", "High-value order", fmt.Sprintf("Undelivered order exceeds the ₹%.0f threshold.", config.HighValueThreshold), "High", "Escalate with the carrier and contact the customer immediately.", now.Add(time.Hour))
	}
	return exceptions
}
