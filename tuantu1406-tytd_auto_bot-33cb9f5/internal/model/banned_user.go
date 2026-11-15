package model

import (
	"time"

	"gorm.io/gorm"
)

// BannedUser represents a user banned from using the bot
type BannedUser struct {
	ChatID   int64     `gorm:"primaryKey" json:"chat_id"`
	Username string    `json:"username"`
	BannedAt time.Time `json:"banned_at"`
	Reason   string    `json:"reason"`
}

// AddBannedUser adds a user to the ban list
func AddBannedUser(db *gorm.DB, chatID int64, username, reason string) error {
	bannedUser := BannedUser{
		ChatID:   chatID,
		Username: username,
		BannedAt: time.Now(),
		Reason:   reason,
	}
	return db.Save(&bannedUser).Error
}

// RemoveBannedUser removes a user from the ban list
func RemoveBannedUser(db *gorm.DB, chatID int64) error {
	return db.Delete(&BannedUser{}, chatID).Error
}

// IsBanned checks if a user is banned
func IsBanned(db *gorm.DB, chatID int64) bool {
	var count int64
	db.Model(&BannedUser{}).Where("chat_id = ?", chatID).Count(&count)
	return count > 0
}

// GetAllBannedUsers retrieves all banned users
func GetAllBannedUsers(db *gorm.DB) ([]BannedUser, error) {
	var users []BannedUser
	err := db.Find(&users).Error
	return users, err
}
