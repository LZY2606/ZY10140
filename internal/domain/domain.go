// Package domain defines the core data model shared by ingestion, assembly,
// profiling, storage and the HTTP API.
package domain

import (
	"encoding/json"
	"time"
)

// DeviceSerial is the radiosonde/transmitter serial number. Packets are
// deduplicated within a sounding by (DeviceSerial, Seq).
type DeviceSerial = string

// Phase is the motion state assigned to a run of observations.
type Phase string

const (
	PhaseAscent    Phase = "ascent"
	PhaseFloat     Phase = "float"
	PhaseDescent   Phase = "descent"
	PhaseTerminate Phase = "terminate"
)

func (p Phase) Valid() bool {
	switch p {
	case PhaseAscent, PhaseFloat, PhaseDescent, PhaseTerminate:
		return true
	}
	return false
}

// Packet as received from the instrument. Missing measurements are nil.
// ReceivedAt is ingestion arrival time, distinct from onboard ObsTime.
type Packet struct {
	Device    DeviceSerial `json:"device"`
	Seq       int          `json:"seq"`
	ObsTime   time.Time    `json:"obs_time"`
	Pressure  *float64     `json:"pressure_hpa"`
	Temp      *float64     `json:"temp_c"`
	RH        *float64     `json:"rh_pct"`
	Lat       *float64     `json:"lat"`
	Lon       *float64     `json:"lon"`
	AltGPS    *float64     `json:"alt_gps_m"`
	Status    string       `json:"status"`
	PayloadID string       `json:"payload_id,omitempty"`
}

// CanonicalPayload renders the measurement payload for content hashing.
// Two rows with the same (device, seq) and identical canonical JSON are
// retries; differing canonical JSON are conflicts.
func (p Packet) CanonicalPayload() []byte {
	b, _ := json.Marshal(struct {
		Device    string    `json:"device"`
		Seq       int       `json:"seq"`
		ObsTime   time.Time `json:"obs_time"`
		Pressure  *float64  `json:"pressure_hpa"`
		Temp      *float64  `json:"temp_c"`
		RH        *float64  `json:"rh_pct"`
		Lat       *float64  `json:"lat"`
		Lon       *float64  `json:"lon"`
		AltGPS    *float64  `json:"alt_gps_m"`
		Status    string    `json:"status"`
		PayloadID string    `json:"payload_id"`
	}{
		Device: p.Device, Seq: p.Seq, ObsTime: p.ObsTime.UTC().Truncate(time.Second),
		Pressure: p.Pressure, Temp: p.Temp, RH: p.RH, Lat: p.Lat, Lon: p.Lon,
		AltGPS: p.AltGPS, Status: p.Status, PayloadID: p.PayloadID,
	})
	return b
}

// DupKind classifies a newly received packet against an existing seq.
type DupKind string

const (
	DupFirst    DupKind = "first"    // no previous packet with this seq
	DupRetry    DupKind = "retry"    // same seq, byte-identical payload
	DupConflict DupKind = "conflict" // same seq, different payload
)

// Finding is one automatic (algorithm-produced) annotation attached to a
// version. Manual judgments live in a separate table and take precedence.
type Finding struct {
	ID        int64     `json:"id,omitempty"`
	VersionID int64     `json:"version_id,omitempty"`
	Kind      string    `json:"kind"` // descent, icing, gps_gap, pressure_reversal, burst, terminate, dup, conflict, late
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Detail    string    `json:"detail,omitempty"`
	Evidence  string    `json:"evidence,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Judgment is a human-signed decision. Judgments survive re-processing: a
// new algorithm run only rewrites drafts and never published profiles or
// judgments.
type Judgment struct {
	ID           int64      `json:"id,omitempty"`
	SoundingID   string     `json:"sounding_id"`
	Kind         string     `json:"kind"` // phase_override, icing, variant_choice, retain_both
	Start        *time.Time `json:"start,omitempty"`
	End          *time.Time `json:"end,omitempty"`
	Phase        Phase      `json:"phase,omitempty"`       // for phase_override
	Device       string     `json:"device,omitempty"`      // scope
	Seq          *int       `json:"seq,omitempty"`         // for variant_choice
	ChosenHash   string     `json:"chosen_hash,omitempty"` // variant_choice
	Reason       string     `json:"reason,omitempty"`
	SignedBy     string     `json:"signed_by"`
	Rev          int        `json:"rev"`
	CreatedAt    time.Time  `json:"created_at,omitempty"`
	SupersededAt *time.Time `json:"superseded_at,omitempty"`
}

// Point is an assembled observation on a candidate trajectory.
type Point struct {
	VersionID int64      `json:"version_id,omitempty"`
	Device    string     `json:"device"`
	Seq       int        `json:"seq"`
	ObsTime   time.Time  `json:"obs_time"`
	Pressure  *float64   `json:"pressure_hpa,omitempty"`
	Temp      *float64   `json:"temp_c,omitempty"`
	RH        *float64   `json:"rh_pct,omitempty"`
	Lat       *float64   `json:"lat,omitempty"`
	Lon       *float64   `json:"lon,omitempty"`
	AltGPS    *float64   `json:"alt_gps_m,omitempty"`
	Status    string     `json:"status,omitempty"`
	Hash      string     `json:"hash,omitempty"`
	OrderIdx  int        `json:"order_idx"`
	BranchID  int        `json:"branch_id"`
	Phase     Phase      `json:"phase"`
	Flags     StringList `json:"flags,omitempty"`
}

// StringList is a JSON text array column.
type StringList []string

// Layer is one standard-pressure level derived for one branch.
type Layer struct {
	VersionID    int64      `json:"version_id"`
	BranchID     int        `json:"branch_id"`
	Phase        Phase      `json:"phase"`
	Pressure     float64    `json:"pressure_hpa"`
	ObsTime      *time.Time `json:"obs_time,omitempty"` // set when exact observation used
	Temp         *float64   `json:"temp_c,omitempty"`
	RH           *float64   `json:"rh_pct,omitempty"`
	AltGPS       *float64   `json:"alt_gps_m,omitempty"`
	Lat          *float64   `json:"lat,omitempty"`
	Lon          *float64   `json:"lon,omitempty"`
	Exact        bool       `json:"exact"`
	Interpolated bool       `json:"interpolated"`
	Flags        StringList `json:"flags,omitempty"`
	Missing      string     `json:"missing_reason,omitempty"`
}

// Published layers carry a publish record; the combination is signed.
type PublishRecord struct {
	SoundingID  string    `json:"sounding_id"`
	VersionID   int64     `json:"version_id"`
	PublishedAt time.Time `json:"published_at"`
	PublishedBy string    `json:"published_by"`
	LayerCount  int       `json:"layer_count"`
	Note        string    `json:"note,omitempty"`
}
