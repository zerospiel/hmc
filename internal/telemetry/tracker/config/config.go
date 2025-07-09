package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type TelemetryConfig struct {
	StorageDir string
	Frequency  time.Duration
}

func NewTelemetryConfig(dir string, freq time.Duration) (*TelemetryConfig, error) {
	if dir == "" {
		if env := os.Getenv("TELEMETRYDIR"); env != "" {
			dir = env
		} else {
			ucd, err := os.UserConfigDir()
			if err != nil {
				return nil, fmt.Errorf("getting user config dir: %w", err)
			}
			dir = filepath.Join(ucd, "telemetry")
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating storage dir: %w", err)
	}

	return &TelemetryConfig{
		StorageDir: dir,
		Frequency:  freq,
	}, nil
}
