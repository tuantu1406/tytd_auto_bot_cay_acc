package model

import (
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
)

type User struct {
	ID                string    `gorm:"primaryKey" json:"id"`
	Username          string    `json:"username"`
	IngameName        string    `json:"ingame_name"`
	Password          string    `json:"password"`
	ServerID          int       `json:"server_id"`
	GameUID           uint64    `json:"game_uid"`
	VipLevel          int       `json:"vip_level"`
	VipExpireAt       time.Time `json:"vip_expire_at"`
	Running           bool      `json:"running"`
	MinBottleLevel    int       `json:"min_bottle_level"`
	MinPeachLevel     int       `json:"min_peach_level"`
	MinLuuLyLevel     int       `json:"min_luu_ly_level"`
	BottlePrefered    bool      `json:"bottle_prefered"`
	AutoLogin         bool      `json:"auto_login"`
	AutoMouse         bool      `json:"auto_mouse"`
	AutoGather        bool      `json:"auto_gather"`          // permanent Tụ Linh (auto gather energy) switch
	MinGatherIncome   int64     `json:"min_gather_income"`    // Tụ Linh formation income floor, 0 = no bar
	AutoSkyTrade      bool      `json:"auto_sky_trade"`       // opt-in auto buy/sell/run boat for Tiên Vực Thương Đồ (only when a leg is profitable)
	AutoSkyTradeSpeed bool      `json:"auto_sky_trade_speed"` // opt-in: burn acceleration items at 21:58 so the last caravan legs finish before the 22:00 close
	AutoRebornTrial   bool      `json:"auto_reborn_trial"`    // opt-in auto-play Luân Hồi Bí Cảnh (free moves only: explore, resolve events, worship, challenge, claims)
	AutoTienCanh      bool      `json:"auto_tiencanh"`        // opt-in auto-play Tiên Cảnh Vị Ương (free moves only: explore, resolve events, worship, claims, tree water from config-folded donate items; stamina item NEVER burned)
	AutoSeaWar        bool      `json:"auto_seawar"`          // opt-in auto-play Thương Minh Yêu Vực (type 349; free moves only: explore, resolve events, worship, welfare/liquid claims, free next-region; energy item NEVER burned — button only)
	// NOTE: DanGioiOff (web opt-out for the auto Đan Giới loop) was DELETED
	// by owner 2026-10-17 — the loop is always-on with no switch. The sqlite
	// column may still exist in old DBs; gorm simply ignores it.
	IsLeader bool   `json:"is_leader"`
	LeaderID string `json:"leader_id"`
	// DeputyOf is the chat id of ANOTHER leader this account (a regular user's
	// own account, or one of a leader's members) delegates to — EVENT-ONLY. The
	// delegate sees the account on their Sự kiện tab (can force login/logout as
	// an EventPlayer and fire event orders) but NOT on their Đệ tử tab; they
	// never learn the password and cannot change config/password/remove/VIP.
	// Set per account (the account's own settings page, or the owning leader's
	// Đệ tử config panel), never in bulk. "" = no delegation. (user feature
	// 2026-10-07)
	DeputyOf           string    `json:"deputy_of"`
	LastLoginFail      time.Time `json:"last_login_fail"`
	ChuotStartSchedule string    `gorm:"type:varchar(128)" json:"chuot_start_schedule"`
	ChuotStopSchedule  string    `gorm:"type:varchar(128)" json:"chuot_stop_schedule"`
	LoginSchedule      string    `gorm:"type:varchar(128)" json:"login_schedule"`
	LogoutSchedule     string    `gorm:"type:varchar(128)" json:"logout_schedule"`
}

// CreateUser creates or updates a user
func CreateUser(db *gorm.DB, user *User) error {
	return db.Save(user).Error
}

// GetUser retrieves a user by ID
func GetUser(db *gorm.DB, id string) (*User, error) {
	var u User
	err := db.First(&u, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UserForAutoLogin answers the clan-triggered auto-login question for one
// GAME account (gameUID = the server-side member id seen in MClanInfo):
// which user row — if any — should bring it online. Several rows can share
// one game_uid when different people configure the same game account (owner
// 2026-10-27), so this MUST scan every row, not First(): the account is
// eligible iff SOME owner toggled auto_login on and their row is not
// running; the FIRST such row by id (lowest chat id = the owner who asked
// first) decides, and THAT row's credentials/VIP/notify-chat are used — the
// owner who enabled auto-login gets their own configuration executed.
// (Old GetUserByGameUID/IsUserAutoLoginEnabled did `WHERE game_uid = ? ...
// First()` — a missing-chatid lookup that silently applied one stranger's
// auto_login=false to everyone else's account, the ohop1 gate bug class.)
func UserForAutoLogin(db *gorm.DB, gameUID uint64) (*User, bool) {
	// 0/1 are sentinels (rows whose credentials were just reset never have a
	// real game uid; the in-game member id is never that small either).
	if db == nil || gameUID <= 1 {
		return nil, false
	}
	var rows []User
	if err := db.Where("game_uid = ? AND auto_login = ?", gameUID, true).
		Order("id asc").Find(&rows).Error; err != nil {
		return nil, false
	}
	for i := range rows {
		if !rows[i].Running {
			return &rows[i], true
		}
	}
	return nil, false
}

// UpdateUser - generic update not strictly needed if we use CreateUser for upsert,
// but sticking to standard naming. Using Save for upsert.
func UpdateUser(db *gorm.DB, user *User) error {
	return db.Save(user).Error
}

// DeleteUser deletes a user by ID
func DeleteUser(db *gorm.DB, id string) error {
	return db.Delete(&User{ID: id}).Error
}

// UserExists checks if a user exists
func UserExists(db *gorm.DB, id string) bool {
	var count int64
	db.Model(&User{}).Where("id = ?", id).Count(&count)
	return count > 0
}

// GetAllUserIDs retrieves all user IDs
func GetAllUserIDs(db *gorm.DB) []string {
	var users []User
	db.Select("id").Find(&users)

	ret := make([]string, len(users))
	for i, u := range users {
		ret[i] = u.ID
	}
	return ret
}

// UpdateGameUID updates the GameUID for a specific user
func UpdateGameUID(db *gorm.DB, userID string, gameUID uint64) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("game_uid", gameUID).Error
}

// UpdateUserIngameName stores the account's in-game display name (already
// passed through SanitizeIngameName by the caller).
func UpdateUserIngameName(db *gorm.DB, userID, name string) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("ingame_name", name).Error
}

// The five per-event opt-in switches (AutoGather, AutoSkyTrade,
// AutoSkyTradeSpeed, AutoRebornTrial, AutoTienCanh) are read ONLY from the
// Player object at run time (owner 2026-10-27: "tất cả các event chỉ được
// check trong object player"). They must NEVER be looked up by game_uid —
// several user rows can share one game_uid when different people configure
// the same game account, and First() would silently apply one owner's toggle
// to another owner's session. The row is persisted for the next login and
// the live session is patched in place at every save site
// (Player.seedAutoGates / TeleBot.patchAutoGates).

// GatherMinIncome returns the account's minimum formation income (speed)
// bar for Tụ Linh joining — set via /tu_linh_min or the settings pages.
// 0 means no bar. A missing/unreadable row reads as 0: the bar only ever
// turns joining off, so a lookup hiccup must not silently stop gathering.
func GatherMinIncome(db *gorm.DB, gameUID uint64) int64 {
	var u User
	if err := db.Where("game_uid = ?", gameUID).First(&u).Error; err != nil {
		return 0
	}
	if u.MinGatherIncome < 0 { // defensive: the setter rejects negatives
		return 0
	}
	return u.MinGatherIncome
}

// IsAutoTuLinhGateOpen reports the state of the GLOBAL Tụ Linh gate — the
// master switch admin sets with /set_auto_tu_linh, mirroring auto_login:
// every gather attempt checks this gate and the account's own switch.
// A missing or unparseable row reads as open, because gathering ran for
// everyone before the gate existed and a lookup hiccup must not silently
// stop live accounts.
func IsAutoTuLinhGateOpen(db *gorm.DB) bool {
	if db == nil {
		return true // no config store to honour — same read as "missing"
	}
	cfg, err := GetConfig(db, ConfigKeyAutoTuLinh)
	if err != nil || cfg == nil {
		return true
	}
	v, err := strconv.ParseBool(cfg.Value)
	if err != nil {
		return true
	}
	return v
}

// GetMembersByLeader retrieves all members belonging to a leader
func GetMembersByLeader(db *gorm.DB, leaderID string) ([]User, error) {
	var members []User
	err := db.Where("leader_id = ?", leaderID).Find(&members).Error
	return members, err
}

// GetMembersCount counts members under a leader
func GetMembersCount(db *gorm.DB, leaderID string) int64 {
	var count int64
	db.Model(&User{}).Where("leader_id = ?", leaderID).Count(&count)
	return count
}

// GetDelegatedUsers returns every account that DELEGATED itself to leaderID for
// events only (deputy_of = leaderID). These are regular users or ANOTHER
// leader's members — deliberately NOT merged into GetMembersByLeader, so the
// delegate's Đệ tử tab never shows them; only the Sự kiện tab does (2026-10-07).
func GetDelegatedUsers(db *gorm.DB, leaderID string) ([]User, error) {
	var out []User
	err := db.Where("deputy_of = ?", leaderID).Find(&out).Error
	return out, err
}

// IsLeaderChat reports whether chatID is a real leader account (IsLeader and
// not itself someone's member — a member cannot be a delegation target).
func IsLeaderChat(db *gorm.DB, chatID string) bool {
	var u User
	if err := db.First(&u, "id = ?", chatID).Error; err != nil {
		return false
	}
	return u.IsLeader && u.LeaderID == ""
}

// GenerateRandomUserID generates a unique random user ID for member accounts
func GenerateRandomUserID(db *gorm.DB) string {
	for {
		// Generate random 10-digit number
		randomID := fmt.Sprintf("9%09d", time.Now().UnixNano()%1000000000)
		if !UserExists(db, randomID) {
			return randomID
		}
	}
}

// GetAllLeaders retrieves all users who are leaders
func GetAllLeaders(db *gorm.DB) ([]User, error) {
	var leaders []User
	err := db.Where("is_leader = ?", true).Find(&leaders).Error
	return leaders, err
}

// GetRunningUsers retrieves all users heavily running
func GetRunningUsers(db *gorm.DB) ([]User, error) {
	var users []User
	err := db.Where("running = ?", true).Find(&users).Error
	return users, err
}

// ResetAllRunningStatus sets all users' running status to false
func ResetAllRunningStatus(db *gorm.DB) error {
	return db.Model(&User{}).Where("running = ?", true).Update("running", false).Error
}

// SetLastLoginFail sets the LastLoginFail timestamp to now for a specific user
func SetLastLoginFail(db *gorm.DB, userID string) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("last_login_fail", time.Now()).Error
}

// SetAutoLogin enables or disables auto login for a specific user
func SetAutoLogin(db *gorm.DB, userID string, enabled bool) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("auto_login", enabled).Error
}

// SetChuotStartSchedule stores the CSV list of hours (e.g. "10,18,22") at which
// chuot should be started daily for the given user.
func SetChuotStartSchedule(db *gorm.DB, userID, hours string) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("chuot_start_schedule", hours).Error
}

// SetChuotStopSchedule stores the CSV list of hours at which chuot should be
// stopped daily for the given user.
func SetChuotStopSchedule(db *gorm.DB, userID, hours string) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("chuot_stop_schedule", hours).Error
}

// GetUsersWithChuotSchedule returns users who have a non-empty chuot schedule.
func GetUsersWithChuotSchedule(db *gorm.DB) []User {
	var users []User
	db.Where("chuot_start_schedule != '' OR chuot_stop_schedule != ''").Find(&users)
	return users
}

// SetLoginSchedule stores the CSV list of "H:MM" times at which the user's
// account should auto-login daily.
func SetLoginSchedule(db *gorm.DB, userID, times string) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("login_schedule", times).Error
}

// SetLogoutSchedule stores the CSV list of "H:MM" times at which the user's
// account should auto-logout daily.
func SetLogoutSchedule(db *gorm.DB, userID, times string) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("logout_schedule", times).Error
}

// GetUsersWithLoginSchedule returns users who have a non-empty login or logout
// schedule.
func GetUsersWithLoginSchedule(db *gorm.DB) []User {
	var users []User
	db.Where("login_schedule != '' OR logout_schedule != ''").Find(&users)
	return users
}
