package store

import (
	"testing"

	"sonde/internal/derive"
)

func latp(v float64) *float64 { return &v }

func assembleDraft(t *testing.T, st *Store, soundingID, rev int64) Draft {
	t.Helper()
	rows, err := st.Packets(tBg(), soundingID)
	if err != nil {
		t.Fatal(err)
	}
	ovs, err := st.ListOverrides(tBg(), soundingID)
	if err != nil {
		t.Fatal(err)
	}
	res := derive.Assemble(derive.Input{Rows: rows, Overrides: ovs})
	return Draft{SoundingID: soundingID, Revision: rev, Result: res, Overrides: ovs}
}
