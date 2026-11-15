package model

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Clan-triggered auto login must survive the multi-owner scenario (owner
// 2026-10-27): several user rows may share one game_uid when different
// people configure the same game account. The old `WHERE game_uid = ? ...
// First()` looked at ONE arbitrary row — a stranger's auto_login=false
// silenced everyone's account (the ohop1 gate bug class).

func TestUserForAutoLoginMultiOwner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:autologin_multi?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	const uid = uint64(9911)
	// Owner with the LOWER chat id never enabled auto login…
	if err := db.Create(&User{ID: "100", Username: "silent", GameUID: uid, AutoLogin: false}).Error; err != nil {
		t.Fatal(err)
	}
	// …the one who DID must still get the account pulled online, on THEIR row.
	if err := db.Create(&User{ID: "200", Username: "want", GameUID: uid, AutoLogin: true}).Error; err != nil {
		t.Fatal(err)
	}
	u, ok := UserForAutoLogin(db, uid)
	if !ok {
		t.Fatal("one opted-in owner must be enough — First() arbitrariness must not silence the account")
	}
	if u.ID != "200" {
		t.Fatalf("deciding row = %s, want the opted-in owner's row 200", u.ID)
	}

	// Two opted-in owners: deterministic pick — lowest id, never a coin flip.
	if err := db.Create(&User{ID: "150", Username: "earlier", GameUID: uid, AutoLogin: true}).Error; err != nil {
		t.Fatal(err)
	}
	u, _ = UserForAutoLogin(db, uid)
	if u.ID != "150" {
		t.Fatalf("deciding row = %s, want lowest id 150", u.ID)
	}

	// The deciding row is running → the next opted-in owner takes over.
	if err := db.Model(&User{}).Where("id = ?", "150").Update("running", true).Error; err != nil {
		t.Fatal(err)
	}
	u, ok = UserForAutoLogin(db, uid)
	if !ok || u.ID != "200" {
		t.Fatalf("running row must yield: got (%v, %v)", u, ok)
	}

	// Every opted-in row running → nobody eligible.
	if err := db.Model(&User{}).Where("id = ?", "200").Update("running", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := UserForAutoLogin(db, uid); ok {
		t.Fatal("all opted-in rows running must refuse")
	}

	// Nobody opted in at all → refuse (auto login is per-account opt-in).
	if err := db.Model(&User{}).Where("id = ?", "200").Update("auto_login", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := UserForAutoLogin(db, uid); ok {
		t.Fatal("no auto_login row must refuse")
	}

	// Unknown game uid / sentinel 1 (credentials changed, never logged in) /
	// nil db all refuse without panicking.
	if _, ok := UserForAutoLogin(db, 424242); ok {
		t.Fatal("unknown game uid must refuse")
	}
	if _, ok := UserForAutoLogin(db, 1); ok {
		t.Fatal("gameUID 0/1 sentinel must refuse")
	}
	if _, ok := UserForAutoLogin(nil, uid); ok {
		t.Fatal("nil db must refuse")
	}
}
