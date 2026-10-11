package client

import "context"

func (c *Client) ListTrucks(ctx context.Context) ([]Truck, error) {
	var out []Truck
	return out, c.Get(ctx, "/trucks", &out)
}

func (c *Client) CreateTruck(ctx context.Context, plate string, model *string) (Truck, error) {
	var out Truck
	body := map[string]any{"plate_number": plate, "model": model}
	return out, c.Post(ctx, "/trucks", body, &out)
}

func (c *Client) CreateDriver(ctx context.Context, name, phone string) (Driver, error) {
	var out Driver
	body := map[string]string{"name": name, "phone": phone}
	return out, c.Post(ctx, "/drivers", body, &out)
}

func (c *Client) CreateDelivery(ctx context.Context, in CreateDeliveryInput) (CreateDeliveryResult, error) {
	var out CreateDeliveryResult
	return out, c.Post(ctx, "/deliveries", in, &out)
}
func (c *Client) ListDeliveries(ctx context.Context, status string) ([]DeliveryListItem, error) {
	var out []DeliveryListItem
	path := "/deliveries"
	if status != "" {
		path += "?status=" + status
	}
	return out, c.Get(ctx, path, &out)
}

func (c *Client) GetDelivery(ctx context.Context, id string) (DeliveryDetail, error) {
	var out DeliveryDetail
	return out, c.Get(ctx, "/deliveries/"+id, &out)
}

func (c *Client) CompleteDelivery(ctx context.Context, deliveryID string) error {
	return c.Post(ctx, "/deliveries/"+deliveryID+"/complete", nil, nil)
}
func (c *Client) CancelDelivery(ctx context.Context, deliveryID string) error {
	return c.Post(ctx, "/deliveries/"+deliveryID+"/cancel", nil, nil)
}

func (c *Client) FailDelivery(ctx context.Context, deliveryID, reason string) error {
	return c.Post(ctx, "/deliveries/"+deliveryID+"/fail", map[string]string{"reason": reason}, nil)
}

func (c *Client) AssignTruck(ctx context.Context, deliveryID, truckID string) error {
	return c.Post(ctx, "/deliveries/"+deliveryID+"/assign", map[string]string{"truck_id": truckID}, nil)
}

func (c *Client) StartDelivery(ctx context.Context, deliveryID string) error {
	return c.Post(ctx, "/deliveries/"+deliveryID+"/start", nil, nil)
}

func (c *Client) ArriveAtStop(ctx context.Context, deliveryID, stopID string) error {
	return c.Post(ctx, "/deliveries/"+deliveryID+"/stops/"+stopID+"/arrive", nil, nil)
}

func (c *Client) CompleteStop(ctx context.Context, deliveryID, stopID string) error {
	return c.Post(ctx, "/deliveries/"+deliveryID+"/stops/"+stopID+"/complete", nil, nil)
}
func (c *Client) UpdateTruck(ctx context.Context, truckID string, driverID, status *string) error {
	body := map[string]any{}
	if driverID != nil {
		body["driver_id"] = *driverID
	}
	if status != nil {
		body["status"] = *status
	}
	return c.Patch(ctx, "/trucks/"+truckID, body, nil)
}
func (c *Client) PostLocations(ctx context.Context, truckID string, pings []Ping) error {
	body := map[string]any{"truck_id": truckID, "pings": pings}
	return c.Post(ctx, "/locations", body, nil)
}
