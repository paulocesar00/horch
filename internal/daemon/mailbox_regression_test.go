package daemon

import (
	"context"
	"testing"

	"github.com/ellingtonsp/herdr-orch/internal/store"
)

func TestCheckRedeliversUntilAcknowledged(t *testing.T) {
	x := newHarness(t)
	m, err := x.st.InsertMessage(store.Message{From: store.Daemon, To: coord, Kind: store.KindDone, Body: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := checkConsumer(context.Background(), x.e, coord, "coordinator", true, false, 10)
		if err != nil {
			t.Fatal(err)
		}
		msgs := out.([]store.Message)
		if len(msgs) != 1 || msgs[0].ID != m.ID || msgs[0].ReadAt != 0 {
			t.Fatalf("unacknowledged check %d lost mail: %+v", i, msgs)
		}
	}
	if _, err := x.e.opAck(context.Background(), coord, mustJSON(AckArgs{ID: m.ID})); err != nil {
		t.Fatal(err)
	}
	out, err := checkConsumer(context.Background(), x.e, coord, "coordinator", false, false, 0)
	if err != nil || len(out.([]store.Message)) != 0 {
		t.Fatalf("acknowledged mail repeated: %+v err=%v", out, err)
	}
}
