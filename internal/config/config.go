package config

import (
	"os"
)

type Config struct {
	HTTPAddr       string
	DatabaseURL    string
	RedisAddr      string
	TokenSecret    string
	AllowTestClock bool
	EnableBgJobs   bool
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func Load() Config {
	return Config{
		HTTPAddr:       getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:    getenv("DATABASE_URL", "postgres://appuser:appuser@localhost:5433/badminton?sslmode=disable"),
		RedisAddr:      getenv("REDIS_ADDR", "localhost:6379"),
		TokenSecret:    getenv("TOKEN_SECRET", "gym-badminton-dev-secret"),
		AllowTestClock: os.Getenv("ALLOW_TEST_CLOCK") == "1" || os.Getenv("ALLOW_TEST_CLOCK") == "true",
		EnableBgJobs:   os.Getenv("DISABLE_BG_JOBS") != "1" && os.Getenv("DISABLE_BG_JOBS") != "true",
	}
}
