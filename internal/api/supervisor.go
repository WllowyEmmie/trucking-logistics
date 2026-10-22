package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

// Auto mode: the agent acts instead of proposing. It can do exactly two
// things, and only while auto mode is on in Settings:
//
//   - pair: give a driver who is on shift and has no truck a free truck
//     with no driver;
//   - assign: give a pending delivery to an available truck whose driver
//     is on shift.
//
// Both are checked here inside one transaction, whatever the agent thinks
// it saw, and each is recorded with the agent's reason so the console can
// list it and a dispatcher can undo it.

type supervisorInput struct {
	DriverID   string `json:"driver_id"`
	TruckID    string `json:"truck_id"`
	DeliveryID string `json:"delivery_id"`
	Reason     string `json:"reason"`
	// "model" or "rule".
	DecidedBy string `json:"decided_by"`
}

func (in *supervisorInput) check() error {
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len(in.Reason) > 500 {
		return errBadRequest("a reason of 1 to 500 characters is required")
	}
	if in.DecidedBy != "model" && in.DecidedBy != "rule" {
		return errBadRequest(`decided_by must be "model" or "rule"`)
	}
	return nil
}

func parseID(raw, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, errBadRequest("invalid " + what + " id")
	}
	return id, nil
}

// supervise runs fn in a transaction, but only in auto mode.
func (s *Server) supervise(w http.ResponseWriter, r *http.Request, op string,
	fn func(ctx context.Context, qtx *db.Queries, in supervisorInput) (db.SupervisorAction, error)) {
	if !s.st().AutoAssign {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "auto mode is off in Settings", "code": codeAutoModeOff})
		return
	}
	var in supervisorInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := in.check(); err != nil {
		s.respondErr(w, err, op, "could not "+op)
		return
	}
	tx, err := s.beginTx(r.Context())
	if err != nil {
		s.respondErr(w, err, "begin tx", "could not "+op)
		return
	}
	defer tx.Rollback(r.Context())
	action, err := fn(r.Context(), s.queries.WithTx(tx), in)
	if err != nil {
		s.respondErr(w, err, op, "could not "+op)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.respondErr(w, err, "commit tx", "could not "+op)
		return
	}
	s.logger.Info("supervisor", "kind", action.Kind, "summary", action.Summary, "by", action.DecidedBy)
	writeJSON(w, http.StatusOK, action)
}

// supervisorPair is POST /supervisor/pair {driver_id, truck_id, reason, decided_by}.
func (s *Server) supervisorPair(w http.ResponseWriter, r *http.Request) {
	s.supervise(w, r, "give the driver a truck", func(ctx context.Context, qtx *db.Queries, in supervisorInput) (db.SupervisorAction, error) {
		var zero db.SupervisorAction
		driverID, err := parseID(in.DriverID, "driver")
		if err != nil {
			return zero, err
		}
		truckID, err := parseID(in.TruckID, "truck")
		if err != nil {
			return zero, err
		}
		// Lock the driver first: two pairings of one driver queue up here.
		name, err := qtx.LockDriver(ctx, driverID)
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, errNotFound("driver not found")
		}
		if err != nil {
			return zero, err
		}
		if since, err := qtx.OpenShift(ctx, driverID); err != nil {
			return zero, err
		} else if since == nil {
			return zero, errConflictCode(codeDriverOffShift, name+" is off shift")
		}
		if plate, err := qtx.TruckOfDriver(ctx, driverID); err != nil {
			return zero, err
		} else if plate != "" {
			return zero, errConflict(name + " already drives " + plate)
		}
		truck, err := qtx.GetTruckForUpdate(ctx, truckID)
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, errNotFound("truck not found")
		}
		if err != nil {
			return zero, err
		}
		if truck.Status != "available" {
			return zero, errConflictCode(codeTruckBusy, "truck "+truck.PlateNumber+" is "+truck.Status)
		}
		if truck.DriverID != nil {
			return zero, errConflict("truck " + truck.PlateNumber + " already has a driver")
		}
		if err := qtx.SetTruckDriver(ctx, truckID, driverID); err != nil {
			return zero, err
		}
		return qtx.AddSupervisorAction(ctx, db.NewSupervisorAction{
			Kind: "pair", DriverID: &driverID, TruckID: &truckID,
			Summary:   fmt.Sprintf("Gave %s to %s", truck.PlateNumber, name),
			Reason:    in.Reason,
			DecidedBy: in.DecidedBy,
		})
	})
}

// supervisorAssign is POST /supervisor/assign {delivery_id, truck_id, reason, decided_by}.
func (s *Server) supervisorAssign(w http.ResponseWriter, r *http.Request) {
	s.supervise(w, r, "assign the delivery", func(ctx context.Context, qtx *db.Queries, in supervisorInput) (db.SupervisorAction, error) {
		var zero db.SupervisorAction
		deliveryID, err := parseID(in.DeliveryID, "delivery")
		if err != nil {
			return zero, err
		}
		truckID, err := parseID(in.TruckID, "truck")
		if err != nil {
			return zero, err
		}
		// assignTruckTx locks the delivery then the truck (the order every
		// assign uses), and checks it's pending and the truck available with
		// a driver. Auto mode also wants that driver working: checked after,
		// and the whole thing rolls back if not.
		updated, err := s.assignTruckTx(ctx, qtx, deliveryID, truckID)
		if err != nil {
			return zero, err
		}
		truck, err := qtx.GetTruckForUpdate(ctx, truckID)
		if err != nil {
			return zero, err
		}
		if since, err := qtx.OpenShift(ctx, *truck.DriverID); err != nil {
			return zero, err
		} else if since == nil {
			return zero, errConflictCode(codeDriverOffShift, "the driver of "+truck.PlateNumber+" is off shift")
		}
		note := "automatically: " + in.Reason
		if err := qtx.CreateDeliveryEvent(ctx, db.CreateDeliveryEventParams{
			DeliveryID: deliveryID, EventType: "auto_assigned", Note: &note,
		}); err != nil {
			return zero, err
		}
		if err := qtx.ExpirePendingProposals(ctx, deliveryID); err != nil {
			return zero, err
		}
		name, err := qtx.DriverName(ctx, *truck.DriverID)
		if err != nil {
			return zero, err
		}
		summary := fmt.Sprintf("Assigned %s to %s", updated.Reference, truck.PlateNumber)
		if name != "" {
			summary += " (" + name + ")"
		}
		return qtx.AddSupervisorAction(ctx, db.NewSupervisorAction{
			Kind: "assign", DriverID: truck.DriverID, TruckID: &truckID, DeliveryID: &deliveryID,
			Summary: summary, Reason: in.Reason, DecidedBy: in.DecidedBy,
		})
	})
}

// supervisorActions is GET /supervisor/actions?limit=: what the agent did, newest first.
func (s *Server) supervisorActions(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	out, err := s.queries.RecentSupervisorActions(r.Context(), limit)
	if err != nil {
		s.respondErr(w, err, "list supervisor actions", "could not list what the agent did")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func errBadRequest(msg string) error { return apiError{status: http.StatusBadRequest, message: msg} }
