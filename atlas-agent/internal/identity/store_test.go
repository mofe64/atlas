package identity

import "testing"

func TestLoadOrCreatePersistsStableIdentity(t *testing.T) {
	directory := t.TempDir()
	first, err := LoadOrCreate(directory)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	second, err := LoadOrCreate(directory)
	if err != nil {
		t.Fatalf("second LoadOrCreate() error = %v", err)
	}
	if first != second || first.InstallationID == "" || first.DroneID == "" {
		t.Fatalf("identities are not stable: first=%#v second=%#v", first, second)
	}
}
