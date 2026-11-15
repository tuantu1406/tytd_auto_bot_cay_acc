package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"tytd_go"
)

func main() {
	filePath := flag.String("file", "", "Đường dẫn file tài khoản (.xlsx hoặc .csv)")
	delaySec := flag.Int("delay", 3, "Độ trễ (giây) giữa các lần đăng nhập tài khoản")
	flag.Parse()

	printBanner()

	// 1. Tự động tìm file tài khoản nếu không truyền qua flag
	targetFile := *filePath
	if targetFile == "" {
		if fileExists("accounts.xlsx") {
			targetFile = "accounts.xlsx"
		} else if fileExists("accounts.csv") {
			targetFile = "accounts.csv"
		} else {
			// Tạo file mẫu nếu chưa có
			targetFile = "accounts.csv"
			_ = tytd_go.CreateSampleCSV(targetFile)
			fmt.Printf("⚠️ Chưa tìm thấy file tài khoản. Đã tạo sẵn file mẫu: %s\n", targetFile)
			fmt.Printf("👉 Vui lòng mở file '%s' bằng Excel, điền danh sách tài khoản (username, password, server_id), lưu lại và chạy lại tool!\n", targetFile)
			return
		}
	}

	fmt.Printf("📂 Đang nạp danh sách tài khoản từ: %s...\n", targetFile)
	accounts, err := tytd_go.LoadAccounts(targetFile)
	if err != nil {
		log.Fatalf("❌ Lỗi nạp file tài khoản: %v", err)
	}

	if len(accounts) == 0 {
		fmt.Printf("⚠️ File '%s' không có tài khoản hợp lệ nào. Vui lòng kiểm tra lại nội dung!\n", targetFile)
		return
	}

	fmt.Printf("✅ Đã tìm thấy %d tài khoản hợp lệ.\n", len(accounts))
	fmt.Println("🚀 Bắt đầu tiến trình tự động cày acc lên Level 100...")
	fmt.Println("--------------------------------------------------------------------------------")

	// Quản lý tín hiệu dừng (Ctrl+C)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
startAccounts:
	for i, acc := range accounts {
		select {
		case <-ctx.Done():
			fmt.Println("\n🛑 Nhận tín hiệu dừng, ngừng khởi chạy các tài khoản còn lại...")
			break startAccounts
		default:
		}

		wg.Add(1)
		go func(idx int, account tytd_go.GameAccount) {
			defer wg.Done()
			runGrindAccount(ctx, idx+1, len(accounts), account)
		}(i, acc)

		// Độ trễ an toàn giữa các lượt login tránh Funtap chặn IP do spam
		if i < len(accounts)-1 {
			delay := time.Duration(*delaySec) * time.Second
			if delay < 0 {
				delay = 0
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				fmt.Println("\n🛑 Nhận tín hiệu dừng, ngừng khởi chạy các tài khoản còn lại...")
				break startAccounts
			case <-timer.C:
			}
		}
	}

	if ctx.Err() == nil {
		fmt.Println("--------------------------------------------------------------------------------")
		fmt.Println("✨ Đã khởi chạy quản lý tự động cho tất cả tài khoản.")
		fmt.Println("💡 Nhấn Ctrl + C để dừng phần mềm và ngắt kết nối an toàn.")

		// Chờ tín hiệu kết thúc từ người dùng
		<-ctx.Done()
	}
	fmt.Println("\n⏳ Đang ngắt kết nối các tài khoản an toàn...")
	wg.Wait()
	fmt.Println("👋 Đã dừng phần mềm. Tạm biệt đạo hữu!")
}

const (
	firstReconnectDelay = 10 * time.Second
	maxReconnectDelay   = 2 * time.Minute
	stableSessionPeriod = 5 * time.Minute
)

func runGrindAccount(ctx context.Context, index, total int, account tytd_go.GameAccount) {
	delay := firstReconnectDelay
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return
		}
		player := &tytd_go.Player{
			Username:      account.Username,
			Password:      account.Password,
			ServerID:      account.ServerID,
			GrindRun:      true,
			ClanMember:    make(map[uint64]string),
			NotiChan:      make(chan tytd_go.NotifyMGS, 64),
			AutoStartChan: make(chan uint64, 10),
		}
		fmt.Printf("[%d/%d] 🔑 Đang đăng nhập: %s (Server: %d, lần %d)...\n", index, total, player.Username, player.ServerID, attempt)

		stopWatchDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				player.StopConnection()
			case <-stopWatchDone:
			}
		}()

		started := time.Now()
		connected := player.Run()
		close(stopWatchDone)
		if ctx.Err() != nil {
			player.StopConnection()
			return
		}

		if time.Since(started) >= stableSessionPeriod {
			delay = firstReconnectDelay
		}
		if connected {
			fmt.Printf("[%s] ⚠️ Phiên game đã ngắt; sẽ đăng nhập lại sau %s.\n", player.Username, delay)
		} else {
			fmt.Printf("[%s] ⚠️ Đăng nhập/kết nối thất bại; sẽ thử lại sau %s.\n", player.Username, delay)
		}
		if !waitContext(ctx, delay) {
			return
		}
		delay *= 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func printBanner() {
	fmt.Println(`
╔══════════════════════════════════════════════════════════════════════════════╗
║               TYTD AUTO GRIND TOOL (CÀY ACC LÊN LEVEL 100)                   ║
║                     (Phiên bản Độc lập - Không cần Telegram)                 ║
║                                                                              ║
║  • Tự chặt đào & mặc đồ tăng lực chiến (Auto-Equip)                          ║
║  • Tự nâng cấp Tiên Thụ, xin trợ giúp bang & nhận quả                        ║
║  • Tự vượt ải Mạo Hiểm (có thuật toán chống kẹt khi gặp quái mạnh)           ║
║  • Tự quét, hoàn thành và nhận thưởng Nhiệm Vụ Tuyến Chính                   ║
║  • Hỗ trợ import danh sách tài khoản hàng loạt từ file Excel (.xlsx / .csv) ║
╚══════════════════════════════════════════════════════════════════════════════╝`)
}
