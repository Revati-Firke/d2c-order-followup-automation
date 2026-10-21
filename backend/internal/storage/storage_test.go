package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"d2c-order-followup-automation/internal/domain"
)

var storageTestHeaders = []string{"Order ID", "Order Date", "Customer Name", "Phone Number", "Order Value", "Payment Type", "Order Status", "Dispatch Date", "Expected Delivery Date", "Assigned Employee"}

func storageTestRow(id, status, orderDate, dispatchDate, expectedDate, value string) []string {
	return []string{id, orderDate, "Test Customer", "9876501234", value, "Prepaid", status, dispatchDate, expectedDate, "Asha Menon"}
}

func TestSQLiteImportDeduplicatesAndReassignmentSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "orders.sqlite")
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	store, err := OpenStore(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	rows := [][]string{storageTestHeaders, storageTestRow("persisted", "Unconfirmed", now.Add(-3*time.Hour).Format(time.RFC3339), "Pending", now.Add(time.Hour).Format(time.RFC3339), "1200")}
	if _, err := store.ImportRows(ctx, rows, "first.csv", "Importer", now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.Tasks(ctx, TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("expected one task, got %d, error %v", len(tasks), err)
	}
	if _, err := store.UpdateTask(ctx, tasks[0].ID, "   ", "In Progress", "Team Lead", now); err == nil {
		t.Fatal("whitespace-only assignee should be rejected by storage")
	}
	deadline := tasks[0].Deadline
	if _, err := store.db.Exec(`UPDATE tasks SET deadline=? WHERE id=?`, timeString(now.Add(-time.Hour)), tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportRows(ctx, rows, "repeat.csv", "Importer", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	tasks, err = store.Tasks(ctx, TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("repeat import duplicated an active task: count=%d error=%v", len(tasks), err)
	}
	if tasks[0].Deadline.Equal(deadline) {
		t.Fatal("test deadline override was unexpectedly lost")
	}
	preservedDeadline := tasks[0].Deadline
	updated, err := store.UpdateTask(ctx, tasks[0].ID, "Rohan Shah", "In Progress", "Team Lead", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Deadline.Equal(preservedDeadline) || updated.Employee != "Rohan Shah" || updated.Status != "In Progress" {
		t.Fatalf("reassignment changed task state unexpectedly: %+v", updated)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	tasks, err = reopened.Tasks(ctx, TaskFilter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task did not persist: count=%d error=%v", len(tasks), err)
	}
	if !tasks[0].Deadline.Equal(preservedDeadline) || tasks[0].Employee != "Rohan Shah" || tasks[0].Status != "In Progress" {
		t.Fatalf("persisted task mismatch: %+v", tasks[0])
	}
	audit, err := reopened.Audit(ctx, 20)
	if err != nil || len(audit) != 3 {
		t.Fatalf("expected assignment, reassignment and status history, got %d; error %v", len(audit), err)
	}
	if audit[0].PreviousValue != "Open" || audit[0].NewValue != "In Progress" || audit[1].PreviousValue != "Asha Menon" || audit[1].NewValue != "Rohan Shah" {
		t.Fatalf("missing reassignment/status values: %+v", audit[:2])
	}
	metrics, err := reopened.Dashboard(ctx, TaskFilter{}, now.Add(3*time.Minute))
	if err != nil || metrics.OverdueTasks != 1 {
		t.Fatalf("overdue task lost after reassignment: %+v, error %v", metrics, err)
	}
}

func TestSaveConfigReevaluatesPersistedOrders(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "config.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	rows := [][]string{storageTestHeaders, storageTestRow("config-order", "Confirmed", now.Add(-18*time.Hour).Format(time.RFC3339), "Pending", now.Add(time.Hour).Format(time.RFC3339), "2200")}
	if _, err := store.ImportRows(ctx, rows, "config.csv", "Manager", now); err != nil {
		t.Fatal(err)
	}
	tasks, _ := store.Tasks(ctx, TaskFilter{})
	if len(tasks) != 0 {
		t.Fatalf("default 24 hour threshold unexpectedly created tasks: %+v", tasks)
	}
	config := domain.DefaultConfig
	config.DispatchHours = 12
	if err := store.SaveConfig(ctx, config, "Manager", now); err != nil {
		t.Fatal(err)
	}
	tasks, err = store.Tasks(ctx, TaskFilter{})
	if err != nil || len(tasks) != 1 || tasks[0].Rule != "dispatch-delay" {
		t.Fatalf("12 hour rule did not create an exception: %+v; error %v", tasks, err)
	}
}

func TestMigratesLegacyJSONIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	legacy := `{
		"orders":[{"orderId":"old-order","orderDate":"2026-10-20T00:00:00Z","customerName":"Old Customer","phone":"9876501234","orderValue":1200,"paymentType":"Prepaid","status":"Failed Delivery","dispatchDate":"2026-10-20T00:00:00Z","expectedDeliveryDate":"2026-10-22T00:00:00Z","assignedEmployee":"Asha Menon"}],
		"tasks":[{"id":"task-old","orderId":"old-order","orderDate":"2026-10-20T00:00:00Z","customerName":"Old Customer","phone":"9876501234","orderValue":1200,"rule":"failed-attempt","reason":"Failed","category":"Failed delivery","priority":"High","employee":"Asha Menon","action":"Call","deadline":"2026-10-21T00:00:00Z","status":"Open","createdAt":"2026-10-20T00:00:00Z","updatedAt":"2026-10-20T00:00:00Z"}],
		"audit":[{"orderId":"old-order","taskId":"task-old","action":"Task assigned","timestamp":"2026-10-20T00:00:00Z","actor":"Manager","newValue":"Asha Menon"}],
		"imports":[{"id":"import-legacy","filename":"legacy.csv","uploadedAt":"2026-10-20T00:00:00Z","receivedRows":1,"acceptedRows":1,"invalidRows":[]}],
		"config":{"dispatchHours":12,"unconfirmedHours":2,"highValueThreshold":5000}
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "migrated.sqlite"), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	orders, err := store.Orders(context.Background())
	if err != nil || len(orders) != 1 {
		t.Fatalf("legacy order not migrated: %+v; error %v", orders, err)
	}
	audit, err := store.Audit(context.Background(), 10)
	if err != nil || len(audit) != 1 || audit[0].Actor != "Manager" {
		t.Fatalf("legacy audit not migrated: %+v; error %v", audit, err)
	}
	config, err := store.Config(context.Background())
	if err != nil || config.DispatchHours != 12 {
		t.Fatalf("legacy config not migrated: %+v; error %v", config, err)
	}
	imports, err := store.Imports(context.Background(), 10)
	if err != nil || len(imports) != 1 || imports[0].Filename != "legacy.csv" {
		t.Fatalf("legacy import history not migrated: %+v; error %v", imports, err)
	}
}

func TestLegacyMigrationWithNoValidOrdersRunsOnlyOnce(t *testing.T) {
	ctx := context.Background()
	legacyPath := filepath.Join(t.TempDir(), "empty-orders.json")
	legacy := `{"orders":[],"tasks":[],"audit":[],"imports":[{"id":"import-empty","filename":"empty.csv","uploadedAt":"2026-10-20T00:00:00Z","receivedRows":2,"acceptedRows":0,"invalidRows":[]}],"config":{"dispatchHours":24,"unconfirmedHours":2,"highValueThreshold":5000}}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "empty-migration.sqlite")
	for range 2 {
		store, err := OpenStore(dbPath, legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		imports, err := store.Imports(ctx, 10)
		if err != nil || len(imports) != 1 {
			t.Fatalf("legacy import duplicated or missing: count=%d error=%v", len(imports), err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportCountsRowsWhenHeadersAreInvalid(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "invalid-header.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.ImportRows(context.Background(), [][]string{{"wrong header"}, {"order one"}, {""}, {"order two"}}, "invalid.csv", "Manager", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if record.ReceivedRows != 2 || record.AcceptedRows != 0 || len(record.InvalidRows) != 1 {
		t.Fatalf("incorrect import counts: %+v", record)
	}
}

func TestSuccessfulImportReturnsEmptyErrorArray(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "success-import.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := [][]string{storageTestHeaders, storageTestRow("clean-order", "Delivered", "2026-10-21", "2026-10-21", "2026-10-24", "1200")}
	record, err := store.ImportRows(context.Background(), rows, "clean.csv", "Manager", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if record.InvalidRows == nil || len(record.InvalidRows) != 0 {
		t.Fatalf("expected an empty, non-null row error array: %#v", record.InvalidRows)
	}
}

func TestEmptyImportReturnsValidationErrorWithoutPanic(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "empty-import.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.ImportRows(context.Background(), nil, "empty.csv", "Manager", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if record.ReceivedRows != 0 || record.AcceptedRows != 0 || len(record.InvalidRows) != 1 || record.InvalidRows[0].Errors[0] != "The file is empty" {
		t.Fatalf("empty import response incorrect: %+v", record)
	}
}

func TestTaskUpdateValidationRejectsWhitespaceAssignee(t *testing.T) {
	if err := ValidateTaskUpdate("   ", "In Progress"); err == nil {
		t.Fatal("whitespace-only employee should not be accepted")
	}
	if err := ValidateTaskUpdate("", "In Progress"); err != nil {
		t.Fatalf("status-only update should remain valid: %v", err)
	}
}

func TestReopeningResolvedTaskCannotDuplicateActiveTask(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "task-reopen.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	rows := [][]string{storageTestHeaders, storageTestRow("one-rule", "Unconfirmed", now.Add(-3*time.Hour).Format(time.RFC3339), "Pending", now.Add(time.Hour).Format(time.RFC3339), "1200")}
	if _, err := store.ImportRows(ctx, rows, "first.csv", "Manager", now); err != nil {
		t.Fatal(err)
	}
	firstTasks, err := store.Tasks(ctx, TaskFilter{})
	if err != nil || len(firstTasks) != 1 {
		t.Fatalf("expected one initial task: tasks=%+v error=%v", firstTasks, err)
	}
	if _, err := store.UpdateTask(ctx, firstTasks[0].ID, "", "Resolved", "Manager", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportRows(ctx, rows, "repeat.csv", "Manager", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTask(ctx, firstTasks[0].ID, "", "Open", "Manager", now.Add(3*time.Minute)); !errors.Is(err, ErrActiveTaskExists) {
		t.Fatalf("expected active-task conflict when reopening, got %v", err)
	}
	tasks, err := store.Tasks(ctx, TaskFilter{})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("expected resolved task plus one active task: tasks=%+v error=%v", tasks, err)
	}
}

func TestTaskFiltersAndDashboardUseStoredTaskData(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "task-filters.sqlite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	secondRow := storageTestRow("filter-b", "Failed Delivery", "2026-10-20T08:00:00Z", "2026-10-20T12:00:00Z", "2026-10-23T12:00:00Z", "2400")
	secondRow[9] = "Rohan Shah"
	rows := [][]string{
		storageTestHeaders,
		storageTestRow("filter-a", "Unconfirmed", "2026-10-21T08:00:00Z", "Pending", "2026-10-23T12:00:00Z", "1200"),
		secondRow,
	}
	if _, err := store.ImportRows(ctx, rows, "filters.csv", "Manager", now); err != nil {
		t.Fatal(err)
	}
	all, err := store.Tasks(ctx, TaskFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("expected two stored tasks: tasks=%+v error=%v", all, err)
	}
	var unconfirmed, failed domain.Task
	for _, task := range all {
		if task.OrderID == "filter-a" {
			unconfirmed = task
		}
		if task.OrderID == "filter-b" {
			failed = task
		}
	}
	if _, err := store.UpdateTask(ctx, unconfirmed.ID, "", "In Progress", "Manager", now); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		filter TaskFilter
		want   string
	}{
		{"category", TaskFilter{Category: "Order confirmation"}, "filter-a"},
		{"employee", TaskFilter{Employee: "Rohan Shah"}, "filter-b"},
		{"priority", TaskFilter{Priority: "High"}, ""},
		{"date", TaskFilter{Date: "2026-10-21"}, "filter-a"},
		{"status", TaskFilter{Status: "In Progress"}, "filter-a"},
		{"open status", TaskFilter{Status: "Open"}, "filter-b"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			filtered, err := store.Tasks(ctx, testCase.filter)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.name == "priority" {
				if len(filtered) != 2 || filtered[0].Priority != "High" {
					t.Fatalf("priority filter mismatch: %+v", filtered)
				}
				return
			}
			if len(filtered) != 1 || filtered[0].OrderID != testCase.want {
				t.Fatalf("filter mismatch: %+v, wanted order %s", filtered, testCase.want)
			}
		})
	}
	metrics, err := store.Dashboard(ctx, TaskFilter{Employee: "Rohan Shah"}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if metrics.TotalOrders != 2 || metrics.AttentionOrders != 1 || metrics.OpenTasks != 1 || metrics.ByEmployee["Rohan Shah"] != 1 || metrics.ByCategory["Failed delivery"] != 1 {
		t.Fatalf("dashboard did not aggregate filtered SQLite tasks: %+v", metrics)
	}
	_ = failed
}
