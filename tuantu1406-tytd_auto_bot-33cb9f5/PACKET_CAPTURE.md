# Hướng dẫn capture và phân tích packet

Tài liệu này hướng dẫn ghi **payload giao thức game đã được giải mã ở tầng WebSocket** từ chính kết nối mà bot mở. WebSocket/TLS đã giải mã TLS trong bộ nhớ trước khi `ReadMessage()` trả frame, nên cách này không cần PCAP, MITM proxy hay cài CA vào LDPlayer.

## Giới hạn và dữ liệu nhạy cảm

- Chỉ capture kết nối do bot trong repo này mở. Chức năng này **không đọc socket của app game chạy riêng trong LDPlayer** và không giải mã PCAP TLS đã ghi trước đó.
- Không ghi HTTP login, mật khẩu, `AUTH_CMD` hoặc token phiên. UID trong header cũng không được ghi vào JSONL.
- Payload của game vẫn có thể chứa tên nhân vật, trạng thái tài khoản, vật phẩm hoặc dữ liệu riêng khác. Chỉ dùng tài khoản test, giữ file cục bộ và không commit/chia sẻ file capture.
- Mỗi file mới được tạo độc quyền; file đã tồn tại sẽ không bị ghi đè. Capture tự dừng trước khi file vượt quá 32 MiB.
- `account_tag` là hash rút gọn ổn định để gom frame cùng tài khoản trong một phiên. Đây là mã giả danh, không phải mã hóa.

## 1. Build bot từ source

Build lại binary sau khi cập nhật source; binary cũ sẽ không có chức năng capture mới. Theo `Makefile`, build native dùng `make build-native` và cần toolchain CGO/SQLCipher tương ứng với máy. Chạy lệnh từ thư mục gốc repo.

```powershell
make build-native
```

Trên Windows, target native thường tạo `build\main.exe`. Nếu môi trường của bạn tạo tên khác, dùng đúng binary vừa build.

## 2. Bật capture cho một lần chạy

Mở PowerShell tại thư mục gốc repo. Chọn đường dẫn tuyệt đối mới cho file JSONL:

```powershell
$captureDir = Join-Path $env:LOCALAPPDATA 'TYTD Auto Bot\packet-captures'
New-Item -ItemType Directory -Force -Path $captureDir | Out-Null
$captureFile = Join-Path $captureDir 'session-01.jsonl'
$env:TYTD_PACKET_CAPTURE = '1'
$env:TYTD_PACKET_CAPTURE_FILE = $captureFile
.\build\main.exe
```

Bot vẫn đọc `config.yaml` theo cấu hình hiện tại của repo. Để dừng, nhấn `Ctrl+C` như khi chạy bình thường. Khi capture xong, tắt cờ trong cửa sổ PowerShell:

```powershell
Remove-Item Env:TYTD_PACKET_CAPTURE
Remove-Item Env:TYTD_PACKET_CAPTURE_FILE
```

Mỗi lần chạy hãy dùng một tên file mới. Nếu bật `TYTD_PACKET_CAPTURE=1` nhưng thiếu đường dẫn tuyệt đối, log sẽ báo capture bị tắt. Nếu file đã tồn tại, chương trình từ chối ghi đè.

## 3. Định dạng file

Mỗi dòng là một JSON object độc lập, ví dụ:

```json
{"time":"2026-11-17T06:30:00Z","account_tag":"a1b2c3d4e5f6","direction":"in","opcode":"0845","payload_size":7,"payload_hex":"0a0508011002"}
```

- `time`: thời điểm UTC nhận/gửi frame.
- `account_tag`: mã hash rút gọn để phân biệt tài khoản mà không ghi username.
- `direction`: `out` là bot gửi, `in` là server trả về.
- `opcode`: mã message theo parser của repo.
- `payload_size`: số byte protobuf.
- `payload_hex`: payload protobuf dạng hex; header frame và UID không nằm trong trường này.

Xem nhanh danh sách opcode trong PowerShell:

```powershell
Get-Content $captureFile |
  ForEach-Object { $_ | ConvertFrom-Json } |
  Select-Object time, account_tag, direction, opcode, payload_size
```

Giải mã hex thành byte trong Python:

```powershell
python -c "import json,sys; [print(x['opcode'], x['direction'], bytes.fromhex(x['payload_hex'])) for x in map(json.loads, open(sys.argv[1], encoding='utf-8'))]" $captureFile
```

Để phân tích protobuf, tìm message tương ứng theo opcode trong `command.go` và schema trong `internal/proto/tytd.proto`; các message Go đã sinh nằm trong `internal/proto/tytd.pb.go`. Nếu chưa biết schema, dùng cách probe `wireTree` trong `live_probes_test.go` để xem field number, wire type và varint trước khi định nghĩa struct.

## 4. Dùng capture để thêm/sửa event

1. Ghi lại thời điểm thao tác và tài khoản test đang ở đúng server/event.
2. Chạy bot với capture bật, gửi một thao tác nhỏ trong bot hoặc chờ một push server có liên quan.
3. Dừng capture sớm sau khi có frame cần thiết; giữ lại một request và câu trả lời tương ứng.
4. Lọc JSONL theo `opcode` và `direction`; giải mã payload hex.
5. Đối chiếu opcode với `command.go`, protobuf với `internal/proto/tytd.proto`, và routing với reader trong `player.go`/`player_event.go`.
6. Thêm fixture byte chính xác vào unit test; không suy đoán opcode, item ID hay field. Quy trình chi tiết về discovery, transport và live tests nằm trong `AGENTS.md` và mục 12 của `README.md`.

`TYTD_DEBUG_RAWMSG=1` vẫn chỉ ghi opcode và độ dài payload. Dùng `TYTD_PACKET_CAPTURE=1` khi cần payload hex.

## 5. Live probe có sẵn trong repo

Repo còn có probe đọc trực tiếp WebSocket bằng credentials lấy từ environment; ví dụ `TestLiveRewardProbes` trong `live_probes_test.go` lưu response hex và in cây protobuf thô. README yêu cầu cả build tag `live` và biến `TYTD_LIVE=1`; chỉ chạy với tài khoản test được phép. Probe có thể gửi lệnh claim/nhận quà và thay đổi trạng thái game, nên đọc phần đầu test để biết tác động trước khi chạy.

```powershell
$env:TYTD_LIVE = '1'
$env:TYTD_LIVE_USER = '<test-account>'
$env:TYTD_LIVE_PASS = '<test-password>'
go test -count=1 -tags 'sqlcipher live' -run TestLiveRewardProbes -v -timeout 240s .
```

Không lưu credentials trong file capture, test source, tài liệu, hoặc lịch sử Git. Xóa các biến khỏi PowerShell sau khi dùng:

```powershell
Remove-Item Env:TYTD_LIVE, Env:TYTD_LIVE_USER, Env:TYTD_LIVE_PASS
```
