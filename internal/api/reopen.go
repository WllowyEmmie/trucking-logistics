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

// How a failed delivery goes out again. Which make sense depends on where
// the goods are (see goodsOnTruck):
//
//	resume   the stops still to do, on a truck. Goods still at the pickup:
//	         any truck ("reassign"). Goods on the truck: that same truck
//	         ("try again", e.g. the customer wasn't in).
//	handover goods on a truck that can't go on (breakdown): another truck
//	         collects them from where it stopped, then does the rest.
//	restart  every stop again from the pickup, on any truck (goods damaged).
const (
	reopenResume   = "resume"
	reopenHandover = "handover"
	reopenRestart  = "restart"
)

var validReopenModes = map[string]bool{reopenResume: true, reopenHandover: true, reopenRestart: true}

// reopenDelivery is POST /deliveries/{id}/reopen {mode, truck_id}
// (dispatchers). One transaction: if the truck is refused, the delivery
// stays failed.
func (s *Server) reopenDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}
	var body struct {
		Mode    string `json:"mode"`
		TruckID string `json:"truck_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !validReopenModes[body.Mode] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be resume, handover or restart"})
		return
	}
	truckID, err := uuid.Parse(body.TruckID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose a truck"})
		return
	}

	tx, err := s.beginTx(r.Context())
	if err != nil {
		s.respondErr(w, err, "begin tx", "could not send the delivery out again")
		return
	}
	defer tx.Rollback(r.Context())

	updated, err := s.reopenTx(r.Context(), s.queries.WithTx(tx), deliveryID, truckID, body.Mode, actorName(r))
	if err != nil {
		s.respondErr(w, err, "reopen delivery", "could not send the delivery out again")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.respondErr(w, err, "commit", "could not send the delivery out again")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// reopenTx applies the rules inside a caller's transaction. The endpoint
// and an approved agent proposal both go through here.
func (s *Server) reopenTx(ctx context.Context, qtx *db.Queries, deliveryID, truckID uuid.UUID, mode, actor string) (db.AssignTruckRow, error) {
	var zero db.AssignTruckRow
	if !validReopenModes[mode] {
		return zero, apiError{status: http.StatusBadRequest, message: "mode must be resume, handover or restart"}
	}
	d, err := qtx.GetDeliveryForUpdate(ctx, deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, errNotFound("delivery not found")
	}
	if err != nil {
		return zero, err
	}
	if d.Status != "failed" {
		return zero, errConflictCode(codeWrongStatus, "delivery is "+d.Status+"; only a failed delivery can be sent out again")
	}
	stops, err := qtx.ListStopsForDelivery(ctx, deliveryID)
	if err != nil {
		return zero, err
	}

	var oldPlate string
	var oldDriver, oldPhone *string
	if d.TruckID != nil {
		if oldPlate, oldDriver, oldPhone, err = qtx.TruckPlateAndDriver(ctx, *d.TruckID); err != nil {
			return zero, err
		}
	}
	loaded := goodsOnTruck(stops)
	sameTruck := d.TruckID != nil && *d.TruckID == truckID

	var note string
	switch mode {
	case reopenResume:
		if loaded && !sameTruck {
			return zero, errConflict("the goods are on " + oldPlate + ": send " + oldPlate + " again, or hand them over to another truck")
		}
		note = "reassigned"
		if loaded {
			note = "trying again"
		}
		err = qtx.ResetStops(ctx, deliveryID, false)
	case reopenHandover:
		if !loaded || d.TruckID == nil {
			return zero, errConflict("nothing has been collected yet, so there's nothing to hand over: reassign it instead")
		}
		if sameTruck {
			return zero, errConflict("a handover goes to a different truck than " + oldPlate)
		}
		var ok bool
		ok, err = qtx.InsertHandoverStop(ctx, deliveryID, firstOpen(stops), *d.TruckID, "Handover from "+oldPlate, oldDriver, oldPhone)
		if err == nil && !ok {
			return zero, errConflict(oldPlate + " has never reported a position, so there's no handover point: start over instead")
		}
		if err == nil {
			err = qtx.ResetStops(ctx, deliveryID, false)
		}
		note = "handover from " + oldPlate
	case reopenRestart:
		note = "starting over from the pickup"
		err = qtx.ResetStops(ctx, deliveryID, true)
	}
	if err != nil {
		return zero, err
	}
	if err := qtx.ClearDeliveryTruck(ctx, deliveryID); err != nil {
		return zero, err
	}
	note += " · by " + actor
	if err := qtx.CreateDeliveryEvent(ctx, db.CreateDeliveryEventParams{DeliveryID: deliveryID, EventType: "reopened", Note: &note}); err != nil {
		return zero, err
	}
	return s.assignTruckTx(ctx, qtx, deliveryID, truckID)
}

// goodsOnTruck: a pickup was collected and there's still a stop to do, so
// the goods are on the truck that had the delivery.
func goodsOnTruck(stops []db.StopRow) bool {
	collected, open := false, false
	for _, s := range stops {
		if s.StopType == "pickup" && s.Status == "completed" {
			collected = true
		}
		if s.Status == "pending" || s.Status == "arrived" {
			open = true
		}
	}
	return collected && open
}

// firstOpen is the sequence of the first stop still to do.
func firstOpen(stops []db.StopRow) int32 {
	for _, s := range stops {
		if s.Status == "pending" || s.Status == "arrived" {
			return s.Sequence
		}
	}
	return int32(len(stops) + 1)
}
