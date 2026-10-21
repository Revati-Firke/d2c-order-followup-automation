# D2C Order Follow-up Automation

Internal order exception desk for Vitanatura Elements. The backend is Go + SQLite; the frontend is React + Vite. The two applications have separate dependencies and clear entry points.

## Project Layout

```text
backend/
	cmd/api/             # Executable and dependency wiring
	internal/domain/     # Order types, CSV validation, exception rules
	internal/httpapi/    # REST handlers and static-file serving
	internal/sample/     # 100+ order sample dataset
	internal/storage/    # SQLite schema, queries, JSON migration
	go.mod
frontend/
	src/                 # React application
	public/styles.css    # Dashboard styles
	package.json
	vite.config.js
data/                  # Local SQLite database and legacy JSON backup
```

## Requirements

- Go 1.26 or newer
- Node.js 22 or newer and npm

## First-Time Setup

```powershell
npm --prefix frontend install
npm --prefix frontend run build
```

## Run Locally

For a production-style local run, build React and start the Go API:

```powershell
npm --prefix frontend run build
go -C backend run ./cmd/api
```

Open [http://localhost:3000](http://localhost:3000). During development, start the backend and frontend in separate terminals from the repository root:

```powershell
go -C backend run ./cmd/api
```

```powershell
npm --prefix frontend run dev
```

Vite serves the UI at [http://localhost:5173](http://localhost:5173) and proxies `/api` to Go on port 3000. Start the backend first. Optional settings are `PORT` (Go listen port, default `3000`), `DB_PATH` (SQLite file, default `../data/orders.sqlite` from the backend working directory), `FRONTEND_DIST` (built static UI directory, default `../frontend/dist` from the backend working directory), and `VITE_API_TARGET` (Vite API proxy target, default `http://localhost:3000`).

`GET /health` and `GET /api/health` return `{"status":"ok","database":"ok"}` when SQLite is reachable.

The server creates `data/orders.sqlite` on first run. If `data/store.json` from the earlier Node implementation exists and the database is empty, Go imports its orders, tasks, audit trail, import history, and rule settings once. The JSON file is retained as a backup; subsequent writes go to SQLite.

## Import and sample data

Use **Import orders** for `.csv` or `.xlsx` uploads. Excel worksheets are converted in the browser and then validated by the Go importer. The header row must contain these columns; capitalization and spaces are ignored:

`Order ID`, `Order Date`, `Customer Name`, `Phone Number`, `Order Value`, `Payment Type`, `Order Status`, `Dispatch Date`, `Expected Delivery Date`, `Assigned Employee`

Payment values are `Prepaid` or `COD`. Supported order statuses are `Unconfirmed`, `Confirmed`, `Dispatched`, `Delivered`, `Undelivered`, and `Failed Delivery`. Use `Pending` for an order not yet dispatched. Invalid rows are returned with row numbers and reasons; they are recorded in import history but do not create orders or tasks.

**Load sample orders** creates 100 regular orders plus six exception cases and three duplicate/incomplete/invalid-date rows: 109 input rows total, 106 accepted and 3 reported for correction. It includes `VN-RULE-DISPATCH-12H`, a confirmed order aged 18 hours, to demonstrate the dispatch threshold change. Repeat imports update orders and do not duplicate active tasks for the same order/rule.

## Rules and task workflow

Defaults: unconfirmed after 2 hours; confirmed, not dispatched after 24 hours; overdue dispatched/undelivered shipments; failed delivery attempts; and undelivered orders above ₹5,000 past expected delivery. **Rule settings** persists thresholds and reevaluates existing orders. Invalid order data is reported for correction rather than used to generate exceptions.

Tasks have category, reason, priority, employee, action, deadline, and status. Reassignment and status changes persist with previous/new values in the audit trail. Reassigning an overdue task does not change its deadline. Dashboard metrics and queue filters are computed from SQLite data. **Daily report** downloads new, unresolved, overdue, high-priority, and employee-pending actions as CSV.

## Test and Build

```powershell
go -C backend test -count=1 ./...
go -C backend vet ./...
npm --prefix frontend test
npm --prefix frontend run build
```

Go tests cover validation, all five rules, SQLite persistence/migration, task deduplication, reassignment, and HTTP routes. Frontend tests cover Excel-to-CSV conversion and cell/date edge cases. The React build is separate so backend and UI changes can be verified independently.

## Demo Walkthrough

1. Start the backend and frontend, then click **Load sample orders**. Confirm 106 accepted rows and 3 correction rows.
2. With the default 24-hour dispatch setting, search the queue for `VN-RULE-DISPATCH-12H`; it should not have a task.
3. Open **Rule settings**, change dispatch deadline to `12`, and save. The same order should now have a dispatch-delay task.
4. Reassign the overdue `VN-RULE-FAILED` task and save. Its owner and audit entry should change; its deadline and overdue label should remain.
5. Use the date/category/employee/priority/status filters and download **Daily report** to demonstrate backend-derived results.