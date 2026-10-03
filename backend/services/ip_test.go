package services

import (
	"errors"
	"testing"

	"hector/backend/types"
)

// Hetzner refuses a create body that carries both `location` and an
// assignee — they are mutually exclusive. That is exactly what a
// "create and assign now" form posts, so the rule lives in a test rather
// than in a support ticket.
func TestPrimaryIPCreateBodyLocationAndAssignee(t *testing.T) {
	id := int64(42)

	if _, err := primaryIPCreateBody(types.PrimaryIPCreateRequest{Name: "spare", Type: "ipv4"}); err == nil {
		t.Error("unassigned without a location = nil error, want a field error")
	}

	body, err := primaryIPCreateBody(types.PrimaryIPCreateRequest{
		Name: "web-1", Type: "ipv4", Location: "fsn1", AssigneeID: &id,
	})
	if err != nil {
		t.Fatalf("assigned create: %v", err)
	}
	if body.Location != "" {
		t.Errorf("location = %q, want it dropped when an assignee is present", body.Location)
	}
	if body.AssigneeID == nil || *body.AssigneeID != id {
		t.Errorf("assignee_id = %v, want %d", body.AssigneeID, id)
	}
	if body.AssigneeType != "server" {
		t.Errorf("assignee_type = %q, want server", body.AssigneeType)
	}

	body, err = primaryIPCreateBody(types.PrimaryIPCreateRequest{Name: "spare", Type: "ipv6", Location: " FSN1 "})
	if err != nil {
		t.Fatalf("unassigned create: %v", err)
	}
	if body.Location != "fsn1" {
		t.Errorf("location = %q, want fsn1", body.Location)
	}
	if body.AssigneeID != nil {
		t.Errorf("assignee_id = %v, want none", *body.AssigneeID)
	}

	if _, err := primaryIPCreateBody(types.PrimaryIPCreateRequest{Name: "x", Type: "ipx"}); !errors.Is(err, ErrBadField) {
		t.Errorf("bad type = %v, want ErrBadField", err)
	}
	if _, err := primaryIPCreateBody(types.PrimaryIPCreateRequest{Type: "ipv4", Location: "fsn1"}); err == nil {
		t.Error("no name = nil error, want a field error")
	}
}
