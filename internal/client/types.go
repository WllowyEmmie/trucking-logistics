package client

import "time"

type Truck struct {
	ID          string     `json:"id"`
	PlateNumber string     `json:"plate_number"`
	Model       *string    `json:"model"`
	Status      string     `json:"status"`
	DriverID    *string    `json:"driver_id"`
	Lng         *float64   `json:"lng"`
	Lat         *float64   `json:"lat"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
}

type Driver struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

type StopInput struct {
	StopType     string  `json:"stop_type"`
	Address      string  `json:"address"`
	Lat          float64 `json:"lat"`
	Lng          float64 `json:"lng"`
	ContactName  *string `json:"contact_name,omitempty"`
	ContactPhone *string `json:"contact_phone,omitempty"`
}

type CreateDeliveryInput struct {
	CustomerName  string      `json:"customer_name"`
	CustomerPhone *string     `json:"customer_phone,omitempty"`
	GoodsDesc     *string     `json:"goods_desc,omitempty"`
	Stops         []StopInput `json:"stops"`
}

type CreateDeliveryResult struct {
	Delivery struct {
		ID            string `json:"id"`
		Reference     string `json:"reference"`
		TrackingToken string `json:"tracking_token"`
		Status        string `json:"status"`
	} `json:"delivery"`
	Stops []struct {
		ID       string `json:"id"`
		Sequence int    `json:"sequence"`
		StopType string `json:"stop_type"`
	} `json:"stops"`
}

type Ping struct {
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	RecordedAt time.Time `json:"recorded_at"`
	SpeedKph   *float32  `json:"speed_kph,omitempty"`
	AccuracyM  *float32  `json:"accuracy_m,omitempty"`
}
type DeliveryListItem struct {
	ID           string    `json:"id"`
	Reference    string    `json:"reference"`
	Status       string    `json:"status"`
	CustomerName string    `json:"customer_name"`
	TruckID      *string   `json:"truck_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type Stop struct {
	ID          string     `json:"id"`
	Sequence    int        `json:"sequence"`
	StopType    string     `json:"stop_type"`
	Address     string     `json:"address"`
	Lng         *float64   `json:"lng"`
	Lat         *float64   `json:"lat"`
	Status      string     `json:"status"`
	ArrivedAt   *time.Time `json:"arrived_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type DeliveryDetail struct {
	Delivery struct {
		ID          string   `json:"id"`
		Reference   string   `json:"reference"`
		Status      string   `json:"status"`
		TruckID     *string  `json:"truck_id"`
		PlateNumber *string  `json:"plate_number"`
		TruckLng    *float64 `json:"truck_lng"`
		TruckLat    *float64 `json:"truck_lat"`
	} `json:"delivery"`
	Stops []Stop `json:"stops"`
}
