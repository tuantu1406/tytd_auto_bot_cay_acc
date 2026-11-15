# TYTD Auto Bot

Bot tự động game **Tiểu Yêu Tầm Đạo (Funtap)** điều khiển hoàn toàn qua **Telegram**, kèm **web dashboard** và **Mini App** (bản đồ Côn Lôn, Bắc Minh, Di Tích, Cửu U, Bồng Lai, Thiên Ma, Thương Đồ, Luân Hồi, SeaWarWar…) chạy ngay trong Telegram.

Mỗi người dùng Telegram là một tài khoản game riêng (đăng nhập game bằng acc Funtap của chính họ). Bot chạy nhiều server, **một binary duy nhất cho mọi nền tảng**, bí mật (token bot, admin ID) lấy từ file `config.yaml` — **không hardcode trong code hay trong binary**.

> Web dashboard + Mini App là **tính năng tuỳ chọn** (cần domain + HTTPS). Không mua domain thì không mở được giao diện web, nhưng **bot vẫn chạy bình thường** — mọi lệnh Telegram dùng đủ.

---

## Mục lục

1. [Yêu cầu](#1-yêu-cầu)
2. [Tạo bot Telegram (BotFather)](#2-tạo-bot-telegram-botfather)
3. [Lấy Admin Chat ID](#3-lấy-admin-chat-id)
4. [Cấu hình `config.yaml`](#4-cấu-hình-configyaml)
5. [Build — tải bản dựng sẵn & tự build](#5-build--tải-bản-dựng-sẵn--tự-build)
6. [Chạy bot](#6-chạy-bot)
7. [Mua VPS DigitalOcean](#7-mua-vps-digitalocean)
8. [Domain + HTTPS (Cloudflare, không cần nginx)](#8-domain--https-cloudflare-không-cần-nginx)
9. [Web dashboard & Mini App](#9-web-dashboard--mini-app)
10. [Deploy nhiều server](#10-deploy-nhiều-server)
11. [Lệnh thường dùng](#11-lệnh-thường-dùng)
12. [Test & live test](#12-test--live-test)
13. [Bảo mật](#13-bảo-mật)
14. [Khắc phục sự cố](#14-khắc-phục-sự-cố)
15. [Cấu trúc project](#15-cấu-trúc-project)
16. [Miễn trừ trách nhiệm](#16-miễn-trừ-trách-nhiệm)

---

## 1. Yêu cầu

**Máy chủ chạy bot (runtime):** chỉ cần một binary — tải bản dựng sẵn trong `build/` (mục 5.1) là đủ, không cần cài Go.

**Máy build:**

| Thành phần | Tối thiểu | Ghi chú |
|---|---|---|
| Go | 1.22+ (dev đang dùng bản mới) | https://go.dev/dl |
| GCC + build-essential | có sẵn | bot dùng **CGO** (sqlite3/sqlcipher) |
| libssl-dev (OpenSSL headers) | bắt buộc | sqlcipher link với OpenSSL |
| `protoc` + `protoc-gen-go` | chỉ khi regen proto | `make proto` |

Toolchain cross-compile (chỉ cần khi build **cho nền tảng khác máy đang build**):

| Target | Cần cài |
|---|---|
| linux/amd64 (build từ máy ARM)  | `gcc-x86_64-linux-gnu` (`x86_64-linux-gnu-gcc`)  |
| linux/arm64 (build từ máy x86)  | `gcc-aarch64-linux-gnu` (`aarch64-linux-gnu-gcc`) |
| windows/amd64 | `gcc-mingw-w64` (`x86_64-w64-mingw32-gcc`) |
| darwin (macOS) | [osxcross](https://github.com/tpoechtrager/osxcross) — `OSXCROSS=/opt/osxcross` |

Thiếu toolchain nào thì `make build` **tự skip** target đó kèm thông báo, các target còn vẫn build đủ.

> Build trực tiếp trên đúng nền tảng đích (`make build-native`) thì không cần cross-compiler.

---

## 2. Tạo bot Telegram (BotFather)

1. Mở Telegram, chat với **[@BotFather](https://t.me/BotFather)**.
2. Gõ `/newbot` → đặt **tên hiển thị** (VD: `TYTD Auto`) → đặt **username** kết thúc bằng `bot` (VD: `tytd_auto_bot`).
3. BotFather trả về **HTTP API token** dạng `1234567890:AAE...abcd` — **lưu lại**, đây là `telegram_token` trong config. Token chỉ có quyền: gửi/nhận tin nhắn, đổi tên bot… — mất token = mất bot, **không chat bừa token vào group nào**.
4. Cấu hình khuyến nghị trong BotFather:
   - `/setprivacy` → chọn **Disable** (bot cần đọc lệnh trong group, nếu bot của bạn hoạt động theo group).
   - `/setdescription`, `/setabouttext`, `/setuserpic` — tuỳ ý.
   - Mini App (Menu Button) — làm ở [bước 9](#9-web-dashboard--mini-app), cần domain + HTTPS xong đã; chưa có domain thì bỏ qua, bot vẫn chạy đủ lệnh Telegram.

### Test token ngay bằng curl

```bash
curl -s "https://api.telegram.org/bot<TOKEN>/getMe"
# {"ok":true,"result":{"id":...,"username":"tytd_auto_bot",...}} → token hợp lệ
```

---

## 3. Lấy Admin Chat ID

`admin_chat_id` là **số ID Telegram (chat ID) của bạn** — người được seed quyền admin khi DB còn trống.

Cách lấy:

- Chat **[@userinfobot](https://t.me/userinfobot)** (hoặc @getidsbot) → nó trả về `Id` của bạn (VD: `787613537`).
- Đưa đúng số đó vào `admin_chat_id`. Bot **chỉ nhận ID dương** (tài khoản cá nhân) — ID group (số âm kiểu `-100…`) sẽ bị báo lỗi khi khởi động.

---

## 4. Cấu hình `config.yaml`

> 🟢 **Tải bản dựng sẵn ở `build/` (mục 5.1)?** Đã có sẵn `build/config.yaml` để điền — mở file đó sửa 2 chỗ (`telegram_token`, `admin_chat_id`) là chạy, **không phải copy file mẫu**. Đọc lướt bảng key bên dưới để biết từng chỗ điền gì.

Không thì copy file mẫu rồi sửa:

```bash
cp config.example.yaml config.yaml
chmod 600 config.yaml          # chỉ chủ nhân đọc được — trong có token
```

File mẫu:

```yaml
telegram_token: "1234567890:AAE...abcd"
# telegram_token_encrypted: "hex-string-tu-cmd-gen"
admin_chat_id: 787613537
debug: false
```

| Khoá | Bắt buộc | Ý nghĩa |
|---|---|---|
| `telegram_token` | 1 trong 2 | Token BotFather dạng thường. |
| `telegram_token_encrypted` | 1 trong 2 | Token đã mã hoá AES-256-GCM (hex) — dùng khi không muốn token nằm plaintext trong file. |
| `admin_chat_id` | ✅ | Chat ID admin (seed lúc DB trắng). |
| `debug` | ❌ | `true` = log verbose + chế độ dev. |

Hai khoá token **chỉ được bật đúng một**; `debug` default `false`.

### Mã hoá token (tuỳ chọn, khuyến nghị cho server)

```bash
make build-gen                 # tạo build/gen
./build/gen "1234567890:AAE...abcd"   # in ra hex -> dán vào telegram_token_encrypted
```

Xoá `telegram_token` khỏi config khi đã dùng bản mã hoá.

### Thứ tự ưu tiên khi nạp config

1. Cờ `-config /duong/dan/config.yaml`
2. Env `TYTD_CONFIG=/duong/dan`
3. `./config.yaml` cạnh binary

Env override (qua config, dùng cho Docker/CI):

| Env | Ghi đè |
|---|---|
| `TYTD_TELEGRAM_TOKEN` | `telegram_token` |
| `TYTD_TELEGRAM_TOKEN_ENCRYPTED` | `telegram_token_encrypted` |
| `TYTD_ADMIN_CHAT_ID` | `admin_chat_id` |

File YAML có khoá lạ (typo) sẽ **bị từ chối ngay lúc khởi động** — sửa theo thông báo lỗi. `config.yaml` đã nằm trong `.gitignore` — **không bao giờ commit**.

---

## 5. Build — tải bản dựng sẵn & tự build

### 5.1. Tải bản dựng sẵn (không cần máy cài Go)

Repo commit sẵn binary biên dịch tĩnh trong thư mục [`build/`](build/) — chỉ việc tải về chạy, chẳng phải cài gì thêm:

| File | Dùng cho |
|---|---|
| `main-linux-amd64` | Linux 64-bit thường (x86_64) — đa số VPS (DigitalOcean loại Regular…) |
| `main-linux-arm64` | Linux ARM 64-bit (VPS Premium ARM, Raspberry Pi 64-bit, Apple Silicon chạy Linux…) |
| `main-windows-amd64.exe` | Windows 64-bit — cách chạy chi tiết xem [mục 6 – Chạy trên Windows](#chạy-trên-windows) |
| `config.yaml` | File cấu hình **mẫu để điền sẵn** — đặt cạnh binary, sửa 2 chỗ là chạy (mục 4) |

Xem máy mình thuộc loại nào: `uname -m` (`x86_64` → amd64, `aarch64`/`arm64` → arm64).

Tải trực tiếp từ repo (GitHub) — binary **kèm luôn `config.yaml` mẫu** đã để sẵn trong `build/`:

```bash
wget https://raw.githubusercontent.com/<owner>/<repo>/main/build/main-linux-amd64 -O main
wget https://raw.githubusercontent.com/<owner>/<repo>/main/build/config.yaml
chmod +x main
nano config.yaml     # chỉ sửa 2 chỗ: telegram_token + admin_chat_id (xem mục 4)
./main
```

(hoặc đơn giản hơn: `git clone` repo rồi copy cả `main-*` + `config.yaml` trong `build/` sang server — xem lệnh scp ở mục 7.5.)

> ⚠️ Lưu ý khi commit binary vào git:
> - Mỗi file ~50 MB — GitHub **chặn cứng >100 MB/file** (dưới là push được), nhưng mỗi lần rebuild + commit lại cộng thêm ~50 MB vào **lịch sử repo** (binary không diff-nén được). repo phình to thì chuyển sang **Git LFS** hoặc đưa lên **Releases**.
> - Bản **macOS không có sẵn** trong `build/` (máy build Linux không có osxcross) — dân Mac chạy `make build-native` trên máy mình.
> - `make clean` **xoá sạch `build/*`** kể cả bản đang được git theo dõi — cẩn thận kẻo commit luôn vụ xoá.
> - `build/config.yaml` là **bản trống**. Nếu ai đó điền token thật vào rồi commit (hoặc copy ngược vào repo) = **lộ token bot** — luôn để ý `git status` trước khi push.

### 5.2. Tự build từ source

```bash
make help              # xem toàn bộ target
make build             # MỘT phát ra đủ mọi arch -> build/
make build-native      # chỉ binary cho đúng máy đang build -> build/main[.exe]
```

`make build` sinh ra:

```
build/main-linux-amd64
build/main-linux-arm64
build/main-windows-amd64.exe
build/main-darwin-amd64      # cần osxcross; reminder: codesign -s - trước khi dùng
build/main-darwin-arm64
```

Target lẻ: `build-linux-amd64`, `build-linux-arm64`, `build-windows-amd64`, `build-darwin-amd64`, `build-darwin-arm64`.

Toolchain khác tên thì override:

```bash
make build CC_WINDOWS=x86_64-w64-mingw32.posix-gcc OSXCROSS=/home/me/osxcross
```

Binary **static** (trên linux/windows) — copy sang server nào cũng chạy, không cần thư viện theo kèm.

---

## 6. Chạy bot

```bash
make run                              # build-native + chạy bằng ./config.yaml hiện tại
./build/main                          # chạy thẳng
./build/main -config /etc/tytd/config.yaml
```

Cơ sở dữ liệu `data.dat` (sqlite **mã hoá sqlcipher**) tự tạo cạnh thư mục chạy — giữ file này như giữ bí mật (chứa tài khoản game của user).

### systemd (khuyến nghị cho VPS Linux)

`/etc/systemd/system/tytd.service`:

```ini
[Unit]
Description=TYTD Auto Bot
After=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/tytd
ExecStart=/opt/tytd/main -config /opt/tytd/config.yaml
Restart=always
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tytd
journalctl -u tytd -f          # xem log
```

### Chạy trên Windows

**Chuẩn bị (2 phút):**

1. Tạo thư mục, VD `C:\tytd`.
2. Tải từ [`build/`](build/) về 2 file, bỏ vào thư mục đó:
   - `main-windows-amd64.exe` → đổi tên gọn thành `main.exe`
   - `config.yaml`
3. Mở `config.yaml` bằng Notepad, sửa đúng **2 chỗ** (`telegram_token`, `admin_chat_id`) như mục 4, lưu file.

**Chạy lần đầu — dùng cmd, đừng double-click:**

Double-click `main.exe` nếu config sai thì cửa sổ bật lên rồi biến mất trong 1 giây không kịp đọc lỗi. Mở **Command Prompt** (gõ `cmd` vào Start):

```bat
cd C:\tytd
main.exe
```

Log in ra ngay trên màn hình; bot chạy tới khi bạn tắt máy / bấm Ctrl+C / đóng cửa sổ. Thấy dòng lỗi đỏ đầu tiên → xem mục [Khắc phục sự cố](#14-khắc-phục-sự-cố).

> ℹ️ **Windows báo "Windows protected your PC" / SmartScreen xanh dương?** File chưa có chữ ký số nên Windows cảnh chung như vậy — bấm **More info → Run anyway**. Nếu phần mềm diệt virus xoá file: khôi phục (quarantine → restore) rồi thêm loại trừ thư mục `C:\tytd`.

**Để bot tự chạy nền, bật lại khi khởi động:**

Cách gọn — Task Scheduler (có sẵn trong Windows):
1. `Start` → gõ **Task Scheduler** → **Create Task…**
2. Tab *General*: đặt tên `TYTD`; chọn **Run whether user is logged on or not**; tick *Run with highest privileges* nếu cần.
3. Tab *Actions* → New: *Program/script* = `C:\tytd\main.exe`, *Start in* = `C:\tytd`.
4. Tab *Settings*: tick **If the task fails, restart every** 1 minute.
5. OK (nhập mật khẩu Windows) → chuột phải task → **Run**.

Cách chuyên nghiệp hơn: [NSSM](https://nssm.cc) biến bot thành Windows Service (`nssm install TYTD` → trỏ exe + working directory `C:\tytd`) — bot chạy trước cả khi ai đó đăng nhập, tự restart khi crash.

**Web/Mini App trên Windows (tuỳ chọn — không dùng thì bỏ qua hết):**

- Cho phép firewall cho bot nhận kết nối (cmd **Run as administrator**):

  ```bat
  netsh advfirewall firewall add rule name="TYTD web" dir=in action=allow program="C:\tytd\main.exe" enable=yes
  ```

- Port 80 trên Windows hay bị IIS/`http.sys` giữ sẵn (dấu hiệu: bot báo `address already in use`). Đổi port:

  ```bat
  set WEB_PORT=8080&& main.exe
  ```

  (PowerShell: `$env:WEB_PORT="8080"; .\main.exe`) — rồi thêm rule firewall cho port 8080 tương tự.
- Chạy web ở máy Win cá nhân còn cần **IP công khai cố định** (đa số mạng VN là động + sau NAT → phải mở port router + DDNS). Vì vậy thực tế: **Windows chỉ nên làm máy chạy bot lệnh**; muốn web/mini app mượt thì đặt bot trên VPS (mục 7) và trỏ domain về đó.

**Backup:** `data.dat` (DB toàn bộ acc game) nằm trong `C:\tytd` cạnh `main.exe` — copy về nơi khác định kỳ.

---

## 7. Mua VPS DigitalOcean

Đa số mọi người chạy bot trên một con VPS Linux rẻ. DigitalOcean (DO) hay được chọn vì ổn định, giá rõ ràng, có free credit cho acc mới.

### 7.1. Lập tài khoản

1. Vào https://www.digitalocean.com → **Sign Up** (đăng ký bằng Google/GitHub/email).
2. Thêm phương thức thanh toán: **Visa/Mastercard** (khớp tên chủ thẻ, còn hạn, bật thanh toán online) hoặc PayPal. Tài khoản mới thường được tặng **credit dùng thử** (VD $200/60 ngày) — trừ vào credit trước khi tính tiền thật.
3. Nếu bị từ chối thẻ, thử xác minh (Verify) trong **Billing** hoặc dùng thẻ khác — DO kiểm tra khá gắt.

### 7.2. Tạo Droplet (VPS)

1. Dashboard → **Create → Droplets**.
2. **Image**: `Ubuntu 24.04 LTS x64` (mặc định).
3. **Region (khu vực)**: chọn **Singapore** — gần Việt Nam nhất, latency ~25–50ms. (Nếu Singapore hết IP trắng có thể thử `Frankfurt`/`Amsterdam`.)
4. **Plans**: khuyến nghị **Basic Regular (AMD/Intel) — $4/tháng** (1 vCPU / **512 MB** RAM / 10 GB SSD) — bot Go + web nhẹ, chạy dư dả.
   - 512 MB thì **bắt buộc tạo swap** (bước 7.4 là bắt buộc, không phải khuyến nghị).
   - Đông user (vài chục player online đồng thời) thì hẵng nâng $6 (1 GB RAM).
   - Premium ARM cũng $4–6: OK, nhưng ⚠️ chọn **ARM phải tải binary `main-linux-arm64`**, loại thường là `main-linux-amd64`.
5. **Authentication**: chọn **SSH Keys** → **Add New SSH Key** → dán nội dung file `~/.ssh/id_ed25519.pub` trên máy bạn (chưa có thì chạy `ssh-keygen -t ed25519` rồi `cat ~/.ssh/id_ed25519.pub`). **Không chọn Password** — key an toàn hơn và DO có thể từ chối kích hoạt nếu chỉ đặt mật khẩu yếu.
6. **Droplet options**: bật **IPv6** (tuỳ), **Monitoring** (bật cũng được, miễn phí). Đặt **hostname**: `tytd-1`.
7. Số lượng: `1` → **Create Droplet**. Chờ ~30 giây.
8. DO gửi **email chứa IP + (nếu đặt mật khẩu) thông tin đăng nhập**. Lấy IP, ví dụ `203.0.113.10`.

### 7.3. Đăng nhập lần đầu

```bash
ssh root@203.0.113.10
# lần đầu có hỏi "Are you sure?" -> yes
```

### 7.4. Cấu hình ban đầu (chạy lần lượt trên VPS)

```bash
apt update && apt upgrade -y

# swap 1G — BẮT BUỘC với VPS 512MB
fallocate -l 1G /swapfile && chmod 600 /swapfile
mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab

# firewall: chỉ mở SSH + web (web chạy :80 để Cloudflare truy cập nguồn, xem mục 8)
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp
ufw allow 80/tcp
ufw allow 443/tcp
ufw --force enable

# chống dò mật khẩu SSH (tuỳ chọn, khuyến nghị)
apt install -y fail2ban && systemctl enable --now fail2ban
```

> Có thể dùng **Cloud Firewalls** của DO (mục *Networking → Firewalls*) thay cho ufw — tác dụng tương đương, cấu hình trên web.

### 7.5. Đưa bot lên VPS

Lấy binary **build sẵn trong `build/` của repo** (mục 5.1) hoặc tự build ở máy bạn (mục 5.2), rồi copy **đúng arch** + config:

```bash
# trên máy build:
scp build/main-linux-amd64 root@203.0.113.10:/opt/tytd/main
ssh root@203.0.113.10 'mkdir -p /opt/tytd'   # nếu chưa có thư mục
scp config.yaml root@203.0.113.10:/opt/tytd/config.yaml
ssh root@203.0.113.10 'chmod 600 /opt/tytd/config.yaml && chmod +x /opt/tytd/main'
```

Bot bind `:80` (mặc định, xem mục 8). Chạy bằng user root qua systemd là đơn giản nhất; nếu muốn chạy user không đặc quyền thì cấp quyền bind port thấp:

```bash
ssh root@203.0.113.10 "setcap 'cap_net_bind_service=+ep' /opt/tytd/main"
```

Cài service như [mục 6](#6-chạy-bot) rồi `systemctl enable --now tytd`. Kiểm tra: `curl -I http://203.0.113.10/login` trả `200`.

### 7.6. Chi phí & vệ sinh

- $4/tháng tự trừ theo giờ (xoá droplet thì ngừng tính). Hoá đơn ở **Billing**.
- **Backup**: tạo **Snapshot/Droplet Backup** trong DO, hoặc đơn giản làscp `data.dat` về máy định kỳ — **file DB là tất cả những gì mất khi reinstall** (chứa toàn bộ acc game đã login).
- Không dùng nữa: **Destroy** droplet + gỡ card trong Billing để không bị tính tiếp.

---

## 8. Domain + HTTPS (Cloudflare, không cần nginx)

> ℹ️ **Domain chỉ cần cho web/Mini App.** Không mua domain thì đơn giản là **không dùng được giao diện web** — còn bot vẫn chạy hoàn toàn bình thường, mọi lệnh Telegram (auto, map lệnh, quản lý user…) đều dùng được. Bỏ qua mục 8 và 9 nếu bạn chỉ cần bot lệnh.

Web dashboard + Mini App **bắt buộc HTTPS** (Telegram WebView và cookie `Secure` không chạy với HTTP). Bot web chỉ nói **HTTP** thuần (không có TLS trong code), nên cách gọn nhất là để **Cloudflare đứng trước làm HTTPS** — không cần cài nginx, không cần certbot.

### 8.1. Mua domain

Mua ở đâu cũng được — Namecheap, Cloudflare Registrar, PA Việt Nam, Matbao… (VD `tytd-tool.com`, ~$10/năm với `.com`). Cloudflare Registrar bán giá vốn (rẻ, không markup) nếu bạn đã dùng Cloudflare.

### 8.2. Dùng Cloudflare proxy (miễn phí)

1. Đăng ký https://www.cloudflare.com → **Add a Site** → nhập `tytd-tool.com` → chọn gói **Free**.
2. Cloudflare cấp 2 **nameserver** (VD `ana.ns.cloudflare.com` / `bob.ns.cloudflare.com`) → vào nơi mua domain, đổi **Nameservers** về 2 cái đó (thay DNS mặc định của registrar). Chờ vài phút tới vài giờ cho lan toả.
3. Trong DNS Records của Cloudflare, tạo:

   | Type | Name | Content | Proxy status |
   |---|---|---|---|
   | A | `@` (hoặc `app`) | IP droplet DigitalOcean | **Proxied** 🟠 (mây cam) |

4. **SSL/TLS → Overview**: chọn chế độ **Flexible** *( trình duyệt ↔ Cloudflare: HTTPS; Cloudflare ↔ server: HTTP :80 )* — khớp đúng với bot HTTP-only.

   > ⚠️ **Không chọn Full / Full (strict)** khi bot chỉ nghe HTTP — sẽ lỗi `525/521`. Ngược lại **Flexible chỉ ổn vì bot không tự phục vụ HTTPS**; nếu sau này bot có TLS thì nâng lên Full.
5. Server mở cổng **80** (đã cho phép ở ufw mục 7.4). Cloudflare còn cho origin chạy ở các port HTTP quen khác (8080, 8880…) nếu bạn đặt `WEB_PORT=8080` — lúc đó thêm quy tắc **Origin Rules** trỏ port, đơn giản hơn cứ để 80.
6. Kiểm tra:

   ```bash
   curl -I https://tytd-tool.com/login    # HTTP/2 200 → xong
   ```

   Người dùng luôn thấy ổ khoá HTTPS; chứng chỉ Let's Encrypt do Cloudflare tự cấp và **tự gia hạn**, bạn không phải đụng tới file cert nào.

### 8.3. Muốn không dùng Cloudflare?

Bot không có TLS sẵn trong code — nếu không qua Cloudflare thì bắt buộc phải có một lớp reverse proxy đứng trước để kết thúc HTTPS và tự quản chứng chỉ. README này chủ đích không đặt thêm dịch vụ nào trên server; Cloudflare proxy là con đường ngắn nhất để có HTTPS sạch mà không cần cài gì thêm.

---

## 9. Web dashboard & Mini App

### 9.1. Bật web/Mini App (lệnh admin)

Từ chat Telegram của **admin**, đúng một lệnh:

```
/web on
```

**Không cần ghi domain** — đăng nhập Mini App hoàn toàn tự động vì `initData` do Telegram ký sẵn bằng token của chính bot đang chạy.

Tuỳ chọn, chỉ khi muốn **cả nút đăng nhập cho trình duyệt thường** (Telegram Login Widget, không nằm trong Telegram) thì mới khai domain + username bot:

```
/web <domain> <bot_username> on        # VD: /web tytd-tool.com tytd_auto_bot on
```

Các lệnh khác:

- `/web off` — tắt web.
- `/web status` — hiện trạng thái + **fingerprint token (sha256 8 ký tự đầu)** + **bàn phím nút Mini App** cho từng map (Côn Lôn, Bắc Minh, Di Tích, Cửu U, Bồng Lai, Thiên Ma, Thương Đồ) bấm là mở ngay trong Telegram. So sha khi cần kiểm tra web và bot chạy cùng token.

Các khoá config web (`web_enabled`, `web_domain`, `web_bot_username`) lưu trong DB — **không cần config.yaml riêng cho web**, đổi bằng lệnh `/web` là xong.

### 9.2. Web login hoạt động thế nào

- Người dùng mở `https://tytd-tool.com/login` → đăng nhập bằng **Telegram Login Widget** (`auth/callback`).
- Mini App mở bằng chính URL gốc `https://tytd-tool.com` — web nhận diện môi trường Telegram rồi chuyển vào màn hình mini app, bridge `/miniapp/login` xác thực bằng **`initData` ký HMAC với token bot** — không cần mật khẩu thứ hai.
- Cookie phiên đặt `SameSite=None; Secure` **vì Telegram nhúng mini app trong iframe cross-site** — thiếu `Secure`/`SameSite=None` là browser chặn cookie → trang báo chưa đăng nhập loop. Đây là lý do HTTPS **bắt buộc**, không phải khuyến nghị. (Với Cloudflare Flexible ở mục 8, trình duyệt luôn thấy HTTPS nên cookie hợp lệ.)

### 9.3. Tạo Mini App trong BotFather

Làm sau khi domain đã có HTTPS và bot đã `/web on`. **Chỉ cần thêm đúng một URL** — không phải tạo app riêng cho từng trang, không cấu hình subpath nào cả; các map (Côn Lôn, Bắc Minh, Di Tích, Cửu U, Bồng Lai, Thiên Ma, Thương Đồ, Luân Hồi, SeaWarWar…) tự điều hướng bên trong web sau khi mở:

1. Chat @BotFather → **Menu Button** (Bot Settings → Menu Button, hoặc lệnh `/menu`) → nhập URL **root domain, không kèm subpath nào**:

   ```
   https://tytd-tool.com
   ```

   Web tự nhận ra đang mở trong Telegram và vào thẳng màn hình Mini App. Người dùng bấm nút Menu cạnh ô gõ là dùng.
2. (Tuỳ chọn) Muốn có link `/tên_bot/app` để chia sẻ riêng, dùng `/newapp` và vẫn nhập cùng URL gốc `https://tytd-tool.com`.

> Trang map yêu cầu quyền **leader** (`authedLeader`) — member thường mở sẽ bị chặn; đó là chủ ý, không phải bug.

### 9.4. Chọn port bind cho web

| Cách | Kết quả |
|---|---|
| (mặc định) | `:80` — hợp cặp Cloudflare Flexible (mục 8) |
| `WEB_PORT=8080` | `:8080` (Cloudflare vẫn truy cập được qua Origin Rules/port cho phép) |
| `WEB_PORT=127.0.0.1:8080` | chỉ nghe loopback — chỉ dùng khi có một proxy khác cùng máy đứng trước |
| `TYTD_WEB_BIND=...` | override tuyệt đối (ẩn, nâng cao) |

---

## 10. Deploy nhiều server

- **Một binary duy nhất** (đúng arch của server) dùng chung mọi máy; khác nhau chỉ ở `config.yaml` + `data.dat`.
- Copy: `build/main-linux-amd64` → `/opt/tytd/main`, kèm `config.yaml` riêng (token bot riêng nếu mỗi server một bot).

```bash
scp build/main-linux-amd64 root@server:/opt/tytd/main
scp config.server2.yaml root@server:/opt/tytd/config.yaml
ssh root@server 'systemctl restart tytd'
```

> ⚠️ `deploy.sh` trong repo viết theo Makefile **cũ** (`make build_<label>`, cred.txt phân khối label/IP/pass). Makefile mới không còn target label — hoặc chạy scp+systemd tay như trên, hoặc cập nhật `deploy.sh` trước khi dùng.

Nhiều máy cùng một DB thì **không** — `data.dat` là file sqlite cục bộ, mỗi bot instance một DB riêng. Mỗi server có thể một domain/subdomain + một tài khoản Cloudflare Proxied riêng.

---

## 11. Lệnh thường dùng

Gõ `/start` trong bot để xem menu lệnh đầy đủ. Bộ thường gặp:

**Người dùng**

| Lệnh | Tác dụng |
|---|---|
| `/auth <key>` | Kích hoạt quyền dùng bot (key do admin đặt) |
| `/login` / `/logout` | Đăng nhập / off tài khoản game |
| `/info` | Xem trạng thái tài khoản |
| `/start_chuot`, `/start_maohiem`, `/start_taydu` … | Bật các auto (chuột chạy, mạo hiểm, Tây Du…) |
| `/bacminh_map`, `/diich_map`, `/bonglai_map` … | Mở map tương ứng (Mini App) |

**Admin**

| Lệnh | Tác dụng |
|---|---|
| `/set_auth_key <key>` | Đặt key cấp quyền cho user |
| `/web on` / `/web off` / `/web status` | Bật/tắt web & Mini App, xem trạng thái (mục 9.1) |
| `/broadcast …` | Thông báo toàn user |
| `/ban_usr`, `/add_good_usr`, blacklist clan… | Quản lý user |

Admin được xác định bởi `admin_chat_id` trong `config.yaml` (seed lúc DB trắng) — các quyền admin sau đó quản lý theo DB.

---

## 12. Test & live test

```bash
make test            # = go test -tags sqlcipher ./...   (offline, an toàn, chạy trên CI)
```

**Live test** (đánh thật vào server game) bị **khoá 2 lớp**: build tag `live` + env gate. Không có credentials trong repo — đọc từ môi trường:

```bash
TYTD_LIVE=1 TYTD_LIVE_USER=<acc-game> TYTD_LIVE_PASS=<pass> \
  go test -count=1 -tags "sqlcipher live" -run TestLiveXxx -v -timeout 180s .
```

Thiếu env thì live test **tự skip sạch** — vô tình `go test ./...` không bao giờ chạm tài khoản thật. Chỉ dùng **acc test**, tuyệt đối không acc chính.

Proto: sửa `internal/proto/tytd.proto` → `make proto` (cần `protoc` + `protoc-gen-go`).

---

## 13. Bảo mật

- **Không commit**: `config.yaml` (bản root đã `.gitignore`), `data.dat*`, `cred.txt`. `build/` thì **có** commit — binary dựng sẵn + `build/config.yaml` **bản trống** cho người dùng tải về điền (mục 5.1); tuyệt đối không để phiên bản **đã điền token thật** của file đó bị commit theo. Trước khi push luôn `git status` soát lần nữa.
- `chmod 600 config.yaml`; VPS chỉ mở 22/80/443 qua ufw (mục 7.4).
- Token bot đã từng nằm trong binary cũ coi như **rò rỉ** → cấp lại token mới trong BotFather (`/revoke`) rồi regen bản mã hoá.
- Mật khẩu game từng xuất hiện trong test cũ **đã xoá khỏi code** — vẫn phải **đổi mật khẩu các acc đó** trên web Funtap, vì lịch sử git còn giữ chúng.
- `data.dat` mã hoá sqlcipher nhưng **key DB vẫn hardcode trong code** (`getDK`) — chưa đổi được thì coi `data.dat` = full leak, giữ như giữ token.
- Live test credentials: chỉ env, không bao giờ viết literal vào test mới.

---

## 14. Khắc phục sự cố

| Triệu chứng | Nguyên nhân / cách xử lý |
|---|---|
| Bot không trả lời, log `unauthorized` | Token sai/thu hồi → kiểm tra `getMe`, thử `telegram_token` khác. |
| `telegram_token (or telegram_token_encrypted) is required` / reject khoá lạ | Config sai đường dẫn (`-config`, `TYTD_CONFIG`, `./config.yaml`), thiếu key, hoặc typo key — sửa theo đúng tên trong bảng mục 4. |
| `address already in use :80` | Port 80 có service khác chiếm → tắt nó đi, hoặc `WEB_PORT=8080` + mở port tương ứng trên ufw + khai origin port bên Cloudflare. |
| `permission denied` khi bind :80 | Chạy user thường → `setcap 'cap_net_bind_service=+ep' main` (mục 7.5) hoặc chạy root. |
| Cloudflare báo **Error 521/525/520** khi mở domain | Origin chưa mở 80 (ufw chưa cho / bot chưa chạy), hoặc SSL mode đang để **Full** trong khi bot chỉ HTTP → hạ về **Flexible**. |
| Mini app trắng trang / báo lỗi WebView | thiếu HTTPS; URL Menu Button sai domain; chưa bật `/web on`. |
| Web đăng nhập lặp lại mãi | Cookie `Secure` bị loại vì truy cập HTTP, hoặc `web_bot_username` khai sai khác token thật → so token **sha** trên `/web status` với token đang chạy. |
| `cc: unknown argument -m64` / build cross fail | thiếu cross-gcc đúng target — xem bảng toolchain mục 1, hoặc `make build-native` trên máy đích. |
| `make build` báo `[skip] darwin/... osxcross` | không có SDK macOS trên máy Linux — build bản darwin bằng `make build-native` trực tiếp trên Mac. |
| Binary macOS chạy bị kill | chưa ký: `codesign -s - main-darwin-arm64`. |
| Bot chết OOM-kill trên VPS 512MB (`oom-kill` trong `dmesg`) | chưa tạo swap (bắt buộc với 512MB — mục 7.4) hoặc quá tải user → resize lên 1 GB. |
| `database is locked` / `malformed` | nhiều process cùng mở một `data.dat` — mỗi instance một DB riêng, copy DB khi bot đã dừng. |
| `no such table` khởi động | DB cũ không tương thích: backup rồi cho bot tạo `data.dat` mới (mất acc đã lưu). |

---

## 15. Cấu trúc project

```
cmd/                     main.go (entrypoint), gen.go (mã hoá token), các probe tool
internal/appconfig/      nạp config.yaml + env override (+ unit test)
internal/crypto/         AES-256-GCM cho telegram_token_encrypted
internal/proto/          tytd.proto + code gen (make proto)
internal/model/          DB model, system_config (web_enabled, web_domain, ...)
internal/request/        login Funtap passport, HTTP client
internal/pack/           đóng/mở frame game protocol
telebot.go               wiring bot Telegram, handler registry
telebot_*.go             auth, admin, lệnh, auto từng tính năng
web*.go                  dashboard + mini app (routes mục 9.3), verify initData
build/                   binary dựng sẵn + config.yaml mẫu (commit trong git): linux amd64/arm64, windows amd64
Makefile                 ma trận build đa nền tảng
config.example.yaml      file config mẫu (bản thật: config.yaml, không commit)
deploy.sh                script deploy CŨ — cần cập nhật theo Makefile mới
*_test.go                unit test offline
*_live_test.go           live test (tag `live` + env gate, không có credential trong repo)
```

---

## 16. Miễn trừ trách nhiệm

Project được phát triển và chia sẻ **chỉ với mục đích học tập, nghiên cứu** — về Telegram Bot API, giao thức mạng/Protobuf, kiến trúc bot và tự động hoá. Bạn chỉ nên dùng bot với **tài khoản game hợp pháp của chính mình**.

Bằng việc tải, cài đặt hoặc sử dụng phần mềm này, bạn hiểu và tự chịu trách nhiệm với các điều sau:

- Tự động hoá có thể **vi phạm Điều khoản sử dụng của nhà phát hành game**. Mọi hệ quả — cảnh cáo, khoá tài khoản tạm thời hoặc vĩnh viễn, xoá/về mốc dữ liệu nhân vật — **tác giả không chịu trách nhiệm**.
- Không chịu trách nhiệm với bất kỳ thiệt hại nào phát sinh khi sử dụng: mất tài khoản, mất vật phẩm/tài sản trong game, rò rỉ thông tin đăng nhập, giờ máy chủ bị chiếm… Phần mềm cung cấp theo dạng **"nguyên trạng" (as is), không bảo hành** dưới mọi hình thức.
- Nghiêm cấm sử dụng vào mục đích vi phạm pháp luật hoặc gây hại cho người khác (lừa đảo, chiếm đoạt tài khoản không phải của mình, tấn công dịch vụ…).
- Nếu bạn không đồng ý với các điều trên, **vui lòng không sử dụng phần mềm**.

Việc tiếp tục sử dụng được coi là bạn đã đọc và chấp nhận toàn bộ nội dung mục này.

---

**Hỗ trợ:** @tytd_tool_support
