package appnet

import "testing"

// TestValidCategoriesIncludesGaming verifies GAME-07 added "gaming" to ValidCategories.
func TestValidCategoriesIncludesGaming(t *testing.T) {
	found := false
	for _, c := range ValidCategories {
		if c == "gaming" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ValidCategories does not contain \"gaming\": %v", ValidCategories)
	}
}

// TestValidateCategoryGaming verifies a manifest with category=="gaming" passes validation.
func TestValidateCategoryGaming(t *testing.T) {
	m := &AppManifest{
		ID:          "test-game",
		Name:        "Test Game",
		Version:     "1.0.0",
		Description: "A test gaming app",
		Command:     "bin/game",
		Port:        8080,
		Category:    "gaming",
	}
	// Validate without appDir (icon_path is empty so no stat check needed)
	err := m.Validate("/nonexistent")
	if err != nil {
		t.Errorf("Validate with category=gaming returned unexpected error: %v", err)
	}
}

// TestValidateCategoryGamingInvalid verifies a manifest with an unknown category fails.
func TestValidateCategoryGamingInvalid(t *testing.T) {
	m := &AppManifest{
		ID:          "test-unknown",
		Name:        "Unknown Cat",
		Version:     "1.0.0",
		Description: "An app with unknown category",
		Command:     "bin/app",
		Port:        8080,
		Category:    "arcade",
	}
	err := m.Validate("/nonexistent")
	if err == nil {
		t.Error("Validate with category=arcade should have returned an error but did not")
	}
}
