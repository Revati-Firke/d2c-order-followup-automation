package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"d2c-order-followup-automation/internal/domain"
	"d2c-order-followup-automation/internal/storage"
)

var httpTestHeaders = []string{"Order ID", "Order Date", "Customer Name", "Phone Number", "Order Value", "Payment Type", "Order Status", "Dispatch Date", "Expected Delivery Date", "Assigned Employee"}

func httpTestRow(id, status, orderDate, dispatchDate, expectedDate, value string) []string {
	return []string{id, orderDate, "Test Customer", "9876501234", value, "Prepaid", status, dispatchDate, expectedDate, "Asha Menon"}
}

func TestHTTPImportTaskUpdateAndReport(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "http.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewServer(store)
	now := time.Now().UTC()
	validDate := now.Add(-3 * time.Hour).Format(time.RFC3339)
	expectedDate := now.Add(24 * time.Hour).Format(time.RFC3339)
	csvData := strings.Join([]string{
		strings.Join(httpTestHeaders, ","),
		strings.Join(httpTestRow("http-order", "Unconfirmed", validDate, "Pending", expectedDate, "1200"), ","),
		strings.Join(httpTestRow("http-order", "Unconfirmed", validDate, "Pending", expectedDate, "1200"), ","),
		"invalid,2026-02-30,,123,-2,Cash,Unknown,bad-date,2026-10-24,",
	}, "\n")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "orders.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(file, csvData)
	_ = writer.WriteField("actor", "CSV Tester")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("import status %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Import domain.ImportRecord `json:"import"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Import.AcceptedRows != 1 || len(result.Import.InvalidRows) != 2 {
		t.Fatalf("unexpected CSV result: %+v", result.Import)
	}

	filter := httptest.NewRequest(http.MethodGet, "/api/tasks?category="+url.QueryEscape("Order confirmation"), nil)
	filtered := httptest.NewRecorder()
	handler.ServeHTTP(filtered, filter)
	var tasks []domain.Task
	if err := json.Unmarshal(filtered.Body.Bytes(), &tasks); err != nil || len(tasks) != 1 {
		t.Fatalf("task filter failed: status=%d tasks=%+v error=%v", filtered.Code, tasks, err)
	}
	task := tasks[0]
	deadline := task.Deadline
	update := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+strconvFormat(task.ID), strings.NewReader(`{"employee":"Rohan Shah","status":"In Progress","actor":"Team Lead"}`))
	update.Header.Set("Content-Type", "application/json")
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK {
		t.Fatalf("task update status %d: %s", updated.Code, updated.Body.String())
	}
	var updatedPayload struct {
		Task domain.Task `json:"task"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedPayload); err != nil {
		t.Fatal(err)
	}
	if updatedPayload.Task.Employee != "Rohan Shah" || updatedPayload.Task.Status != "In Progress" || !updatedPayload.Task.Deadline.Equal(deadline) {
		t.Fatalf("task reassignment changed deadline: %+v", updatedPayload.Task)
	}
	auditRequest := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, auditRequest)
	var auditEntries []domain.AuditEntry
	if err := json.Unmarshal(auditResponse.Body.Bytes(), &auditEntries); err != nil || len(auditEntries) != 3 {
		t.Fatalf("audit response missing changes: entries=%+v error=%v", auditEntries, err)
	}
	reportRequest := httptest.NewRequest(http.MethodGet, "/api/report.csv", nil)
	reportResponse := httptest.NewRecorder()
	handler.ServeHTTP(reportResponse, reportRequest)
	if reportResponse.Code != http.StatusOK || !strings.Contains(reportResponse.Body.String(), "Employee pending action") {
		t.Fatalf("daily report incomplete: %s", reportResponse.Body.String())
	}

	sampleRequest := httptest.NewRequest(http.MethodPost, "/api/sample", strings.NewReader(`{"actor":"Tester"}`))
	sampleResponse := httptest.NewRecorder()
	handler.ServeHTTP(sampleResponse, sampleRequest)
	if sampleResponse.Code != http.StatusOK {
		t.Fatalf("sample endpoint status %d: %s", sampleResponse.Code, sampleResponse.Body.String())
	}
	var sampleResult struct {
		Import domain.ImportRecord `json:"import"`
	}
	if err := json.Unmarshal(sampleResponse.Body.Bytes(), &sampleResult); err != nil {
		t.Fatal(err)
	}
	if sampleResult.Import.AcceptedRows != 106 || len(sampleResult.Import.InvalidRows) != 3 {
		t.Fatalf("sample endpoint did not return 106 accepted and 3 invalid rows: %+v", sampleResult.Import)
	}
	if tasks, err := store.Tasks(context.Background(), storage.TaskFilter{}); err != nil {
		t.Fatal(err)
	} else if hasTask(tasks, "VN-RULE-DISPATCH-12H", "dispatch-delay") {
		t.Fatal("18-hour sample order should not trigger at the default 24-hour threshold")
	}
	configRequest := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"dispatchHours":12,"unconfirmedHours":2,"highValueThreshold":5000}`))
	configRequest.Header.Set("Content-Type", "application/json")
	configResponse := httptest.NewRecorder()
	handler.ServeHTTP(configResponse, configRequest)
	if configResponse.Code != http.StatusOK {
		t.Fatalf("config update status=%d body=%s", configResponse.Code, configResponse.Body.String())
	}
	tasksAfterConfig, err := store.Tasks(context.Background(), storage.TaskFilter{})
	if err != nil || !hasTask(tasksAfterConfig, "VN-RULE-DISPATCH-12H", "dispatch-delay") {
		t.Fatalf("12-hour config did not create the sample dispatch exception: tasks=%+v error=%v", tasksAfterConfig, err)
	}
	if _, err := store.Config(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSampleRejectsMalformedJSON(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "sample-json.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/sample", strings.NewReader(`{"actor":`))
	response := httptest.NewRecorder()
	NewServer(store).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestJSONEndpointsRejectTrailingValues(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "trailing-json.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"dispatchHours":12,"unconfirmedHours":2,"highValueThreshold":5000}{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(store).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUnknownAPIPathReturnsJSONNotFrontend(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "unknown-route.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/unknown", nil)
	response := httptest.NewRecorder()
	NewServer(store).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API path status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
}

func TestHealthEndpointsCheckSQLite(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "health.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, path := range []string{"/health", "/api/health"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		NewServer(store).ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"database":"ok"`) {
			t.Errorf("health endpoint %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestReopenConflictReturnsHTTP409(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "reopen-conflict.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	rows := [][]string{httpTestHeaders, httpTestRow("conflict-order", "Unconfirmed", now.Add(-3*time.Hour).Format(time.RFC3339), "Pending", now.Add(time.Hour).Format(time.RFC3339), "1200")}
	if _, err := store.ImportRows(t.Context(), rows, "first.csv", "Manager", now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.Tasks(t.Context(), storage.TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("expected one task: tasks=%+v error=%v", tasks, err)
	}
	if _, err := store.UpdateTask(t.Context(), tasks[0].ID, "", "Resolved", "Manager", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportRows(t.Context(), rows, "repeat.csv", "Manager", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+strconvFormat(tasks[0].ID), strings.NewReader(`{"status":"Open"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(store).ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate active-task reopen status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConfigEndpointAcceptsNumericRules(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "config-api.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"dispatchHours":12,"unconfirmedHours":2,"highValueThreshold":5000,"actor":"Manager"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(store).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("config update status=%d body=%s", response.Code, response.Body.String())
	}
	config, err := store.Config(t.Context())
	if err != nil || config.DispatchHours != 12 {
		t.Fatalf("config not persisted: %+v error=%v", config, err)
	}
}

func TestImportRejectsMalformedCSV(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "malformed-csv.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "broken.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(file, `"unterminated`)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	NewServer(store).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed CSV status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEmptyCSVReturnsRowLevelError(t *testing.T) {
	store, err := storage.OpenStore(filepath.Join(t.TempDir(), "empty-csv.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if _, err := writer.CreateFormFile("file", "empty.csv"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	NewServer(store).ServeHTTP(response, request)
	var payload struct {
		Import domain.ImportRecord `json:"import"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || payload.Import.ReceivedRows != 0 || len(payload.Import.InvalidRows) != 1 || payload.Import.InvalidRows[0].Errors[0] != "The file is empty" {
		t.Fatalf("empty CSV response status=%d payload=%+v body=%s", response.Code, payload, response.Body.String())
	}
}

func TestSafeReportRowPreventsSpreadsheetFormulas(t *testing.T) {
	got := safeReportRow([]string{"=1+1", "  +SUM(A1:A2)", "-value", "@name", "normal"})
	want := []string{"'=1+1", "'  +SUM(A1:A2)", "'-value", "'@name", "normal"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("safe report row=%q, want %q", got, want)
	}
}

func strconvFormat(value int64) string {
	return strconv.FormatInt(value, 10)
}

func hasTask(tasks []domain.Task, orderID, rule string) bool {
	for _, task := range tasks {
		if task.OrderID == orderID && task.Rule == rule {
			return true
		}
	}
	return false
}
