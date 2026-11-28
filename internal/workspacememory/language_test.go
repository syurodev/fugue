package workspacememory

import (
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestLanguageCheck(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{
			name:    "pure english prose",
			text:    "The host process owns the database and enforces authorization in Go services. Clients talk to the host over a local socket.",
			wantErr: false,
		},
		{
			name:    "english with embedded vietnamese terms and names",
			text:    "The memory slot displays a badge labeled \"chưa xác nhận\" for unverified entries. Users in Hồ Chí Minh City configure the workspace root in the settings panel.",
			wantErr: false,
		},
		{
			name:    "vietnamese with diacritics",
			text:    "Máy chủ sở hữu toàn bộ dữ liệu và kiểm tra quyền truy cập trước khi chạy công cụ. Các phiên làm việc không được phép tự ý bỏ qua bước xác nhận của người dùng.",
			wantErr: true,
		},
		{
			name:    "vietnamese without diacritics",
			text:    "May chu so huu toan bo du lieu va kiem tra quyen truy cap truoc khi chay cong cu. Cac phien lam viec khong duoc phep tu y bo qua buoc xac nhan cua nguoi dung.",
			wantErr: true,
		},
		{
			name:    "mixed majority vietnamese",
			text:    "This sentence is written in English for testing purposes. Tuy nhiên câu thứ hai lại viết hoàn toàn bằng tiếng Việt khá dài. Và câu thứ ba cũng tiếp tục dùng tiếng Việt để mô tả hệ thống.",
			wantErr: true,
		},
		{
			name:    "mixed majority english",
			text:    "The host verifies every request before executing a tool. The database migration runs once at startup with forward-only SQL. Câu này viết bằng tiếng Việt để thử tỷ lệ.",
			wantErr: false,
		},
		{
			name:    "short sentences and code blocks ignored",
			text:    "OK.\n```go\nfunc TestFoo() { return }\n```\nUse `internal/workspacememory` with `layerWeights` and https://example.com/docs.",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckLanguage(tc.text, DefaultLanguageMinWords, DefaultLanguageMaxNonEn)
			if tc.wantErr && errcode.CodeOf(err) != errcode.MemoryLanguage {
				t.Fatalf("CheckLanguage(%q) code = %q (err=%v), want %q", tc.name, errcode.CodeOf(err), err, errcode.MemoryLanguage)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("CheckLanguage(%q) unexpected error: %v", tc.name, err)
			}
		})
	}
}
