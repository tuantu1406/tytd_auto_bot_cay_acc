package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"tytd_go/internal/db"
	"tytd_go/internal/model"

	"gopkg.in/yaml.v3"
)

type YamlUser struct {
	Username       string `yaml:"username"`
	Password       string `yaml:"password"`
	ServerID       int    `yaml:"server_id"`
	VipLevel       int    `yaml:"vip_level"`
	Running        bool   `yaml:"running"`
	MinBottleLevel int    `yaml:"min_bottle_level"`
	MinPeachLevel  int    `yaml:"minpeachlevel"`
	MinLuuLyLevel  int    `yaml:"minluulylevel"`
}

func main() {
	// 1. Read YAML
	data, err := os.ReadFile("cmd/data.yaml")
	if err != nil {
		log.Fatalf("Failed to read data.yaml: %v", err)
	}

	// 2. Parse YAML
	var usersMap map[string]YamlUser
	err = yaml.Unmarshal(data, &usersMap)
	if err != nil {
		log.Fatalf("Failed to unmarshal YAML: %v", err)
	}
	fmt.Printf("found %d users in YAML\n", len(usersMap))

	// 3. Connect DB
	dbPath := "./data.sqlite" // Production DB path as per telebot.go
	d, err := db.NewDB(dbPath, "")
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}

	// 4. Migrate
	count := 0
	vipExpire := time.Now().Add(30 * 24 * time.Hour)

	for id, yUser := range usersMap {
		u := model.User{
			ID:             id,
			Username:       yUser.Username,
			Password:       yUser.Password,
			ServerID:       yUser.ServerID,
			VipLevel:       1,         // Requested: all are vip 1
			VipExpireAt:    vipExpire, // Requested: now + 30 days
			Running:        yUser.Running,
			MinBottleLevel: yUser.MinBottleLevel,
			MinPeachLevel:  yUser.MinPeachLevel,
			MinLuuLyLevel:  yUser.MinLuuLyLevel,
		}

		err := model.CreateUser(d.GetDB(), &u)
		if err != nil {
			log.Printf("Failed to migrate user %s: %v", id, err)
		} else {
			count++
		}
	}

	fmt.Printf("Migrated %d users successfully.\n", count)
}
