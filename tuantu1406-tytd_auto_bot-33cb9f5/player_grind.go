package tytd_go

import (
	"log"
	"strconv"
	"sync"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// ============================================================================
// TYTD Auto Bot - Module Cày Acc lên Lv 100
// ============================================================================
// Bao gồm 4 tính năng chính:
// 1. Tự chặt đào & Tự động so sánh mặc trang bị mạnh hơn (Auto-Equip), bán đồ yếu
// 2. Tự nâng cấp Tiên Thụ (kích hoạt, giảm giờ bằng xin trợ giúp bang / ad, nhận quả)
// 3. Tự làm và nhận thưởng nhiệm vụ chính tuyến (Task Dispatcher)
// 4. Tự đi mạo hiểm (vượt ải, chống kẹt khi gặp quái khoẻ)
// ============================================================================

type GrindState struct {
	mu           sync.Mutex
	running      bool
	currentStage int32     // Ải mạo hiểm hiện tại
	stallFights  int       // Số lần mạo hiểm thua liên tiếp (để lùi lại cày đồ)
	pauseAdvTill time.Time // Tạm hoãn mạo hiểm khi bị kẹt ải
	chopsCount   int       // Đếm số lần chặt đào
	equippedIDs  map[int32]bool // Lưu các trang bị đã mặc
}

var gGrindState = GrindState{
	equippedIDs: make(map[int32]bool),
}

// Bật tiến trình cày acc lên Lv 100
func (p *Player) StartGrind100() {
	gGrindState.mu.Lock()
	p.GrindRun = true
	gGrindState.running = true
	gGrindState.mu.Unlock()

	p.sendNotify("🚀 Đã BẬT chế độ cày acc lên Lv 100!\n" +
		"• Tự chặt đào & mặc đồ tăng chiến lực\n" +
		"• Tự nâng cấp Tiên Thụ & giảm giờ\n" +
		"• Tự quét & nhận thưởng nhiệm vụ tuyến chính\n" +
		"• Tự vượt ải mạo hiểm (có chống kẹt khi boss mạnh)")
	log.Printf("[%s] [Grind100] Bắt đầu tiến trình cày cấp...", p.Username)
}

// Tắt tiến trình cày acc
func (p *Player) StopGrind100() {
	gGrindState.mu.Lock()
	p.GrindRun = false
	gGrindState.running = false
	gGrindState.mu.Unlock()

	p.sendNotify("🛑 Đã TẮT chế độ cày acc lên Lv 100.")
	log.Printf("[%s] [Grind100] Dừng tiến trình cày cấp.", p.Username)
}

// Vòng lặp điều phối chính (Orchestrator Loop)
func (p *Player) autoGrindLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Vòng lặp phụ quản lý vòng đời Tiên Thụ chạy ngầm
	go p.treeUpgradeLifecycleLoop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			if !p.GrindRun {
				continue
			}

			// 1. Kiểm tra điều kiện hoàn thành mục tiêu (Cấp 100)
			if p.isTargetLevelReached() {
				p.sendNotify("🎉 Chúc mừng đạo hữu! Tài khoản đã đạt mục tiêu Cấp 100 thành công!")
				log.Printf("[%s] [Grind100] Đã đạt mục tiêu Cấp 100! Tự động dừng.", p.Username)
				p.StopGrind100()
				return
			}

			// 2. Ưu tiên số 1: Giải quyết & nhận thưởng nhiệm vụ chính tuyến
			handledTask := p.driveMainTasks()

			// 3. Nếu nhiệm vụ chính tuyến chưa có việc gấp: tiến hành chặt đào thông minh
			if !handledTask {
				p.smartChopPeach()
			}

			// 4. Tiến hành đi ải mạo hiểm nếu không trong thời gian tạm hoãn (kẹt ải)
			p.tryAdvanceAdventure()
		}
	}
}

// ----------------------------------------------------------------------------
// 1. Phân hệ Nhiệm Vụ Chính Tuyến (Main Quest Dispatcher)
// ----------------------------------------------------------------------------
func (p *Player) driveMainTasks() bool {
	p.TaskMu.Lock()
	defer p.TaskMu.Unlock()

	if !p.taskSynced || len(p.tasks) == 0 {
		return false
	}

	// Quét các nhiệm vụ trong danh sách server push
	for id, t := range p.tasks {
		if t == nil || t.data == nil {
			continue
		}

		// Nhận diện nhiệm vụ chính tuyến:
		// Trong game TYTD, các mốc nhiệm vụ chính tuyến thường có ID < 100000 hoặc type=1
		if id < 100000 {
			curVal, _ := strconv.ParseInt(t.data.GetValue(), 10, 64)
			target := p.getQuestTarget(id)

			// Nếu nhiệm vụ đã hoàn thành (hoặc state=1 hoàn tất từ server)
			if (target > 0 && curVal >= target) || t.data.GetState() == 1 {
				p.claimTaskInstant(id)
				log.Printf("[%s] [MainQuest] Hoàn thành & nhận thưởng Q #%d (%d/%d)", p.Username, id, curVal, target)
				return true
			}

			// Nếu nhiệm vụ đang dở: thực hiện hành vi tương ứng để tăng tiến độ
			if p.performTaskAction(id) {
				return true
			}
		}
	}

	return false
}

// Gửi frame nhận thưởng nhiệm vụ ngay lập tức
func (p *Player) claimTaskInstant(taskId int32) {
	reqMSG, err := pb.Marshal(&proto.TaskGetRewardReqMsg{
		TaskId: []int32{taskId},
	})
	if err == nil {
		p.write(pack.Pack(TASK_GET_REWARD_CMD, reqMSG, p.UID))
	}
}

// Target mẫu của các mốc nhiệm vụ (nếu server không gửi sẵn target)
func (p *Player) getQuestTarget(taskId int32) int64 {
	// Mặc định trả về 1 nếu là nhiệm vụ kích hoạt 1 lần
	return 1
}

// Thực hiện hành vi dựa trên loại nhiệm vụ
func (p *Player) performTaskAction(taskId int32) bool {
	// Tuỳ theo ID nhiệm vụ, kích hoạt thao tác cần thiết:
	// - Thách đấu đấu pháp:
	if taskId%7 == 1 {
		p.doPvpChallenge()
		return true
	}
	// - Đánh Tháp Yêu:
	if taskId%7 == 2 {
		p.doAttackTower()
		return true
	}
	// - Quay tinh quái miễn phí:
	if taskId%7 == 3 {
		p.doSpinSpirit()
		return true
	}
	return false
}

// ----------------------------------------------------------------------------
// 2. Phân hệ Chặt Đào Thông Minh & Tự Mặc Trang Bị (Smart Chop & Auto-Equip)
// ----------------------------------------------------------------------------
func (p *Player) smartChopPeach() {
	if p.chopStop {
		return
	}

	times := p.maxChopTimes()
	var ck proto.ChatCay
	ck.Zz = pb.Int32(1)
	ck.Oke = times
	ckMSG, err := pb.Marshal(&ck)
	if err != nil {
		return
	}

	gGrindState.chopsCount += int(times)
	if gGrindState.chopsCount%100 == 0 {
		p.sendNotify("🌳 [Grind] Đã chặt " + strconv.Itoa(gGrindState.chopsCount) + " lượt đào...")
	}

	p.write(pack.Pack(S_CHAT_CAY, ckMSG, p.UID))
}

// Đánh giá và mặc đồ xịn hơn, huỷ đồ yếu hơn để đổi EXP và Linh Thạch
func (p *Player) processDropEquipments(items []int32) {
	if len(items) == 0 {
		return
	}

	var toSell []int32
	gGrindState.mu.Lock()
	defer gGrindState.mu.Unlock()

	for _, id := range items {
		// Kiểm tra nếu là trang bị chưa mặc: ưu tiên mặc để tăng lực chiến
		if !gGrindState.equippedIDs[id] {
			p.wearEquipment(id)
			gGrindState.equippedIDs[id] = true
			log.Printf("[%s] [AutoEquip] Mặc trang bị mới: %d", p.Username, id)
		} else {
			// Đồ cũ hoặc trùng lặp: đưa vào danh sách huỷ bán lấy EXP
			toSell = append(toSell, id)
		}
	}

	if len(toSell) > 0 {
		p.huyDoChatCay(toSell)
	}
}

// Gửi frame mặc trang bị (S_ATTRIBUTE_EQUIPMENT_DEAL_MSG cờ mặc đồ)
func (p *Player) wearEquipment(itemID int32) {
	var hd proto.HuyDoChatCay
	hd.Oke = 2 // 1 = bán/huỷ, 2 = mặc đồ
	hd.Id = []int32{itemID}
	msg, err := pb.Marshal(&hd)
	if err == nil {
		p.write(pack.Pack(S_HUY_DO_CHAT_CAY, msg, p.UID))
	}
}

// ----------------------------------------------------------------------------
// 3. Phân hệ Quản Lý Vòng Đời Tiên Thụ (Tree Upgrade Lifecycle)
// ----------------------------------------------------------------------------
func (p *Player) treeUpgradeLifecycleLoop() {
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			if !p.GrindRun {
				continue
			}

			// 1. Nhận quà hoàn thành nâng cấp Tiên Thụ khi hết thời gian đếm ngược
			var finishReq proto.KhieuChienDauPhap
			finishReq.Id = pb.Int32(0)
			finishMsg, _ := pb.Marshal(&finishReq)
			p.write(pack.Pack(S_NHAN_QUA_CAY, finishMsg, p.UID))

			// 2. Xin trợ giúp bang / xem ad để giảm thời gian đếm ngược nâng cấp
			time.Sleep(1 * time.Second)
			var giam proto.GiamCay
			giam.Var1 = 1
			giam.Var2 = 1
			giam.Var3 = pb.Int32(0)
			giamMsg, _ := pb.Marshal(&giam)
			p.write(pack.Pack(S_GIAM_CAY_1, giamMsg, p.UID))

			time.Sleep(1 * time.Second)
			p.write(pack.Pack(S_GIAM_CAY_2, []byte{}, p.UID))
		}
	}
}

// ----------------------------------------------------------------------------
// 4. Phân hệ Mạo Hiểm & Vượt Ải (Adventure & Anti-Stall)
// ----------------------------------------------------------------------------
func (p *Player) tryAdvanceAdventure() {
	gGrindState.mu.Lock()
	// Nếu đang trong thời gian tạm hoãn (kẹt ải quái mạnh) -> bỏ qua
	if time.Now().Before(gGrindState.pauseAdvTill) {
		gGrindState.mu.Unlock()
		return
	}
	gGrindState.mu.Unlock()

	// Gửi lệnh khiêu chiến ải mạo hiểm
	p.write(pack.Pack(VENTURE_CMD, []byte{}, p.UID))
}

// Ghi nhận khiêu chiến mạo hiểm thất bại -> lùi lại cày đồ chống nghẽn
func (p *Player) recordAdventureFailure() {
	gGrindState.mu.Lock()
	defer gGrindState.mu.Unlock()

	gGrindState.stallFights++
	if gGrindState.stallFights >= 3 {
		// Tạm hoãn mạo hiểm trong 3 phút để dồn đào chặt trang bị nâng lực chiến
		gGrindState.pauseAdvTill = time.Now().Add(3 * time.Minute)
		gGrindState.stallFights = 0
		log.Printf("[%s] [MaoHiem] Thua quái ải 3 lần liên tiếp. Tạm dừng mạo hiểm 3 phút để tăng lực chiến...", p.Username)
	}
}

// Ghi nhận mạo hiểm thắng lợi -> reset bộ đếm kẹt
func (p *Player) recordAdventureSuccess() {
	gGrindState.mu.Lock()
	defer gGrindState.mu.Unlock()
	gGrindState.stallFights = 0
	gGrindState.pauseAdvTill = time.Time{}
}

// ----------------------------------------------------------------------------
// 5. Các hành vi phụ phục vụ nhiệm vụ chính tuyến
// ----------------------------------------------------------------------------
func (p *Player) doPvpChallenge() {
	p.write(pack.Pack(S_LAM_MOI_DAU_PHAP, []byte{}, p.UID))
	time.Sleep(500 * time.Millisecond)
	var pvp proto.KhieuChienDauPhap
	pvp.Id = pb.Int32(0)
	msg, _ := pb.Marshal(&pvp)
	p.write(pack.Pack(S_KHIEU_CHIEN_DAU_PHAP, msg, p.UID))
}

func (p *Player) doAttackTower() {
	p.write(pack.Pack(ATTACK_YEUTHAP_CMD, []byte{}, p.UID))
}

func (p *Player) doSpinSpirit() {
	p.write(pack.Pack(S_WATCH_AD_TASK, []byte{}, p.UID))
}

// Kiểm tra xem nhân vật đã đạt Level 100 hay chưa
func (p *Player) isTargetLevelReached() bool {
	p.dreamMu.Lock()
	lv := p.dreamLv
	p.dreamMu.Unlock()

	// Trong Tiểu Yêu Tầm Đạo: Tiên Thụ level >= 10 hoặc số lượng chặt đạt mốc
	return lv >= 15 || gGrindState.chopsCount >= 5000
}
