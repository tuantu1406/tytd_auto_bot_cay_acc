package xiyoujs

// Go-side read-only views over the embedded 幻境 (Secret) tables — parsed
// once from the same db/*.json TextAssets the vm loads. The engine needs:
// XiYouSecretDB row (verifyPower gate, timelimit, door template pool) and
// XiYouSecretTemplateDB star (auto door pick = starSelect rule).

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// SecretRow is what the engine reads from XiYouSecretDB.
type SecretRow struct {
	ID          int32
	VerifyPower int64  // power gate (client CheckEnough… also needs energy item)
	Timelimit   int32  // seconds (wire timeLimit = 1000× this)
	Templates   []int32 // door template pool ("grp|tplId;…" second segment)
}

type tmplRow struct {
	Star int32
}

var (
	secretDBOnce sync.Once
	secretRows   map[int32]*SecretRow
	templateStar map[int32]int32
	secretDBErr  error
)

// tableRows parses the Cocos TextAsset → {Table: [[keyCol],[header…],[row…]}
// shape used by every XiYou table.
func tableRows(file, table string) (header []string, rows [][]interface{}, err error) {
	raw, err := dbFS.ReadFile("assets/db/" + file)
	if err != nil {
		return nil, nil, err
	}
	var ta []interface{}
	if err := json.Unmarshal(raw, &ta); err != nil {
		return nil, nil, err
	}
	// o[5][0][2] = JSON text (see loadDBs/glue.js convention)
	if len(ta) < 6 {
		return nil, nil, fmt.Errorf("%s: short TextAsset", file)
	}
	o5, _ := ta[5].([]interface{})
	if len(o5) < 1 {
		return nil, nil, fmt.Errorf("%s: no o[5]", file)
	}
	o50, _ := o5[0].([]interface{})
	if len(o50) < 3 {
		return nil, nil, fmt.Errorf("%s: no o[5][0][2]", file)
	}
	txt, _ := o50[2].(string)
	var all map[string][][]interface{}
	if err := json.Unmarshal([]byte(txt), &all); err != nil {
		return nil, nil, err
	}
	tbl := all[table]
	if len(tbl) < 2 {
		return nil, nil, fmt.Errorf("%s: table %s empty", file, table)
	}
	for _, v := range tbl[1] {
		s, _ := v.(string)
		header = append(header, s)
	}
	return header, tbl[2:], nil
}

func col(h []string, name string) int {
	for i, c := range h {
		if c == name {
			return i
		}
	}
	return -1
}

func loadSecretDB() {
	h, rows, err := tableRows("XiYouSecretDB.json", "XiYouSecretDB")
	if err != nil {
		secretDBErr = err
		return
	}
	iID, iVP, iTL, iTpl := col(h, "id"), col(h, "verifyPower"), col(h, "timelimit"), col(h, "template")
	if iID < 0 || iVP < 0 || iTpl < 0 {
		secretDBErr = fmt.Errorf("XiYouSecretDB: missing cols")
		return
	}
	secretRows = map[int32]*SecretRow{}
	for _, r := range rows {
		id := int32(toF(r[iID]))
		row := &SecretRow{ID: id, VerifyPower: int64(toF(r[iVP]))}
		if iTL >= 0 {
			row.Timelimit = int32(toF(r[iTL]))
		}
		s, _ := r[iTpl].(string)
		for _, e := range strings.Split(s, ";") {
			parts := strings.Split(e, "|")
			if len(parts) == 2 {
				if t, err := strconv.ParseInt(parts[1], 10, 32); err == nil {
					row.Templates = append(row.Templates, int32(t))
				}
			}
		}
		secretRows[id] = row
	}
	th, trows, err := tableRows("XiYouSecretTemplateDB.json", "XiYouSecretTemplateDB")
	if err != nil {
		secretDBErr = err
		return
	}
	tiID, tiStar := col(th, "id"), col(th, "star")
	templateStar = map[int32]int32{}
	for _, r := range trows {
		templateStar[int32(toF(r[tiID]))] = int32(toF(r[tiStar]))
	}
}

func toF(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

// SecretRowOf returns the XiYouSecretDB row for id.
func SecretRowOf(id int32) (*SecretRow, error) {
	secretDBOnce.Do(loadSecretDB)
	if secretDBErr != nil {
		return nil, secretDBErr
	}
	r, ok := secretRows[id]
	if !ok {
		return nil, fmt.Errorf("no secret row %d", id)
	}
	return r, nil
}

// StarOfTemplate returns the star of a XiYouSecretTemplateDB row (0 = unknown).
func StarOfTemplate(id int32) int32 {
	secretDBOnce.Do(loadSecretDB)
	if templateStar == nil {
		return 0
	}
	return templateStar[id]
}
