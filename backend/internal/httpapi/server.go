package httpapi

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"d2c-order-followup-automation/internal/domain"
	"d2c-order-followup-automation/internal/sample"
	"d2c-order-followup-automation/internal/storage"
)

type Server struct {
	store     *storage.Store
	staticDir string
}

func NewServer(store *storage.Store) http.Handler {
	staticDir := os.Getenv("FRONTEND_DIST")
	if staticDir == "" {
		staticDir = filepath.Join("..", "frontend", "dist")
	}
	server := &Server{store: store, staticDir: staticDir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /api/health", server.health)
	mux.HandleFunc("GET /api/state", server.state)
	mux.HandleFunc("GET /api/tasks", server.tasks)
	mux.HandleFunc("GET /api/dashboard", server.dashboard)
	mux.HandleFunc("GET /api/audit", server.audit)
	mux.HandleFunc("GET /api/report.csv", server.report)
	mux.HandleFunc("POST /api/import", server.importCSV)
	mux.HandleFunc("POST /api/sample", server.sample)
	mux.HandleFunc("PATCH /api/config", server.config)
	mux.HandleFunc("PATCH /api/tasks/{id}", server.updateTask)
	mux.HandleFunc("GET /", server.static)
	return recoverMiddleware(mux)
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain one JSON value")
		}
		return err
	}
	return nil
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orders, err := s.store.Orders(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks, err := s.store.Tasks(ctx, storage.TaskFilter{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	audit, err := s.store.Audit(ctx, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	imports, err := s.store.Imports(ctx, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	config, err := s.store.Config(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders, "tasks": tasks, "audit": audit, "imports": imports, "config": config})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "ok"})
}

func taskFilter(r *http.Request) storage.TaskFilter {
	query := r.URL.Query()
	return storage.TaskFilter{Category: query.Get("category"), Employee: query.Get("employee"), Priority: query.Get("priority"), Date: query.Get("date"), Status: query.Get("status")}
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Tasks(r.Context(), taskFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.Dashboard(r.Context(), taskFilter(r), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.store.Audit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.DailyReport(r.Context(), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="daily-exception-report.csv"`)
	writer := csv.NewWriter(w)
	for _, row := range rows {
		if err := writer.Write(safeReportRow(row)); err != nil {
			return
		}
	}
	writer.Flush()
}

func (s *Server) importCSV(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "Upload a CSV file under 12 MB")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "CSV file field is required")
		return
	}
	defer file.Close()
	if strings.ToLower(filepath.Ext(header.Filename)) != ".csv" {
		writeError(w, http.StatusUnsupportedMediaType, "Upload a CSV file")
		return
	}
	filename := r.FormValue("filename")
	if filename == "" {
		filename = header.Filename
	}
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "Unable to read CSV: "+err.Error())
		return
	}
	actor := r.FormValue("actor")
	if actor == "" {
		actor = "Manager"
	}
	record, err := s.store.ImportRows(r.Context(), rows, filename, actor, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]domain.ImportRecord{"import": record})
}

func (s *Server) sample(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Actor string `json:"actor"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "Invalid sample request JSON")
		return
	}
	if input.Actor == "" {
		input.Actor = "Manager"
	}
	record, err := s.store.ImportRows(r.Context(), sample.SampleRows(time.Now().UTC()), "sample-orders.csv", input.Actor, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]domain.ImportRecord{"import": record})
}

func safeReportRow(row []string) []string {
	result := append([]string(nil), row...)
	for index, value := range result {
		first := strings.TrimLeft(value, " \t\r\n")
		if first != "" && strings.ContainsRune("=+-@", rune(first[0])) {
			result[index] = "'" + value
		}
	}
	return result
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	var input struct {
		domain.Config
		Actor string `json:"actor"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid configuration JSON")
		return
	}
	if input.DispatchHours < 1 || input.DispatchHours > 168 || input.UnconfirmedHours < 1 || input.UnconfirmedHours > 72 || input.HighValueThreshold < 0 {
		writeError(w, http.StatusBadRequest, "Dispatch hours must be 1-168, confirmation hours 1-72, and value threshold non-negative")
		return
	}
	actor := input.Actor
	if actor == "" {
		actor = "Manager"
	}
	if err := s.store.SaveConfig(r.Context(), input.Config, actor, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]domain.Config{"config": input.Config})
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "Invalid task ID")
		return
	}
	var input struct {
		Employee string `json:"employee"`
		Status   string `json:"status"`
		Actor    string `json:"actor"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid task update JSON")
		return
	}
	if err := storage.ValidateTaskUpdate(input.Employee, input.Status); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	actor := input.Actor
	if actor == "" {
		actor = "Manager"
	}
	task, err := s.store.UpdateTask(r.Context(), id, input.Employee, input.Status, actor, time.Now().UTC())
	if errors.Is(err, storage.ErrActiveTaskExists) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]domain.Task{"task": task})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "API endpoint not found")
		return
	}
	root := s.staticDir
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/"))))
	if !strings.HasPrefix(path, filepath.Clean(root)+string(os.PathSeparator)) && path != filepath.Clean(root) {
		writeError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		path = filepath.Join(root, "index.html")
	}
	http.ServeFile(w, r, path)
}
