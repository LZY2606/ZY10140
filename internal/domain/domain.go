// Package domain holds the core data types shared by storage, derivation
// and transport layers.
package domain

import "time"

// Phase is the motion classification of an observed point.
type Phase string

const (
	PhaseAscent     Phase = "ascent"
	PhaseFloat      Phase = "float"
	PhaseDescent    Phase = "descent"
	PhaseTerminated Phase = "terminated"
)

// Packet is one sensor telegram reported by a radiosonde.
// Pointer scalars distinguish "missing observation" from a zero reading.
type Packet struct {
	SoundingID int64     `json:"sounding_id,omitempty"`
	Seq        int64     `json:"seq"`
	ObservedAt time.Time `json:"observed_at"`
	Pressure   *float64  `json:"pressure_hpa"`
	Temp       *float64  `json:"temp_c"`
	RH         *float64  `json:"rh_pct"`
	Lat        *float64  `json:"lat"`
	Lon        *float64  `json:"lon"`
	Alt        *float64  `json:"alt_m"`
	Status     string    `json:"status,omitempty"`
	Payload    string    `json:"payload_hash,omitempty"`
	ReceivedAt time.Time `json:"received_at,omitempty"`
	Late       bool      `json:"late,omitempty"`
}

// OverrideKind enumerates the manual decisions an analyst can record.
type OverrideKind string

const (
	OverridePhase   OverrideKind = "phase"
	OverrideIcing   OverrideKind = "icing"
	OverrideResolve OverrideKind = "resolve"
)

// Override is a signed manual decision. Manual and automatic layers are
// stored separately; overrides survive algorithm re-runs.
type Override struct {
	ID         int64        `json:"id,omitempty"`
	SoundingID int64        `json:"sounding_id"`
	Kind       OverrideKind `json:"kind"`
	Start      time.Time    `json:"start_time"`
	End        *time.Time   `json:"end_time,omitempty"`
	// Phase carries the analyst label for kind=phase.
	Phase *Phase `json:"phase,omitempty"`
	// Icing is true/false for kind=icing.
	Icing *bool `json:"icing,omitempty"`
	// Seq selects the duplicate sequence for kind=resolve.
	Seq *int64 `json:"seq,omitempty"`
	// Candidate selects the candidate id; -1 means keep both trajectories.
	Candidate *int64    `json:"candidate_id,omitempty"`
	Author    string    `json:"author"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Candidate is one of possibly several payloads seen for the same sequence.
type Candidate struct {
	ID        int64     `json:"candidate_id"`
	Seq       int64     `json:"seq"`
	FirstSeen time.Time `json:"first_seen"`
	Packet    Packet    `json:"packet"`
}

// Flag is a per-point quality marker with human/audit evidence.
type Flag struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	Severity string `json:"severity"` // info | warning | error
	Source   string `json:"source"`   // auto | manual
	Reason   string `json:"reason"`
}

// Point is the assembled, de-duplicated timeline record used for derivation.
type Point struct {
	Seq         int64     `json:"seq"`
	TrackID     int64     `json:"track_id"`
	Time        time.Time `json:"time"`
	Pressure    *float64  `json:"pressure_hpa"`
	Temp        *float64  `json:"temp_c"`
	RH          *float64  `json:"rh_pct"`
	Lat         *float64  `json:"lat"`
	Lon         *float64  `json:"lon"`
	Alt         *float64  `json:"alt_m"`
	Status      string    `json:"status,omitempty"`
	Phase       Phase     `json:"phase"`
	PhaseSource string    `json:"phase_source"` // auto | manual
	BranchID    int       `json:"branch_id"`
	Icing       bool      `json:"icing"`
	IcingSource string    `json:"icing_source"` // auto | manual | ""
	GPSGap      bool      `json:"gps_gap"`
	Reversal    bool      `json:"reversal"`
	Conflict    bool      `json:"conflict"`
	CandidateID int64     `json:"candidate_id"`
	KeptBoth    bool      `json:"kept_both"`
	Late        bool      `json:"late"`
	Flags       []Flag    `json:"flags"`
}

// Branch is a maximal run of points sharing a motion phase.
type Branch struct {
	ID        int       `json:"id"`
	GroupID   int       `json:"group_id"`
	Kind      Phase     `json:"kind"`
	StartSeq  int64     `json:"start_seq"`
	EndSeq    int64     `json:"end_seq"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Source    string    `json:"source"`
}

// Transition records why a new branch started.
type Transition struct {
	FromSeq int64     `json:"from_seq"`
	ToSeq   int64     `json:"to_seq"`
	From    Phase     `json:"from"`
	To      Phase     `json:"to"`
	Reason  string    `json:"reason"`
	Source  string    `json:"source"`
	Time    time.Time `json:"time"`
}

// Level is one standard-pressure level derived from a single branch.
type Level struct {
	Pressure  float64        `json:"pressure_hpa"`
	BranchID  int            `json:"branch_id"`
	Kind      Phase          `json:"kind"`
	Primary   bool           `json:"primary"`
	Alt       *float64       `json:"alt_m"`
	Temp      *float64       `json:"temp_c"`
	RH        *float64       `json:"rh_pct"`
	Lat       *float64       `json:"lat"`
	Lon       *float64       `json:"lon"`
	Time      *time.Time     `json:"time"`
	Exact     bool           `json:"exact"`
	Ambiguous bool           `json:"ambiguous"`
	Missing   []MissingField `json:"missing,omitempty"`
}

// MissingField explains why a scalar could not be derived at a level.
type MissingField struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
