package admin

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gateway/db"
	"gateway/upstreamurl"
)

type AdminAPI struct {
	db         *db.DB
	limitCache limitCacheInvalidator
}

type limitCacheInvalidator interface {
	InvalidateUser(userID string)
	InvalidateAll()
}

func RegisterRoutes(mux *http.ServeMux, database *db.DB, limits limitCacheInvalidator) {
	api := &AdminAPI{db: database, limitCache: limits}

	mux.HandleFunc("/api/stats", api.handleStats)
	mux.HandleFunc("/api/users", api.handleUsers)
	mux.HandleFunc("/api/plans", api.handlePlans)
	mux.HandleFunc("/api/budgets", api.handleBudgets)
	mux.HandleFunc("/api/keys", api.handleKeys)
	mux.HandleFunc("/api/providers", api.handleProviders)
	mux.HandleFunc("/api/providers/test", api.handleProviderTest)
	mux.HandleFunc("/api/models", api.handleModels)
	mux.HandleFunc("/api/models/test", api.handleModelTest)
	mux.HandleFunc("/api/coverage", api.handleCoverage)
	mux.HandleFunc("/api/settings", api.handleSettings)
	mux.HandleFunc("/api/logs", api.handleLogs)
	mux.HandleFunc("/api/logs/summary", api.handleLogSummary)
	mux.HandleFunc("/api/usage-resets", api.handleUsageResets)
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
			if errors.Is(err, sql.ErrNoRows) {
				api.errorResponse(w, http.StatusNotFound, "User not found")
				return
			}
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
		api.dbErrorResponse(w, err)
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
		if u.ID != "" && !validID(u.ID) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid user ID")
			return
		}
		if !validName(u.Name) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid name")
			return
		}
		if !validEmail(u.Email) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid email")
			return
		}
		if u.PlanID != "" && !validID(u.PlanID) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid plan ID")
			return
		}
		if u.Status != "" && !validStatus(u.Status, "active", "suspended") {
			api.errorResponse(w, http.StatusBadRequest, "Invalid status")
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
		api.invalidateUser(u.ID)
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
		if !validID(u.ID) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid user ID")
			return
		}
		if !validName(u.Name) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid name")
			return
		}
		if !validEmail(u.Email) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid email")
			return
		}
		if u.PlanID != "" && !validID(u.PlanID) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid plan ID")
			return
		}
		if u.Status != "" && !validStatus(u.Status, "active", "suspended") {
			api.errorResponse(w, http.StatusBadRequest, "Invalid status")
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
		api.invalidateUser(u.ID)
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
		api.invalidateUser(id)
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
		if message := validatePlan(p); message != "" {
			api.errorResponse(w, http.StatusBadRequest, message)
			return
		}
		p.CreatedAt = time.Now()

		if err := api.db.CreatePlan(p); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.invalidateAllLimits()
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
		if message := validatePlan(p); message != "" {
			api.errorResponse(w, http.StatusBadRequest, message)
			return
		}
		if err := api.db.UpdatePlan(p); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				api.errorResponse(w, http.StatusNotFound, "Plan not found")
				return
			}
			api.dbErrorResponse(w, err)
			return
		}
		api.invalidateAllLimits()
		api.jsonResponse(w, http.StatusOK, p)

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			api.errorResponse(w, http.StatusBadRequest, "ID query parameter is required")
			return
		}
		if err := api.db.DeletePlan(id); err != nil {
			switch {
			case errors.Is(err, db.ErrPlanInUse):
				api.errorResponse(w, http.StatusConflict, "Plan is assigned to users. Reassign those users before deleting it.")
			case errors.Is(err, sql.ErrNoRows):
				api.errorResponse(w, http.StatusNotFound, "Plan not found")
			default:
				api.dbErrorResponse(w, err)
			}
			return
		}
		api.invalidateAllLimits()
		api.jsonResponse(w, http.StatusOK, map[string]string{"message": "deleted"})

	default:
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func validatePlan(plan db.Plan) string {
	if !validID(plan.ID) {
		return "Invalid plan ID"
	}
	if !validName(plan.Name) {
		return "Invalid plan name"
	}
	if plan.RPMLimit < 0 || plan.TPMLimit < 0 {
		return "RPM and TPM limits cannot be negative"
	}
	seenDurations := make(map[int]bool, len(plan.BudgetWindows))
	for _, window := range plan.BudgetWindows {
		if window.ID != "" && !validID(window.ID) {
			return "Invalid budget window ID"
		}
		if !validName(window.Name) {
			return "Every budget window requires a valid name"
		}
		if window.DurationSeconds <= 0 {
			return "Budget window duration must be greater than zero"
		}
		if window.BudgetUSD < 0 || window.BudgetNanoUSD < 0 {
			return "Budget window amount cannot be negative"
		}
		if seenDurations[window.DurationSeconds] {
			return "Budget windows cannot use duplicate durations"
		}
		seenDurations[window.DurationSeconds] = true
	}
	return ""
}

func (api *AdminAPI) invalidateUser(userID string) {
	if api.limitCache != nil {
		api.limitCache.InvalidateUser(userID)
	}
}

func (api *AdminAPI) invalidateAllLimits() {
	if api.limitCache != nil {
		api.limitCache.InvalidateAll()
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
		api.invalidateAllLimits()
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
		api.invalidateAllLimits()
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
		api.invalidateAllLimits()
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
		if !validName(vk.Name) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid name")
			return
		}
		if !validID(vk.UserID) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid user ID")
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
		if !validID(vk.ID) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid key ID")
			return
		}
		if vk.Status != "" && !validStatus(vk.Status, "active", "revoked") {
			api.errorResponse(w, http.StatusBadRequest, "Invalid status")
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
		// Validate the fields that are about to be persisted (the revoke path
		// above deletes and carries none of them).
		if !validName(vk.Name) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid name")
			return
		}
		if !validID(vk.UserID) {
			api.errorResponse(w, http.StatusBadRequest, "Invalid user ID")
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

var modelNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]*$`)

// validateModel enforces the invariants an operator can otherwise violate by
// hand: a routable slug name, no duplicate names, a real provider, a target
// model, and non-negative numbers. It returns a non-zero errCode
// (with message) to reject, or a list of non-blocking warnings that the admin
// UI surfaces as a toast (e.g. "$0 and active", "provider inactive"). selfID is
// the row being updated (excluded from the duplicate check); "" on create.
// validateModelShape holds the DB-free invariants (name format, target,
// non-negative numbers) so they are unit-testable without a database. Returns a
// non-zero errCode with message on rejection.
func validateModelShape(m *db.Model) (errCode int, errMsg string) {
	name := strings.TrimSpace(m.Name)
	if name == "" {
		return http.StatusBadRequest, "Model name is required."
	}
	if !modelNameRe.MatchString(name) {
		return http.StatusBadRequest, "Model name must be lowercase letters/digits and . _ : - only, starting with a letter or digit."
	}
	if strings.TrimSpace(m.TargetModel) == "" {
		return http.StatusBadRequest, "Target model (the upstream provider model id) is required."
	}
	if m.InputCostPerMillion < 0 || m.OutputCostPerMillion < 0 ||
		m.CacheReadCostPerMillion < 0 || m.CacheWriteCostPerMillion < 0 || m.PricePerMinute < 0 {
		return http.StatusBadRequest, "Prices cannot be negative."
	}
	if m.ContextWindow < 0 || m.MaxOutputTokens < 0 {
		return http.StatusBadRequest, "Token limits cannot be negative."
	}
	if m.MaxAttachmentMB < 0 {
		return http.StatusBadRequest, "Max attachment size cannot be negative (0 = client default)."
	}
	if code, msg := validateMimeList(m.AcceptedMimeTypes); code != 0 {
		return code, msg
	}
	return 0, ""
}

// mimeTypeRe accepts type/subtype MIME entries, including vendor trees,
// wildcards and suffixes (image/*, application/pdf, audio/x-m4a, text/*).
var mimeTypeRe = regexp.MustCompile(`^[a-zA-Z0-9!#$&^_.+-]+/(\*|[a-zA-Z0-9!#$&^_.*+-]+)$`)

// validateMimeList checks the optional comma-separated accepted_mime_types
// allowlist: every non-empty entry must look like a MIME type. Empty is valid
// (capability flags alone define the accepted set).
func validateMimeList(list string) (errCode int, errMsg string) {
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if !mimeTypeRe.MatchString(p) {
			return http.StatusBadRequest, "Accepted MIME types must be comma-separated type/subtype entries (e.g. image/png, application/pdf); '" + p + "' is not one."
		}
	}
	return 0, ""
}

func (api *AdminAPI) validateModel(m *db.Model, selfID string) (warnings []string, errCode int, errMsg string) {
	if code, msg := validateModelShape(m); code != 0 {
		return nil, code, msg
	}
	name := strings.TrimSpace(m.Name)

	// Duplicate name (case-insensitive), excluding the row being updated.
	all, err := api.db.ListModels()
	if err != nil {
		return nil, http.StatusInternalServerError, "Failed to check for duplicate model names."
	}
	for i := range all {
		if all[i].ID != selfID && strings.EqualFold(strings.TrimSpace(all[i].Name), name) {
			return nil, http.StatusConflict, "A model named '" + name + "' already exists."
		}
	}

	// Provider must exist.
	prov, err := api.db.GetProvider(m.ProviderID)
	if err != nil {
		return nil, http.StatusInternalServerError, "Failed to look up the model's provider."
	}
	if prov == nil {
		return nil, http.StatusBadRequest, "Provider '" + m.ProviderID + "' does not exist. Create the provider first."
	}

	// Non-blocking warnings — the model saves, but the operator is told why it
	// might not behave as expected.
	if m.Status == "active" && !m.Transcribe && m.InputCostPerMillion == 0 && m.OutputCostPerMillion == 0 {
		warnings = append(warnings, "This model is $0 and active — upstream free-tier rate limits may apply.")
	}
	if prov.Status != "active" {
		warnings = append(warnings, "Provider '"+m.ProviderID+"' is inactive — this model cannot serve traffic until the provider is activated.")
	}
	return warnings, 0, ""
}

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
		m.RoutingTier = "none"
		// Honor an explicit status if the form sent one; default to active so the
		// common "add and use immediately" flow stays frictionless.
		if m.Status == "" {
			m.Status = "active"
		}
		m.CreatedAt = time.Now()

		warnings, code, msg := api.validateModel(&m, "")
		if code != 0 {
			api.errorResponse(w, code, msg)
			return
		}
		if err := api.db.CreateModel(m); err != nil {
			if errors.Is(err, db.ErrInvalidModelConfig) {
				api.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusCreated, map[string]interface{}{"model": m, "warnings": warnings})

	case http.MethodPut:
		// Merge-patch: read the body once, load the existing row, then overlay
		// only the fields the body actually contains. This prevents a partial
		// payload from zeroing unspecified columns (the old blind full-overwrite
		// hazard — a form that omitted supports_vision would silently clear it).
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Failed to read request body")
			return
		}
		var incoming db.Model
		if err := json.Unmarshal(bodyBytes, &incoming); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		if incoming.ID == "" {
			api.errorResponse(w, http.StatusBadRequest, "Model ID is required")
			return
		}
		existing, err := api.db.GetModel(incoming.ID)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		if existing == nil {
			api.errorResponse(w, http.StatusNotFound, "Model not found")
			return
		}
		merged := *existing
		if err := json.Unmarshal(bodyBytes, &merged); err != nil {
			api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		// Identity/creation are immutable via update.
		merged.ID = existing.ID
		merged.CreatedAt = existing.CreatedAt
		merged.RoutingTier = "none"
		if merged.Status == "" {
			merged.Status = existing.Status
		}

		warnings, code, msg := api.validateModel(&merged, existing.ID)
		if code != 0 {
			api.errorResponse(w, code, msg)
			return
		}
		if err := api.db.UpdateModel(merged); err != nil {
			if errors.Is(err, db.ErrInvalidModelConfig) {
				api.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]interface{}{"model": merged, "warnings": warnings})

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

// --- Health-check & coverage endpoints ---

var healthHTTPClient = &http.Client{Timeout: 10 * time.Second}

// adminModelVisionCapable mirrors proxy.modelMatchesVision (flag-first, then the
// name heuristic) so the coverage banner counts vision exactly as the router
// would route it. Kept as a small local copy to avoid an admin→proxy import; if
// the router predicate changes, update both (there is a test pinning the list).
func adminModelVisionCapable(m *db.Model) bool {
	if m.SupportsVision {
		return true
	}
	hay := strings.ToLower(m.Name + " " + m.TargetModel)
	for _, kw := range []string{"gpt-4o", "claude-3-5-sonnet", "vision", "-vl", "gemini", "gemma", "pixtral", "llava"} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

// testProvider probes a provider's upstream model-list endpoint to confirm the
// base URL + key work. OpenAI dialect: GET {base}/models with a bearer token.
// Anthropic dialect: GET {anthropic_base}/v1/models with x-api-key. Returns a
// truncated upstream message on failure.
func testProvider(baseURL, anthropicBaseURL, apiKey string) (ok bool, status int, message string, modelCount int) {
	var req *http.Request
	var err error
	switch {
	case strings.TrimSpace(baseURL) != "":
		req, err = http.NewRequest(http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/models", nil)
		if err != nil {
			return false, 0, "bad base_url: " + err.Error(), 0
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	case strings.TrimSpace(anthropicBaseURL) != "":
		req, err = http.NewRequest(http.MethodGet, strings.TrimSuffix(anthropicBaseURL, "/")+"/v1/models", nil)
		if err != nil {
			return false, 0, "bad anthropic_base_url: " + err.Error(), 0
		}
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		return false, 0, "provider has no base URL configured", 0
	}

	resp, err := healthHTTPClient.Do(req)
	if err != nil {
		return false, http.StatusBadGateway, "connection failed: " + err.Error(), 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 400 {
		return false, resp.StatusCode, truncateMsg(string(body)), 0
	}
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &parsed)
	return true, resp.StatusCode, "ok", len(parsed.Data)
}

// postJSONProbe sends a tiny POST and returns whether the upstream accepted it.
func postJSONProbe(url string, headers map[string]string, payload interface{}) (bool, int, string) {
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return false, 0, "bad url: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := healthHTTPClient.Do(req)
	if err != nil {
		return false, http.StatusBadGateway, "connection failed: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 400 {
		return false, resp.StatusCode, truncateMsg(string(body))
	}
	return true, resp.StatusCode, "ok"
}

// testModelCompletion sends a 1-token completion so "will this model work?" is a
// one-click answer, in whichever dialect the provider speaks.
func testModelCompletion(p *db.Provider, targetModel string) (bool, int, string) {
	if strings.TrimSpace(p.BaseURL) != "" {
		return postJSONProbe(
			upstreamurl.ChatCompletions(p.BaseURL),
			map[string]string{"Authorization": "Bearer " + p.APIKey},
			openAIModelProbePayload(p, targetModel))
	}
	if strings.TrimSpace(p.AnthropicBaseURL) != "" {
		return postJSONProbe(
			strings.TrimSuffix(p.AnthropicBaseURL, "/")+"/v1/messages",
			map[string]string{"x-api-key": p.APIKey, "anthropic-version": "2023-06-01"},
			map[string]interface{}{
				"model":      targetModel,
				"max_tokens": 1,
				"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			})
	}
	return false, 0, "provider has no base URL configured"
}

func openAIModelProbePayload(provider *db.Provider, targetModel string) map[string]interface{} {
	payload := map[string]interface{}{
		"model": targetModel, "messages": []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1, "stream": false,
	}
	identity := strings.ToLower(provider.ID + " " + provider.BaseURL + " " + targetModel)
	if strings.Contains(identity, "deepseek") {
		payload["thinking"] = map[string]string{"type": "disabled"}
	}
	return payload
}

func truncateMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

type providerTestRequest struct {
	ID               string `json:"id"`
	BaseURL          string `json:"base_url"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	APIKey           string `json:"api_key"`
}

// handleProviderTest probes a provider (stored by id, or a draft from the form)
// and reports whether its upstream is reachable + authenticated.
func (api *AdminAPI) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var body providerTestRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	baseURL, anthURL, apiKey := body.BaseURL, body.AnthropicBaseURL, body.APIKey
	if strings.TrimSpace(body.ID) != "" {
		p, err := api.db.GetProvider(body.ID)
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		if p == nil {
			api.errorResponse(w, http.StatusNotFound, "Provider not found")
			return
		}
		if baseURL == "" {
			baseURL = p.BaseURL
		}
		if anthURL == "" {
			anthURL = p.AnthropicBaseURL
		}
		// A blank key on the form means "use the stored one" (GET redacts it).
		if strings.TrimSpace(apiKey) == "" {
			apiKey = p.APIKey
		}
	}
	ok, status, message, count := testProvider(baseURL, anthURL, apiKey)
	api.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"ok": ok, "status": status, "message": message, "model_count": count,
	})
}

// handleModelTest sends a 1-token probe to a model's provider (transcription
// models fall back to a provider reachability check).
func (api *AdminAPI) handleModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.errorResponse(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if strings.TrimSpace(body.ID) == "" {
		api.errorResponse(w, http.StatusBadRequest, "Model id is required")
		return
	}
	m, err := api.db.GetModel(body.ID)
	if err != nil {
		api.dbErrorResponse(w, err)
		return
	}
	if m == nil {
		api.errorResponse(w, http.StatusNotFound, "Model not found")
		return
	}
	p, err := api.db.GetProvider(m.ProviderID)
	if err != nil {
		api.dbErrorResponse(w, err)
		return
	}
	if p == nil {
		api.errorResponse(w, http.StatusBadRequest, "Model's provider '"+m.ProviderID+"' does not exist")
		return
	}

	start := time.Now()
	if m.Transcribe {
		ok, status, message, _ := testProvider(p.BaseURL, p.AnthropicBaseURL, p.APIKey)
		api.jsonResponse(w, http.StatusOK, map[string]interface{}{
			"ok": ok, "status": status,
			"upstream_message": "transcription target validated by provider reachability: " + message,
			"latency_ms":       time.Since(start).Milliseconds(),
		})
		return
	}
	ok, status, message := testModelCompletion(p, m.TargetModel)
	api.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"ok": ok, "status": status, "upstream_message": message,
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

type coverageResult struct {
	ActiveModels     int      `json:"active_models"`
	ActiveVision     int      `json:"active_vision"`
	ActiveThinking   int      `json:"active_thinking"`
	ActiveAudio      int      `json:"active_audio"`
	ActiveVideo      int      `json:"active_video"`
	ActiveDocuments  int      `json:"active_documents"`
	ActiveTranscribe int      `json:"active_transcribe"`
	ActiveProviders  int      `json:"active_providers"`
	Warnings         []string `json:"warnings"`
}

// computeCoverage is the pure capability-coverage analysis (DB-free, testable).
// It counts active capabilities and composes actionable warnings: no active
// vision model (image routing will fail), a transcription/other model stranded
// on an inactive provider, and free-only vision (free-tier limits).
func computeCoverage(models []db.Model, providers []db.Provider) coverageResult {
	provActive := map[string]bool{}
	res := coverageResult{Warnings: []string{}}
	for i := range providers {
		if providers[i].Status == "active" {
			provActive[providers[i].ID] = true
			res.ActiveProviders++
		}
	}

	freeVisionOnly := 0
	paidVisionExists := false
	var stranded []string
	for i := range models {
		m := &models[i]
		if m.Status != "active" {
			continue
		}
		res.ActiveModels++
		if m.Transcribe {
			res.ActiveTranscribe++
			if !provActive[m.ProviderID] {
				stranded = append(stranded, "Transcription model '"+m.Name+"' is active but its provider '"+m.ProviderID+"' is inactive — transcription is down.")
			}
			continue
		}
		if !provActive[m.ProviderID] {
			stranded = append(stranded, "Model '"+m.Name+"' is active but its provider '"+m.ProviderID+"' is inactive — it cannot serve traffic.")
			continue
		}
		if adminModelVisionCapable(m) {
			res.ActiveVision++
			if m.InputCostPerMillion == 0 && m.OutputCostPerMillion == 0 {
				freeVisionOnly++
			} else {
				paidVisionExists = true
			}
		}
		if m.SupportsThinking {
			res.ActiveThinking++
		}
		if m.SupportsAudio {
			res.ActiveAudio++
		}
		if m.SupportsVideo {
			res.ActiveVideo++
		}
		if m.SupportsDocuments {
			res.ActiveDocuments++
		}
	}

	// Highest-priority warning first, then per-model stranding, then advisories.
	if res.ActiveVision == 0 {
		res.Warnings = append(res.Warnings, "No active vision-capable model on an active provider — image requests will fail routing. Activate a vision model (or tick its Vision flag) in Admin → Models.")
	}
	res.Warnings = append(res.Warnings, stranded...)
	if res.ActiveVision > 0 && !paidVisionExists && freeVisionOnly > 0 {
		res.Warnings = append(res.Warnings, "Only free ($0) models provide vision — daily free-tier limits apply. Consider adding a paid vision model as a fallback.")
	}
	return res
}

// handleCoverage reports capability coverage across active models + providers,
// with actionable warnings. Drives the admin Models banner.
func (api *AdminAPI) handleCoverage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	models, err := api.db.ListModels()
	if err != nil {
		api.dbErrorResponse(w, err)
		return
	}
	providers, err := api.db.ListProviders()
	if err != nil {
		api.dbErrorResponse(w, err)
		return
	}
	api.jsonResponse(w, http.StatusOK, computeCoverage(models, providers))
}

// --- System Settings Handler ---

// secretSettingKeys is the set of system_settings whose value is a live
// credential: never serialized on read, honors blank-means-keep on write. Every
// other setting (gateway_name, theme_accent, ...) round-trips normally.
var secretSettingKeys = map[string]bool{
	"tavily_api_key": true,
	"serper_api_key": true,
}

// settingView is the browser-safe projection of a system setting: a secret
// value is never serialized, only whether one is configured. Mirrors
// providerView above.
type settingView struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	HasValue bool   `json:"has_value"`
}

func redactSettings(list []db.SystemSetting) []settingView {
	out := make([]settingView, 0, len(list))
	for _, s := range list {
		hasValue := strings.TrimSpace(s.Value) != ""
		value := s.Value
		if secretSettingKeys[s.Key] {
			value = ""
		}
		out = append(out, settingView{Key: s.Key, Value: value, HasValue: hasValue})
	}
	return out
}

func (api *AdminAPI) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := api.db.ListSettings()
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, redactSettings(list))

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
		// A blank incoming secret means "unchanged" (the GET response redacts
		// it), so keep the stored value instead of wiping it. Non-secret keys
		// keep their normal set-to-anything behavior.
		if secretSettingKeys[s.Key] && strings.TrimSpace(s.Value) == "" {
			api.jsonResponse(w, http.StatusOK, s)
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
	if r.Method != http.MethodGet {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	limit := positiveQueryInt(r, "limit", 50, 200)
	offset := nonnegativeQueryInt(r, "offset", 0, 1_000_000_000)
	userID := r.URL.Query().Get("user_id")
	keyID := r.URL.Query().Get("virtual_key_id")
	if r.URL.Query().Has("page") || r.URL.Query().Has("page_size") {
		page := positiveQueryInt(r, "page", 1, 1_000_000)
		pageSize := positiveQueryInt(r, "page_size", 50, 200)
		status := r.URL.Query().Get("status")
		if status != "" && status != "all" && status != "success" && status != "error" {
			api.errorResponse(w, http.StatusBadRequest, "status must be all, success, or error")
			return
		}
		var since time.Time
		if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				api.errorResponse(w, http.StatusBadRequest, "since must be RFC3339")
				return
			}
			since = parsed.UTC()
		}
		logPage, err := api.db.ListRequestLogsPage(db.RequestLogQuery{
			Limit: pageSize, Offset: (page - 1) * pageSize,
			UserID: userID, KeyID: keyID,
			Search: strings.TrimSpace(r.URL.Query().Get("search")),
			Status: status, ModelID: r.URL.Query().Get("model"), Since: since,
		})
		if err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, logPage)
		return
	}
	list, err := api.db.ListRequestLogs(limit, offset, userID, keyID)
	if err != nil {
		api.dbErrorResponse(w, err)
		return
	}
	api.jsonResponse(w, http.StatusOK, list)
}

func (api *AdminAPI) handleLogSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if !validID(userID) {
		api.errorResponse(w, http.StatusBadRequest, "valid user_id is required")
		return
	}
	var since time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			api.errorResponse(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = parsed.UTC()
	}
	summary, err := api.db.GetRequestUsageSummary(userID, since)
	if err != nil {
		api.dbErrorResponse(w, err)
		return
	}
	api.jsonResponse(w, http.StatusOK, summary)
}

func (api *AdminAPI) handleUsageResets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	resets, err := api.db.ListUsageResets(positiveQueryInt(r, "limit", 100, 200))
	if err != nil {
		api.dbErrorResponse(w, err)
		return
	}
	api.jsonResponse(w, http.StatusOK, resets)
}

func positiveQueryInt(r *http.Request, name string, fallback, maximum int) int {
	raw := r.URL.Query().Get(name)
	parsed, err := strconv.Atoi(raw)
	if raw == "" || err != nil || parsed < 1 || parsed > maximum {
		return fallback
	}
	return parsed
}

func nonnegativeQueryInt(r *http.Request, name string, fallback, maximum int) int {
	raw := r.URL.Query().Get(name)
	parsed, err := strconv.Atoi(raw)
	if raw == "" || err != nil || parsed < 0 || parsed > maximum {
		return fallback
	}
	return parsed
}

// --- Helpers ---

// idPattern is the opaque-slug charset every server-generated ID in this file
// uses (user-<hex>, plan-<hex>, model-<hex>, budget-<hex>, log-<hex>,
// topup_<base36>, lowercased provider names). A client-chosen ID outside it
// could break out of the JS-string context the admin panel interpolates it
// into (onclick="fn('...')") - escaping the panel side does not help, since the
// HTML parser entity-decodes an attribute before the JS is compiled.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validID(s string) bool {
	return idPattern.MatchString(s)
}

// validName rejects control characters and the angle brackets that open an HTML
// tag; the admin panel renders these free-text fields into innerHTML.
func validName(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '<' || r == '>' {
			return false
		}
	}
	return true
}

// validEmail requires a syntactically real address (net/mail) and bans the
// angle brackets and control characters a stored-XSS payload would need. The
// addr.Address == s check rejects the display-name form ParseAddress accepts,
// e.g. "Alice <a@b.c>".
func validEmail(s string) bool {
	if s == "" || len(s) > 255 || strings.ContainsAny(s, "<>") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s
}

// validStatus enforces the enums the User/VirtualKey struct comments (and the
// DB CHECK constraints) already claim but the decode path never checked.
func validStatus(s string, allowed ...string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

func (api *AdminAPI) jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (api *AdminAPI) errorResponse(w http.ResponseWriter, status int, message string) {
	api.jsonResponse(w, status, map[string]string{"error": message})
}

// dbErrorResponse logs a database/internal error server-side and answers the
// caller.
//
// The full error still never reaches the wire — a raw err.Error() risks leaking
// query fragments. But a recognised Postgres failure gets a classified,
// actionable message instead of the generic line: this endpoint is
// admin-authenticated, and telling the one person who can fix a missing
// migration only "see the logs" costs a production debugging session to learn
// something the error already knew (see describeDatabaseError).
func (api *AdminAPI) dbErrorResponse(w http.ResponseWriter, err error) {
	log.Printf("[ADMIN-ERROR] %v", err)
	if message, classified := describeDatabaseError(err); classified {
		api.errorResponse(w, http.StatusInternalServerError, message)
		return
	}
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
		// A caller-supplied ID must satisfy the same charset every other
		// identifier does. This was the one CRUD handler that accepted one
		// verbatim, and the top-up ID is rendered into the admin panel — the
		// exact hazard the validID comment above describes.
		if t.ID == "" {
			t.ID = "topup_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		} else if !validID(t.ID) {
			api.errorResponse(w, http.StatusBadRequest, "invalid id")
			return
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
		if body.ID == "" || !validID(body.ID) {
			api.errorResponse(w, http.StatusBadRequest, "a valid id is required")
			return
		}
		if err := api.db.SetUserTopupExpiry(body.ID, body.ExpiresAt); err != nil {
			api.dbErrorResponse(w, err)
			return
		}
		api.jsonResponse(w, http.StatusOK, map[string]interface{}{"id": body.ID, "expires_at": body.ExpiresAt})

	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" || !validID(id) {
			api.errorResponse(w, http.StatusBadRequest, "a valid id is required")
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
