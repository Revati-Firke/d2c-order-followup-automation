package sample

import (
	"fmt"
	"time"
)

var sampleHeaders = []string{"Order ID", "Order Date", "Customer Name", "Phone Number", "Order Value", "Payment Type", "Order Status", "Dispatch Date", "Expected Delivery Date", "Assigned Employee"}

func SampleRows(now time.Time) [][]string {
	rows := [][]string{sampleHeaders}
	date := func(days int, hour int) string {
		return now.AddDate(0, 0, days).UTC().Truncate(24 * time.Hour).Add(time.Duration(hour) * time.Hour).Format(time.RFC3339)
	}
	employees := []string{"Asha Menon", "Rohan Shah", "Priya Nair", "Kabir Mehta", "Neha Iyer"}
	names := []string{"Aarav Sharma", "Ananya Rao", "Ishaan Patel", "Diya Nair", "Vivaan Mehta", "Sara Khan", "Arjun Das", "Mira Joshi"}
	for index := 1; index <= 100; index++ {
		daysAgo := -((index % 9) + 1)
		dispatchDaysAgo := -((index % 7) + 1)
		status := "Delivered"
		if index%12 == 0 {
			status = "Undelivered"
		} else if index%4 == 0 {
			status = "Dispatched"
		}
		expectedOffset := 4
		if index%12 == 0 {
			expectedOffset = -2
		}
		rows = append(rows, []string{fmt.Sprintf("VN-%d", 24000+index), date(daysAgo, 11), names[index%len(names)], fmt.Sprintf("981000%04d", index), fmt.Sprint(850 + (index*347)%9200), map[bool]string{true: "COD", false: "Prepaid"}[index%3 == 0], status, date(dispatchDaysAgo, 15), date(expectedOffset, 11), employees[index%len(employees)]})
	}
	rows = append(rows,
		[]string{"VN-RULE-UNCONF", date(-1, 8), "Kavya Kapoor", "9876501001", "2199", "COD", "Unconfirmed", "Pending", date(3, 11), "Asha Menon"},
		[]string{"VN-RULE-DISPATCH", date(-2, 8), "Aditya Verma", "9876501002", "4899", "Prepaid", "Confirmed", "Pending", date(2, 11), "Rohan Shah"},
		[]string{"VN-RULE-DISPATCH-12H", now.Add(-18 * time.Hour).UTC().Format(time.RFC3339), "Nisha Gupta", "9876501010", "2799", "Prepaid", "Confirmed", "Pending", date(2, 11), "Asha Menon"},
		[]string{"VN-RULE-DELIVERY", date(-5, 8), "Ira Kulkarni", "9876501003", "3299", "Prepaid", "Dispatched", date(-4, 14), date(-1, 11), "Priya Nair"},
		[]string{"VN-RULE-FAILED", date(-2, 8), "Dev Malhotra", "9876501004", "1799", "COD", "Failed Delivery", date(-1, 14), date(1, 11), "Kabir Mehta"},
		[]string{"VN-RULE-HIGH", date(-8, 8), "Sana Fernandes", "9876501005", "12999", "COD", "Undelivered", date(-7, 14), date(-2, 11), "Neha Iyer"},
		[]string{"VN-24001", date(-1, 11), "Duplicate Sample", "9876502001", "1250", "COD", "Delivered", date(-1, 15), date(2, 11), "Asha Menon"},
		[]string{"VN-INVALID-MISSING", date(-1, 11), "", "9876502002", "1250", "COD", "Delivered", date(-1, 15), date(2, 11), "Asha Menon"},
		[]string{"VN-INVALID-DATE", "not-a-date", "Ritesh Sen", "9876502003", "2200", "Prepaid", "Delivered", date(-1, 15), date(2, 11), "Rohan Shah"},
	)
	return rows
}
