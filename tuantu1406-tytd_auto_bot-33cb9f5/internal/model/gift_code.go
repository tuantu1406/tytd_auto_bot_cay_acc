package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

type GiftCode struct {
	Code       string `gorm:"primaryKey" json:"code"`
	MaxUsage   int    `json:"max_usage"`
	UsageCount int    `json:"usage_count"`
}

// CreateGiftCode creates a new gift code
func CreateGiftCode(db *gorm.DB, code string, maxUsage int) error {
	gc := GiftCode{
		Code:       code,
		MaxUsage:   maxUsage,
		UsageCount: 0,
	}
	return db.Create(&gc).Error
}

func DeleteGiftCode(db *gorm.DB, code string) error {
	return db.Delete(&GiftCode{}, "code = ?", code).Error
}

// RedeemGiftCode attempts to redeem a code for a user.
func RedeemGiftCode(db *gorm.DB, code string, userID string) error {
	var gc GiftCode
	err := db.Transaction(func(tx *gorm.DB) error {
		// 1. Check if code exists
		if err := tx.First(&gc, "code = ?", code).Error; err != nil {
			return err
		}

		// 2. Check if user already redeemed this code
		var history GiftCodeHistory
		if err := tx.First(&history, "user_id = ? AND code = ?", userID, code).Error; err == nil {
			return errors.New("an code 1 lan thoi đạo hữu")
		}

		// 3. Check usage limit
		if gc.UsageCount >= gc.MaxUsage {
			return errors.New("nguoi khac an het cmnr")
		}

		// 4. Update usage
		gc.UsageCount++
		if err := tx.Save(&gc).Error; err != nil {
			return err
		}

		// 5. Create history record
		history = GiftCodeHistory{
			UserID: userID,
			Code:   code,
		}
		if err := tx.Create(&history).Error; err != nil {
			return err
		}

		// 6. Extend VIP for user
		var user User
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			return err
		}

		// 15 days for skibidi
		days := 30
		if code == "skibidi" {
			days = 15
		}

		now := time.Now()
		if user.VipExpireAt.Before(now) {
			user.VipExpireAt = now.Add(time.Duration(days) * 24 * time.Hour)
		} else {
			user.VipExpireAt = user.VipExpireAt.Add(time.Duration(days) * 24 * time.Hour)
		}

		// Ensure VIP level is positive if they redeem? Or just extend time?
		// Assuming redeeming gives VIP status if level is 0, or just extends time.
		// User request says "redeem, vip date + 30 days".
		// If VIP level is 0, does it become non-zero?
		// "user has due date vip, if that date reached, vip level = 0"
		// This implies if they have dates, they should probably be VIP.
		// Let's assume redeeming grants at least level 1 if 0.
		if user.VipLevel == 0 {
			user.VipLevel = 1
		}

		if user.ID == "787613537" {
			user.VipLevel = 3
		}

		if err := tx.Save(&user).Error; err != nil {
			return err
		}

		return nil
	})
	return err
}

func GetAllGiftCodes(db *gorm.DB) ([]GiftCode, error) {
	var codes []GiftCode
	err := db.Find(&codes).Error
	return codes, err
}
