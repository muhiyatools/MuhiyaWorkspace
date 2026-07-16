package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gateway/db"
)

type AdminAPI struct {
	db *db.DB
}

func RegisterRoutes(mux *http.ServeMux, database *db.DB) {
	api := &AdminAPI{db: database}

	mux.HandleFunc("/api/stats", api.handleStats)
	mux.HandleFunc("/api/users", api.handleUsers)
	mux.HandleFunc("/api/plans", api.handlePlans)
	mux.HandleFunc("/api/budgets", api.handleBudgets)
	mux.HandleFunc("/api/keys", api.handleKeys)
	mux.HandleFunc("/api/providers", api.handleProviders)
	mux.HandleFunc("/api/models", api.handleModels)
	mux.HandleFunc("/api/settings", api.handleSettings)
	mux.HandleFunc("/api/logs", api.handleLogs)
	mux.HandleFunc("/api/users/topups", api.handleUserTopups)
	mux.HandleFunc("/api/users/reset-usage", api.handleResetUsage)
}

// handleResetUsage is the admin bonus budget-reset (Part B). POST {scope:"all"|
// "user", user_id?, note?} raises the usage floor to now — clearing current
// in-window usage WITHOUT moving any scheduled reset time (INV-5). Protected by
// the same Basic auth as every /api route (main.go wraps this mux).
func (api *AdminAPI) handleResetUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var body struct {
		Scope  string `json:"scope"`
		UserID string `json:"user_id"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	switch body.Scope {
	case "all":
		affected, err := api.db.ResetAllUsersUsage(body.Note)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]interface{}{"scope": "all", "users_reset": affected})
	case "user":
		if body.UserID == "" {
			api.errorResponse(w, http.StatusBadRequest, "user_id is required for scope=user")
			return
		}
		if err := api.db.ResetUserUsage(body.UserID, body.Note); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]interface{}{"scope": "user", "user_id": body.UserID})
	default:
		api.errorResponse(w, http.StatusBadRequest, "scope must be 'all' or 'user'")
	}
}

func (api *AdminAPI) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	stats, err := api.db.GetDashboardStats()
	if err != nil {
		api.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.jsonResponse(w, http.StatusOK, stats)
}

// --- Users Handler ---
func (api *AdminAPI) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if id != "" {
			u, err := api.db.GetUser(id)
			if err != nil {
				api.dbErrorResponse(w, err)
				return
			}
			if u == nil {
				api.errorResponse(w, http.StatusNotFound, "User not found")
				return
			}
			api.jsonResponse(w, http.StatusOK, u)
			return
		}
		list, err := api.db.ListUsers()
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, list)

	case http.MethodPost:
		var u db.User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if u.ID == "" {
			u.ID = "user-" + generateRandomString(8)
		}
		u.CreatedAt = time.Now()
		if u.Status == "" {
			u.Status = "active"
		}

		if err := api.db.CreateUser(u); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, u)

	case http.MethodPut:
		var u db.User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if u.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "User ID is required")
			return
		}
		existing, err := api.db.GetUser(u.ID)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		if existing == nil {
			api.errorResponse(w, http.StatusNotFound, "User not found")
			return
		}
		if u.Status == "" {
			u.Status = existing.Status
		}
		if u.CreatedAt.IsZero() {
			u.CreatedAt = existing.CreatedAt
		}
		if err := api.db.UpdateUser(u); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, u)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "ID query parameter is required")
			return
		}
		if err := api.db.DeleteUser(id); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// --- Plans Handler ---
func (api *AdminAPI) handlePlans(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := api.db.ListPlans()
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, list)

	case http.MethodPost:
		var p db.Plan
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if p.ID == "" {
			p.ID = "plan-" + generateRandomString(8)
		}
		p.CreatedAt = time.Now()

		if err := api.db.CreatePlan(p); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, p)

	case http.MethodPut:
		var p db.Plan
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if p.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "Plan ID is required")
			return
		}
		if err := api.db.UpdatePlan(p); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, p)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "ID query parameter is required")
			return
		}
		if err := api.db.DeletePlan(id); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// --- Budget Windows Handler ---
func (api *AdminAPI) handleBudgets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		planID := r.URL.Query().Get("plan_id")
		var list []db.BudgetWindow
		var err error
		if planID != "" {
			list, err = api.db.ListBudgetWindowsByPlan(planID)
		} else {
			list, err = api.db.ListBudgetWindows()
		}
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, list)

	case http.MethodPost:
		var bw db.BudgetWindow
		if err := json.NewDecoder(r.Body).Decode(&bw); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if bw.ID == "" {
			bw.ID = "budget-" + generateRandomString(8)
		}
		bw.CreatedAt = time.Now()

		if err := api.db.CreateBudgetWindow(bw); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, bw)

	case http.MethodPut:
		var bw db.BudgetWindow
		if err := json.NewDecoder(r.Body).Decode(&bw); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if bw.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "Budget Window ID is required")
			return
		}
		if err := api.db.UpdateBudgetWindow(bw); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, bw)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "ID query parameter is required")
			return
		}
		if err := api.db.DeleteBudgetWindow(id); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// --- Virtual Keys Handler ---
func (api *AdminAPI) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		userID := r.URL.Query().Get("user_id")
		var list []db.VirtualKey
		var err error
		if userID != "" {
			list, err = api.db.ListVirtualKeysByUserID(userID)
		} else {
			list, err = api.db.ListVirtualKeys()
		}
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, list)

	case http.MethodPost:
		var vk db.VirtualKey
		if err := json.NewDecoder(r.Body).Decode(&vk); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		// CreateVirtualKey generates both the internal ID and the bearer
		// token itself (storing only the token's hash); any client-supplied
		// ID is ignored so a caller can never choose - and thereby learn
		// something about - the value used to key lookups.
		vk.ID = ""
		vk.Status = "active"
		vk.CreatedAt = time.Now()

		created, err := api.db.CreateVirtualKey(vk)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		// created.Token carries the plaintext bearer credential - the ONLY
		// response that will ever contain it. Callers must copy it now.
		api.jsonResponse(w, http.StatusCreated, created)

	case http.MethodPut:
		var vk db.VirtualKey
		if err := json.NewDecoder(r.Body).Decode(&vk); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if vk.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "Key ID is required")
			return
		}
		existing, err := api.db.GetVirtualKeyByID(vk.ID)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		if existing == nil {
			api.errorResponse(w, http.StatusNotFound, "Virtual Key not found")
			return
		}
		if vk.Status == "revoked" {
			if err := api.db.DeleteVirtualKey(vk.ID); err != nil {
				api.dbErrorResponse(w, err)
				return
			}
			api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})
			return
		}
		if vk.Status == "" {
			vk.Status = existing.Status
		}
		if vk.CreatedAt.IsZero() {
			vk.CreatedAt = existing.CreatedAt
		}
		if vk.ExpiresAt == nil && existing.ExpiresAt != nil {
			vk.ExpiresAt = existing.ExpiresAt
		}
		if err := api.db.UpdateVirtualKey(vk); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, vk)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "ID query parameter is required")
			return
		}
		if err := api.db.DeleteVirtualKey(id); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// --- Providers Handler ---
// providerView is the browser-safe projection of a provider: the upstream API
// key is never serialized, only whether one is configured.
type providerView struct {
	db.Provider
	APIKey    string `json:"api_key"` // always "" — shadows the embedded secret
	HasAPIKey bool   `json:"has_api_key"`
}

func redactProviderKeys(list []db.Provider) []providerView {
	out := make([]providerView, 0, len(list))
	for _, p := range list {
		hasKey := strings.TrimSpace(p.APIKey) != ""
		p.APIKey = ""
		out = append(out, providerView{Provider: p, APIKey: "", HasAPIKey: hasKey})
	}
	return out
}

func (api *AdminAPI) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := api.db.ListProviders()
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		// Never ship upstream API keys to the browser. Redact the secret and
		// expose only whether one is configured; the edit form preserves the
		// stored key when the field is left blank (see PUT below).
		api.jsonResponse(w, http.StatusOK, redactProviderKeys(list))

	case http.MethodPost:
		var p db.Provider
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if p.ID == "" {
			p.ID = strings.ToLower(p.Name)
		}
		p.Status = "active"
		p.CreatedAt = time.Now()
		p.UpdatedAt = time.Now()

		if err := api.db.CreateProvider(p); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, redactProviderKeys([]db.Provider{p})[0])

	case http.MethodPut:
		var p db.Provider
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if p.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "Provider ID is required")
			return
		}
		existing, err := api.db.GetProvider(p.ID)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		if existing == nil {
			api.errorResponse(w, http.StatusNotFound, "Provider not found")
			return
		}
		if p.Status == "" {
			p.Status = existing.Status
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = existing.CreatedAt
		}
		// A blank incoming key means "unchanged" (the GET response redacts it),
		// so preserve the stored secret instead of wiping it.
		if strings.TrimSpace(p.APIKey) == "" {
			p.APIKey = existing.APIKey
		}
		p.UpdatedAt = time.Now()
		if err := api.db.UpdateProvider(p); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, redactProviderKeys([]db.Provider{p})[0])

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "ID query parameter is required")
			return
		}
		if err := api.db.DeleteProvider(id); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// --- Models Handler ---
func (api *AdminAPI) handleModels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := api.db.ListModels()
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, list)

	case http.MethodPost:
		var m db.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if m.ID == "" {
			m.ID = "model-" + generateRandomString(8)
		}
		m.Status = "active"
		m.CreatedAt = time.Now()

		if err := api.db.CreateModel(m); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, m)

	case http.MethodPut:
		var m db.Model
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if m.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "Model ID is required")
			return
		}
		existing, err := api.db.GetModel(m.ID)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		if existing == nil {
			api.errorResponse(w, http.StatusNotFound, "Model not found")
			return
		}
		if m.Status == "" {
			m.Status = existing.Status
		}
		if m.CreatedAt.IsZero() {
			m.CreatedAt = existing.CreatedAt
		}
		if err := api.db.UpdateModel(m); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, m)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "ID query parameter is required")
			return
		}
		if err := api.db.DeleteModel(id); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// --- System Settings Handler ---
func (api *AdminAPI) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := api.db.ListSettings()
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, list)

	case http.MethodPost:
		var s db.SystemSetting
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if s.Key == "" {
			api.errorResponse(w, http.StatusBadRequest, "Key is required")
			return
		}
		if err := api.db.SetSetting(s.Key, s.Value); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, s)

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// --- Logs Handler ---
func (api *AdminAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var log db.RequestLog
		if err := json.NewDecoder(r.Body).Decode(&log); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if log.ID == "" {
			log.ID = "log-" + generateRandomString(16)
		}
		if log.CreatedAt.IsZero() {
			log.CreatedAt = time.Now()
		}
		if err := api.db.InsertRequestLog(log); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, log)
		return
	}

	if r.Method != http.MethodGet {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	limit := 50
	offset := 0
	userID := r.URL.Query().Get("user_id")
	keyID := r.URL.Query().Get("virtual_key_id")

	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if val, err := strconv.Atoi(lStr); err == nil {
			limit = val
		}
	}
	if oStr := r.URL.Query().Get("offset"); oStr != "" {
		if val, err := strconv.Atoi(oStr); err == nil {
			offset = val
		}
	}

	list, err := api.db.ListRequestLogs(limit, offset, userID, keyID)
	if err != nil {
		api.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.jsonResponse(w, http.StatusOK, list)
}

// --- Helpers ---

func (api *AdminAPI) jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (api *AdminAPI) errorResponse(w http.ResponseWriter, status int, message string) {
	api.jsonResponse(w, status, map[string]string{"error": message})
}

// dbErrorResponse logs a database/internal error server-side (with whatever
// detail is useful for debugging) and returns a generic message to the
// client - a raw err.Error() risks leaking schema, driver, or query
// fragments to whoever is calling the admin API.
func (api *AdminAPI) dbErrorResponse(w http.ResponseWriter, err error) {
	log.Printf("[ADMIN-ERROR] %v", err)
	api.errorResponse(w, http.StatusInternalServerError, "Internal server error. See gateway logs for details.")
}

func generateRandomString(length int) string {
	b := make([]byte, length/2+1)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:length]
}

func (api *AdminAPI) handleUserTopups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			api.errorResponse(w, http.StatusBadRequest, "user_id is required")
			return
		}
		list, err := api.db.ListUserTopups(userID)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, list)

	case http.MethodPost:
		var t db.UserTopup
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if t.UserID == "" || t.Credits <= 0 {
			api.errorResponse(w, http.StatusBadRequest, "user_id and credits are required")
			return
		}
		if t.ID == "" {
			t.ID = "topup_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		t.UsedCredits = 0
		t.CreatedAt = time.Now()

		if err := api.db.CreateUserTopup(t); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, t)

	case http.MethodPut:
		// Set or clear a top-up's expiry (Part C). Body: {id, expires_at?}. A null
		// or omitted expires_at makes the top-up permanent.
		var body struct {
			ID        string     `json:"id"`
			ExpiresAt *time.Time `json:"expires_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if body.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "id is required")
			return
		}
		if err := api.db.SetUserTopupExpiry(body.ID, body.ExpiresAt); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]interface{}{"id": body.ID, "expires_at": body.ExpiresAt})

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "id is required")
			return
		}
		if err := api.db.DeleteUserTopup(id); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]interface{}{"id": id, "deleted": true})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
