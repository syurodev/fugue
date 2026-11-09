# Spike Gio: bốn chỗ Gio buộc phải tự làm

Ngày 2026-11-09. Nhánh `spike/gio-risk-widgets`, thư mục `spikes/gio-widgets/` (module riêng, Go 1.27.3, `gioui.org` v0.10.3 như `ui/go.mod`). Không đụng `ui/` hay `internal/`.

Mục tiêu: đo xem Gio có gánh được bốn thứ mà desktop-app §5 cần (chọn chữ xuyên item, composer, diff, danh sách dài) trước khi vào F10.3/F10.4. Đây là spike, không phải tính năng.

## 1. Kết luận một dòng cho mỗi spike

| Spike | Kết luận |
| --- | --- |
| 1. selection (chữ xuyên item) | **Đạt nhưng tốn công**: phải tự viết engine chữ + chọn (~750 dòng trong spike, ước 1.200–1.600 dòng khi vào `ui/`), độ khó vừa–cao. Chạy đúng, nhanh |
| 2. composer | **Đạt nhưng tốn công nhẹ**: Enter/Shift+Enter, chuyển từ, menu slash, dán chạy được; thiếu undo theo cụm (~100 dòng) và cần chặn dán quá lớn. IME Telex chưa kiểm được (chủ dự án thử tay) |
| 3. diff | **Đạt**: 5.000 dòng, màu cú pháp bằng chroma, gutter, nền thêm/xóa, chọn và copy đúng text diff; tokenize phải làm ngoài luồng UI |
| 4. list | **Đạt**: `layout.List` của Gio đã bám đáy và giữ vị trí khi user cuộn lên; gập/mở không làm item phía trên nhúc nhích (0 px) |
| Chung | **Giữ Gio kèm điều kiện** (§8). Hai khoảng trống không phải do bốn spike: không có accessibility trên desktop, và font Inter biến thiên không có chữ đậm |

## 2. Cách đo và giới hạn của số đo

**Quan trọng, đọc trước khi tin số nào.**

- Máy chạy spike: Ubuntu 26.04, GNOME 50, Wayland, Intel Iris Xe. Có `WAYLAND_DISPLAY` nhưng **cửa sổ thật không nhận frame callback** trong phiên này (probe: 2 frame lúc mở, rồi 1 frame sau 3 s, rồi hết). Nên **fps của cửa sổ thật không đo được**. Phần còn lại đo bằng hai đường:
  - **`-bench`**: chạy kịch bản không cửa sổ, trên `gpu/headless` (EGL), vòng lặp frame liên tục không vsync. "fps" trong bảng là *số frame tối đa máy này vẽ được* (layout + dựng ops + nộp GPU), dùng như độ dư so với 60 fps, **không phải fps hiển thị**. Cột CPU của `-bench` luôn xấp xỉ 100–125% vì vòng lặp bận, bỏ qua cột đó.
  - **`-auto -only idle`**: cửa sổ thật, chờ 2 s cho hết khởi động rồi đo CPU 7 s. Đây là số "rảnh". Cửa sổ có thể không hiển thị nên đây là chứng cứ yếu hơn máy thật.
- Layout p95 là thời gian hàm `Layout` của app (không tính nộp GPU). Đây là số so với ngưỡng 8 ms.
- Cột "layout max" thường dính frame đầu tiên (nạp font, dựng cache): 10–30 ms một lần lúc mở. Bình thường.
- Không đo trên MacBook; ngưỡng §9 là cho máy Ubuntu của chủ dự án nên chủ dự án chạy lại (§9 cuối báo cáo).

Số gốc ở `results/*.txt`, ảnh chụp headless ở `results/selection.png` và `results/diff.png`.

## 3. Spike 1: chọn chữ xuyên item (rủi ro lớn nhất)

**Kết quả: Đạt nhưng tốn công.**

### Vì sao phải tự làm
`widget.Selectable` chỉ chọn trong một nhãn, và `textView` bên trong là không export. Không có API "điểm ảnh → vị trí ký tự" cho code ngoài, nên không dựng được vùng chọn xuyên nhiều nhãn bằng widget có sẵn.

### Đã làm (`internal/flow`, `internal/view`)
- Item = danh sách block (đoạn, mục list, code block); block = các run có kiểu (thường, đậm, code nội dòng, mono, mờ). `flow.Layout` xếp chữ theo từ (shaper đo từng từ + khoảng trắng đuôi, lưu x của từng ranh giới rune), ngắt dòng tham lam, code block không ngắt dòng.
- Vùng chọn là hai điểm neo **(chỉ số item, offset rune trong text của item)**, không phải pixel. Nhờ đó nó sống sót qua cuộn, ảo hóa và item mới stream vào cuối.
- `Selector` đăng ký một handler con trỏ phủ cả viewport (bật `PassOp` để thanh cuộn và cuộn bánh xe của list vẫn chạy), đổi pixel → (item, offset) bằng hình học của frame trước, hỗ trợ: kéo chuột, Shift+click mở rộng, kéo ra ngoài viewport thì tự cuộn, Ctrl+C ghi clipboard plain text đúng thứ tự (đoạn cuối của A, nguyên các item giữa, đoạn đầu của C, nối bằng dòng trống), Ctrl+A.
- List dùng thẳng `layout.List` + `material.List` (thanh cuộn có sẵn).

### Kiểm chứng (test chạy với con trỏ và phím giả qua `input.Router`)
- Round-trip pixel↔offset đúng cho mọi offset của 40 item ngẫu nhiên ở ba bề rộng (`flow_test.go`).
- Chọn từ giữa item A tới giữa item C xuyên code block: clipboard bằng đúng text mong đợi, đủ các item giữa (`TestSelectAcrossItemsAndCodeBlocks`).
- Cuộn xa rồi stream thêm vào item cuối và thêm item mới: text copy **không đổi** (cùng test).
- Shift+click, kéo ra dưới viewport tự cuộn (120 frame đi từ item 0 tới item 40).
- Kịch bản `-bench` (cuộn 6 s, rồi stream 600 ký tự/s + thêm item mỗi 2 s): vùng chọn đầu và cuối **giữ nguyên** sau chạy.

### Số đo (2.000 item markdown, headless)

| Pha | fps tối đa | layout p95 | render p95 | RSS |
| --- | --- | --- | --- | --- |
| cuộn | 362 | 0,84 ms | 3,0 ms | 143 MB |
| stream 600 ký tự/s | 290 | 0,80 ms | 3,7 ms | 143 MB |
| rảnh (cửa sổ thật) | — | — | — | CPU 0,0%, 132 MB |

Dựng lại một tin nhắn 20 KB mỗi delta: ~1,0 ms (benchmark `BenchmarkLayout20KBWarm`), tức dưới 3% một core ở 30 delta/s.

### Vấn đề gặp
1. **Không có chữ đậm.** Gio v0.10.3 không áp trục biến thiên của font (không có mã variation trong `font/opentype`), nên `Inter-Variable.ttf` luôn ra nét thường: ảnh chụp cho thấy tiêu đề `**Fugue**` không đậm. Phải đóng gói file **tĩnh** `Inter-Bold` (OFL, ~300 KB) và đăng ký bằng `fonts.go` (vài dòng). Đây là lỗi của skeleton F10.2a, không riêng spike. Spike chưa thêm vì không có file tĩnh sẵn ở máy; **cần xử lý trước F10.3**.
2. **Xếp chữ theo từ.** Không kerning/ligature qua khoảng trắng (không đáng kể với tiếng Việt, Latin). Chữ không có khoảng trắng (CJK, Thái) không có điểm ngắt: hiện chỉ cắt cứng theo rune khi một từ dài hơn dòng. RTL chưa hỗ trợ. Với phạm vi giai đoạn A (Việt, Anh) chấp nhận được; ghi vào giới hạn.
3. `layout.List` đôi khi gọi phần tử thêm một item thừa ngoài viewport (trước/sau); `Selector.End` đã lọc theo `Position.First/Count`.
4. Router không tự cấp focus cho handler nếu thiếu `key.FocusFilter`: Ctrl+C im lặng. Đã gặp và sửa.
5. Thanh cuộn của `material.List` tính vị trí theo chỉ số item, nên con trượt có thể nhảy nhẹ khi chiều cao item chênh nhiều. Chưa thấy bằng mắt (không có màn hình), cần chủ dự án xem.

### Khi đưa vào `ui/` thật
- Chuyển `flow` thành package `ui/internal/textflow`; renderer goldmark (DA-03) sinh `[]Block` thay cho `gen`. Còn phải làm: link (nhấp mở, hiện host), bảng (lưới cột), heading, nhãn ảnh, tab trong code (DA-E03), chọn từ/dòng bằng nhấp đúp/ba, menu chuột phải "Sao chép", con trỏ tay trên link, cache layout theo `item_id`+`revision` thay cho cache theo chỉ số.
- Ước lượng: 1.200–1.600 dòng (kể cả test golden), 2–3 tuần cho một người. Độ khó vừa–cao, không có ẩn số kỹ thuật nào còn lại; ẩn số còn lại là cảm giác tay (§9).
- Kéo chọn rất dài (vài nghìn item) dựa vào tự cuộn khi kéo ra mép + Shift+click; đủ dùng, không cần thêm.

## 4. Spike 2: composer

**Kết quả: Đạt nhưng tốn công nhẹ.** Dựng trên `widget.Editor`, `internal/composer` (175 dòng).

| Yêu cầu | Kết quả | Ghi chú |
| --- | --- | --- |
| Enter gửi, Shift+Enter xuống dòng | Đạt, có sẵn | `Editor.Submit = true`. Test qua router |
| Undo/redo | **Có sẵn nhưng quá vụn** | Mỗi phím là một bước: gõ `hello world` rồi Ctrl+Z ra `hello worl`. Cần gom theo từ/khoảng nghỉ: ước ~80–120 dòng (ngăn xếp riêng chặn Ctrl+Z trước Editor, lưu diff thay vì cả chuỗi), độ khó thấp–vừa |
| Ctrl+←/→ theo từ | Đạt, có sẵn | Đi 8 → 4 → 0, rồi sang phải tới 3 |
| Dán 100 KB không treo | Đạt cho dán thường; **không đạt cho trường hợp xấu** | Xem số đo |
| Menu slash khi gõ `/` ở đầu | Đạt, tự làm 120 dòng | Lọc theo tiền tố, ↑/↓/Tab/Enter/Esc và nhấp chuột; menu chỉ giành phím khi đang mở (nên Enter vẫn gửi khi menu đóng). `/` giữa dòng không bật menu |
| IME Telex qua IBus | **Chưa kiểm được** | Chủ dự án thử tay (§9) |

### Số đo (headless, mỗi phím gõ một frame)

| Văn bản đã có | frame p50 | frame p95 | RSS |
| --- | --- | --- | --- |
| 1 KB | 1,8 ms | 2,7 ms | 232 MB |
| 20 KB (320 dòng) | 5,6 ms | 6,0 ms | 263 MB |
| 100 KB (1.600 dòng) | 23 ms | 28 ms | 280 MB (đỉnh 307) |
| 100 KB một đoạn không xuống dòng | 82 ms | 101 ms | **570 MB** |

Thời gian `Layout` chỉ ~0,1–0,3 ms vì `Editor` làm lười; chi phí thật nằm ở `Insert`/xếp lại chữ khi sửa, hiện trong "frame". Đo riêng một lần dán: chèn 100 KB nhiều dòng mất 47 ms (cộng 27 ms layout đầu), một đoạn 100 KB mất 112 ms. Một cú giật 1–2 frame, không treo.

### Vấn đề gặp
1. Gõ vào văn bản lớn chậm theo kích thước: dưới ~20–30 KB vẫn trong ngân sách; 100 KB thì mỗi phím trễ 23 ms, đoạn dài không xuống dòng 82 ms và ăn 570 MB RAM (vượt ngưỡng 300 MB). Đề xuất: văn bản dán vượt ~20 KB thành **tệp đính kèm** ("đã dán 100 KB") thay vì chèn vào ô; chi phí ~60 dòng.
2. Ở chế độ `Submit`, một `EditEvent` chứa `\n` (ví dụ IME commit có xuống dòng) bị cắt tại `\n` rồi gửi (`widget/editor.go` ~dòng 441). Dán bằng Ctrl+V thì không bị (đi qua `DataEvent` → `Insert`, giữ xuống dòng); test dán của spike gọi `Insert` nên khớp đường này, nhưng chưa chạy qua clipboard thật. Cần thử tay: dán nhiều dòng từ clipboard thật.
3. `Editor` không tự đặt con trỏ sau `EditEvent`; lớp `app.Window` phản hồi bằng `SelectionEvent`. Test dùng router trần phải giả điều đó. Nghĩa là **đúng-sai của Telex nằm ở lớp cửa sổ của Gio**, không phải ở code mình: chỉ thử tay mới biết.
4. Các phím menu phải đăng ký với `Focus: &Editor` để lấy trước Editor; hoạt động nhưng phụ thuộc việc Editor dùng chính con trỏ của nó làm tag.

### Khi đưa vào `ui/`
`composer` như spike + undo gom cụm + chip đính kèm cho dán lớn + nút "Lập đội", chọn model, nút dừng lượt. Ước 450–600 dòng cho cả F10.3 phần composer.

## 5. Spike 3: diff

**Kết quả: Đạt.** `internal/diff` (198 dòng) dựng 5.000 dòng diff từ mã Go thật (`net/http` của thư viện chuẩn, thêm/xóa/sửa/chèn ngẫu nhiên, ngữ cảnh 3 dòng, có tiêu đề `@@`).

- Tô màu: `github.com/alecthomas/chroma/v2` v2.27.0, **MIT** (kéo theo `dlclark/regexp2/v2`, cũng MIT; lexer Go dùng định nghĩa XML kiểu Pygments, BSD-2). Thuần Go, không cgo.
- Mỗi dòng diff là một item của cùng engine với transcript: gutter số dòng cũ/mới (vẽ, không chọn được), nền xanh/đỏ cho thêm/xóa, cuộn, chọn xuyên dòng, Ctrl+C. Text copy **đúng từng ký tự là dòng diff gốc** (có `+`/`-`/khoảng trắng đầu dòng); test kiểm cả 5.000 dòng (`TestItemsCarryTheDiffText`).

| Pha | fps tối đa | layout p95 | render p95 | RSS |
| --- | --- | --- | --- | --- |
| cuộn 5.000 dòng | 467 | 1,03 ms | 1,9 ms | 176 MB |
| rảnh (cửa sổ thật) | — | — | — | CPU 0,0%, 173 MB |

Chi phí dựng: tokenize hai phía (~21.000 dòng nguồn) **~580 ms** (≈29 µs/dòng), dựng item 1 ms. Một diff 5.000 dòng tốn khoảng 150 ms tokenize.

### Vấn đề gặp
1. Tokenize phải chạy **ngoài luồng UI** và cache theo hash nội dung; chưa làm trong spike (đo trực tiếp).
2. Trong app thật chỉ có diff của `file_change` (transcript §3), không có nguyên file. Spike tokenize cả file rồi cắt theo dòng, nên chuỗi nhiều dòng (comment khối, raw string) luôn đúng màu. Với diff thuần, trạng thái đầu hunk không biết, có thể sai màu ở vài dòng giữa comment khối. Chấp nhận, hoặc dựng lại "bên cũ/bên mới" từ các hunk và tokenize từng hunk.
3. Chưa có: cuộn ngang cho dòng dài (hiện cắt, ước +60 dòng), xuống dòng tùy chọn, tô khác biệt trong dòng, hai cột song song, gập hunk không đổi, ngôn ngữ khác ngoài Go (chroma có sẵn, chọn lexer theo đuôi tệp: ~10 dòng), mở rộng tab.

### Khi đưa vào `ui/`
Diff mở tại chỗ trong item `file_change` (§5): dùng lại engine, thêm tokenize bất đồng bộ + cache, cuộn ngang, nút sao chép không kèm `+/-`. Ước 350–450 dòng.

## 6. Spike 4: danh sách dài

**Kết quả: Đạt.** 10.000 item cao thấp khác nhau (người dùng, trợ lý markdown, `tool_call`, `reasoning`), dùng `layout.List` neo theo (item đầu, offset), nên không cần biết tổng chiều cao.

Test (`internal/view`, con trỏ giả qua router):
- Ở đáy với `ScrollToEnd`: stream 5 lần, item cuối luôn chạm đúng mép dưới viewport. **Đạt.**
- Cuộn lên bằng bánh xe: `BeforeEnd` bật, stream tiếp 5 lần, **không item nào dịch chuyển** (0 px, cũng đo trong `-bench`: "largest jump of a held item: 0 px"). **Đạt.**
- Cuộn lại xuống đáy: tự bám đáy trở lại. **Đạt.**
- Gập/mở `tool_call`/`reasoning`: mọi item từ đầu tới item vừa gập đứng yên (0 px); chỉ phần bên dưới dịch, đúng ý. **Đạt.**

| Pha | fps tối đa | layout p95 | render p95 | RSS |
| --- | --- | --- | --- | --- |
| bám đáy + stream 30 delta/s | 370 | 0,73 ms | 3,2 ms | 165 MB |
| đã cuộn lên + stream | 589 | 0,42 ms | 1,5 ms | 172 MB |
| gập/mở + stream | 610 | 0,40 ms | 1,5 ms | 161 MB |
| rảnh (cửa sổ thật) | — | — | — | CPU 0,1%, 154 MB |

### Vấn đề gặp
1. Khi đang bám đáy mà gập/mở item gần đáy, nội dung phía trên dịch (vì đáy là mốc). Là hành vi của mọi chat bám đáy; chấp nhận hoặc tắt bám đáy khi user bấm gập.
2. Chiều cao đã dựng cache theo `(chỉ số, rev, bề rộng)`; spike cache trong item. Thiết kế §4 muốn cache theo `item_id`+`revision` với trần 2.000 item: cần làm khi vào `ui/`. RSS 165 MB cho 10.000 item chưa có trần cache.
3. Thanh cuộn: xem vấn đề 5 của spike 1.

### Khi đưa vào `ui/`
`transcript.View` = `layout.List` + `Selector` + cache theo revision + nhận delta từ host; ước 300–400 dòng ngoài engine chữ.

## 7. Nghiên cứu (không code)

Nguồn: mã nguồn và README trong module cache (`gioui.org@v0.10.3`, `gioui.org/x@v0.10.3`), không có tài liệu web nào của Gio nói rõ hơn (tìm web không ra thông tin về accessibility của Gio).

**Accessibility / screen reader.** Gio có `gioui.org/io/semantic` (nhãn, mô tả, lớp, đã chọn, bật/tắt) và các widget phát ra thông tin đó (`widget/button.go`, `label.go`, `editor.go`). **Nhưng chỉ `app/os_android.go` đọc cây này (TalkBack)**; không có mã AT-SPI (Linux), NSAccessibility (macOS) hay UI Automation (Windows) trong module (grep toàn cây `app/`, chỉ `window.go` và `os_android.go` nhắc tới semantics). Nghĩa là trên Ubuntu và macOS, Orca/VoiceOver **không đọc được app**. Engine chữ tự viết ở spike 1 cũng không phát semantics. Trong thiết kế hiện tại accessibility không nằm trong phạm vi; cần ghi rõ là giới hạn đã biết của giai đoạn A. Tự viết cầu AT-SPI qua `godbus` là việc lớn (ước >1.500 dòng, khó) nên không khuyến nghị cho giai đoạn A. GTK4 có `GtkAccessible` nối AT-SPI sẵn (nguồn: [Evolving accessibility, blog.gtk.org](https://blog.gtk.org/2023/06/)), còn trên macOS GTK cũng không có VoiceOver ([gtk#1087](https://gitlab.gnome.org/GNOME/gtk/issues/1087)), nên lợi thế chỉ ở Linux.

**Hộp chọn tệp.** `gioui.org/x/explorer` (README: "Linux (with XDG Portals)"): trên Linux gọi `org.freedesktop.portal.FileChooser` qua D-Bus (`explorer_linux.go`, `godbus`), có `ChooseFile`, `CreateFile`, `ChooseFolder`; macOS, Windows có bản gốc. API ghi "unstable". Trên GNOME ra hộp thoại gốc của hệ thống.

**Thông báo hệ thống.** `gioui.org/x/notify`: Linux qua D-Bus (`github.com/esiqveland/notify`, bất kể X11/Wayland), macOS "preliminary; code-signing-related issues" (README). Giai đoạn A macOS là bundle **không ký** (§8), nên thông báo macOS cần thử riêng; có thể phải gọi cách khác.

**Clipboard.** Nằm trong lõi, không phải `x`: `gioui.org/io/clipboard` (`WriteCmd`, `ReadCmd`, mime `application/text`), triển khai Wayland bằng `wl_data_offer` (`app/os_wayland.go`). Spike chỉ kiểm tới đầu ra của router; dán/copy qua clipboard thật của GNOME là mục thử tay.

`gioui.org/x` đã nằm cùng phiên bản v0.10.3 với lõi, thêm phụ thuộc `godbus` (đã là phụ thuộc gián tiếp của `ui/`), `esiqveland/notify`. Không cần thêm cgo.

## 8. So sánh với GTK4 (qua `gotk4`): ước lượng, không có code

Cơ sở ước lượng: tôi biết các widget GTK4 nhưng **không viết gì bằng `gotk4` trong spike này**; số dòng là ước lượng có lý do, độ chắc thấp hơn các số đo ở trên. Giả định phương án một codebase Go dùng `gotk4` (cgo, binding gotk4 và gotk4-sourceview).

| Việc | Gio (đã làm trong spike) | GTK4 qua gotk4 (ước lượng) | Lý do |
| --- | --- | --- | --- |
| Chọn chữ xuyên item | ~750 dòng engine, vào `ui/` 1.200–1.600 | 600–1.000 dòng, nhưng đổi kiến trúc | GTK4 cũng **không** chọn xuyên widget: một `GtkLabel` mỗi item có đúng vấn đề đó. Cách thực tế là cả transcript trong **một `GtkTextView`** với `GtkTextBuffer` và tag (chọn, copy, IME, cuộn có sẵn). Đổi lại, card `tool_call`, diff mở tại chỗ phải là tag hoặc widget neo (child anchor) và chọn bị ngắt quanh widget neo, nên ngược lại với thiết kế item của §5 |
| Composer | ~175 dòng + undo ~100 + chip ~60 | 150–250 dòng | `GtkTextView` có chuyển từ, IME gốc, undo (qua `GtkTextBuffer`/`GtkSourceBuffer`, cần kiểm mức gom cụm) (IBus/Wayland); Enter/Shift+Enter bằng `GtkEventControllerKey` (~30 dòng), menu slash bằng `GtkPopover` (~80 dòng). Dán quá lớn vẫn nặng với đoạn dài một dòng (Pango) |
| Diff 5.000 dòng | ~200 dòng + chroma + cuộn ngang ~60 | 250–350 dòng | `GtkSourceView` (qua binding `gotk4-sourceview`, thêm phụ thuộc C) có tô cú pháp, gutter số dòng, nền theo dòng bằng tag; không cần chroma. 5.000 dòng nhẹ. Thêm phụ thuộc hệ thống |
| Danh sách 10.000 item, bám đáy | ~100 dòng, đạt sẵn | 150–250 dòng nếu dùng `GtkListView`; không có nếu dùng một `GtkTextView` | `GtkListView` ảo hóa được nhưng ước lượng chiều cao item chưa dựng nên thanh cuộn và vị trí dễ nhảy; bám đáy và không nhảy phải xử lý thủ công trên `GtkAdjustment`. Với một `GtkTextView` thì chỉ cần giữ adjustment ở đáy |
| Accessibility | Không có trên desktop | AT-SPI sẵn trên Linux; macOS không | Xem §7 |
| macOS | Metal qua cgo, một codebase | GTK4 trên macOS là cổng thứ hai: cần Homebrew, IME và VoiceOver kém | Thiết kế §1 đòi Ubuntu và macOS |
| Build | Một `go build` + gói dev đồ họa | cgo nặng hơn, thời gian build dài, đóng gói GTK trên macOS | gotk4 sinh nhiều mã |

Tổng: GTK4 rẻ hơn ở composer và diff (~600–900 dòng so với ~1.100 dòng của Gio), có accessibility và IME gốc trên Linux. Nhưng chỗ rủi ro lớn nhất, chọn chữ xuyên item, chỉ rẻ khi bỏ mô hình item riêng lẻ, và phần macOS phải trả bằng một cổng GTK thứ hai. Tôi không thấy lý do để chuyển ở thời điểm này, trừ khi điều kiện bên dưới trượt.

## 9. Khuyến nghị

**Giữ Gio, kèm điều kiện.**

Bốn spike đều qua trên mọi số có thể đo được: layout p95 từ 0,1 đến 1,0 ms (ngưỡng 8 ms), RAM 132–176 MB với 2.000–10.000 item (ngưỡng 300 MB), rảnh 0,0–0,1% CPU (ngưỡng 1%), và các hành vi khó (chọn xuyên item bền qua stream, bám đáy, giữ vị trí) đều đúng ở mức test tự động. Phần Gio buộc tự làm là có thật nhưng bị chặn được: ~1.200–1.600 dòng engine chữ.

Điều kiện (không đạt điều nào thì xét lại, theo thứ tự nặng nhẹ):

1. **IME Telex trong composer** trên Wayland và X11 (DA-E02, DA-06). Spike không kiểm được; đây là cổng cứng. Nếu trượt và không sửa được nhanh ở `app/` thì chuyển sang GTK4.
2. **Thêm font tĩnh Inter Bold** (và Italic nếu cần) vào `ui/internal/fonts` trước F10.3; font biến thiên không cho chữ đậm trong Gio v0.10.3.
3. **Chấp nhận khoảng trống accessibility** trên desktop và ghi vào thiết kế (§1 hay §7 của desktop-app). Nếu sau này accessibility thành yêu cầu thì đó là lý do chính để xét lại GTK4.
4. **Đo lại fps trên máy thật** (lệnh dưới). Spike không đo được fps hiển thị.
5. Ngân sách việc kèm theo: undo gom cụm, chặn dán lớn thành tệp đính kèm, tokenize diff ngoài luồng UI, cache layout theo `item_id`+`revision` với trần 2.000, link/bảng/chọn từ trong renderer.

## 10. Chủ dự án thử tay

Chạy trong `spikes/gio-widgets` (cần gói dev như `ui/README.md`). Mọi cửa sổ cần có hiển thị thật trên máy Ubuntu của chủ dự án.

```bash
go run ./cmd/selection -auto
go run ./cmd/list -auto
go run ./cmd/diff -auto
go run ./cmd/composer -auto
```

`-auto` in bảng fps, layout p95, CPU, RSS của cửa sổ thật (cột `render p95` để trống ở đây). `fps` phải ≥ 60 (hoặc bằng tần số quét nếu thấp hơn). Để chạy không cửa sổ: thay `-auto` bằng `-bench`. Chạy tay (không `-auto`): `go run ./cmd/selection`, `./cmd/list`, `./cmd/diff`, `./cmd/composer`.

Checklist (đánh dấu từng mục):

- [ ] **IME Telex, composer, Wayland:** gõ `Vieejt Nam`, `dduwowcj`, `hoaf`. Chữ đang soạn hiện đúng, commit đúng, Backspace xóa đúng một ký tự, không mất hoặc nhân đôi chữ. Lặp lại trên X11 (đăng nhập phiên "Ubuntu on Xorg"). Thử với văn bản đã có chữ trước và sau con trỏ, và khi vùng chọn không rỗng.
- [ ] **Composer, dán:** dán một đoạn nhiều dòng từ clipboard thật (Ctrl+V): xuống dòng được giữ, **không** tự gửi giữa chừng. Dán 100 KB (ví dụ `seq 1 20000 | wl-copy`) rồi gõ tiếp: cảm giác trễ?
- [ ] **Composer, phím:** Enter gửi; Shift+Enter xuống dòng; Ctrl+Z gom ra sao (mỗi phím một bước, ghi nhận); Ctrl+←/→ nhảy theo từ; gõ `/` ở đầu hiện menu, `/fu` lọc còn nhóm `fugue:`, ↑/↓/Tab/Enter chọn và Enter **không** gửi khi menu mở.
- [ ] **Chọn chữ, selection:** kéo từ giữa một đoạn tới giữa một item ở dưới, xuyên code block; thả; Ctrl+C; dán vào composer hay trình soạn: đúng thứ tự, đúng chữ, code block đủ dòng. Kéo ra mép dưới/trên: tự cuộn có mượt không. Cuộn bánh xe khi đang kéo. Shift+click mở rộng. Bấm Ctrl+A rồi Ctrl+C trên 2.000 item: có treo không?
- [ ] **Chọn bền khi stream:** bấm Ctrl+S (bật stream vào cuối), cuộn tới chỗ vùng chọn: vùng chọn còn nguyên, chữ không nhảy.
- [ ] **Cuộn:** bánh xe, touchpad (quán tính), kéo thanh cuộn, PageUp/PageDown nếu có: cảm giác mượt? Con trượt của thanh cuộn có nhảy khi item cao thấp khác nhau? Có xé hình hay tụt dưới 60 fps khi cuộn nhanh qua code block lớn?
- [ ] **List, bám đáy:** `go run ./cmd/list`, Ctrl+S: đáy tự bám; cuộn lên một chút, vẫn đứng yên khi stream; cuộn lại đáy, bám lại. Bấm tiêu đề `tool`/`reasoning` để gập mở: phần phía trên không giật.
- [ ] **Diff:** `go run ./cmd/diff`: cuộn 5.000 dòng mượt; màu cú pháp đọc được ở cả giao diện tối; chọn vài dòng, Ctrl+C, dán: ra đúng dòng diff. Dòng dài bị cắt (đã biết).
- [ ] **Chữ:** chữ đậm không hiện (đã biết); khoảng trắng quanh code nội dòng và chữ có dấu tiếng Việt (ê, ơ, ư, ạ ở mọi dấu) hiển thị đúng, không ô vuông.
- [ ] **Số đo máy thật:** dán bảng `-auto` của bốn binary vào PR của spike hoặc ghi chú: fps, layout p95, CPU rảnh, RSS.
- [ ] **Clipboard, hộp tệp, thông báo:** thử `gioui.org/x/explorer` và `notify` trên GNOME (chưa kiểm trong spike, chỉ đọc mã).

## 11. Chưa làm hoặc chưa kiểm được

- fps của cửa sổ thật (không có frame callback trong phiên này); mọi "fps" ở trên là số khung tối đa headless.
- IME Telex (Wayland, X11); clipboard thật; hộp tệp và thông báo (chỉ đọc mã); macOS.
- Các giới hạn đã liệt kê ở từng spike (cuộn ngang diff, tokenize ngoài luồng, chữ đậm, link/bảng).
- Ước lượng GTK4 chưa kiểm bằng code.

## 12. Cấu trúc code

```
spikes/gio-widgets/
  cmd/{selection,composer,diff,list}   binary của từng spike (cờ -bench, -auto, -only)
  internal/flow                        engine chữ + chọn xuyên item (480 + 270 dòng)
  internal/view                        transcript ảo hóa: layout.List + flow (+ test)
  internal/composer                    ô soạn trên widget.Editor + menu slash (+ test)
  internal/diff                        diff 5.000 dòng + chroma (+ test)
  internal/{gen,perf,shot,fonts}       dữ liệu giả, đo fps/layout/CPU/RSS, ảnh headless, font copy từ ui/
  results/                             số gốc và ảnh chụp
```

Tổng ~2.120 dòng mã và ~525 dòng test (hơi vượt ngưỡng ~2.500 nếu tính cả test).
