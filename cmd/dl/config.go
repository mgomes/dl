package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type config struct {
	boost   int
	retries int
}

func loadConfig() config {
	cfg := config{
		boost:   8,
		retries: 3,
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return cfg
	}

	configPath := filepath.Join(home, ".dlrc")
	file, err := os.Open(configPath)
	if err != nil {
		return cfg
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "boost":
			if v, err := strconv.Atoi(value); err == nil && v > 0 {
				cfg.boost = v
			}
		case "retries":
			if v, err := strconv.Atoi(value); err == nil && v > 0 {
				cfg.retries = v
			}
		}
	}

	return cfg
}

func parseBandwidthLimit(limit string) (int64, error) {
	if limit == "" {
		return 0, nil
	}

	limit = strings.TrimSuffix(strings.ToUpper(limit), "/S")
	limit = strings.TrimSpace(limit)

	var numStr string
	var unit string
	for i, ch := range limit {
		if ch >= '0' && ch <= '9' || ch == '.' {
			continue
		}
		numStr = limit[:i]
		unit = limit[i:]
		break
	}

	if numStr == "" {
		numStr = limit
	}

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bandwidth limit: %s", limit)
	}

	multiplier := float64(1)
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "G", "GB":
		multiplier = 1024 * 1024 * 1024
	case "M", "MB":
		multiplier = 1024 * 1024
	case "K", "KB":
		multiplier = 1024
	case "B", "":
		multiplier = 1
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}

	return int64(num * multiplier), nil
}
