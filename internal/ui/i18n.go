package ui

import (
	"fmt"
	"strings"
)

// The setup page ships in Vietnamese and English. Vietnamese is the default
// because the dashboard, pricing and support are Vietnamese-first.
const (
	langVI     = "vi"
	langEN     = "en"
	langCookie = "smr_lang"
)

type message struct{ vi, en string }

var messages = map[string]message{
	"header.local":     {"Cấu hình localhost", "Localhost setup"},
	"header.listening": {"nghe tại", "listening on"},
	"header.log":       {"Xem log", "View log"},
	"nav.label":        {"Các bước cài đặt", "Setup steps"},

	"steps.connect": {"Kết nối ACME AI", "Connect to ACME AI"},
	"steps.models":  {"Model theo tier", "Models per tier"},
	"steps.install": {"Cài vào agent", "Install into agent"},
	"steps.verify":  {"Kiểm tra", "Verify"},

	"step1.desc":          {"Key chỉ lưu trên máy này và chỉ dùng để gọi bộ phân loại. Kiểm tra xong bạn sẽ thấy số dư thật của tài khoản.", "The key stays on this machine and is used only for the classifier. A successful check shows your real balance."},
	"step1.key_saved":     {"Đã có key", "Key saved"},
	"step1.key_missing":   {"Chưa có key", "No key yet"},
	"step1.callout.title": {"Chưa có API key? Tạo tài khoản ở dashboard ACME AI.", "No API key yet? Create an account on the ACME AI dashboard."},
	"step1.callout.s1":    {"Đăng ký bằng Google hoặc GitHub.", "Sign up with Google or GitHub."},
	"step1.callout.s2":    {"Nạp tiền từ 20.000 VND, thanh toán bằng QR ngân hàng.", "Top up from 20,000 VND with a bank QR transfer."},
	"step1.callout.s3":    {"Tạo API key, copy rồi dán vào ô bên dưới.", "Create an API key, copy it, then paste it below."},
	"step1.callout.note":  {"Key chỉ hiện một lần. Dashboard mở ở tab mới; copy xong thì quay lại tab này.", "The key is shown once. The dashboard opens in a new tab; come back here after copying it."},
	"step1.callout.cta":   {"Mở dashboard", "Open dashboard"},
	"step1.rotate":        {"Cần key mới? Tạo trên dashboard rồi dán vào ô bên dưới.", "Need a new key? Create one on the dashboard, then paste it below."},
	"step1.base_url":      {"Base URL bộ phân loại", "Classifier base URL"},
	"step1.api_key":       {"API key", "API key"},
	"step1.api_key_hint":  {"Tạo key ở mục API Key trên dashboard. Để trống nếu giữ key đã lưu.", "Create the key under API Key on the dashboard. Leave blank to keep the saved key."},
	"step1.test":          {"Kiểm tra kết nối", "Test connection"},
	"step1.test_note":     {"Key mới sẽ được lưu ngay khi kiểm tra thành công.", "A new key is saved as soon as the check succeeds."},
	"step1.ok":            {"Kết nối thành công.", "Connected."},
	"step1.ok_detail":     {"Số dư %d VND · tối đa %d ký tự mỗi lượt · cache %d giây.", "Balance %d VND · up to %d characters per request · cache %d seconds."},
	"step1.saved":         {"Đã lưu key và Base URL vừa nhập vào cấu hình SMR.", "Saved the key and base URL to the SMR config."},
	"step1.fail":          {"Chưa kết nối được.", "Could not connect."},
	"step1.advanced":      {"Cấu hình nâng cao", "Advanced settings"},
	"step1.max_chars":     {"Max state chars", "Max state chars"},
	"step1.max_chars_hint": {
		"Phần đầu hội thoại gửi cho bộ phân loại. Lớn hơn thì chính xác hơn nhưng tốn token phân loại.",
		"Leading part of the conversation sent to the classifier. More characters are more accurate but cost classifier tokens.",
	},
	"step1.fallback":      {"Fallback tier", "Fallback tier"},
	"step1.fallback_hint": {"Dùng khi bộ phân loại lỗi tạm thời sau khi đã retry.", "Used when the classifier keeps failing after a retry."},
	"step1.env_override":  {"ACMEAI_API_KEY hoặc ACMEAI_BASE_URL đang được đặt bằng biến môi trường, nên giá trị dưới đây chỉ được lưu vào file. Daemon vẫn dùng biến môi trường cho tới khi bạn bỏ biến đó.", "ACMEAI_API_KEY or ACMEAI_BASE_URL is set in the environment, so the fields below are only saved to the file. The daemon keeps using the variable until you remove it."},

	"step2.desc":         {"Mỗi agent dùng một profile riêng. SMR chuyển tiếp nguyên định dạng request, không đổi giữa OpenAI và Anthropic.", "Each agent uses its own profile. SMR forwards the request format as-is and never converts between OpenAI and Anthropic."},
	"step2.configured":   {"%d/%d tier đã đủ", "%d/%d tiers ready"},
	"step2.claude.title": {"Claude Code cần API key/token Anthropic tương thích.", "Claude Code needs an Anthropic-compatible API key or token."},
	"step2.claude.body":  {"Claude Pro OAuth không dùng được cho profile này. Base URL phải là endpoint trả về định dạng Anthropic Messages.", "Claude Pro OAuth cannot be used for this profile. The base URL must return the Anthropic Messages format."},
	"step2.hint":         {"Tier thiếu Base URL hoặc Model sẽ trả 503 tier_not_configured khi request rơi vào tier đó. Có thể dùng cùng một provider cho cả 4 tier.", "A tier missing its base URL or model returns 503 tier_not_configured when a request lands there. All four tiers may share one provider."},
	"step2.save_note":    {"Lưu cho cả bước 1 và bước 2", "Saves both step 1 and step 2"},
	"step2.save":         {"Lưu cấu hình", "Save configuration"},

	"step3.desc":         {"SMR không ghi vào file cấu hình của Codex hay Claude Code. Trang này chỉ hiện đúng phần bạn cần thêm hoặc sửa; bạn tự mở file, dán vào rồi kiểm tra lại.", "SMR does not write to the Codex or Claude Code config files. This page shows exactly what to add or change; you open the file, paste the blocks and check the result."},
	"step3.using":        {"Đang dùng SMR", "Using SMR"},
	"step3.not_using":    {"Chưa trỏ vào SMR", "Not pointed at SMR"},
	"step3.has_file":     {"Đã có file cấu hình.", "The config file exists."},
	"step3.current_base": {"Base URL hiện tại:", "Current base URL:"},
	"step3.model":        {"Model:", "Model:"},
	"step3.no_file":      {"Chưa có file cấu hình. Bạn tự tạo file rồi dán phần bên dưới.", "No config file yet. Create it, then paste the blocks below."},
	"step3.copy":         {"Sao chép", "Copy"},
	"step3.copied":       {"Đã sao chép", "Copied"},
	"step3.codex.default": {
		"Dòng mặc định — đặt ở đầu file, TRƯỚC mọi bảng [..]. Nếu đã có model_provider, sửa giá trị thành smr.",
		"Default line — put it at the top of the file, BEFORE any [table]. If model_provider already exists, change its value to smr.",
	},
	"step3.codex.block": {
		"Thêm nguyên block này vào cuối file:",
		"Add this block at the end of the file:",
	},
	"step3.codex.note": {
		"Đừng dán lẫn dòng model_provider vào trong bảng; TOML sẽ hiểu nó là khóa của bảng cuối cùng. SMR không tự sửa file; bạn tự quyết định có đặt SMR làm mặc định hay không.",
		"Do not paste the model_provider line inside a table; TOML would read it as a key of that table. SMR never edits the file; you decide whether to make SMR the default.",
	},
	"step3.claude.block": {
		"Gộp vào object env đang có trong settings.json (đừng thay cả file):",
		"Merge into the existing env object in settings.json (do not replace the file):",
	},
	"step3.claude.note": {
		"ANTHROPIC_AUTH_TOKEN ở đây chỉ là placeholder; SMR thay bằng key của profile Claude khi forward. Nếu file đang có ANTHROPIC_API_KEY thì giữ hoặc bỏ đều được, SMR không dùng giá trị đó. Khởi động lại Claude Code sau khi sửa.",
		"ANTHROPIC_AUTH_TOKEN here is only a placeholder; SMR replaces it with the Claude profile key when forwarding. If the file also has ANTHROPIC_API_KEY, keep it or remove it; SMR does not use that value. Restart Claude Code after the edit.",
	},

	"step4.before":   {"Mở agent, gửi một câu hỏi, rồi quay lại đây. Header", "Open the agent, send one prompt, then come back here. The header"},
	"step4.mid":      {"cho biết tier đã chọn,", "shows the chosen tier and"},
	"step4.after":    {"là phí của lượt đó.", "the charge for that request."},
	"step4.seen":     {"Đã có request", "Requests seen"},
	"step4.none":     {"Chưa có request", "No requests yet"},
	"step4.daemon":   {"Daemon", "Daemon"},
	"step4.running":  {"Đang chạy", "Running"},
	"step4.requests": {"%d request · %d đã forward", "%d requests · %d forwarded"},
	"step4.classify": {"Phân loại", "Classifier"},
	"step4.unconfirmed": {
		"Chưa xác nhận",
		"Not confirmed",
	},
	"step4.never":       {"Chưa gọi lần nào", "Never called"},
	"step4.last":        {"Lượt gần nhất", "Last request"},
	"step4.last_detail": {"%d VND · số dư %d VND", "%d VND · balance %d VND"},
	"step4.cache":       {"Cache hit", "Cache hits"},
	"step4.cache_hint":  {"Tính 10% giá gốc, tối thiểu 1 VND", "Charged at 10% of the original, minimum 1 VND"},
	"step4.view_log":    {"Xem log", "View log"},
	"step4.config_file": {"File cấu hình:", "Config file:"},
	"step4.readable":    {"chỉ user hiện tại đọc được", "readable by the current user only"},

	"footer.loopback": {"Chỉ bind loopback. Kết nối LAN bị từ chối.", "Bound to loopback only; LAN connections are refused."},
	"footer.no_log":   {"Không log prompt, API key hoặc token.", "Never logs prompts, API keys or tokens."},

	"locked.title":    {"Cần mã truy cập", "Access token required"},
	"locked.lead":     {"SMR chỉ mở trang cấu hình qua lệnh trong terminal. Nhờ vậy một trang web khác không thể đổi cấu hình hay đọc API key trên máy bạn.", "SMR opens this page only from a terminal command. That keeps other websites from changing your config or reading the API key on this machine."},
	"locked.step1":    {"Mở Terminal (PowerShell, Terminal.app hoặc shell).", "Open a terminal (PowerShell, Terminal.app or a shell)."},
	"locked.step2":    {"Chạy lệnh:", "Run this command:"},
	"locked.step3":    {"Trình duyệt tự mở trang cấu hình kèm mã truy cập.", "The browser opens the setup page with the access token."},
	"locked.copy":     {"Sao chép", "Copy"},
	"locked.copied":   {"Đã sao chép", "Copied"},
	"locked.invalid":  {"Mã truy cập không đúng hoặc đã hết hiệu lực. Chạy lại lệnh dưới đây để lấy mã mới.", "The access token is wrong or expired. Run the command below to get a new one."},
	"locked.manual":   {"Nếu trình duyệt không tự mở, terminal in ra URL đầy đủ. Mã cũng được lưu trong %s và chỉ tài khoản của bạn đọc được.", "If the browser does not open, the terminal prints the full URL. The token is also stored in %s, readable by your account only."},
	"locked.footnote": {"Bạn đang mở http://%s trực tiếp nên chưa có mã.", "You opened http://%s directly, so no token was sent."},

	"tier.easy":         {"Dễ", "Easy"},
	"tier.lower_medium": {"Trung bình thấp", "Lower medium"},
	"tier.upper_medium": {"Trung bình cao", "Upper medium"},
	"tier.hard":         {"Khó", "Hard"},

	"placeholder.no_key":    {"Chưa có key", "No key yet"},
	"placeholder.key_saved": {"Đã lưu key · để trống nếu không đổi", "Saved · leave blank to keep"},
	"placeholder.none":      {"Chưa có", "Not set"},
	"placeholder.saved":     {"Đã lưu", "Saved"},

	"notice.saved":     {"Đã lưu cấu hình. Daemon dùng cấu hình mới cho request tiếp theo.", "Configuration saved. The daemon uses it for the next request."},
	"notice.saved_env": {"Đã lưu vào file, nhưng biến môi trường ACMEAI_API_KEY/ACMEAI_BASE_URL vẫn đang override khi daemon chạy.", "Saved to the file, but ACMEAI_API_KEY/ACMEAI_BASE_URL is still overriding it while the daemon runs."},

	"error.save":      {"Không lưu được cấu hình: %s", "Could not save the configuration: %s"},
	"error.test_save": {"Key hợp lệ nhưng không lưu được: %s", "The key is valid but could not be saved: %s"},
	"error.no_key":    {"Chưa có API key. Dán key vào ô API key rồi kiểm tra lại.", "No API key yet. Paste one into the API key field and check again."},
	"error.invalid_key": {
		"API key không hợp lệ hoặc đã bị thu hồi.",
		"The API key is invalid or revoked.",
	},
	"error.no_balance": {"Key hợp lệ nhưng tài khoản chưa có số dư.", "The key is valid but the account has no balance."},
	"error.network":    {"Không kết nối được api.acmeai.vn. Kiểm tra mạng và base URL.", "Could not reach api.acmeai.vn. Check the network and the base URL."},

	"note.codex.no_config":   {"Chưa có cấu hình Codex; bạn tự tạo ~/.codex/config.toml rồi dán các block bên dưới.", "No Codex config yet; create ~/.codex/config.toml yourself, then paste the blocks below."},
	"note.codex.env_key":     {"Codex đang đọc API key từ biến môi trường %s. SMR không sao chép giá trị này.", "Codex reads its API key from the %s environment variable. SMR does not copy that value."},
	"note.codex.no_provider": {"Không tìm thấy provider tùy chỉnh trong Codex config.", "No custom provider found in the Codex config."},
	"note.claude.no_settings": {
		"Chưa có Claude Code settings; bạn tự tạo ~/.claude/settings.json rồi dán object env bên dưới.",
		"No Claude Code settings yet; create ~/.claude/settings.json yourself, then paste the env object below.",
	},
	"note.claude.invalid_json": {"settings.json hiện tại không phải JSON hợp lệ; SMR sẽ không ghi đè.", "settings.json is not valid JSON; SMR will not overwrite it."},
	"note.claude.no_base_url":  {"Không tìm thấy ANTHROPIC_BASE_URL trong Claude Code settings.", "ANTHROPIC_BASE_URL is missing from the Claude Code settings."},
	"note.claude.needs_key":    {"Claude Code cần Anthropic API key/token tương thích. Claude Pro OAuth không được hỗ trợ.", "Claude Code needs an Anthropic-compatible API key or token. Claude Pro OAuth is not supported."},

	"logs.title":  {"Log gần đây", "Recent log"},
	"logs.back":   {"Về cấu hình", "Back to setup"},
	"logs.footer": {"Log không chứa prompt, API key hay token.", "The log never contains prompts, API keys or tokens."},
	"logs.empty":  {"Chưa có log tại %s", "No log yet at %s"},
}

func parseLanguage(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case langVI:
		return langVI, true
	case langEN:
		return langEN, true
	default:
		return "", false
	}
}

// tr returns the message for a language, falling back to Vietnamese when a key
// or translation is missing so the page never renders an empty label.
func tr(lang, key string) string {
	entry, ok := messages[key]
	if !ok {
		return key
	}
	if lang == langEN && entry.en != "" {
		return entry.en
	}
	return entry.vi
}

func trf(lang, key string, args ...any) string {
	return fmt.Sprintf(tr(lang, key), args...)
}

func tierLabel(lang, tier string) string {
	if label := tr(lang, "tier."+tier); label != "tier."+tier {
		return label
	}
	return tier
}

// translateNote converts a hostcfg note code into the current language. A note
// may carry one argument after a colon, for example codex.env_key:OPENAI_KEY.
func translateNote(lang, note string) string {
	key, arg, hasArg := strings.Cut(note, ":")
	full := "note." + key
	if _, ok := messages[full]; !ok {
		// Unknown codes fall back to the raw value so a new note is still
		// visible instead of rendering an internal identifier.
		return note
	}
	if hasArg {
		return trf(lang, full, arg)
	}
	return tr(lang, full)
}
