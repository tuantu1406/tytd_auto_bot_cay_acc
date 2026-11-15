package tytd_go

// Đan Giới task table — the GameTaskDB rows of type 51 (nhiệm vụ ngày) and
// 52 (nhiệm vụ tích lũy) that belong to Linh Điền, transcribed from the
// decoded client DB (/workspace/remoteAll/GameTaskDB.decoded.json,
// 2026-10-16). The bot never counts progress itself: p.tasks carries the
// server's live value/state for each id (501/502 pushes) and the SERVER is
// the arbiter of when a reward unlocks; this table only says which ids are
// Đan Giới tasks, what target each expects, and what kind of work it is.
//
// completeType legend (from the shipped vi language table):
//
//	496 = Thu hoạch N× <cây> (params "N|grassId")  ← plantable by the loop
//	497 = Thu hoạch Linh Thực bất kỳ N lần          ← any herb harvest counts
//	486/489/491/499/501/504/505/506/507/512/527/566 = sao Đan Sư, đổi Linh
//	  Thực, mở ô vườn, dùng đan, nâng cấp/bồi dưỡng, đơn hàng, tiếp đón Dược
//	  Tiên, mua Chợ Tiên Tài, mời Đan Hữu, luyện chế — NOT plant/harvest work:
//	  their rewards are auto-claimed but the bot never performs them.
type elixirTaskRow struct {
	cType  int32
	target int64
	grass  int32 // completeType 496 only: the tree to harvest
}

var elixirTaskStatic = map[int32]elixirTaskRow{
	// --- type 51: daily (server refreshes them; ids 51xxxx) ---
	510001: {505, 1, 860001},
	510002: {496, 80, 860001},
	510003: {566, 6, 0},
	510004: {499, 6, 0},
	510005: {486, 2, 0},
	510006: {505, 1, 860002},
	510007: {496, 80, 860002},
	510008: {566, 6, 0},
	510009: {499, 12, 0},
	510010: {486, 3, 0},
	510011: {491, 9, 0},
	510012: {505, 1, 860003},
	510013: {496, 80, 860003},
	510014: {566, 6, 0},
	510015: {499, 18, 0},
	510016: {506, 1, 0},
	510017: {486, 4, 0},
	510018: {491, 12, 0},
	510019: {505, 1, 860004},
	510020: {496, 80, 860004},
	510021: {566, 6, 0},
	510022: {499, 24, 0},
	510023: {501, 1, 0},
	510024: {486, 5, 0},
	510025: {566, 6, 0},
	510026: {486, 6, 0},
	510027: {527, 1, 0},
	510028: {486, 7, 0},
	510029: {489, 1, 0},
	// --- type 52: season/accumulative (ids 52xxxx) ---
	520001: {512, 40, 0},
	520002: {507, 50, 0},
	520003: {507, 100, 0},
	520004: {507, 200, 0},
	520005: {507, 400, 0},
	520006: {507, 600, 0},
	520007: {507, 900, 0},
	520008: {497, 3000, 0},
	520009: {497, 6000, 0},
	520010: {497, 12000, 0},
	520011: {497, 20000, 0},
	520012: {497, 30000, 0},
	520013: {504, 25, 0},
	520014: {504, 40, 0},
	520015: {504, 55, 0},
	520016: {504, 70, 0},
	// --- type 53/55: cùng bảng Dược Tiên ghé thăm / đơn hàng Dược Tiên ---
	// (owner 2026-10-17: "3 nhiệm vụ hoàn thành rồi mà bot không tự nhận,
	// nhiệm vụ ở chỗ dược tiên ghé thăm/đơn hàng dược tiên" — live 501/502
	// <live-user>@710564 carries 53xxxx/55xxxx rows the baked table never
	// knew, so the claim gate skipped them ALL as "not Đan Giới". Rows
	// transcribed from GameTaskDB. 530008–530012 (ct2 "Chặt cây") and
	// 530013–530015 (ct560 "dùng Tiên Ngọc") live on the same board but
	// belong to OTHER systems — owner: "đừng nhầm lẫn với nhiệm vụ chỗ
	// khác" — deliberately NOT claimed by the elixir loop.)
	530001: {520, 1, 0},
	530002: {506, 50, 0},
	530003: {506, 100, 0},
	530004: {506, 200, 0},
	530005: {497, 300, 0},
	530006: {497, 600, 0},
	530007: {497, 900, 0},
	// --- type 55: bậc thang "hoàn thành N đơn hàng Dược Tiên" (nguồn tiến
	// độ duy nhất = submit đơn hàng trong panel; bot trồng đúng cây đơn
	// hàng đang đòi VÀ tự nộp (elixirSubmitOrders) cho tới MỐC 5 — owner
	// 2026-10-21 (chốt từ 25 về 5): "chuyển giới hạn max nhiệm vụ hoàn
	// thành từ 25 về 5, sau khi đến max, không nhận thưởng nhiệm vụ nữa
	// nhưng vẫn tiếp tục check cây còn thiếu như thường, nếu không có cây
	// nào thiếu thì trồng cây còn ít trong túi", cap ở elixirOrderAutoCap;
	// ladder ≥ 5 thì MỌI bậc ct506 bot không tự nhận, dưới 5 chỉ bậc ≤ 5).
	// "0/40" owner thấy = 550008. ---
	550001: {506, 5, 0},
	550002: {506, 10, 0},
	550003: {506, 15, 0},
	550004: {506, 20, 0},
	550005: {506, 25, 0},
	550006: {506, 30, 0},
	550007: {506, 35, 0},
	550008: {506, 40, 0},
}

// elixirTaskRewards rides the same GameTaskDB rows: what each pays, kept
// only as documentation for the /dangioi line (Nguyên Dịch 100000, Đan Linh
// Ngọc 100302, Trữ Linh Diệp 100324, vé 901xxx..904xxx, rương 851xxx…).
