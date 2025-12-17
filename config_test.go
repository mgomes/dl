package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseBandwidthLimit(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		hasError bool
	}{
		{"1M", 1024 * 1024, false},
		{"1MB", 1024 * 1024, false},
		{"500K", 500 * 1024, false},
		{"500KB", 500 * 1024, false},
		{"100KB/s", 100 * 1024, false},
		{"1.5M", int64(1.5 * 1024 * 1024), false},
		{"1G", 1024 * 1024 * 1024, false},
		{"", 0, false},
		{"invalid", 0, true},
		{"1X", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseBandwidthLimit(tt.input)
			if tt.hasError {
				if err == nil {
					t.Errorf("expected error for input %s, but got none", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for input %s: %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("for input %s, expected %d but got %d", tt.input, tt.expected, result)
				}
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("defaults when no config file", func(t *testing.T) {
		cfg := loadConfig()
		if cfg.boost != 8 {
			t.Errorf("expected default boost=8, got %d", cfg.boost)
		}
		if cfg.retries != 3 {
			t.Errorf("expected default retries=3, got %d", cfg.retries)
		}
	})

	t.Run("reads config from file", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot get home dir")
		}

		configPath := filepath.Join(home, ".dlrc")
		original, err := os.ReadFile(configPath)
		hadOriginal := err == nil

		testConfig := []byte("boost = 16\nretries = 5\n")
		if err := os.WriteFile(configPath, testConfig, 0644); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if hadOriginal {
				os.WriteFile(configPath, original, 0644)
			} else {
				os.Remove(configPath)
			}
		}()

		cfg := loadConfig()
		if cfg.boost != 16 {
			t.Errorf("expected boost=16, got %d", cfg.boost)
		}
		if cfg.retries != 5 {
			t.Errorf("expected retries=5, got %d", cfg.retries)
		}
	})

	t.Run("ignores comments and empty lines", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("cannot get home dir")
		}

		configPath := filepath.Join(home, ".dlrc")
		original, err := os.ReadFile(configPath)
		hadOriginal := err == nil

		testConfig := []byte("# comment\n\nboost = 4\n# another comment\nretries = 2\n")
		if err := os.WriteFile(configPath, testConfig, 0644); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if hadOriginal {
				os.WriteFile(configPath, original, 0644)
			} else {
				os.Remove(configPath)
			}
		}()

		cfg := loadConfig()
		if cfg.boost != 4 {
			t.Errorf("expected boost=4, got %d", cfg.boost)
		}
		if cfg.retries != 2 {
			t.Errorf("expected retries=2, got %d", cfg.retries)
		}
	})
}
