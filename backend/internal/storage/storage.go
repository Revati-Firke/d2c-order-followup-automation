package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"d2c-order-followup-automation/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

var ErrActiveTaskExists = errors.New("an active task already exists for this order and exception")

type TaskFilter struct {
	Category string
	Employee string
	Priority string
	Date     string
	Status   string
}

type Dashboard struct {
	TotalOrders     int            `json:"totalOrders"`
	AttentionOrders int            `json:"attentionOrders"`
	OpenTasks       int            `json:"openTasks"`
	OverdueTasks    int            `json:"overdueTasks"`
	HighPriority    int            `json:"highPriority"`
	ByCategory      map[string]int `json:"byCategory"`
	ByEmployee      map[string]int `json:"byEmployee"`
}

type legacyState struct {
	Orders  []domain.Order `json:"orders"`
	Tasks   []legacyTask   `json:"tasks"`
	Audit   []legacyAudit  `json:"audit"`
	Imports []legacyImport `json:"imports"`
	Config  domain.Config  `json:"config"`
}

type legacyImport struct {
	Filename     string            `json:"filename"`
	UploadedAt   time.Time         `json:"uploadedAt"`
	ReceivedRows int               `json:"receivedRows"`
	AcceptedRows int               `json:"acceptedRows"`
	InvalidRows  []domain.RowError `json:"invalidRows"`
}

type legacyTask struct {
	ID           string    `json:"id"`
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

type legacyAudit struct {
	OrderID       string    `json:"orderId"`
	TaskID        string    `json:"taskId"`
	Action        string    `json:"action"`
	Timestamp     time.Time `json:"timestamp"`
	Actor         string    `json:"actor"`
	PreviousValue string    `json:"previousValue"`
	NewValue      string    `json:"newValue"`
}

func OpenStore(path, legacyPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.createSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrateLegacy(context.Background(), legacyPath); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) createSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS orders (
			order_id TEXT PRIMARY KEY, order_date TEXT NOT NULL, customer_name TEXT NOT NULL, phone TEXT NOT NULL,
			order_value REAL NOT NULL, payment_type TEXT NOT NULL, status TEXT NOT NULL, dispatch_date TEXT NOT NULL,
			expected_delivery_date TEXT NOT NULL, assigned_employee TEXT NOT NULL, row_number INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT, order_id TEXT NOT NULL, order_date TEXT NOT NULL, customer_name TEXT NOT NULL,
			phone TEXT NOT NULL, order_value REAL NOT NULL, rule TEXT NOT NULL, reason TEXT NOT NULL, category TEXT NOT NULL,
			priority TEXT NOT NULL, employee TEXT NOT NULL, action TEXT NOT NULL, deadline TEXT NOT NULL, status TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY(order_id) REFERENCES orders(order_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_active_task_per_order_rule ON tasks(order_id, rule) WHERE status IN ('Open', 'In Progress')`,
		`CREATE TABLE IF NOT EXISTS audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT, order_id TEXT NOT NULL, task_id INTEGER NOT NULL, action TEXT NOT NULL,
			timestamp TEXT NOT NULL, actor TEXT NOT NULL, previous_value TEXT NOT NULL DEFAULT '', new_value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS imports (
			id INTEGER PRIMARY KEY AUTOINCREMENT, filename TEXT NOT NULL, uploaded_at TEXT NOT NULL,
			received_rows INTEGER NOT NULL, accepted_rows INTEGER NOT NULL, invalid_rows TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS app_config (id INTEGER PRIMARY KEY CHECK(id = 1), config_json TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS app_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO app_config(id, config_json) VALUES(1, ?)`, marshal(domain.DefaultConfig))
	return err
}

func marshal(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (s *Store) migrateLegacy(ctx context.Context, path string) error {
	var migrated bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_migrations WHERE name='legacy-json')`).Scan(&migrated); err != nil || migrated {
		return err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`).Scan(&count); err != nil || count > 0 {
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO app_migrations(name,applied_at) VALUES('legacy-json',?)`, timeString(time.Now()))
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var legacy legacyState
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("read legacy store %s: %w", path, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, order := range legacy.Orders {
		if order.OrderID == "" || order.OrderDate.IsZero() || order.ExpectedDeliveryDate.IsZero() {
			continue
		}
		if err := saveOrder(ctx, tx, order); err != nil {
			return err
		}
	}
	taskIDs := make(map[string]int64)
	for _, task := range legacy.Tasks {
		id, err := insertTask(ctx, tx, domain.Task{OrderID: task.OrderID, OrderDate: task.OrderDate, CustomerName: task.CustomerName, Phone: task.Phone,
			OrderValue: task.OrderValue, Rule: task.Rule, Reason: task.Reason, Category: task.Category, Priority: task.Priority,
			Employee: task.Employee, Action: task.Action, Deadline: task.Deadline, Status: task.Status, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt})
		if err != nil {
			return err
		}
		taskIDs[task.ID] = id
	}
	for _, entry := range legacy.Audit {
		taskID := taskIDs[entry.TaskID]
		if taskID == 0 {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO audit(order_id,task_id,action,timestamp,actor,previous_value,new_value) VALUES(?,?,?,?,?,?,?)`,
			entry.OrderID, taskID, entry.Action, timeString(entry.Timestamp), entry.Actor, entry.PreviousValue, entry.NewValue)
		if err != nil {
			return err
		}
	}
	for _, record := range legacy.Imports {
		_, err := tx.ExecContext(ctx, `INSERT INTO imports(filename,uploaded_at,received_rows,accepted_rows,invalid_rows) VALUES(?,?,?,?,?)`,
			record.Filename, timeString(record.UploadedAt), record.ReceivedRows, record.AcceptedRows, marshal(record.InvalidRows))
		if err != nil {
			return err
		}
	}
	if legacy.Config.DispatchHours > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE app_config SET config_json=? WHERE id=1`, marshal(legacy.Config))
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_migrations(name,applied_at) VALUES('legacy-json',?)`, timeString(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

func timeString(value time.Time) string {
	if value.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func saveOrder(ctx context.Context, tx *sql.Tx, order domain.Order) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO orders(order_id,order_date,customer_name,phone,order_value,payment_type,status,dispatch_date,expected_delivery_date,assigned_employee,row_number)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(order_id) DO UPDATE SET order_date=excluded.order_date,customer_name=excluded.customer_name,
		phone=excluded.phone,order_value=excluded.order_value,payment_type=excluded.payment_type,status=excluded.status,dispatch_date=excluded.dispatch_date,
		expected_delivery_date=excluded.expected_delivery_date,assigned_employee=excluded.assigned_employee,row_number=excluded.row_number`,
		order.OrderID, timeString(order.OrderDate), order.CustomerName, order.Phone, order.OrderValue, order.PaymentType, order.Status,
		order.DispatchDate, timeString(order.ExpectedDeliveryDate), order.AssignedEmployee, order.RowNumber)
	return err
}

func insertTask(ctx context.Context, tx *sql.Tx, task domain.Task) (int64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO tasks(order_id,order_date,customer_name,phone,order_value,rule,reason,category,priority,employee,action,deadline,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, task.OrderID, timeString(task.OrderDate), task.CustomerName, task.Phone, task.OrderValue,
		task.Rule, task.Reason, task.Category, task.Priority, task.Employee, task.Action, timeString(task.Deadline), task.Status,
		timeString(task.CreatedAt), timeString(task.UpdatedAt))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) Config(ctx context.Context) (domain.Config, error) {
	var encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT config_json FROM app_config WHERE id=1`).Scan(&encoded); err != nil {
		return domain.Config{}, err
	}
	var config domain.Config
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		return domain.Config{}, err
	}
	return config, nil
}

func (s *Store) SaveConfig(ctx context.Context, config domain.Config, actor string, now time.Time) error {
	actor = normalizeActor(actor)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE app_config SET config_json=? WHERE id=1`, marshal(config)); err != nil {
		return err
	}
	orders, err := listOrders(ctx, tx)
	if err != nil {
		return err
	}
	for _, order := range orders {
		if err := syncExceptions(ctx, tx, order, now, config, actor); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ImportRows(ctx context.Context, rows [][]string, filename, actor string, now time.Time) (domain.ImportRecord, error) {
	actor = normalizeActor(actor)
	orders, invalid := domain.ValidateRows(rows)
	if invalid == nil {
		invalid = []domain.RowError{}
	}
	config, err := s.Config(ctx)
	if err != nil {
		return domain.ImportRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ImportRecord{}, err
	}
	defer tx.Rollback()
	record := domain.ImportRecord{Filename: filename, UploadedAt: now.UTC(), ReceivedRows: countInputRows(rows), InvalidRows: invalid}
	for _, order := range orders {
		if err := saveOrder(ctx, tx, order); err != nil {
			return domain.ImportRecord{}, err
		}
		record.AcceptedRows++
		if err := syncExceptions(ctx, tx, order, now, config, actor); err != nil {
			return domain.ImportRecord{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO imports(filename,uploaded_at,received_rows,accepted_rows,invalid_rows) VALUES(?,?,?,?,?)`,
		record.Filename, timeString(record.UploadedAt), record.ReceivedRows, record.AcceptedRows, marshal(record.InvalidRows))
	if err != nil {
		return domain.ImportRecord{}, err
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return domain.ImportRecord{}, err
	}
	return record, tx.Commit()
}

func normalizeActor(actor string) string {
	if actor = strings.TrimSpace(actor); actor == "" {
		return "Manager"
	}
	return actor
}

func countInputRows(rows [][]string) int {
	if len(rows) < 2 {
		return 0
	}
	count := 0
	for _, row := range rows[1:] {
		for _, value := range row {
			if strings.TrimSpace(value) != "" {
				count++
				break
			}
		}
	}
	return count
}

func syncExceptions(ctx context.Context, tx *sql.Tx, order domain.Order, now time.Time, config domain.Config, actor string) error {
	for _, exception := range domain.EvaluateExceptions(order, now, config) {
		var taskID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE order_id=? AND rule=? AND status IN ('Open','In Progress')`, order.OrderID, exception.Rule).Scan(&taskID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET customer_name=?,phone=?,order_value=?,reason=?,category=?,priority=?,action=?,updated_at=? WHERE id=?`,
				order.CustomerName, order.Phone, order.OrderValue, exception.Reason, exception.Category, exception.Priority, exception.Action, timeString(now), taskID)
			if err != nil {
				return err
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		task := domain.Task{OrderID: order.OrderID, OrderDate: order.OrderDate, CustomerName: order.CustomerName, Phone: order.Phone,
			OrderValue: order.OrderValue, Rule: exception.Rule, Reason: exception.Reason, Category: exception.Category,
			Priority: exception.Priority, Employee: order.AssignedEmployee, Action: exception.Action, Deadline: exception.Deadline,
			Status: "Open", CreatedAt: now, UpdatedAt: now}
		taskID, err = insertTask(ctx, tx, task)
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, domain.AuditEntry{OrderID: order.OrderID, TaskID: taskID, Action: "Task assigned", Timestamp: now, Actor: actor, NewValue: task.Employee}); err != nil {
			return err
		}
	}
	return nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, entry domain.AuditEntry) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit(order_id,task_id,action,timestamp,actor,previous_value,new_value) VALUES(?,?,?,?,?,?,?)`,
		entry.OrderID, entry.TaskID, entry.Action, timeString(entry.Timestamp), entry.Actor, entry.PreviousValue, entry.NewValue)
	return err
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listOrders(ctx context.Context, query queryer) ([]domain.Order, error) {
	rows, err := query.QueryContext(ctx, `SELECT order_id,order_date,customer_name,phone,order_value,payment_type,status,dispatch_date,expected_delivery_date,assigned_employee,row_number FROM orders ORDER BY order_date DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := []domain.Order{}
	for rows.Next() {
		var order domain.Order
		var orderDate, expectedDate string
		if err := rows.Scan(&order.OrderID, &orderDate, &order.CustomerName, &order.Phone, &order.OrderValue, &order.PaymentType,
			&order.Status, &order.DispatchDate, &expectedDate, &order.AssignedEmployee, &order.RowNumber); err != nil {
			return nil, err
		}
		order.OrderDate, _ = domain.ParseDate(orderDate)
		order.ExpectedDeliveryDate, _ = domain.ParseDate(expectedDate)
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

func (s *Store) Orders(ctx context.Context) ([]domain.Order, error) { return listOrders(ctx, s.db) }

func (s *Store) Tasks(ctx context.Context, filter TaskFilter) ([]domain.Task, error) {
	query := `SELECT id,order_id,order_date,customer_name,phone,order_value,rule,reason,category,priority,employee,action,deadline,status,created_at,updated_at FROM tasks WHERE 1=1`
	var args []any
	if filter.Category != "" {
		query += ` AND category=?`
		args = append(args, filter.Category)
	}
	if filter.Employee != "" {
		query += ` AND employee=?`
		args = append(args, filter.Employee)
	}
	if filter.Priority != "" {
		query += ` AND priority=?`
		args = append(args, filter.Priority)
	}
	if filter.Date != "" {
		query += ` AND substr(order_date,1,10)=?`
		args = append(args, filter.Date)
	}
	if filter.Status != "" {
		query += ` AND status=?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY CASE status WHEN 'Open' THEN 0 WHEN 'In Progress' THEN 1 ELSE 2 END, deadline ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []domain.Task{}
	for rows.Next() {
		var task domain.Task
		var orderDate, deadline, createdAt, updatedAt string
		if err := rows.Scan(&task.ID, &task.OrderID, &orderDate, &task.CustomerName, &task.Phone, &task.OrderValue,
			&task.Rule, &task.Reason, &task.Category, &task.Priority, &task.Employee, &task.Action, &deadline, &task.Status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		task.OrderDate, _ = domain.ParseDate(orderDate)
		task.Deadline, _ = domain.ParseDate(deadline)
		task.CreatedAt, _ = domain.ParseDate(createdAt)
		task.UpdatedAt, _ = domain.ParseDate(updatedAt)
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) Dashboard(ctx context.Context, filter TaskFilter, now time.Time) (Dashboard, error) {
	var result Dashboard
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`).Scan(&result.TotalOrders); err != nil {
		return result, err
	}
	tasks, err := s.Tasks(ctx, filter)
	if err != nil {
		return result, err
	}
	orders := make(map[string]bool)
	result.ByCategory = make(map[string]int)
	result.ByEmployee = make(map[string]int)
	for _, task := range tasks {
		if task.Status != "Resolved" {
			orders[task.OrderID] = true
			result.ByCategory[task.Category]++
			result.ByEmployee[task.Employee]++
			if task.Priority == "High" {
				result.HighPriority++
			}
		}
		if task.Status == "Open" {
			result.OpenTasks++
		}
		if task.Status != "Resolved" && task.Deadline.Before(now) {
			result.OverdueTasks++
		}
	}
	result.AttentionOrders = len(orders)
	return result, nil
}

func (s *Store) Audit(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,order_id,task_id,action,timestamp,actor,previous_value,new_value FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []domain.AuditEntry{}
	for rows.Next() {
		var entry domain.AuditEntry
		var timestamp string
		if err := rows.Scan(&entry.ID, &entry.OrderID, &entry.TaskID, &entry.Action, &timestamp, &entry.Actor, &entry.PreviousValue, &entry.NewValue); err != nil {
			return nil, err
		}
		entry.Timestamp, _ = domain.ParseDate(timestamp)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) Imports(ctx context.Context, limit int) ([]domain.ImportRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,filename,uploaded_at,received_rows,accepted_rows,invalid_rows FROM imports ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []domain.ImportRecord{}
	for rows.Next() {
		var record domain.ImportRecord
		var uploaded, invalid string
		if err := rows.Scan(&record.ID, &record.Filename, &uploaded, &record.ReceivedRows, &record.AcceptedRows, &invalid); err != nil {
			return nil, err
		}
		record.UploadedAt, _ = domain.ParseDate(uploaded)
		if err := json.Unmarshal([]byte(invalid), &record.InvalidRows); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) UpdateTask(ctx context.Context, id int64, employee, status, actor string, now time.Time) (domain.Task, error) {
	if err := ValidateTaskUpdate(employee, status); err != nil {
		return domain.Task{}, err
	}
	employee = strings.TrimSpace(employee)
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "Manager"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	task, err := taskByID(ctx, tx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if employee != "" && employee != task.Employee {
		previous := task.Employee
		task.Employee = employee
		if err := insertAudit(ctx, tx, domain.AuditEntry{OrderID: task.OrderID, TaskID: task.ID, Action: "Task reassigned", Timestamp: now, Actor: actor, PreviousValue: previous, NewValue: employee}); err != nil {
			return domain.Task{}, err
		}
	}
	if status != "" && status != task.Status {
		if status != "Resolved" {
			var activeCount int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE order_id=? AND rule=? AND id<>? AND status IN ('Open','In Progress')`, task.OrderID, task.Rule, task.ID).Scan(&activeCount); err != nil {
				return domain.Task{}, err
			}
			if activeCount > 0 {
				return domain.Task{}, ErrActiveTaskExists
			}
		}
		previous := task.Status
		task.Status = status
		if err := insertAudit(ctx, tx, domain.AuditEntry{OrderID: task.OrderID, TaskID: task.ID, Action: "Status changed", Timestamp: now, Actor: actor, PreviousValue: previous, NewValue: status}); err != nil {
			return domain.Task{}, err
		}
	}
	task.UpdatedAt = now
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET employee=?,status=?,updated_at=? WHERE id=?`, task.Employee, task.Status, timeString(now), id)
	if err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func taskByID(ctx context.Context, query queryer, id int64) (domain.Task, error) {
	var task domain.Task
	var orderDate, deadline, createdAt, updatedAt string
	rows, err := query.QueryContext(ctx, `SELECT id,order_id,order_date,customer_name,phone,order_value,rule,reason,category,priority,employee,action,deadline,status,created_at,updated_at FROM tasks WHERE id=?`, id)
	if err != nil {
		return task, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return task, err
		}
		return task, sql.ErrNoRows
	}
	if err := rows.Scan(&task.ID, &task.OrderID, &orderDate, &task.CustomerName, &task.Phone, &task.OrderValue, &task.Rule,
		&task.Reason, &task.Category, &task.Priority, &task.Employee, &task.Action, &deadline, &task.Status, &createdAt, &updatedAt); err != nil {
		return task, err
	}
	task.OrderDate, _ = domain.ParseDate(orderDate)
	task.Deadline, _ = domain.ParseDate(deadline)
	task.CreatedAt, _ = domain.ParseDate(createdAt)
	task.UpdatedAt, _ = domain.ParseDate(updatedAt)
	return task, nil
}

func (s *Store) DailyReport(ctx context.Context, now time.Time) ([][]string, error) {
	tasks, err := s.Tasks(ctx, TaskFilter{})
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"Report section", "Order ID", "Customer", "Exception category", "Priority", "Employee", "Action required", "Status", "Deadline"}}
	add := func(section string, task domain.Task) {
		rows = append(rows, []string{section, task.OrderID, task.CustomerName, task.Category, task.Priority, task.Employee, task.Action, task.Status, timeString(task.Deadline)})
	}
	for _, task := range tasks {
		if task.CreatedAt.UTC().Format("2006-01-02") == now.UTC().Format("2006-01-02") {
			add("New exception", task)
		}
		if task.Status != "Resolved" {
			add("Unresolved exception", task)
			if task.Deadline.Before(now) {
				add("Overdue task", task)
			}
			if task.Priority == "High" {
				add("High-priority case", task)
			}
			add("Employee pending action", task)
		}
	}
	return rows, nil
}

func ValidateTaskUpdate(employee, status string) error {
	trimmedEmployee := strings.TrimSpace(employee)
	if employee != "" && trimmedEmployee == "" {
		return fmt.Errorf("assigned employee is required")
	}
	if trimmedEmployee == "" && status == "" {
		return fmt.Errorf("employee or status update is required")
	}
	if status != "" && status != "Open" && status != "In Progress" && status != "Resolved" {
		return fmt.Errorf("status must be Open, In Progress, or Resolved")
	}
	return nil
}
