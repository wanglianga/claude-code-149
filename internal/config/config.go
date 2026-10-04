package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr       string
	DatabaseURL    string
	RedisAddr      string
	MigrateOnStart bool
}

func Load() Config {
	c := Config{
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		DatabaseURL:    env("DATABASE_URL", "postgres://postgres:postgres@127.0.0.1:5432/courtbook?sslmode=disable"),
		RedisAddr:      env("REDIS_ADDR", "127.0.0.1:6379"),
		MigrateOnStart: envBool("MIGRATE_ON_START", true),
	}
	if v := os.Getenv("PORT"); v != "" && os.Getenv("HTTP_ADDR") == "" {
		c.HTTPAddr = ":" + v
	}
	return c
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			panic(fmt.Sprintf("invalid bool %s=%q", k, v))
		}
		return b
	}
	return def
}
