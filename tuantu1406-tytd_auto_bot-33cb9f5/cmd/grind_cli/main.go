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
	var players []*tytd_go.Player

	for i, acc := range accounts {
		select {
		case <-ctx.Done():
			fmt.Println("\n🛑 Nhận tín hiệu dừng, ngừng khởi chạy các tài khoản còn lại...")
			break
		default:
		}

		p := &tytd_go.Player{
			Username:      acc.Username,
			Password:      acc.Password,
			ServerID:      acc.ServerID,
			GrindRun:      true,
			Peach:         true,
			Maohiem:       true,
			ClanMember:    make(map[uint64]string),
			NotiChan:      make(chan tytd_go.NotifyMGS, 64),
			AutoStartChan: make(chan uint64, 10),
		}
		players = append(players, p)

		wg.Add(1)
		go func(idx int, player *tytd_go.Player) {
			defer wg.Done()
			fmt.Printf("[%d/%d] 🔑 Đang đăng nhập: %s (Server: %d)...\n", idx+1, len(accounts), player.Username, player.ServerID)
			
			// Khởi động bot trực tiếp không qua Telegram
			ok := player.Run()
			if !ok {
				fmt.Printf("[%s] ❌ Đăng nhập / kết nối thất bại!\n", player.Username)
			}
		}(i, p)

		// Độ trễ an toàn giữa các lượt login tránh Funtap chặn IP do spam
		if i < len(accounts)-1 {
			time.Sleep(time.Duration(*delaySec) * time.Second)
		}
	}

	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("✨ Tất cả tài khoản đã được kích hoạt chế độ Cày Level 100 tự động.")
	fmt.Println("💡 Nhấn Ctrl + C để dừng phần mềm và ngắt kết nối an toàn.")

	// Chờ tín hiệu kết thúc từ người dùng
	<-ctx.Done()
	fmt.Println("\n⏳ Đang ngắt kết nối các tài khoản an toàn...")

	// Dừng từng player
	for _, p := range players {
		p.StopGrind100()
	}

	time.Sleep(2 * time.Second)
	fmt.Println("👋 Đã dừng phần mềm. Tạm biệt đạo hữu!")
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
