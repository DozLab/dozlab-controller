package events

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// apiEvent mirrors dozlab-api's websocket.Event, which consumes these events.
type apiEvent struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Source    string                 `json:"source"`
	SessionID string                 `json:"session_id,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	LabID     string                 `json:"lab_id,omitempty"`
	Data      map[string]interface{} `json:"data"`
	Timestamp time.Time              `json:"timestamp"`
}

func TestPhaseChangeEventMatchesAPIWireFormat(t *testing.T) {
	now := time.Date(2026, 9, 27, 22, 0, 0, 0, time.UTC)
	change := PhaseChange{
		UID: "uid-1", Namespace: "dozlab-labs", Name: "lab-session-demo",
		UserID: "user-1", SessionID: "session-1",
		Phase: "Running", Message: "Lab session is running",
		Endpoints: map[string]string{"terminal": "http://10.0.0.1:8081"},
	}
	body, err := json.Marshal(change.Event(now))
	if err != nil {
		t.Fatal(err)
	}

	var got apiEvent
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("dozlab-api could not decode the event: %v", err)
	}
	want := apiEvent{
		ID: "uid-1.Running", Type: "labsession.phase_changed", Source: "dozlab-controller",
		SessionID: "session-1", UserID: "user-1",
		Data: map[string]interface{}{
			"phase": "Running", "message": "Lab session is running",
			"namespace": "dozlab-labs", "name": "lab-session-demo",
			"endpoints": map[string]interface{}{"terminal": "http://10.0.0.1:8081"},
		},
		Timestamp: now,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded event\n got %+v\nwant %+v", got, want)
	}
}

func TestPhaseChangeEventOmitsEmptyReasonAndEndpoints(t *testing.T) {
	e := PhaseChange{UID: "u", Phase: "Creating"}.Event(time.Now())
	if _, ok := e.Data["reason"]; ok {
		t.Error("reason should be omitted when empty")
	}
	if _, ok := e.Data["endpoints"]; ok {
		t.Error("endpoints should be omitted when empty")
	}
	if e.ID != "u.Creating" {
		t.Errorf("ID = %q, want u.Creating", e.ID)
	}

	failed := PhaseChange{UID: "u", Phase: "Failed", Reason: "pod is no longer ready"}.Event(time.Now())
	if failed.Data["reason"] != "pod is no longer ready" {
		t.Errorf("reason = %v", failed.Data["reason"])
	}
}
