package main

// Print the raw enterUrl from the funtap login (one-off archaeology helper:
// find the web client's bundle origin for remoteRebornTrial analysis).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"tytd_go/internal/request"
)

func main() {
	user, pass := os.Getenv("TYTD_LIVE_USER"), os.Getenv("TYTD_LIVE_PASS")
	if user == "" || pass == "" {
		fmt.Println("set TYTD_LIVE_USER / TYTD_LIVE_PASS (live test account)")
		os.Exit(1)
	}
	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		fmt.Println("lp:", err)
		os.Exit(1)
	}
	params := url.Values{"game_id": {"349"}}
	req, _ := http.NewRequest("GET", "https://h5.slfuntap.com/h5/play_games/enterGameUrl?"+params.Encode(), nil)
	req.Header.Set("User-Agent", "Dalvik/2.1.0 (Linux; U; Android 10; SM-G950FD Build/QQ3A.200805.001)")
	req.AddCookie(&http.Cookie{Name: "X_Passport_Token", Value: pt})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("do:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var api struct {
		Result float64                `json:"result"`
		Data   map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(body, &api) != nil {
		fmt.Println("bad json", string(body)[:200])
		os.Exit(1)
	}
	fmt.Println("enterUrl:", api.Data["enterUrl"])
}
