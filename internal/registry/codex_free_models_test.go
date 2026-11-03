package registry

import "testing"

func TestGetCodexFreeModelsOmitsUnsupportedModels(t *testing.T) {
	previous := getModels()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = previous
		modelsCatalogStore.mu.Unlock()
	})

	// Simulate a catalog refresh that re-advertises the unsupported model.
	refreshed := *previous
	refreshed.CodexFree = append(cloneModelInfos(previous.CodexFree), &ModelInfo{ID: "gpt-6.1-terra"})
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &refreshed
	modelsCatalogStore.mu.Unlock()

	for _, model := range GetCodexFreeModels() {
		if model.ID == "gpt-6.1-terra" {
			t.Fatalf("codex-free models must not include %q", model.ID)
		}
	}
	found := false
	for _, model := range GetCodexPlusModels() {
		if model.ID == "gpt-6.1-terra" {
			found = true
		}
	}
	if !found {
		t.Fatal("gpt-6.1-terra must stay available on the plus tier")
	}
}
