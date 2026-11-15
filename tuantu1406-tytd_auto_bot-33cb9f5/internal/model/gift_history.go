package model

type GiftCodeHistory struct {
	UserID string `gorm:"primaryKey" json:"user_id"`
	Code   string `gorm:"primaryKey" json:"code"`
}
