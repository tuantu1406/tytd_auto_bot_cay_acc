package tytd_go

import (
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Linh điền phát nước miễn phí mỗi ngày 2 suất theo giờ VN: 11:00–14:00 và
// 17:00–21:00. Loop này gửi lệnh nhận (fire-and-forget như autoNhanQuaCay)
// đúng một lần cho mỗi suất trong ngày, không cần bật/tắt.

// elixirSlotBoundaries[startHour, endHour] per ICT day, in claim order.
var elixirSlotBoundaries = [][2]int{{11, 14}, {17, 21}}

// elixirWindow trả về chỉ số suất nước đang mở theo giờ VN của now,
// hoặc -1 nếu ngoài cả hai khung.
func elixirWindow(now time.Time) int {
	h := now.In(ictZone).Hour()
	for i, b := range elixirSlotBoundaries {
		if h >= b[0] && h < b[1] {
			return i
		}
	}
	return -1
}

// elixirClaimTick runs one poll: resets the per-day flags on a new ICT day
// and sends the claim for the open slot if it is still unclaimed. It reports
// whether a frame went out. claimed/claimedDay are owned by autoElixirWater.
func (p *Player) elixirClaimTick(now time.Time, claimed *[2]bool, claimedDay *string) bool {
	if day := now.In(ictZone).Format("2006-01-02"); day != *claimedDay {
		*claimedDay = day
		*claimed = [2]bool{}
	}
	slot := elixirWindow(now)
	if slot < 0 || claimed[slot] {
		return false
	}
	// var2 là số suất trong ngày: 1 = 11:00–14:00, 2 = 17:00–21:00
	// (đã xác nhận bằng bắt gói tin client thật ở cả hai khung giờ).
	req := proto.ElixirGetTimeWaterReq{Var1: pb.Int32(0), Var2: pb.Int32(int32(slot) + 1)}
	msg, _ := pb.Marshal(&req)
	p.write(pack.Pack(S_ELIXIR_WATER, msg, p.UID))
	claimed[slot] = true
	log.Printf("[%s] [Nuoc] da nhan suat mien phi %d/%d ngay %s",
		p.Username, slot+1, len(elixirSlotBoundaries), *claimedDay)
	return true
}

// autoElixirWater polls every few minutes and claims the open slot once per
// ICT day.
func (p *Player) autoElixirWater() {
	var claimed [2]bool
	var claimedDay string
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		p.elixirClaimTick(time.Now(), &claimed, &claimedDay)
		time.Sleep(5 * time.Minute)
	}
}
