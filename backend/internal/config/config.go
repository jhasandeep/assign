// Package config reads settings from the environment (and a .env file if present).
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// LoadDotEnv loads .env from the working directory or the repo root.
// Real environment variables win over the file. Missing files are fine.
func LoadDotEnv() {
	_ = godotenv.Load(".env", "../.env")
}

func DatabaseURL() (string, error) {
	v := os.Getenv("DATABASE_URL")
	if v == "" {
		return "", fmt.Errorf("DATABASE_URL is not set (copy .env.example to .env)")
	}
	return v, nil
}

func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Int(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func Float(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func Duration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
