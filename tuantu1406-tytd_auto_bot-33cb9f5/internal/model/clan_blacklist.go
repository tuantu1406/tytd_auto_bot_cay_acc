package model

import "gorm.io/gorm"

type ClanBlacklist struct {
	ClanID uint64 `gorm:"primaryKey" json:"clan_id"`
}

func AddBlacklistClan(db *gorm.DB, clanID uint64) error {
	return db.Create(&ClanBlacklist{ClanID: clanID}).Error
}

func RemoveBlacklistClan(db *gorm.DB, clanID uint64) error {
	return db.Delete(&ClanBlacklist{ClanID: clanID}).Error
}

func IsClanBlacklisted(db *gorm.DB, clanID uint64) bool {
	var count int64
	db.Model(&ClanBlacklist{}).Where("clan_id = ?", clanID).Count(&count)
	return count > 0
}

func GetAllBlacklistClans(db *gorm.DB) ([]uint64, error) {
	var list []ClanBlacklist
	err := db.Find(&list).Error
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, len(list))
	for i, c := range list {
		ids[i] = c.ClanID
	}
	return ids, nil
}
