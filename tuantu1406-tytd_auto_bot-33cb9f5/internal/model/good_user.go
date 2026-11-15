package model

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// GoodUser represents a user marked as "good" by admin
type GoodUser struct {
	ChatID int64 `gorm:"primaryKey" json:"chat_id"`
}

// AddGoodUser adds a user to the good user list
func AddGoodUser(db *gorm.DB, chatID int64) error {
	goodUser := GoodUser{ChatID: chatID}
	return db.Save(&goodUser).Error
}

// RemoveGoodUser removes a user from the good user list
func RemoveGoodUser(db *gorm.DB, chatID int64) error {
	return db.Delete(&GoodUser{}, chatID).Error
}

// IsGoodUser checks if a user is in the good user list
func IsGoodUser(db *gorm.DB, chatID int64) bool {
	var count int64
	db.Model(&GoodUser{}).Where("chat_id = ?", chatID).Count(&count)
	return count > 0
}

// GetAllGoodUsers retrieves all good users
func GetAllGoodUsers(db *gorm.DB) ([]GoodUser, error) {
	var users []GoodUser
	err := db.Find(&users).Error
	return users, err
}

// GetGoodUserIDsString returns good user IDs as a formatted string: [id1,id2,id3]
func GetGoodUserIDsString(db *gorm.DB) string {
	users, err := GetAllGoodUsers(db)
	if err != nil || len(users) == 0 {
		return "[]"
	}

	var ids []string
	for _, user := range users {
		ids = append(ids, fmt.Sprintf("%d", user.ChatID))
	}

	return "[" + strings.Join(ids, ",") + "]"
}
