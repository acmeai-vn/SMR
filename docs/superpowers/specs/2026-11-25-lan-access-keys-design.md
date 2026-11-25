# SMR LAN access with access keys

- **Ngày:** 2026-11-25
- **Trạng thái:** đã duyệt và triển khai
- **Repo:** `acmeai-vn/SMR`

## 1. Mục tiêu

Cho phép máy khác trong mạng nội bộ dùng SMR đang chạy trên máy này, có xác thực bằng access key.

Hiện tại toàn bộ an ninh của SMR nằm ở một dòng: `config.Validate` bắt `listen` phải loopback. Không có xác thực nào trên proxy. Vì vậy mở ra LAN mà không thêm xác thực là mở luôn key provider và số dư ACME AI của chủ máy cho cả mạng.

Người dùng đã chọn **phương án C**: một quy tắc duy nhất — mọi request tới proxy đều cần key, kể cả từ localhost.

## 2. Mục tiêu / phi mục tiêu

**Mục tiêu**

- Proxy chấp nhận kết nối từ LAN khi `listen` không phải loopback.
- Mọi request tới proxy phải có access key hợp lệ; không phân biệt nguồn.
- Nhiều key có tên, tạo và thu hồi độc lập, **có hiệu lực ngay** không cần restart.
- `smr setup` in ra block cấu hình Codex/Claude Code đã điền sẵn URL LAN và key, để máy khác chỉ cần dán.
- Cấu hình sai (bind ra LAN mà không có key) phải bị từ chối khi `serve`, kèm đường sửa được bằng CLI.

**Phi mục tiêu**

- Không mở UI `8788` ra LAN. UI ghi đè được key provider, nên vẫn chỉ loopback.
- Không có TLS, không có user/role, không có quota theo key ở phase này.
- Không đổi hành vi phân loại, tính phí, hay định tuyến tier.
- Không viết file cấu hình Codex/Claude Code (giữ nguyên nguyên tắc "SMR never writes host config").

**Anti-goals** (làm ra là hỏng, dù chạy được)

- Một request không key vẫn tới được provider.
- Một key đã thu hồi vẫn dùng được (kể cả trước lần restart kế tiếp).
- Bind `0.0.0.0` mà không có key mà daemon vẫn khởi động.
- Làm Codex/Claude Code trên chính máy này chết mà không có thông báo nói rõ phải thêm gì.

## 3. Các phương án đã cân nhắc

| | Cách làm | Đánh đổi |
|---|---|---|
| A | Thêm listener LAN riêng (`0.0.0.0:8789`), giữ `127.0.0.1:8787` không key | Máy local không phải sửa gì, nhưng có hai đường vào với hai chính sách, và hai cổng để cấu hình sai |
| B | Một listener, chỉ đòi key khi `RemoteAddr` không phải loopback | Ít code nhất, nhưng tin vào `RemoteAddr`; đặt reverse proxy trước là thủng |
| **C** | **Một listener, đòi key cho mọi request** | **Máy local phải thêm key vào config Codex/Claude — hai ô đó vốn đã tồn tại** |

**Chọn C.** A và B đều dựa vào giả định "loopback là tin được" — đúng là giả định đang phải phá bỏ. C cho trạng thái "có key / không key" đồng nhất, `smr doctor` chỉ cần kiểm tra một điều kiện, và không có nhánh code nào quên kiểm tra.

## 4. Cấu hình

```yaml
access_keys:
  - name: "local"
    key: "smr_lan_9Kx2...cQ7"
    created_at: "2026-11-25T03:14:00Z"
  - name: "may-cua-vo"
    key: "smr_lan_4Fq2...23A1"
    created_at: "2026-11-25T03:20:11Z"
```

- Key dạng `smr_lan_` + 32 ký tự base64url (192 bit), sinh bằng `crypto/rand`.
- Lưu **plaintext**, giống `classifier.api_key` và key provider đang nằm trong cùng file. File đã là `0600` và `fileperm.Restrict`. Băm key không thêm bảo vệ thực chất: ai đọc được file này đã có key OpenAI/Anthropic, thứ giá trị hơn nhiều. Đổi lại, `smr setup` in lại được block cấu hình bất cứ lúc nào.
- `name` là duy nhất; trùng tên bị từ chối.
- Go: `config.AccessKey{Name, Key, CreatedAt}` và `Config.AccessKeys []AccessKey`.

**`config.clone` phải deep-copy `AccessKeys`.** Hiện nó chỉ copy `Profiles`. Nếu để slice chia sẻ backing array thì UI revoke-in-place sẽ race với proxy đang đọc trong `Snapshot()`.

## 5. Tách hai tầng kiểm tra cấu hình

Đây là thay đổi bắt buộc, không phải chi tiết phụ. Hiện `config.Load` gọi `Validate` trước khi trả về, nên nếu file có `listen: 0.0.0.0:8787` mà chưa có key thì **mọi lệnh đều fail**, kể cả `smr keys add` — tức là không có đường tự sửa, và check mới của `doctor` thành code chết.

Tách `Validate` thành:

| Hàm | Nội dung | Ai gọi |
|---|---|---|
| `ValidateSchema` | kiểu dữ liệu, `host:port`, `listen != ui_listen`, tier/config hợp lệ, key có tên và không trùng | `config.Load`, UI, mọi lệnh CLI |
| `ValidateRuntime` | `ui_listen` phải loopback; `listen` non-loopback **chỉ khi** có ≥1 access key | `serve` (trước khi bind), `doctor` (để báo), `keys revoke` (để chặn xoá key cuối) |

`LoadManager` dùng `ValidateSchema`, để daemon và CLI luôn đọc được file hỏng-runtime và sửa lại được. `serve` gọi `ValidateRuntime` ngay sau `LoadManager`, trước `AcquireLock`, để không chiếm lock khi cấu hình sai.

Thông báo lỗi khi thiếu key phải nêu đúng lệnh sửa: `config: listen is not loopback; add an access key with `smr keys add <name>``.

## 6. Địa chỉ: bind / connect / advertise

Builder check chỉ ra lỗi thật: `service.HealthCheck`, `runSetup`, `runStatus`, `runDoctor`, `restartDaemon` đều dựng URL bằng `"http://"+cfg.Listen`. Nếu `listen: 0.0.0.0:8787`, Windows từ chối kết nối tới `0.0.0.0` (`WSAEADDRNOTAVAIL`), nên `smr setup` sẽ restart-loop và `status`/`doctor` báo daemon chết.

Thêm ba helper trên `config.Config`:

| Helper | Quy tắc | Dùng ở |
|---|---|---|
| `BindAddr()` | `cfg.Listen` nguyên bản | `net.Listen` |
| `ConnectAddr()` | host của `listen` là `0.0.0.0` hoặc `::` → `127.0.0.1:<port>`, ngược lại giữ nguyên | `HealthCheck`, `status`, `doctor`, `setup`, `restartDaemon` |
| `AdvertiseURL()` | xem §7 | block cấu hình host |

Mọi caller hiện tại đổi sang `ConnectAddr()`. Không có chỗ nào được dùng `BindAddr()` để kết nối.

`listen == ui_listen` phải so **host:port sau khi chuẩn hoá**, không so string: `0.0.0.0:8788` và `127.0.0.1:8788` trùng cổng và sẽ làm `net.Listen` fail với lỗi khó hiểu. So theo port, và coi wildcard là trùng với mọi host cùng port.

## 7. Địa chỉ quảng bá cho máy khác

`AdvertiseURL()`:

1. Host của `listen` là IP cụ thể (không wildcard) → dùng chính nó. Tránh trường hợp máy có VPN/Docker/WSL và "IPv4 đầu tiên" là interface sai.
2. Host là wildcard → mở UDP socket tới `8.8.8.8:80` (không gửi gói nào) và đọc local address. Đây là cách lấy đúng interface mang default route, tránh bắt nhầm adapter Docker/WSL/VPN.
3. Lỗi → `127.0.0.1`.

Test phải phủ case 1 và case 3; case 2 kiểm tra bằng cách inject hàm lấy default route.

## 8. Xác thực ở proxy

Middleware bọc toàn bộ route trừ `GET /health`.

- Nhận key từ `Authorization: Bearer <key>` (chấp nhận cả `Authorization: <key>` trần) hoặc `x-api-key: <key>`.
- So sánh constant-time với **toàn bộ** key, không thoát sớm — `crypto/subtle.ConstantTimeCompare`, cùng kiểu với `ui.equal`.
- Key đúng: cho qua, gắn tên key vào context để đếm thống kê.
- Key sai/thiếu: `401` + `WWW-Authenticate: Bearer realm="smr"`.
- Body lỗi theo đúng dialect của client: `/v1/messages` và `/v1/messages/count_tokens` trả shape Anthropic (`{"type":"error","error":{"type":"authentication_error","message":"..."}}`); các path còn lại trả shape OpenAI (`{"error":{"code":"authentication_error","message":"..."}}`). Claude Code đọc sai shape sẽ báo lỗi khó hiểu thay vì "sai key".

`GET /health` ở lại không cần key vì `service.HealthCheck` dùng nó để biết daemon còn sống. Payload rút gọn còn:

```json
{"status":"ok","version":"2","auth_required":true}
```

Các trường `classifier.*` chuyển sang `GET /stats` (vốn đã cần key), để probe không tiết lộ đang trỏ testing hay production.

Hệ quả cho CLI: `runStatus` in classifier từ **config local** (không phải từ `/health`), và gọi `/health` chỉ để biết sống/chết. `service.Health` bỏ nhóm `Classifier`.

`GET /stats` bổ sung `auth_failures` và `keys: [{name, requests, last_seen_at}]`.

Log mỗi lần 401 ở mức `Info` kèm `remote`, `path`, `key_name` (nếu có).

## 9. Thay đổi key có hiệu lực ngay

`config.Manager` cache trong RAM và proxy đọc `Snapshot()` mỗi request. Nếu CLI sửa thẳng file YAML thì key mới **không** dùng được, key đã thu hồi **vẫn** dùng được cho tới lần restart — vi phạm anti-goal. Đồng thời có hai writer cùng ghi một file.

Quy tắc: **daemon đang chạy là writer duy nhất.**

- Thêm `POST /access/add` và `POST /access/revoke` vào UI server (loopback + `ui-token`, như các route khác).
- `smr keys add|revoke` gọi hai endpoint này qua `ConnectAddr()` của `ui_listen`, dùng token đọc từ `ui-token`. Hiệu lực tức thì, không restart, không mất request đang chạy.
- Daemon không chạy (health fail) → CLI sửa file trực tiếp. Lúc đó không có writer nào khác nên an toàn, và CLI in `restart SMR to apply` (thực tế `serve` sẽ tự đọc lại khi khởi động).

Cần thêm: nếu `keys revoke` xoá key cuối cùng trong khi `listen` non-loopback, từ chối và nói rõ lý do (lần khởi động sau sẽ fail).

## 10. Tạo key lần đầu

Khi `serve` khởi động mà `access_keys` rỗng: sinh key tên `local`, lưu vào config, ghi log `SMR access key (local): smr_lan_...`.

Lý do: nâng cấp lên bản này sẽ làm Codex/Claude Code trên chính máy chủ nhận 401 ngay. Tự tạo key giúp `smr setup` có sẵn thứ để in, và log là đường thoát nếu UI không mở được.

Đây là lần duy nhất SMR tự sửa config ngoài hành động của người dùng, và nó chỉ thêm key, không đụng key provider.

## 11. CLI

```
smr keys list
smr keys add <name>
smr keys revoke <name>
```

- `add` in key vừa sinh + block cấu hình sẵn sàng dán.
- `list` hiện tên, ngày tạo, số request, lần dùng cuối (từ `/stats`; daemon tắt thì chỉ hiện tên và ngày tạo).
- `smr doctor` thêm: `ValidateRuntime` fail → in lỗi kèm lệnh sửa, không thoát sớm; có key → liệt kê tên và cảnh báo HTTP không mã hoá.

## 12. UI setup

Thêm mục **"Thiết bị truy cập"**: bảng key (tên, ngày tạo, số request, lần dùng cuối), nút "Thêm thiết bị" và "Thu hồi" (có xác nhận). Nếu `listen` đang loopback, hiện hộp nhắc đổi sang `0.0.0.0:8787`.

**Đây không phải một tab profile.** `page.html` và `handleTabsCSS`/`profileNames` hiện sinh tab từ `cfg.Profiles`. Mục này phải là tab riêng trong template (không đi qua vòng lặp profile), hoặc một trang con `/access`; nếu nhét vào `Profiles` thì tab sẽ không hiện hoặc chuyển tab sai.

Bước 3 dùng `AdvertiseURL()` thay `cfg.Listen`:

- Codex block thêm `env_key = "SMR_API_KEY"` và hướng dẫn đặt biến môi trường (PowerShell `setx SMR_API_KEY "..."`, bash `export SMR_API_KEY=...`).
- Claude block điền `ANTHROPIC_AUTH_TOKEN` bằng key thật thay placeholder.
- **Key điền vào block lấy từ key tên `local`** (key của chính máy chủ). Nếu không có `local` thì key đầu tiên; nếu chưa có key nào thì hiện lời nhắc tạo trước. Block cấu hình của một thiết bị mới sinh ra ở luồng "Thêm thiết bị" và mang key của thiết bị đó, không dùng `local`.

`hostUsesSMR` hiện chỉ nhận `127.0.0.1:<port>`; phải nhận cả `AdvertiseURL()` và mọi IP LAN cùng port, nếu không trang sẽ báo "chưa cấu hình" dù đã đúng.

Route mới `POST /access/add`, `POST /access/revoke` sau `authorize` + CSRF như `/save`.

## 13. Cấu hình phía client

Codex:

```toml
[model_providers.smr]
name = "Smart Model Router"
base_url = "http://192.168.1.20:8787/v1"
wire_api = "responses"
env_key = "SMR_API_KEY"
```

Claude Code:

```json
"ANTHROPIC_BASE_URL": "http://192.168.1.20:8787",
"ANTHROPIC_AUTH_TOKEN": "smr_lan_..."
```

`ANTHROPIC_AUTH_TOKEN` đi thẳng vào `Authorization: Bearer`; `ANTHROPIC_API_KEY` (gửi `x-api-key`) cũng được chấp nhận. Cả hai khớp middleware.

## 14. Ràng buộc và giới hạn đã biết

- **HTTP không mã hoá.** Key và toàn bộ prompt đi qua LAN dạng rõ. Trong nhà chấp nhận được; Wi-Fi chung hoặc mạng công cộng phải bọc Tailscale/WireGuard. Ghi rõ trong README.
- **Windows Firewall** sẽ hỏi khi bind `0.0.0.0`. Tài liệu hướng dẫn chỉ cho profile **Private**.
- **IPv6** không xử lý riêng; `AdvertiseURL()` chỉ tìm IPv4.
- **Không chống brute force.** Key 192-bit nên không cần; log để phát hiện client cấu hình sai.

## 15. Ảnh hưởng tới bản đang chạy

- Người dùng hiện tại (Codex không có `env_key`) nhận 401 ngay sau khi nâng cấp cho tới khi đặt `SMR_API_KEY` và thêm `env_key`.
- Giảm đau: key `local` tự tạo, in ra log, hiện ở bước 3 của `smr setup`; `smr doctor` báo đúng việc cần làm.
- Release notes phải nói rõ đây là thay đổi phá vỡ tương thích và ghi 3 bước khắc phục.

## 16. Kiểm thử

- `config`: `ValidateSchema` chấp nhận non-loopback + 0 key (để sửa được); `ValidateRuntime` từ chối; `ui_listen` non-loopback → cả hai đều từ chối; tên key trùng → lỗi; `clone` không chia sẻ backing array (revoke trên bản clone không ảnh hưởng bản gốc); `listen`/`ui_listen` trùng port khi một bên là wildcard → lỗi.
- `address`: `ConnectAddr` map `0.0.0.0`/`::` về `127.0.0.1`, giữ nguyên IP cụ thể; `AdvertiseURL` ưu tiên host cụ thể, fallback `127.0.0.1` khi không có route.
- `proxy`: không key → 401 shape OpenAI; `/v1/messages` không key → 401 shape Anthropic; `Bearer` đúng; `x-api-key` đúng; key sai; key đã thu hồi; `/health` không key → 200 và không chứa `classifier`.
- `keys`: add/revoke qua endpoint loopback có token; revoke key cuối khi listen non-loopback → từ chối; daemon tắt → CLI sửa file và in "restart".
- `hostcfg`: block Codex có `env_key`; block Claude dùng key thật; URL LAN khi listen non-loopback; `hostUsesSMR` nhận URL LAN.
- `ui`: tab "Thiết bị truy cập" render độc lập với `cfg.Profiles`; thêm/revoke qua POST có CSRF.

## 17. Việc còn để người triển khai quyết

- Tên chính xác của mục UI và vị trí tab.
- Có cần `smr setup --lan` (tự đổi `listen`, tạo key) hay chỉ hướng dẫn sửa `config.yaml`.
- Có forward `requests` theo key vào `/stats` hay chỉ hiện trong UI.
