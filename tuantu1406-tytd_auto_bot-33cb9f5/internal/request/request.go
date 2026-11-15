package request

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ict is Vietnam time — every schedule/lock clock the owner reads is UTC+7
// [[tytd-times-utc7]].
var ict = time.FixedZone("ICT", 7*3600)

// snip clips a response body to a short, single-line, %q-safe excerpt so the
// login-failure log names the REAL cause (HTTP status, error JSON) without
// dumping pages of HTML into stdout/the ring buffer.
func snip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(rỗng)"
	}
	if len(s) > 180 {
		s = s[:180] + "…"
	}
	return fmt.Sprintf("%q", s)
}

func LoginGPassport(username, password string) (string, error) {
	baseURL := "https://gpassport.slfuntap.com/web_login/login"
	params := url.Values{
		"packageName": {"com.global.dqvn.pch5"},
		"uname":       {username},
		"password":    {RSAPKCS1Encrypt(password)},
		"game_id":     {"349"},
	}
	headers := map[string]string{
		"User-Agent": "Dalvik/2.1.0 (Linux; U; Android 10; SM-G950FD Build/QQ3A.200805.001)",
	}
	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())
	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("bước 1 gPassport: HTTP %d (uname=%q) — %s", resp.StatusCode, username, snip(body))
	}
	cookies := resp.Cookies()
	for _, cookie := range cookies {
		if cookie.Name == "X_Passport_Token" {
			return cookie.Value, nil
		}
	}
	body, _ := io.ReadAll(resp.Body)
	return "", fmt.Errorf("bước 1 gPassport: không có X_Passport_Token — sai mật khẩu hoặc acc chưa đăng ký (uname=%q) — %s", username, snip(body))
}

// Loginfuntap return urlEncodedJSON, result["userId"], nil
func LoginFuntap(token string) (string, string, error) {
	baseURL := "https://h5.slfuntap.com/h5/play_games/enterGameUrl"
	params := url.Values{
		"game_id": {"349"},
	}
	headers := map[string]string{
		"User-Agent": "Dalvik/2.1.0 (Linux; U; Android 10; SM-G950FD Build/QQ3A.200805.001)",
	}
	cookies := []*http.Cookie{
		{
			Name:  "X_Passport_Token",
			Value: token,
		},
	}

	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())
	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create request: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("bước 2 funtap: HTTP %d — %s", resp.StatusCode, snip(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("bước 2 funtap: đọc response lỗi: %w", err)
	}

	var apiResponse struct {
		Result float64                `json:"result"`
		Data   map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		return "", "", fmt.Errorf("bước 2 funtap: JSON lạ: %v — %s", err, snip(body))
	}
	if apiResponse.Result != 1 {
		return "", "", fmt.Errorf("bước 2 funtap: result=%v ≠ 1 (token passport hết hạn / acc bị chặn) — %s", apiResponse.Result, snip(body))
	}
	enterURL, ok := apiResponse.Data["enterUrl"].(string)
	if !ok {
		return "", "", fmt.Errorf("bước 2 funtap: không có enterUrl trong data — %s", snip(body))
	}
	parsedURL, err := url.Parse(enterURL)
	if err != nil {
		return "", "", fmt.Errorf("bước 2 funtap: enterUrl hỏng (%q): %w", enterURL, err)
	}

	// decodeURIComponent parity (client requestFormal line-for-line): the game
	// reads uid/timestamp/sign with decodeURIComponent, which does NOT touch
	// '+' — while Go's url.Query() maps '+' to SPACE. A base64-style sign
	// carrying a literal '+' was therefore corrupted into " " here and step 4
	// died on signature check with a bare ret that looked like "wrong
	// password". Split RawQuery and use QueryUnescape (percent-only) so our
	// decoded value equals decodeURIComponent's for EVERY byte.
	queryParams := rawQueryParams(parsedURL.RawQuery)

	result := map[string]string{
		"userId":    getFirstValue(queryParams, "uid"),
		"timeStamp": getFirstValue(queryParams, "timestamp"),
		"sign":      getFirstValue(queryParams, "sign"),
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		return "", "", fmt.Errorf("bước 2 funtap: dựng gói uid/sign lỗi: %w", err)
	}
	urlEncodedJSON := url.QueryEscape(string(jsonBytes))
	return urlEncodedJSON, result["userId"], nil
}

// GetServerURL return url server or "" and error
//
// Example: u, err := GetServerURL("123456", 749550)
func GetServerURL(uid string, serverID int) (string, error) {
	baseURL := "https://login-xddq-vn.gamescombine.com/server/list"
	data := struct {
		OpenID    string `json:"openId"`
		ChannelID int    `json:"channelId"`
	}{
		OpenID:    uid,
		ChannelID: 71,
	}
	jsonData, _ := json.Marshal(data)
	headers := map[string]string{
		"User-Agent":   "Dalvik/2.1.0 (Linux; U; Android 10; SM-G950FD Build/QQ3A.200805.001)",
		"Content-Type": "application/json",
	}

	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("bước 3 server/list: HTTP %d — %s", resp.StatusCode, snip(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("bước 3 server/list: đọc response lỗi: %w", err)
	}

	var response struct {
		ServerList []struct {
			ServerID int    `json:"serverId"`
			Address  string `json:"address"`
		} `json:"serverList"`
	}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return "", fmt.Errorf("bước 3 server/list: JSON lạ: %v — %s", err, snip(body))
	}
	for _, server := range response.ServerList {
		if server.ServerID == serverID {
			return server.Address, nil
		}
	}

	return "", fmt.Errorf("bước 3 server/list: server_id=%d KHÔNG tồn tại trong danh sách %d server (kiểm tra lại /server_id — số lẻ/SPACE/kí tự ẩn sẽ rơi vào đây)", serverID, len(response.ServerList))
}

// GetWSKey return response.Token, response.WsAddress, response.PlayerID, nil
func GetWSKey(baseURL, sessionData string) (string, string, uint64, error) {
	baseURL += "/player/login"
	data := struct {
		Data        string `json:"data"`
		LoginType   int    `json:"loginType"`
		DevicePlate string `json:"deviceplate"`
		DeviceID    string `json:"deviceId"`
		ChannelID   int    `json:"channelId"`
		PackageID   string `json:"packageId"`
	}{
		Data:        sessionData,
		LoginType:   2,
		DevicePlate: "Windows",
		DeviceID:    "1234567121123456789ab1234560123456123456789abc123456789ab12345671234567812345678-4123456789abcd12123456789ab-123456712341234567-123456789abcde1234112345671234561212345671234561234123",
		ChannelID:   71,
		PackageID:   "71004001",
	}
	jsonData, _ := json.Marshal(data)
	headers := map[string]string{
		"User-Agent":   "Dalvik/2.1.0 (Linux; U; Android 10; SM-G950FD Build/QQ3A.200805.001)",
		"Content-Type": "application/json",
	}

	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to create request: %w", err)
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", 0, fmt.Errorf("bước 4 player/login: HTTP %d — %s", resp.StatusCode, snip(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", 0, fmt.Errorf("bước 4 player/login: đọc response lỗi: %w", err)
	}

	var response struct {
		Ret              int    `json:"ret"`
		Token            string `json:"token"`
		WsAddress        string `json:"wsAddress"`
		PlayerID         uint64 `json:"playerId"`
		CreatePlayer     bool   `json:"createPlayer"`
		ForbiddenEndTime int64  `json:"forbiddenEndTime"`
	}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return "", "", 0, fmt.Errorf("bước 4 player/login: JSON lạ: %v — %s", err, snip(body))
	}
	if response.Ret != 0 {
		// Decode the fields the game itself uses to choose the error text:
		// a real ban (forbiddenEndTime>0), or "no character on this server /
		// server not open to this account" (playerId==0 && !createPlayer — the
		// exact haianhz25 ret=34 case). Server-side refusal, NOT a bot bug.
		why := "game từ chối"
		hint := ""
		switch {
		case response.ForbiddenEndTime > 0:
			// 505 with a near-future end = temporary login lock (anti-brute /
			// session guard). A failing auto-login loop CAN keep re-triggering
			// it — tell the owner, don't blame character creation.
			why = fmt.Sprintf("acc bị KHÓA TẠM tới %s (ret=505 — thường do bị đăng nhập/đấu nối sai lặp lại nhiều lần; ngừng bot vài phút rồi mới login lại trong game)", time.UnixMilli(response.ForbiddenEndTime).In(ict).Format("2006-01-02 15:04 (giờ VN)"))
		case response.PlayerID == 0 && !response.CreatePlayer:
			why = "không có nhân vật trên server này (chưa vào game tạo nhân vật ở server đó, hoặc server không mở cho acc này)"
			hint = " (đăng nhập 1 lần trong game để tạo nhân vật rồi bot mới vào được)"
		}
		return "", "", 0, fmt.Errorf("bước 4 player/login: %s — ret=%d, playerId=%d, createPlayer=%v%s — %s", why, response.Ret, response.PlayerID, response.CreatePlayer, hint, snip(body))
	}

	return response.Token, response.WsAddress, response.PlayerID, nil
}

// rawQueryParams parses a URL RawQuery EXACTLY like the game client's
// decodeURIComponent: percent-decode only, '+' stays '+'. url.Query()'s
// form-style '+'→space is the wrong semantic for a base64 sign and was a
// real corruption path (one '+' byte in the signature = login dies at step 4
// with an anonymous ret, indistinguishable from a wrong password).
func rawQueryParams(raw string) url.Values {
	v := url.Values{}
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		k, val, _ := strings.Cut(kv, "=")
		if kd, err := url.PathUnescape(k); err == nil {
			k = kd
		}
		if vd, err := url.PathUnescape(val); err == nil {
			val = vd
		}
		v.Add(k, val)
	}
	return v
}

func getFirstValue(values url.Values, key string) string {
	if vals := values[key]; len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func RSAPKCS1Encrypt(text string) string {
	publicKeyPem := `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDq04c6My441Gj0UFKgrqUhAUg+
kQZeUeWSPlAU9fr4HBPDldAeqzx1UR92KJHuQh/zs1HOamE2dgX9z/2oXcJaqoRIA
/FXysx+z2YlJkSk8XQLcQ8EBOkp//MZrixam7lCYpNOjadQBb2Ot0U/Ky+jF2p+Ie
8gSZ7/u+Wnr5grywIDAQAB
-----END PUBLIC KEY-----`

	// Parse public key
	block, _ := pem.Decode([]byte(publicKeyPem))
	if block == nil {
		return ""
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return ""
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return ""
	}
	// Add timestamp
	timestamp := time.Now().UnixMilli()
	payload := fmt.Sprintf("%d|%s", timestamp, text)

	// Encrypt using PKCS1 v1.5
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPub, []byte(payload))
	if err != nil {
		return ""
	}

	// Base64 encode
	b64Encoded := base64.StdEncoding.EncodeToString(encrypted)

	// URL encode (single encoding like the Python version)
	uriEncoded := url.QueryEscape(b64Encoded)

	return uriEncoded
}
