package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// Config — настройки, которые меняются из меню в трее.
type Config struct {
	Mode         string `json:"mode"`       // "extend" — второй монитор, "mirror" — дублирование
	FPS          int    `json:"fps"`        // 0 = как у экрана телефона (до 120)
	Size         string `json:"size"`       // "native" | "medium" | "large"
	Quality      string `json:"quality"`    // "normal" | "high" | "max"
	Encoder      string `json:"encoder"`    // последний рабочий кодировщик
	Codec        string `json:"codec"`      // "h264" | "hevc" (только для приложения)
	Port         int    `json:"port"`       // браузерный клиент и справка
	NativePort   int    `json:"nativePort"` // приложение для Android
	FirstRunDone bool   `json:"firstRunDone"`
	Token        string `json:"token"` // ключ доступа к /ws, передаётся только в адресе страницы
}

func defaultConfig() Config {
	return Config{Mode: "extend", FPS: 0, Size: "native", Quality: "high", Codec: "h264", Port: 28451, NativePort: 28452}
}

func dataDir() string {
	base, err := os.UserCacheDir() // Windows: %LOCALAPPDATA%
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "USBDisplay")
}

func configPath() string { return filepath.Join(dataDir(), "config.json") }

func loadConfig() Config {
	c := defaultConfig()
	b, err := os.ReadFile(configPath())
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			log.Printf("config: %v — используются настройки по умолчанию", err)
			c = defaultConfig()
		}
	}
	if c.Mode != "extend" && c.Mode != "mirror" {
		c.Mode = "extend"
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = 28451
	}
	if c.NativePort <= 0 || c.NativePort > 65535 || c.NativePort == c.Port {
		c.NativePort = 28452
	}
	if c.Codec != "hevc" {
		c.Codec = "h264"
	}
	switch c.Size {
	case "native", "medium", "large":
	default:
		c.Size = "native"
	}
	switch c.Quality {
	case "normal", "high", "max":
	default:
		c.Quality = "normal"
	}
	if len(c.Token) < 16 {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		c.Token = hex.EncodeToString(b)
		saveConfig(c)
	}
	return c
}

func saveConfig(c Config) {
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(configPath(), b, 0o644); err != nil {
		log.Printf("config save: %v", err)
	}
}
