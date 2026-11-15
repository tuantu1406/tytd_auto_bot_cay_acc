package tytd_go

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"tytd_go/internal/db"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// webAppKB builds a one-button inline keyboard that opens url as a Telegram
// Mini App (full-screen web view over the existing web session/initData
// login). Empty url returns nil so callers can pass it unconditionally — an
// unconfigured domain simply means "no button".
func webAppKB(url, text string) models.ReplyMarkup { // ReplyMarkup = any in this lib
	if url == "" {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: text, WebApp: &models.WebAppInfo{URL: url}}},
	}}
}

type NotifyMGS struct {
	MSG    string
	ChatID int64
}

type TeleBot struct {
	Token string
	// AdminID is the Telegram chat ID seeded into ConfigKeyAdminChatID on
	// first run. It comes from config.yaml (appconfig), never hardcoded.
	AdminID       int64
	session       sync.Map
	ydb           db.Store
	notiChan      chan NotifyMGS
	autoStartChan chan uint64
	Debug         bool
}

// sessionDel deletes the session entry only while it still maps to p. Stop
// paths cancel and delete immediately, but the old Run wrapper deletes its key
// up to 5 s later; a re-login inside that window used to get its FRESH entry
// blind-deleted by the dying wrapper — an orphaned, fully-running account with
// no map reference and no way to stop it (audit 2026-10-06, finding #2).
func (t *TeleBot) sessionDel(key any, p any) {
	if v, ok := t.session.Load(key); ok && v == p {
		t.session.Delete(key)
	}
}

// pushNotify delivers msg to chatID through the durable notification channel
// (drained by the send loop in Run). Unlike a login caller's notify closure it
// is independent of who triggered the action and outlives its HTTP/update
// context — disconnect notices must arrive even when the login came from a web
// page that closed long ago. The send is non-blocking: nil channel (bot not
// running) or a full buffer drops the notice instead of wedging the caller.
func (t *TeleBot) pushNotify(chatID int64, msg string) {
	select {
	case t.notiChan <- NotifyMGS{MSG: msg, ChatID: chatID}:
	default:
	}
}

// startPprofListener is the memory-audit hook: TYTD_PPROF=127.0.0.1:6060
// starts a private pprof listener (heap/goroutine/allocs). Zero footprint
// unless the env is set, and never on the public web server. Called by Run
// and by DebugWeb so deployment and debug runs profile identically.
func startPprofListener() {
	addr := strings.TrimSpace(os.Getenv("TYTD_PPROF"))
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() { log.Println(http.ListenAndServe(addr, mux)) }()
}

func (t *TeleBot) Run() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	startPprofListener()

	opts := []bot.Option{
		bot.WithDefaultHandler(defaultHandler),
	}

	b, err := bot.New(t.Token, opts...)
	if nil != err {
		log.Println("loi telegram")
		log.Println(err.Error())
		return
	}

	t.session = sync.Map{}
	t.notiChan = make(chan NotifyMGS, 1024)
	t.autoStartChan = make(chan uint64, 1024)

	// send notify
	go func() {
		localZone := time.FixedZone("UTC+7", 7*60*60)
		// Burst cap (owner 2026-10-21: "tất cả event khi thông báo thì cũng
		// chỉ thông báo 2 cái ở dưới màn hình thôi, cầm nhiều acc, thao tác
		// nhanh cái này mày gửi thông báo kín cả màn hình"): a chat may
		// receive at most notifyBurstCap messages per notifyBurstWin — the
		// async per-account alerts (GI ⚡ refusals etc.) multiplied by a
		// 20-acc fast click used to bury the screen. Dropped alerts are
		// still fully in the log; the order fan-out additionally sends ONE
		// aggregated summary per press (webRunEventOrder).
		const notifyBurstCap = 2
		const notifyBurstWin = 10 * time.Second
		buckets := map[int64][]time.Time{}
		for {
			msg := <-t.notiChan

			// Check quiet hours: 22:00 - 22:15 UTC+7
			now := time.Now().In(localZone)
			if now.Hour() == 22 && now.Minute() >= 0 && now.Minute() <= 15 {
				continue
			}

			stamp := time.Now()
			kept := buckets[msg.ChatID][:0]
			for _, ts := range buckets[msg.ChatID] {
				if stamp.Sub(ts) < notifyBurstWin {
					kept = append(kept, ts)
				}
			}
			if len(kept) >= notifyBurstCap {
				buckets[msg.ChatID] = kept
				log.Printf("[notify] chặn dồn chat %d (> %d trong %s): %s", msg.ChatID, notifyBurstCap, notifyBurstWin, msg.MSG)
				continue
			}
			kept = append(kept, stamp)
			buckets[msg.ChatID] = kept

			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: msg.ChatID,
				Text:   msg.MSG,
			})
			// Same message to any open web page of that chat (toast). Placed
			// after the quiet-hours check on purpose: quiet is quiet
			// everywhere. Push is non-blocking, so a dead browser costs nothing.
			webSSEHub.push(msg.ChatID, "notify", map[string]string{"text": msg.MSG})
			time.Sleep(200 * time.Millisecond)
		}
	}()
	t.ydb, err = db.NewDB("./data.dat", getDK())
	if err != nil {
		log.Println("load database error")
		return
	}

	// daily chuot schedule
	t.startChuotScheduler()

	// evict online accounts whose VIP lapsed while online (owner 2026-11-08)
	t.startVipWatchdog()

	// auto login
	go func() {
		for {
			gameUID := <-t.autoStartChan

			// Check if auto login is enabled globally
			autoLoginConfig, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyAutoLogin)
			if err != nil || autoLoginConfig == nil || autoLoginConfig.Value != "true" {
				// log.Printf("Auto login disabled globally, ignoring auto login request for gameUID %d", gameUID)
				continue
			}

			go func() {
				// Multi-row aware + deterministic: ANY owner's auto_login
				// triggers, the deciding row's config executes (see
				// model.UserForAutoLogin — owner 2026-10-27 no-chatid rule).
				u, want := model.UserForAutoLogin(t.ydb.GetDB(), gameUID)
				if !want {
					log.Println("auto login error 1: no auto_login row wants this game uid online", gameUID)
					return
				}

				uid, _ := strconv.ParseUint(u.ID, 10, 64)
				realUID := int64(uid)

				if model.IsBanned(t.ydb.GetDB(), realUID) {
					log.Println("auto login error 2", u.Username)
					return
				}

				_, ok := t.session.Load(realUID)
				if ok {
					// log.Println("auto login error 3", u.Username)
					return
				}

				if u.LeaderID != "" {
					uid, _ = strconv.ParseUint(u.LeaderID, 10, 64)
				}

				//check vip
				if u.VipExpireAt.Before(time.Now()) || u.VipLevel == 0 {
					// Owner 2026-10-27: auto login mà vấp hết VIP thì TẮT
					// auto_login của dòng này luôn — nếu không nó vẫn ON và
					// bị mọi lần bang push lại trigger hoài (gate chỉ loại
					// dòng Running). Sai pass đã có path tắt sẵn bên dưới
					// (Run() false ⇒ SetAutoLogin false).
					if err := model.SetAutoLogin(t.ydb.GetDB(), u.ID, false); err != nil {
						log.Println("auto login: không tắt được auto_login khi VIP hết hạn:", u.Username, err)
					}
					b.SendMessage(ctx, &bot.SendMessageParams{
						ChatID: uid,
						Text: "🛑 " + u.Username + ": auto login vừa TẮT vì tài khoản đã hết hạn VIP. " +
							"Cho nó chạy lại: gia hạn VIP rồi bật /auto_login true (hoặc bỏ tick trong Cài đặt).",
					})
					log.Println("auto login: VIP hết hạn, đã tắt auto_login của", u.Username, "chat", uid)
					return
				}
				// Check temp_auto config - if false, only allow auto-login for users with a LeaderID
				tempAutoConfig, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyTempAuto)
				if err == nil && tempAutoConfig != nil && tempAutoConfig.Value == "false" {
					// If temp_auto is false and user has no leader, skip auto-login
					if u.LeaderID == "" {
						// log.Println("auto login error 4", u.Username)
						return
					}
				}
				// Check max concurrent player
				if t.getRunningCount() >= t.getMaxPlayers() {
					b.SendMessage(ctx, &bot.SendMessageParams{
						ChatID: uid,
						Text:   "auto login but server is full, try again later",
					})
					return
				}
				if !u.LastLoginFail.IsZero() && time.Since(u.LastLoginFail) < 3*time.Minute {
					wait := 3*time.Minute - time.Since(u.LastLoginFail)
					b.SendMessage(ctx, &bot.SendMessageParams{
						ChatID: uid,
						Text:   fmt.Sprintf("login fail, wait %v", wait.Round(time.Second)),
					})
					return
				}
				p := &Player{}
				p.UID = uint64(realUID)
				p.Username = u.Username
				p.Password = u.Password
				p.ServerID = u.ServerID
				p.VipLevel = u.VipLevel
				// Same routing as startPlayerLogin: a member's notices go to
				// the leader, whose chat may be the only real Telegram one.
				p.ChatID = notifyChatFor(u, int64(uid))
				p.NotiChan = t.notiChan
				p.AutoStartChan = t.autoStartChan
				p.ChuotMinDao = u.MinPeachLevel
				p.ChuotMinBinh = u.MinBottleLevel
				p.ChuotMinBinh = u.MinBottleLevel
				p.ChuotMinLuuly = u.MinLuuLyLevel
				p.BottlePrefered = u.BottlePrefered
				p.DB = t.ydb.GetDB()
				p.seedAutoGates(u)
				if u.AutoMouse && chuotLoginRun(true, u.ChuotStartSchedule, u.ChuotStopSchedule, time.Now()) {
					p.ChuotRun = true
				}
				p.ClanMember = make(map[uint64]string)

				p.Notify = false
				p.OnRunningChange = func() { t.pushWebState(realUID) }
				t.session.Store(realUID, p)
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: uid,
					Text:   "go go! logged in " + u.Username,
				})

				u.Running = true
				model.CreateUser(t.ydb.GetDB(), u)
				go func() {
					if !p.Run() {
						// Disable auto login for this account on login failure, so it is not picked up again
						if err := model.SetAutoLogin(t.ydb.GetDB(), u.ID, false); err != nil {
							log.Println("auto login error: failed to disable auto login", u.Username, err)
						}
						model.SetLastLoginFail(t.ydb.GetDB(), u.ID)
					}
					b.SendMessage(ctx, &bot.SendMessageParams{
						ChatID: uid,
						Text:   "bi da' ra roi` đạo hữu " + u.Username,
					})
					u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", realUID))
					if err != nil {
						t.sessionDel(realUID, p)
						t.pushWebState(realUID)
						return
					}
					u.Running = false
					model.CreateUser(t.ydb.GetDB(), u)
					t.sessionDel(realUID, p)
					t.pushWebState(realUID)
				}()
			}()

			time.Sleep(time.Millisecond * 100)
		}
	}()

	// Seed default admin config if not exists
	_, err = model.GetConfig(t.ydb.GetDB(), model.ConfigKeyAdminChatID)
	if err != nil {
		// Assume record not found or error, try to set default
		if t.AdminID > 0 {
			log.Println("Seeding default admin chat ID")
			model.SetConfig(t.ydb.GetDB(), model.ConfigKeyAdminChatID, strconv.FormatInt(t.AdminID, 10))
		}
	}

	// Seed default max concurrent player if not exists
	_, err = model.GetConfig(t.ydb.GetDB(), model.ConfigKeyMaxConcurrentPlayer)
	if err != nil {
		log.Println("Seeding default max concurrent player")
		model.SetConfig(t.ydb.GetDB(), model.ConfigKeyMaxConcurrentPlayer, "50")
	}

	// Seed and load debug mode (config.yaml's debug only bootstraps a fresh
	// DB; once seeded, the in-DB toggle is the runtime source of truth)
	debugCfg, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyDebugMode)
	if err != nil {
		log.Println("Seeding default debug mode")
		seedDebug := "false"
		if t.Debug {
			seedDebug = "true"
		}
		model.SetConfig(t.ydb.GetDB(), model.ConfigKeyDebugMode, seedDebug)
		t.Debug = seedDebug == "true"
	} else {
		t.Debug = debugCfg.Value == "true"
	}

	// Configure GORM logger based on debug mode
	t.ydb.SetDebugMode(t.Debug)

	// Seed default auth key if not exists
	_, err = model.GetConfig(t.ydb.GetDB(), model.ConfigKeyAuthKey)
	if err != nil {
		log.Println("Seeding default auth key")
		model.SetConfig(t.ydb.GetDB(), model.ConfigKeyAuthKey, "bananhviet")
	}

	// Seed default auto_login if not exists
	_, err = model.GetConfig(t.ydb.GetDB(), model.ConfigKeyAutoLogin)
	if err != nil {
		log.Println("Seeding default auto_login")
		model.SetConfig(t.ydb.GetDB(), model.ConfigKeyAutoLogin, "false")
	}

	// Seed default auto_tu_linh gate if not exists — open keeps the old
	// behaviour (gathering ran for everyone before the gate existed). The
	// seed_auto_gather key survives only as db.NewDB's one-shot backfill
	// marker for the users.auto_gather column.
	_, err = model.GetConfig(t.ydb.GetDB(), model.ConfigKeyAutoTuLinh)
	if err != nil {
		log.Println("Seeding default auto_tu_linh")
		model.SetConfig(t.ydb.GetDB(), model.ConfigKeyAutoTuLinh, "true")
	}

	// Seed default temp_auto if not exists
	_, err = model.GetConfig(t.ydb.GetDB(), model.ConfigKeyTempAuto)
	if err != nil {
		log.Println("Seeding default temp_auto")
		model.SetConfig(t.ydb.GetDB(), model.ConfigKeyTempAuto, "true")
	}

	// OWNER 2026-10-21: "thay vì check user nào đang running rồi thông báo
	// bot restarted thì cho acc đó login luôn khỏi thông báo" — the accounts
	// that were running when the bot died are logged straight back in, with
	// NO Telegram message at all (startPlayerLogin's notify goes to the log).
	// The running SET is captured here, before the resets below clear the
	// flags; the logins only launch after both resets finished, so a fresh
	// session's Running=true can never be wiped by the reset racing it.
	resumeUsers, err := model.GetRunningUsers(t.ydb.GetDB())
	if err != nil {
		log.Printf("startup resume: GetRunningUsers: %v", err)
	}

	// Reset all running status to false on startup
	log.Println("Resetting all running status to false")
	err = model.ResetAllRunningStatus(t.ydb.GetDB())
	if err != nil {
		log.Printf("Failed to reset running status: %v", err)
	}

	// Same for event users (the session map is empty after a restart)
	err = model.ResetAllEventRunningStatus(t.ydb.GetDB())
	if err != nil {
		log.Printf("Failed to reset event running status: %v", err)
	}

	if len(resumeUsers) > 0 {
		log.Printf("startup resume: %d acc đang chạy — tự login lại, không thông báo", len(resumeUsers))
	}
	go func() {
		for _, user := range resumeUsers {
			if user.LeaderID != "" {
				continue // clone members: leader-side owns their lifecycle (same set the old notice pass skipped)
			}
			uid, errParse := strconv.ParseInt(user.ID, 10, 64)
			if errParse != nil {
				continue
			}
			if model.IsBanned(t.ydb.GetDB(), uid) {
				log.Printf("startup resume: bỏ qua %s (banned)", user.Username)
				continue
			}
			if user.VipLevel == 0 || user.VipExpireAt.Before(time.Now()) {
				log.Printf("startup resume: bỏ qua %s (VIP hết hạn)", user.Username)
				continue
			}
			// Same core as /login and the web: running-check, 3-min fail
			// cooldown, max-concurrent gate, session store, p.Run() goroutine,
			// Running bookkeeping, kick notice on death — zero duplicated
			// login code. notify stays in the log: the owner asked for NO
			// restart spam; real disconnect notices still flow via pushNotify
			// inside startPlayerLogin like any other login.
			id := uid
			name := user.Username
			go func() {
				if t.startPlayerLogin(id, func(text string) {
					log.Printf("startup resume [%s]: %s", name, text)
				}) {
					log.Printf("startup resume: %s @%d đang vào lại game", name, user.ServerID)
				}
			}()
			time.Sleep(200 * time.Millisecond) // stagger — never storm the game node with every account at once
		}
	}()

	go func() {
		// print status
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "STT\tUsername\tServerID\tVIP\tRunning\tChuot\tNotify\tChatID\tName\tUID\tClan\tClanID")
				index := 0
				t.session.Range(func(_, value interface{}) bool {
					index += 1
					// session holds *Player and *EventPlayer
					switch p := value.(type) {
					case *Player:
						fmt.Fprintf(w, "%d\t%s\t%d\t%d\t%t\t%t\t%t\t%d\t%s\t%d\t%s\t%d\n",
							index, p.Username, p.ServerID, p.VipLevel, p.Running, p.ChuotRun, p.Notify, p.ChatID, p.Name, p.UID, p.ClanName, p.ClanID)
					case *EventPlayer:
						// event users have no VIP level and no chuot
						fmt.Fprintf(w, "%d\t%s(event)\t%d\t%d\t%t\t%t\t%t\t%d\t%s\t%d\t%s\t%d\n",
							index, p.Username, p.ServerID, 0, p.Running, false, p.Notify, p.ChatID, p.Name, p.UID, p.ClanName, p.ClanID)
					}
					return true
				})
				w.Flush()

			}
		}
	}()

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, t.StartHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/my_id", bot.MatchTypeExact, t.MyIDHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auth", bot.MatchTypePrefix, t.authHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/username", bot.MatchTypePrefix, t.setUsernameHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/password", bot.MatchTypePrefix, t.setPasswordHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/server_id", bot.MatchTypePrefix, t.setServerIDHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/check_config", bot.MatchTypeExact, t.checkConfigHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/login", bot.MatchTypeExact, t.loginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/logout", bot.MatchTypeExact, t.logoutHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_chuot", bot.MatchTypeExact, t.startChuotHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_chuot", bot.MatchTypeExact, t.stopChuotHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_notify", bot.MatchTypeExact, t.startNotifyHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_notify", bot.MatchTypeExact, t.stopNotifyHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_maohiem", bot.MatchTypeExact, t.startMaohiemHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_maohiem", bot.MatchTypeExact, t.stopMaohiemHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_taydu", bot.MatchTypeExact, t.startXiyouHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_taydu", bot.MatchTypeExact, t.stopXiyouHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_taydu_team", bot.MatchTypeExact, t.startXiyouTeamHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_lv100", bot.MatchTypeExact, t.startGrind100Handler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_lv100", bot.MatchTypeExact, t.stopGrind100Handler)

	// Event delegation (uỷ thác). Register the two exact commands BEFORE the
	// /uythac prefix handler — MatchTypePrefix("/uythac") would otherwise also
	// swallow "/uythac_xem". All per-account (the caller's own account).
	b.RegisterHandler(bot.HandlerTypeMessageText, "/uythac_xem", bot.MatchTypeExact, t.uythacXemHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bo_uythac", bot.MatchTypeExact, t.boUythacHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/uythac", bot.MatchTypePrefix, t.uythacHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_lucdao", bot.MatchTypeExact, t.startLucdaoHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_lucdao", bot.MatchTypeExact, t.stopLucdaoHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_huyet_mach", bot.MatchTypeExact, t.startHuyetMachHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_huyet_mach", bot.MatchTypeExact, t.stopHuyetMachHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_yeuthap", bot.MatchTypeExact, t.startYeuthapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_yeuthap", bot.MatchTypeExact, t.stopYeuthapHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/min_binh", bot.MatchTypePrefix, t.setChuotBottleHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/min_dao", bot.MatchTypePrefix, t.setChuotPeachHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/min_luuly", bot.MatchTypePrefix, t.setChuotLuuLyHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/info", bot.MatchTypeExact, t.GetConfigHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ditich_map", bot.MatchTypeExact, t.ballGVGMapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bonglai_map", bot.MatchTypeExact, t.bonglaiMapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thienma_map", bot.MatchTypeExact, t.thienmaMapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/cuuu_map", bot.MatchTypeExact, t.cuuuMapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/kunlun_map", bot.MatchTypeExact, t.kunlunMapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/kunlun_info", bot.MatchTypeExact, t.kunlunInfoHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/lk", bot.MatchTypeExact, t.lkHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/lk_choi", bot.MatchTypeExact, t.lkChoiHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/lk_dung", bot.MatchTypeExact, t.lkDungHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_luyenkhi", bot.MatchTypeExact, t.startLuyenKhiHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/kunlun_follow", bot.MatchTypeExact, t.kunlunFollowHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/kunlun_hoisinh", bot.MatchTypePrefix, t.KunlunHoiSinhHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bacminh_map", bot.MatchTypeExact, t.bacminhMapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bacminh_info", bot.MatchTypeExact, t.bacminhInfoHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bacminh_follow", bot.MatchTypeExact, t.bacminhFollowHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bacminh_hoisinh", bot.MatchTypePrefix, t.BacMinhHoiSinhHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_login", bot.MatchTypePrefix, t.UserSetAutoLoginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_start_chuot", bot.MatchTypePrefix, t.UserSetAutoMouseHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_tu_linh", bot.MatchTypePrefix, t.UserSetAutoGatherHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_thiendo_tienvuc", bot.MatchTypePrefix, t.UserSetAutoSkyTradeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_tangtoc_tienvuc", bot.MatchTypePrefix, t.UserSetAutoSkyTradeSpeedHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/luanhoi_shop", bot.MatchTypeExact, t.rbtShopHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/luanhoi_attack", bot.MatchTypePrefix, t.rbtAttackHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/luanhoi_ve", bot.MatchTypePrefix, t.rbtTicketHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/luanhoi_map", bot.MatchTypeExact, t.rbtMapHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_luanhoi", bot.MatchTypePrefix, t.UserSetAutoRebornTrialHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/tiencanh_shop", bot.MatchTypeExact, t.wyShopHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_tiencanh", bot.MatchTypePrefix, t.UserSetAutoTienCanhHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/auto_thuongminh", bot.MatchTypePrefix, t.UserSetAutoSeaWarHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thuongminh_suc", bot.MatchTypePrefix, t.SeaWarSucHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/dangioi", bot.MatchTypeExact, t.danGioiHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/tu_linh_min", bot.MatchTypePrefix, t.UserSetGatherMinHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/schedule_start_chuot", bot.MatchTypePrefix, t.scheduleStartChuotHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/schedule_stop_chuot", bot.MatchTypePrefix, t.scheduleStopChuotHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/schedule_login", bot.MatchTypePrefix, t.scheduleLoginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/schedule_logout", bot.MatchTypePrefix, t.scheduleLogoutHandler)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/member", bot.MatchTypeExact, t.GetMember)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_chat_cay", bot.MatchTypeExact, t.startChatcayHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_chat_cay_xave", bot.MatchTypeExact, t.startChatcayXaVeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stop_chat_cay", bot.MatchTypeExact, t.stopChatcayHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add_code", bot.MatchTypePrefix, t.AddGiftCodeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/gen_codes", bot.MatchTypePrefix, t.GenGiftCodesHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/redeem", bot.MatchTypePrefix, t.RedeemHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/del_code", bot.MatchTypePrefix, t.DelGiftCodeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/list_codes", bot.MatchTypeExact, t.ListGiftCodesHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add_black_clan", bot.MatchTypePrefix, t.AddBlacklistClanHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/del_black_clan", bot.MatchTypePrefix, t.DelBlacklistClanHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/list_black_clan", bot.MatchTypeExact, t.ListBlacklistClanHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_max_player", bot.MatchTypePrefix, t.SetMaxPlayerHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/get_setting", bot.MatchTypeExact, t.GetSettingHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bottle_pref", bot.MatchTypePrefix, t.SetBottlePreferedHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_debug", bot.MatchTypePrefix, t.SetDebugHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_auth_key", bot.MatchTypePrefix, t.SetAuthKeyHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_must_join_group", bot.MatchTypePrefix, t.SetMustJoinGroupHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_auto_login", bot.MatchTypePrefix, t.SetAutoLoginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_auto_tu_linh", bot.MatchTypePrefix, t.SetAutoTuLinhHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_temp_auto", bot.MatchTypePrefix, t.SetTempAutoHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/broadcast", bot.MatchTypePrefix, t.BroadcastHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_leader", bot.MatchTypePrefix, t.SetLeaderHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/list_leaders", bot.MatchTypeExact, t.ListLeadersHandler)
	// Event user (leader-managed online-only accounts). The "/add_member_event"
	// and "/remove_member_event" prefix handlers MUST be registered before the
	// "/add_member"/"/remove_member" prefixes: the library matches handlers in
	// registration order and those strings also match the shorter prefixes.
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add_member_event", bot.MatchTypePrefix, t.AddEventMemberHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/remove_member_event", bot.MatchTypePrefix, t.RemoveEventMemberHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/my_event_members", bot.MatchTypeExact, t.ListMyEventMembersHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/event_login", bot.MatchTypeExact, t.EventLoginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/event_logout", bot.MatchTypeExact, t.EventLogoutHandler)
	// All three God Island orders take a <city_id> arg, so they match by
	// prefix. "/bonglai_attack_thuve" MUST be registered before
	// "/bonglai_attack" (it is a prefix of the former) and before
	// "/bonglai_move" — the library picks the first matching handler.
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bonglai_attack_thuve", bot.MatchTypePrefix, t.BonglaiAttackThuVeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bonglai_attack", bot.MatchTypePrefix, t.BonglaiAttackHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bonglai_move", bot.MatchTypePrefix, t.BonglaiMoveHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bonglai_canqua", bot.MatchTypePrefix, t.BonglaiCanQuaHandler)
	// no-arg commands → exact match (same as /event_login)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bonglai_tangtoc", bot.MatchTypeExact, t.BonglaiTangTocHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bonglai_hoimau", bot.MatchTypeExact, t.BonglaiHoiMauHandler)
	// Thiên Ma (peak island) twins — same ordering rules as the bonglai set.
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thienma_attack_thuve", bot.MatchTypePrefix, t.ThienmaAttackThuVeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thienma_attack", bot.MatchTypePrefix, t.ThienmaAttackHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thienma_move", bot.MatchTypePrefix, t.ThienmaMoveHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thienma_canqua", bot.MatchTypePrefix, t.ThienmaCanQuaHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thienma_tangtoc", bot.MatchTypeExact, t.ThienmaTangTocHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/thienma_hoimau", bot.MatchTypeExact, t.ThienmaHoiMauHandler)
	// Cửu U (Holy Land) orders — same prefix-ordering rules: the
	// attack_thuve prefix must win over /cuuu_attack; /cuuu_hoisinh
	// is a prefix because it takes an optional "item" word.
	b.RegisterHandler(bot.HandlerTypeMessageText, "/cuuu_attack_thuve", bot.MatchTypePrefix, t.CuuuAttackNpcHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/cuuu_attack", bot.MatchTypePrefix, t.CuuuAttackHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/cuuu_move", bot.MatchTypePrefix, t.CuuuMoveHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/cuuu_auto", bot.MatchTypePrefix, t.CuuuAutoHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/cuuu_tangtoc", bot.MatchTypeExact, t.CuuuTangTocHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/cuuu_hoisinh", bot.MatchTypePrefix, t.CuuuHoiSinhHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ditich_enter", bot.MatchTypePrefix, t.DitichEnterHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ditich_mark", bot.MatchTypePrefix, t.DitichMarkHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ditich_ve", bot.MatchTypeExact, t.DitichVeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ditich_leave", bot.MatchTypeExact, t.DitichLeaveHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ditich_rooms", bot.MatchTypeExact, t.DitichRoomsHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ditich_roster", bot.MatchTypeExact, t.DitichRosterHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add_member", bot.MatchTypePrefix, t.AddMemberHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/remove_member", bot.MatchTypePrefix, t.RemoveMemberHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/my_members", bot.MatchTypeExact, t.ListMyMembersHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_member_password", bot.MatchTypePrefix, t.SetMemberPasswordHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/toggle_member_auto_login", bot.MatchTypePrefix, t.ToggleMemberAutoLoginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/toggle_member_tu_linh", bot.MatchTypePrefix, t.ToggleMemberTuLinhHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_start_notify", bot.MatchTypePrefix, t.MemberNotifyHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_stop_notify", bot.MatchTypePrefix, t.MemberNotifyHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_login", bot.MatchTypeExact, t.MemberLoginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_logout", bot.MatchTypeExact, t.MemberLogoutHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_start_chat_cay", bot.MatchTypeExact, t.MemberStartChatCayHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_start_chat_cay_xave", bot.MatchTypeExact, t.MemberStartChatCayXaVeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_start_chuot", bot.MatchTypeExact, t.MemberStartChuotHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_stop_chuot", bot.MatchTypeExact, t.MemberStopChuotHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_schedule_start_chuot", bot.MatchTypePrefix, t.MemberScheduleStartChuotHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_schedule_stop_chuot", bot.MatchTypePrefix, t.MemberScheduleStopChuotHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_schedule_login", bot.MatchTypePrefix, t.MemberScheduleLoginHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_schedule_logout", bot.MatchTypePrefix, t.MemberScheduleLogoutHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/member_stop_chat_cay", bot.MatchTypeExact, t.MemberStopChatCayHandler)

	// Ban management handlers (admin only)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ban_user", bot.MatchTypePrefix, t.BanUserHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/unban_user", bot.MatchTypePrefix, t.UnbanUserHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/list_banned", bot.MatchTypeExact, t.ListBannedUsersHandler)

	// Good user management handlers (admin only)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add_good_user", bot.MatchTypePrefix, t.AddGoodUserHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/remove_good_user", bot.MatchTypePrefix, t.RemoveGoodUserHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/list_good_users", bot.MatchTypeExact, t.ListGoodUsersHandler)

	// AI Chatbot handlers
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_groq_key", bot.MatchTypePrefix, t.SetGroqAPIKeyHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_copilot_key", bot.MatchTypePrefix, t.SetCopilotAPIKeyHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_chatbot", bot.MatchTypePrefix, t.SetChatbotEnabledHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/set_group_chat_id", bot.MatchTypePrefix, t.SetGroupChatIDHandler)

	// Web dashboard switchboard (admin only)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/web", bot.MatchTypePrefix, t.WebConfigHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start_thanlong", bot.MatchTypePrefix, t.StartThanLongHandler)

	// MCP-over-HTTP switchboard (admin only)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/mcp", bot.MatchTypePrefix, t.MCPConfigHandler)

	// Big Chess external play engine (admin only)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bcengine", bot.MatchTypePrefix, t.BCEngineConfigHandler)

	// Register unknown command handler (catches all unregistered /commands)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/", bot.MatchTypePrefix, t.UnknownCommandHandler)

	// Register group message handler (runs for all messages, including new member greetings)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, t.AIGroupMentionHandler)

	// Web dashboard: always listening, answers only once /web turns it on.
	go t.StartWebServer()

	// MCP-over-HTTP: same idea — always listening, answers only once the
	// admin turns it on AND sets a key (/mcp on, /mcp key …).
	go t.StartMCPServer()

	b.Start(ctx)
}

func defaultHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text: `👋 <b>Xin chào! Chào mừng đến với TYTD Tool Bot</b>

🤖 <b>Giới thiệu:</b>
Bot hỗ trợ tự động hóa game TYTD. Giúp bạn treo acc không cần bật điện thoại vẫn nhặt ngọc auto chuột, mạo hiểm, lục đạo, yêu tháp và nhiều tính năng khác.

🚀 <b>Bắt đầu sử dụng:</b>
Gõ <code>/start</code> để xem danh sách đầy đủ các lệnh

📌 <b>Lệnh cơ bản:</b>
• <code>/auth {key}</code> - Kích hoạt bot
• <code>/login</code> - Đăng nhập vào game
• <code>/logout</code> - Đăng xuất khỏi game
• <code>/info</code> - Xem thông tin tài khoản
• <code>/my_id</code> - Xem Chat ID của bạn

💬 Cần hỗ trợ? Vào group @tytd_tool_support`,
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
	})

	if err == nil && msg != nil {
		// Auto-delete message after 30 seconds
		go func() {
			time.Sleep(60 * time.Second)
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    update.Message.Chat.ID,
				MessageID: msg.ID,
			})
		}()
	}
}

// patchAutoGates applies an opt-in change to the account's LIVE session
// immediately (owner 2026-10-27). Event loops read their gates as Player
// fields, never the DB — so every path that persists a toggle (chat
// /auto_* commands, own settings page, leader's member settings) must
// touch BOTH: the DB row (survives re-login) and the running session
// (applies now). key is the SESSION key = the account's row id (own chat
// id for regular users, member id for clone members) — the same key the
// login path seeded the fields from. Silently no-ops when offline.
func (t *TeleBot) patchAutoGates(key int64, patch func(*Player)) {
	if v, ok := t.session.Load(key); ok {
		if p, ok := v.(*Player); ok && patch != nil {
			patch(p)
		}
	}
}

// seedAutoGates copies the five per-event opt-in switches from the row the
// session is being built for. Every Player construction path calls this —
// the row is always addressed by account id, never by game_uid.
func (p *Player) seedAutoGates(u *model.User) {
	p.AutoSkyTrade = u.AutoSkyTrade
	p.AutoSkyTradeSpeed = u.AutoSkyTradeSpeed
	p.AutoGather = u.AutoGather
	p.AutoRebornTrial = u.AutoRebornTrial
	p.AutoTienCanh = u.AutoTienCanh
	p.AutoSeaWar = u.AutoSeaWar
}

func getDK() string {
	// Obfuscated key: "tytd_2025_secret" XOR 0xAA
	obfuscated := []byte{0xde, 0xd3, 0xde, 0xce, 0xf5, 0x98, 0x9a, 0x98, 0x9f, 0xf5, 0xd9, 0xcf, 0xc9, 0xd8, 0xcf, 0xde}
	key := make([]byte, len(obfuscated))
	xorKey := byte(0xAA)
	for i, b := range obfuscated {
		key[i] = b ^ xorKey
	}
	return string(key)
}

func (t *TeleBot) isAdmin(chatID int64) bool {
	cfg, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyAdminChatID)
	if err != nil {
		return false
	}
	adminID, err := strconv.ParseInt(cfg.Value, 10, 64)
	if err != nil {
		return false
	}
	return chatID == adminID
}

func (t *TeleBot) getMaxPlayers() int {
	cfg, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyMaxConcurrentPlayer)
	if err != nil {
		return 1 // Default fallback
	}
	limit, err := strconv.Atoi(cfg.Value)
	if err != nil {
		return 1
	}
	return limit
}

func (t *TeleBot) getRunningCount() int {
	count := 0
	t.session.Range(func(_, value interface{}) bool {
		// session holds *Player and *EventPlayer
		if sessionRunning(value) {
			count++
		}
		return true
	})
	return count
}

func (t *TeleBot) debugLog(format string, args ...interface{}) {
	if t.Debug {
		log.Printf("[DEBUG] "+format, args...)
	}
}
