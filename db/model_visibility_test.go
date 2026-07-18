package db

import (
	"testing"

	"github.com/google/uuid"
)

// The muhiyacode_visible flag must survive a create → get round-trip and be
// mutable via update (both true→false and false→true). Needs a live DB, so it
// skips when none is configured (same gate as the billing tests).
func TestModelMuhiyaCodeVisibleRoundTrip(t *testing.T) {
	d := openTestDB(t)
	defer d.conn.Close()

	provID := "prov-" + uuid.NewString()[:8]
	if err := d.CreateProvider(Provider{ID: provID, Name: provID, BaseURL: "https://example.com", APIKey: "x", Status: "active"}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer d.DeleteProvider(provID)

	id := "model-" + uuid.NewString()[:8]
	m := Model{
		ID: id, Name: id, ProviderID: provID, TargetModel: "deepseek-chat",
		Status: "active", MuhiyaCodeVisible: true,
	}
	if err := d.CreateModel(m); err != nil {
		t.Fatalf("create model: %v", err)
	}
	defer d.DeleteModel(id)

	got, err := d.GetModel(id)
	if err != nil || got == nil {
		t.Fatalf("get model: %v (nil=%v)", err, got == nil)
	}
	if !got.MuhiyaCodeVisible {
		t.Fatal("muhiyacode_visible did not persist as true on create")
	}

	got.MuhiyaCodeVisible = false
	if err := d.UpdateModel(*got); err != nil {
		t.Fatalf("update model: %v", err)
	}
	after, err := d.GetModel(id)
	if err != nil || after == nil {
		t.Fatalf("re-get model: %v", err)
	}
	if after.MuhiyaCodeVisible {
		t.Fatal("muhiyacode_visible did not persist as false on update")
	}

	// GetModelByName (the inference-resolution path) must also carry the flag so
	// callers that read it there see the real value.
	byName, err := d.GetModelByName(id)
	if err != nil || byName == nil {
		t.Fatalf("get by name: %v", err)
	}
	if byName.MuhiyaCodeVisible {
		t.Fatal("GetModelByName must reflect the updated flag")
	}
}
