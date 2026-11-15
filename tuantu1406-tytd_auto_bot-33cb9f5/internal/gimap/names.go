package gimap

// Localized place names extracted from the game's own LanguageWordDB asset
// (GODISLAND_PLACE_NAME_* / GODISLAND_PLACE_TYPE_* / HolyLand_Name-*). The
// official client labels cities exactly these strings, so the web map shows
// what a player sees in-game. Static client text — never any protocol data.

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"sync"
)

//go:embed event_names.json
var eventNamesJSON []byte

type nameTables struct {
	GICities map[string]string `json:"gi_cities"`
	GITypes  map[string]string `json:"gi_types"`
	HLCities map[string]string `json:"hl_cities"`
	KUCities map[string]string `json:"ku_cities"`
	HLTypes  map[string]string `json:"hl_types"`
}

var (
	namesOnce   sync.Once
	namesTables nameTables
)

func loadNames() *nameTables {
	namesOnce.Do(func() {
		_ = json.Unmarshal(eventNamesJSON, &namesTables) // malformed → empty maps, names just fall back
	})
	return &namesTables
}

// giCityName is the client's display name: the dedicated place name, or the
// tier's type name (peak adds tier 6 whose cities have no individual rows).
func giCityName(id uint64) string {
	t := loadNames()
	if n := t.GICities[strconv.FormatUint(id, 10)]; n != "" {
		return n
	}
	return t.GITypes[strconv.FormatUint(id/1000, 10)]
}

// hlCityName is the client's Holy Land display name: the dedicated place
// name (HOLYLAND_PLACE_NAME_*), falling back to the tier's type name.
func hlCityName(id uint64) string {
	t := loadNames()
	if n := t.HLCities[strconv.FormatUint(id, 10)]; n != "" {
		return n
	}
	return t.HLTypes[strconv.FormatUint(id/1000, 10)]
}
