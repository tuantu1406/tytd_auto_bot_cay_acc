package model

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// EventUser is a leader-owned game account used for in-game events. It mirrors
// a member account but has no VIP level, no schedules and no auto-play flags:
// it only stays logged in, see RunEvent in player_event.go.
type EventUser struct {
	ID            string    `gorm:"primaryKey" json:"id"`
	Username      string    `json:"username"`
	IngameName    string    `json:"ingame_name"`
	Password      string    `json:"password"`
	ServerID      int       `json:"server_id"`
	Running       bool      `json:"running"`
	LeaderID      string    `json:"leader_id"`
	LastLoginFail time.Time `json:"last_login_fail"`
	// Off-switches (leader UI 2026-11-09): deliberately named OFF so existing
	// rows keep the "login là chạy" law — default false = the loop runs. The
	// leader's Sự kiện tab flips these; the EP loops check them each pass.
	OffRebornTrial bool `json:"off_reborn_trial"` // Luân Hồi Bí Cảnh 172
	OffSeaWar      bool `json:"off_sea_war"`      // Thương Minh Yêu Vực 349
}

// CreateEventUser creates or updates an event user
func CreateEventUser(db *gorm.DB, user *EventUser) error {
	return db.Save(user).Error
}

// GetEventUser retrieves an event user by ID
func GetEventUser(db *gorm.DB, id string) (*EventUser, error) {
	var u EventUser
	err := db.First(&u, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateEventUser updates an event user (Save for upsert, same idiom as User)
func UpdateEventUser(db *gorm.DB, user *EventUser) error {
	return db.Save(user).Error
}

// DeleteEventUser deletes an event user by ID
func DeleteEventUser(db *gorm.DB, id string) error {
	return db.Delete(&EventUser{ID: id}).Error
}

// EventUserExists checks if an event user exists
func EventUserExists(db *gorm.DB, id string) bool {
	var count int64
	db.Model(&EventUser{}).Where("id = ?", id).Count(&count)
	return count > 0
}

// GetEventUsersByLeader retrieves all event users belonging to a leader
func GetEventUsersByLeader(db *gorm.DB, leaderID string) ([]EventUser, error) {
	var members []EventUser
	err := db.Where("leader_id = ?", leaderID).Find(&members).Error
	return members, err
}

// GetEventUsersCount counts event users under a leader
func GetEventUsersCount(db *gorm.DB, leaderID string) int64 {
	var count int64
	db.Model(&EventUser{}).Where("leader_id = ?", leaderID).Count(&count)
	return count
}

// UpdateEventUserIngameName stores the account's in-game display name
// (already passed through SanitizeIngameName by the caller).
func UpdateEventUserIngameName(db *gorm.DB, userID, name string) error {
	return db.Model(&EventUser{}).Where("id = ?", userID).Update("ingame_name", name).Error
}

// SetEventUserLastLoginFail sets the LastLoginFail timestamp to now for an event user
func SetEventUserLastLoginFail(db *gorm.DB, userID string) error {
	return db.Model(&EventUser{}).Where("id = ?", userID).Update("last_login_fail", time.Now()).Error
}

// ResetAllEventRunningStatus sets all event users' running status to false
func ResetAllEventRunningStatus(db *gorm.DB) error {
	return db.Model(&EventUser{}).Where("running = ?", true).Update("running", false).Error
}

// GenerateRandomEventUserID generates a unique random user ID for event accounts.
// Event IDs use an "8" prefix so they can never collide with member IDs (which
// use "9" via GenerateRandomUserID): both roles share the same int64-keyed
// TeleBot.session map, so their IDs must be disjoint.
func GenerateRandomEventUserID(db *gorm.DB) string {
	for {
		// Generate random 10-digit number
		randomID := fmt.Sprintf("8%09d", time.Now().UnixNano()%1000000000)
		if !EventUserExists(db, randomID) && !UserExists(db, randomID) {
			return randomID
		}
	}
}
