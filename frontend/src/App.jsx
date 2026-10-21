import { useEffect, useState } from "react";
import { readSheet } from "read-excel-file/browser";
import { excelRowsToCsv } from "./importCsv.js";

const emptyState = { orders: [], tasks: [], audit: [], imports: [], config: { dispatchHours: 24, unconfirmedHours: 2, highValueThreshold: 5000 } };
const money = (value) => new Intl.NumberFormat("en-IN", { style: "currency", currency: "INR", maximumFractionDigits: 0 }).format(Number(value) || 0);
const displayDate = (value, options = { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" }) => new Date(value).toLocaleString("en-IN", options);

async function api(path, options = {}) {
  const response = await fetch(path, options);
  const contentType = response.headers.get("content-type") || "";
  const body = contentType.includes("application/json") ? await response.json() : {};
  if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
  return body;
}

function App() {
  const [snapshot, setSnapshot] = useState(emptyState);
  const [auditEntries, setAuditEntries] = useState([]);
  const [tasks, setTasks] = useState([]);
  const [dashboard, setDashboard] = useState({});
  const [filters, setFilters] = useState({ date: "", category: "", employee: "", priority: "", status: "" });
  const [drafts, setDrafts] = useState({});
  const [actor, setActor] = useState("Manager");
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    const options = { signal: controller.signal };
    const query = new URLSearchParams(Object.entries(filters).filter(([, value]) => value));
    Promise.all([api("/api/state", options), api(`/api/tasks?${query}`, options), api(`/api/dashboard?${query}`, options), api("/api/audit?limit=10", options)])
      .then(([nextSnapshot, nextTasks, nextDashboard, nextAudit]) => {
        setSnapshot(nextSnapshot);
        setAuditEntries(nextAudit || []);
        setTasks(nextTasks || []);
        setDashboard(nextDashboard);
      })
      .catch((error) => {
        if (error.name !== "AbortError") showNotice(error.message, true);
      });
    return () => controller.abort();
  }, [filters, revision]);

  useEffect(() => {
    const timer = setTimeout(() => setNotice(""), 3600);
    return () => clearTimeout(timer);
  }, [notice]);

  useEffect(() => {
    const timer = setInterval(() => setRevision((value) => value + 1), 60_000);
    return () => clearInterval(timer);
  }, []);

  function showNotice(message, isError = false) {
    setNotice(message);
    setNoticeError(isError);
  }

  function refresh() {
    setRevision((value) => value + 1);
  }

  function setFilter(name, value) {
    setFilters((current) => ({ ...current, [name]: value }));
  }

  async function importFile(file) {
    const form = new FormData();
    let upload = file;
    if (file.name.toLowerCase().endsWith(".xlsx")) {
      const rows = await readSheet(file);
      const csv = excelRowsToCsv(rows);
      upload = new File([csv], file.name.replace(/\.xlsx$/i, ".csv"), { type: "text/csv" });
    }
    form.append("file", upload);
    form.append("filename", file.name);
    form.append("actor", actor.trim() || "Manager");
    try {
      const result = await api("/api/import", { method: "POST", body: form });
      showNotice(`${result.import.acceptedRows} orders accepted; ${result.import.invalidRows.length} rows need correction.`);
      refresh();
    } catch (error) {
      showNotice(error.message, true);
    }
  }

  async function loadSample() {
    try {
      const result = await api("/api/sample", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ actor }) });
      showNotice(`Sample dataset loaded: ${result.import.acceptedRows} accepted, ${result.import.invalidRows.length} correction rows.`);
      refresh();
    } catch (error) {
      showNotice(error.message, true);
    }
  }

  async function saveTask(task) {
    const draft = drafts[task.id] || { employee: task.employee, status: task.status };
    try {
      const responsibleUser = actor.trim() || "Manager";
      await api(`/api/tasks/${task.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ employee: draft.employee, status: draft.status, actor: responsibleUser })
      });
      setDrafts((current) => {
        const next = { ...current };
        delete next[task.id];
        return next;
      });
      showNotice("Task updated; deadline and audit history retained.");
      refresh();
    } catch (error) {
      showNotice(error.message, true);
    }
  }

  async function saveConfig(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const config = {
      dispatchHours: Number(form.get("dispatchHours")),
      unconfirmedHours: Number(form.get("unconfirmedHours")),
      highValueThreshold: Number(form.get("highValueThreshold"))
    };
    try {
      await api("/api/config", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...config, actor: actor.trim() || "Manager" })
      });
      showNotice("Rule settings saved and current orders reevaluated.");
      refresh();
    } catch (error) {
      showNotice(error.message, true);
    }
  }

  function updateDraft(task, key, value) {
    setDrafts((current) => ({ ...current, [task.id]: { ...current[task.id], employee: current[task.id]?.employee ?? task.employee, status: current[task.id]?.status ?? task.status, [key]: value } }));
  }

  const categories = [...new Set(snapshot.tasks.map((task) => task.category))].sort();
  const employees = [...new Set(snapshot.tasks.map((task) => task.employee))].sort();
  const latestImport = snapshot.imports[0];
  const invalidRows = latestImport?.invalidRows || [];
  const openCases = Object.entries(dashboard.byCategory || {});
  const byEmployee = Object.entries(dashboard.byEmployee || {}).sort((a, b) => b[1] - a[1]);
  const now = new Date();

  return <>
    <header className="topbar">
      <a className="brand" href="/" aria-label="Vitanatura order follow-up home">
        <span className="brand-mark">V</span>
        <span><strong>VITANATURA</strong><small>ELEMENTS / OPERATIONS</small></span>
      </a>
      <div className="topbar-right">
        <label className="actor-field">Working as <input value={actor} onChange={(event) => setActor(event.target.value)} aria-label="Responsible user" /></label>
        <span className="live-indicator"><i /> Local workspace</span>
      </div>
    </header>

    <main className="workspace">
      <section className="page-heading">
        <div>
          <p className="eyebrow">ORDER OPERATIONS <span>{now.toLocaleDateString("en-IN", { weekday: "long", day: "numeric", month: "long", year: "numeric" })}</span></p>
          <h1>Follow-up desk</h1>
          <p className="subtitle">Exceptions that need a human next step.</p>
        </div>
        <div className="actions">
          <button className="button button-muted" onClick={loadSample} type="button"><span aria-hidden="true">＋</span> Load sample orders</button>
          <label className="button button-primary upload-button" htmlFor="file-input"><span aria-hidden="true">↑</span> Import orders</label>
          <input id="file-input" type="file" accept=".csv,.xlsx,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" onChange={(event) => { if (event.target.files?.[0]) importFile(event.target.files[0]).catch((error) => showNotice(error.message, true)); event.target.value = ""; }} />
          <a className="button button-outline" href="/api/report.csv"><span aria-hidden="true">↓</span> Daily report</a>
        </div>
      </section>

      <section className="metrics" aria-label="Order follow-up metrics">
        <Metric title="Total orders" value={dashboard.totalOrders} note="Accepted order records" />
        <Metric title="Need attention" value={dashboard.attentionOrders} note="Orders with active exceptions" className="metric-attention" />
        <Metric title="Open tasks" value={dashboard.openTasks} note="Awaiting action" />
        <Metric title="Overdue" value={dashboard.overdueTasks} note="Past task deadline" className="metric-overdue" />
        <Metric title="High priority" value={dashboard.highPriority} note="Active cases" className="metric-high" />
      </section>

      <section className="control-bar" aria-label="Task filters">
        <div className="filter-heading"><span className="section-kicker">WORK QUEUE</span><strong>Filters</strong></div>
        <label>Order date<input type="date" value={filters.date} onChange={(event) => setFilter("date", event.target.value)} /></label>
        <label>Exception<select value={filters.category} onChange={(event) => setFilter("category", event.target.value)}><option value="">All types</option>{categories.map((category) => <option key={category}>{category}</option>)}</select></label>
        <label>Employee<select value={filters.employee} onChange={(event) => setFilter("employee", event.target.value)}><option value="">All employees</option>{employees.map((employee) => <option key={employee}>{employee}</option>)}</select></label>
        <label>Priority<select value={filters.priority} onChange={(event) => setFilter("priority", event.target.value)}><option value="">All priorities</option><option>High</option><option>Medium</option><option>Low</option></select></label>
        <label>Status<select value={filters.status} onChange={(event) => setFilter("status", event.target.value)}><option value="">All statuses</option><option>Open</option><option>In Progress</option><option>Resolved</option></select></label>
        <button className="text-button" type="button" onClick={() => setFilters({ date: "", category: "", employee: "", priority: "", status: "" })}>Clear</button>
      </section>

      <div className="content-grid">
        <section className="panel queue-panel">
          <div className="panel-heading">
            <div><span className="section-kicker">FOLLOW-UP</span><h2>Task queue <span>{tasks.length}</span></h2></div>
            <details className="config-menu">
              <summary title="Rule configuration">Rule settings <span aria-hidden="true">⌄</span></summary>
              <form key={`${snapshot.config.dispatchHours}-${snapshot.config.unconfirmedHours}-${snapshot.config.highValueThreshold}`} className="config-form" onSubmit={saveConfig}>
                <label>Dispatch deadline (hours)<input name="dispatchHours" type="number" min="1" max="168" defaultValue={snapshot.config.dispatchHours} required /></label>
                <label>Confirmation window (hours)<input name="unconfirmedHours" type="number" min="1" max="72" defaultValue={snapshot.config.unconfirmedHours} required /></label>
                <label>High-value threshold (₹)<input name="highValueThreshold" type="number" min="0" step="100" defaultValue={snapshot.config.highValueThreshold} required /></label>
                <button className="button button-primary" type="submit">Save rules</button>
              </form>
            </details>
          </div>
          <div className="table-scroll">
            <table>
              <thead><tr><th>Order / customer</th><th>Exception</th><th>Priority</th><th>Owner</th><th>Next action</th><th>Deadline</th><th>Status</th><th /></tr></thead>
              <tbody>{tasks.map((task) => {
                const draft = drafts[task.id] || task;
                const overdue = task.status !== "Resolved" && new Date(task.deadline) < now;
                return <tr key={task.id} className={overdue ? "row-overdue" : ""}>
                  <td><strong>{task.orderId}</strong><small>{task.customerName} · {money(task.orderValue)}</small></td>
                  <td><strong>{task.category}</strong><small>{task.reason}</small></td>
                  <td><span className={`priority priority-${task.priority.toLowerCase()}`}>{task.priority}</span>{overdue && <small className="overdue-label">Overdue</small>}</td>
                  <td><input className="owner-input" list="employee-options" value={draft.employee} aria-label={`Assigned employee for ${task.orderId}`} onChange={(event) => updateDraft(task, "employee", event.target.value)} /></td>
                  <td className="action-cell">{task.action}</td>
                  <td className="deadline-cell">{displayDate(task.deadline)}</td>
                  <td><select className="status-input" value={draft.status} aria-label={`Task status for ${task.orderId}`} onChange={(event) => updateDraft(task, "status", event.target.value)}><option>Open</option><option>In Progress</option><option>Resolved</option></select></td>
                  <td><button className="save-task" type="button" onClick={() => saveTask(task)}>Save</button></td>
                </tr>;
              })}</tbody>
            </table>
            {tasks.length === 0 && <div className="empty-state"><span className="empty-mark">✓</span><strong>Queue is clear</strong><p>Import orders or load the sample set to evaluate exceptions.</p></div>}
          </div>
          <datalist id="employee-options">{employees.map((employee) => <option key={employee} value={employee} />)}</datalist>
        </section>

        <aside className="side-column">
          <section className="panel breakdown-panel">
            <div className="panel-heading"><div><span className="section-kicker">OPEN WORK</span><h2>By exception</h2></div></div>
            <div className="breakdown-list">{openCases.length ? openCases.map(([category, count]) => <div className="breakdown-row" key={category}><span>{category}</span><strong>{count}</strong><i style={{ "--bar": `${Math.max(6, Math.round(count / Math.max(dashboard.attentionOrders, 1) * 100))}%` }} /></div>) : <p className="muted-empty">No active exceptions.</p>}</div>
            <div className="employee-breakdown">{byEmployee.length > 0 && <><h3>Pending by employee</h3>{byEmployee.map(([employee, count]) => <div className="employee-row" key={employee}><span><i className="avatar-dot">{employee.charAt(0)}</i>{employee}</span><strong>{count}</strong></div>)}</>}</div>
          </section>
          <section className="panel import-panel">
            <div className="panel-heading"><div><span className="section-kicker">DATA QUALITY</span><h2>Latest import</h2></div></div>
            {!latestImport ? <div className="import-summary">No orders imported yet.</div> : <>
              <div className="import-summary"><strong>{latestImport.acceptedRows} accepted</strong> from {latestImport.filename}<small>{invalidRows.length} row{invalidRows.length === 1 ? "" : "s"} need correction · {displayDate(latestImport.uploadedAt, { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" })}</small></div>
              <div className="error-list">{invalidRows.slice(0, 8).map((entry) => <div className="error-item" key={`${entry.rowNumber}-${entry.orderId}`}><strong>Row {entry.rowNumber}{entry.orderId ? ` · ${entry.orderId}` : ""}</strong><span>{entry.errors.join("; ")}</span></div>)}{invalidRows.length > 8 && <p className="more-errors">{invalidRows.length - 8} more row errors in import history.</p>}</div>
            </>}
          </section>
        </aside>
      </div>

      <section className="panel audit-panel">
        <div className="panel-heading"><div><span className="section-kicker">CHANGE HISTORY</span><h2>Recent audit trail</h2></div><span className="audit-note">Assignments and task updates</span></div>
        <div className="audit-table-scroll"><table className="audit-table"><thead><tr><th>Time</th><th>Order</th><th>Action</th><th>Change</th><th>Responsible user</th></tr></thead><tbody>{auditEntries.map((entry) => <tr key={entry.id}><td>{displayDate(entry.timestamp)}</td><td><strong>{entry.orderId}</strong></td><td>{entry.action}</td><td>{entry.previousValue ? `${entry.previousValue} → ${entry.newValue}` : entry.newValue}</td><td>{entry.actor}</td></tr>)}</tbody></table></div>
        {auditEntries.length === 0 && <div className="empty-audit">No task changes recorded.</div>}
      </section>
      {notice && <div className={`toast is-visible${noticeError ? " is-error" : ""}`} role="status" aria-live="polite">{notice}</div>}
    </main>
  </>;
}

function Metric({ title, value, note, className = "" }) {
  return <article className={`metric ${className}`}><span>{title}</span><strong>{Number(value || 0).toLocaleString("en-IN")}</strong><small>{note}</small></article>;
}

export default App;