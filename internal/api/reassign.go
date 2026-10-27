package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

// POST /deliveries/{id}/reassign {"truck_id"}: move an assigned delivery
// that hasn't started to another truck, in one step. The old truck is
// freed, the new one goes on_delivery, and the history says from which to
// which. Once the driver has pressed Start the goods may be on board: that
// is a failure and a handover (reopen), not a reassignment.
func (s *Server) reassignDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}
	var body struct {
		TruckID string `json:"truck_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	truckID, err := uuid.Parse(body.TruckID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "truck_id must be a truck's id"})
		return
	}

	tx, err := s.beginTx(r.Context())
	if err != nil {
		s.respondErr(w, err, "begin tx", "could not reassign the delivery")
		return
	}
	defer tx.Rollback(r.Context())
	updated, err := s.reassignTx(r.Context(), s.queries.WithTx(tx), deliveryID, truckID)
	if err != nil {
		s.respondErr(w, err, "reassign", "could not reassign the delivery")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.respondErr(w, err, "commit", "could not reassign the delivery")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) reassignTx(ctx context.Context, qtx *db.Queries, deliveryID, truckID uuid.UUID) (db.AssignTruckRow, error) {
	var zero db.AssignTruckRow
	d, err := qtx.GetDeliveryForUpdate(ctx, deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, errNotFound("delivery not found")
	}
	if err != nil {
		return zero, err
	}
	switch d.Status {
	case "assigned":
	case "pending":
		return zero, errConflictCode(codeWrongStatus, "delivery is pending: assign it instead")
	case "in_transit":
		return zero, errConflictCode(codeWrongStatus, "delivery has started: report a failure and hand it over instead")
	default:
		return zero, errConflictCode(codeWrongStatus, "delivery is "+d.Status+" and cannot be reassigned")
	}
	if d.TruckID == nil {
		return zero, errConflictCode(codeWrongStatus, "delivery has no truck to move it from")
	}
	if *d.TruckID == truckID {
		return zero, errConflictCode(codeWrongStatus, "delivery is already on that truck")
	}

	// Lock both trucks in a fixed order, so two reassignments swapping the
	// same trucks can't deadlock.
	first, second := *d.TruckID, truckID
	if bytes.Compare(first[:], second[:]) > 0 {
		first, second = second, first
	}
	locked := map[uuid.UUID]db.GetTruckForUpdateRow{}
	for _, id := range []uuid.UUID{first, second} {
		t, err := qtx.GetTruckForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, errNotFound("truck not found")
		}
		if err != nil {
			return zero, err
		}
		locked[id] = t
	}
	oldTruck, newTruck := locked[*d.TruckID], locked[truckID]
	if newTruck.Status != "available" {
		return zero, errConflictCode(codeTruckBusy, "truck "+newTruck.PlateNumber+" is "+newTruck.Status)
	}
	if newTruck.DriverID == nil {
		return zero, errConflictCode(codeTruckNoDriver, "truck "+newTruck.PlateNumber+" has no driver; give it a driver in Fleet first")
	}

	updated, err := qtx.AssignTruck(ctx, db.AssignTruckParams{ID: deliveryID, TruckID: &truckID})
	if err != nil {
		return zero, err
	}
	if err := qtx.SetTruckStatus(ctx, db.SetTruckStatusParams{ID: truckID, Status: "on_delivery"}); err != nil {
		return zero, err
	}
	// The old truck had only this delivery (one at a time): it's free again.
	if oldTruck.Status == "on_delivery" {
		if err := qtx.SetTruckStatus(ctx, db.SetTruckStatusParams{ID: oldTruck.ID, Status: "available"}); err != nil {
			return zero, err
		}
	}
	// "assigned to <plate>" at the end, as assign writes it: history and
	// reports read the plate from there.
	note := "from " + oldTruck.PlateNumber + "; assigned to " + newTruck.PlateNumber
	if err := qtx.CreateDeliveryEvent(ctx, db.CreateDeliveryEventParams{
		DeliveryID: deliveryID, EventType: "reassigned", Note: &note,
	}); err != nil {
		return zero, err
	}
	return updated, nil
}
