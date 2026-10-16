package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

var validProposalStatuses = map[string]bool{
	"pending":  true,
	"approved": true,
	"rejected": true,
	"expired":  true,
}

type proposalAction struct {
	Type       string `json:"type"`
	DeliveryID string `json:"delivery_id"`
	TruckID    string `json:"truck_id"`
}
type proposalResponse struct {
	db.Proposal
	Action  json.RawMessage `json:"action"`
	Changes json.RawMessage `json:"changes"`
}

func asProposalResponse(p db.Proposal) proposalResponse {
	return proposalResponse{Proposal: p, Action: p.Action, Changes: p.Changes}
}

func asProposalResponses(ps []db.Proposal) []proposalResponse {
	out := make([]proposalResponse, 0, len(ps))
	for _, p := range ps {
		out = append(out, asProposalResponse(p))
	}
	return out
}

// TODO(auth): no sessions yet. Auto mode passes "agent" instead.
func decidedBy(_ *http.Request) *string {
	by := "dispatcher"
	return &by
}

func (s *Server) listProposals(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		// The console only ever wants the queue.
		status = "pending"
	}
	if !validProposalStatuses[status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid proposal status"})
		return
	}

	proposals, err := s.queries.ListProposalsByStatus(r.Context(), status)
	if err != nil {
		s.logger.Error("list proposals", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list proposals"})
		return
	}
	writeJSON(w, http.StatusOK, asProposalResponses(proposals))
}

type createProposalInput struct {
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Why        string          `json:"why"`
	DeliveryID *string         `json:"delivery_id"`
	TruckID    *string         `json:"truck_id"`
	Action     json.RawMessage `json:"action"`
	Changes    json.RawMessage `json:"changes"`
}

func (s *Server) createProposal(w http.ResponseWriter, r *http.Request) {
	var body createProposalInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	if msg := validateProposalInput(body); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	deliveryID, err := optionalUUID(body.DeliveryID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}
	truckID, err := optionalUUID(body.TruckID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid truck id"})
		return
	}

	changes := body.Changes
	if len(changes) == 0 {
		changes = json.RawMessage("[]")
	}

	created, err := s.queries.CreateProposal(r.Context(), db.CreateProposalParams{
		Kind:       body.Kind,
		Title:      body.Title,
		Why:        body.Why,
		DeliveryID: deliveryID,
		TruckID:    truckID,
		Action:     body.Action,
		Changes:    changes,
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "the delivery or truck this proposal refers to does not exist",
			})
			return
		}
		s.logger.Error("create proposal", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create proposal"})
		return
	}

	writeJSON(w, http.StatusCreated, asProposalResponse(created))
}

// A change must carry something to execute; a flag must not, or a dispatcher
// would be approving nothing. The table enforces this too — this is so the
// agent gets a readable 400 rather than a constraint error.
func validateProposalInput(body createProposalInput) string {
	if body.Kind != "change" && body.Kind != "flag" {
		return "kind must be change or flag"
	}
	if body.Title == "" {
		return "title is required"
	}
	if body.Why == "" {
		return "why is required — a proposal without reasoning cannot be judged"
	}

	hasAction := len(body.Action) > 0 && string(body.Action) != "null"
	if body.Kind == "change" && !hasAction {
		return "a change proposal needs an action"
	}
	if body.Kind == "flag" && hasAction {
		return "a flag has nothing to execute and must not carry an action"
	}

	if hasAction {
		var act proposalAction
		if err := json.Unmarshal(body.Action, &act); err != nil {
			return "action is not valid json"
		}
		if act.Type != "assign" {
			return "action type must be assign"
		}
		if _, err := uuid.Parse(act.DeliveryID); err != nil {
			return "action.delivery_id must be a uuid"
		}
		if _, err := uuid.Parse(act.TruckID); err != nil {
			return "action.truck_id must be a uuid"
		}
	}
	return ""
}

func optionalUUID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (s *Server) approveProposal(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid proposal id"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not approve proposal"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	p, err := qtx.GetProposalForUpdate(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "proposal not found"})
		return
	}
	if err != nil {
		s.logger.Error("get proposal", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not approve proposal"})
		return
	}
	if p.Status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "proposal is already " + p.Status})
		return
	}
	if p.Kind != "change" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a flag has nothing to approve"})
		return
	}

	var act proposalAction
	if err := json.Unmarshal(p.Action, &act); err != nil {
		s.logger.Error("decode proposal action", "err", err, "proposal", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not approve proposal"})
		return
	}

	switch act.Type {
	case "assign":
		deliveryID, err1 := uuid.Parse(act.DeliveryID)
		truckID, err2 := uuid.Parse(act.TruckID)
		if err1 != nil || err2 != nil {
			s.logger.Error("proposal action has bad ids", "proposal", id)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not approve proposal"})
			return
		}

		if _, err := s.assignTruckTx(r.Context(), qtx, deliveryID, truckID); err != nil {
			// The world moved while the proposal sat in the queue. The
			// proposal is stale, not broken: retire it and say why.
			var ae apiError
			if errors.As(err, &ae) && ae.status == http.StatusConflict {
				// Release the FOR UPDATE lock on the proposal first: expiring
				// it outside this transaction would otherwise wait on it.
				// The deferred Rollback still runs at return and gets
				// ErrTxClosed, which it ignores. That's intended, not a leak.
				tx.Rollback(r.Context())
				if err := s.expireProposal(r.Context(), id, ae.message); err != nil {
					s.logger.Error("expire proposal", "err", err)
				}
				writeJSON(w, http.StatusConflict, map[string]string{"error": ae.message})
				return
			}
			s.respondErr(w, err, "approve proposal", "could not approve proposal")
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action type: " + act.Type})
		return
	}

	decided, err := qtx.DecideProposal(r.Context(), db.DecideProposalParams{
		ID:        id,
		Status:    "approved",
		DecidedBy: decidedBy(r),
	})
	if err != nil {
		s.logger.Error("decide proposal", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not approve proposal"})
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not approve proposal"})
		return
	}

	writeJSON(w, http.StatusOK, asProposalResponse(decided))
}

func (s *Server) rejectProposal(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid proposal id"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not reject proposal"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	p, err := qtx.GetProposalForUpdate(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "proposal not found"})
		return
	}
	if err != nil {
		s.logger.Error("get proposal", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not reject proposal"})
		return
	}
	if p.Status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "proposal is already " + p.Status})
		return
	}

	decided, err := qtx.DecideProposal(r.Context(), db.DecideProposalParams{
		ID:        id,
		Status:    "rejected",
		DecidedBy: decidedBy(r),
	})
	if err != nil {
		s.logger.Error("decide proposal", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not reject proposal"})
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not reject proposal"})
		return
	}

	writeJSON(w, http.StatusOK, asProposalResponse(decided))
}

// expireProposal runs outside the caller's transaction, because that
// transaction is about to roll back and this write must survive it.
func (s *Server) expireProposal(ctx context.Context, id uuid.UUID, reason string) error {
	by := "system: " + reason
	_, err := s.queries.DecideProposal(ctx, db.DecideProposalParams{
		ID:        id,
		Status:    "expired",
		DecidedBy: &by,
	})
	return err
}
