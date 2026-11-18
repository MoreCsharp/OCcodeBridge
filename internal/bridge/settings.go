package bridge

import (
	"errors"
	"os"
	"path/filepath"
)

// settings.json holds the UI-edited configuration. YAML from CPA seeds a fresh
// install; the file wins for the fields it carries, and fields this build no
// longer knows are ignored by decodeConfigJSON.
func loadSettings(cfg *Config) error {
	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return decodeConfigJSON(raw, cfg)
}

func saveSettings(cfg Config) error {
	return atomicJSON(filepath.Join(cfg.DataDir, "settings.json"), cfg)
}
