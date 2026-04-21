package image

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const previewRefPrefix = "preview:"

// ImageProxyTTL 单条签名 URL 的默认有效期(24h,够前端离线展示一段时间)。
const ImageProxyTTL = 24 * time.Hour

// imageProxySecret 进程级随机密钥,用于 HMAC 签名图片 URL。
// 进程重启后旧的签名 URL 全部失效,这是故意的(防止长期有效的 URL 泄漏)。
var imageProxySecret []byte

func init() {
	imageProxySecret = make([]byte, 32)
	if _, err := rand.Read(imageProxySecret); err != nil {
		for i := range imageProxySecret {
			imageProxySecret[i] = byte(i*31 + 7)
		}
	}
}

// BuildImageProxyURL 生成代理 URL。返回绝对 path(不含 host),调用方可以直接拼或交给前端同 origin 使用。
func BuildImageProxyURL(taskID string, idx int, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = ImageProxyTTL
	}
	expMs := time.Now().Add(ttl).UnixMilli()
	sig := computeImgSig(taskID, idx, expMs)
	return fmt.Sprintf("/p/img/%s/%d?exp=%d&sig=%s", taskID, idx, expMs, sig)
}

// VerifyImageProxySig 校验图片代理 URL 的 HMAC 签名和过期时间。
func VerifyImageProxySig(taskID string, idx int, expMs int64, sig string) bool {
	if expMs < time.Now().UnixMilli() {
		return false
	}
	want := computeImgSig(taskID, idx, expMs)
	return hmac.Equal([]byte(sig), []byte(want))
}

// MarkPreviewRef 给仅预览兜底的 file ref 打标,便于后续任务查询准确返回 is_preview。
func MarkPreviewRef(ref string) string {
	if strings.HasPrefix(ref, previewRefPrefix) {
		return ref
	}
	return previewRefPrefix + ref
}

// IsPreviewRef 判断某个 file ref 是否来自 preview 兜底。
func IsPreviewRef(ref string) bool {
	return strings.HasPrefix(ref, previewRefPrefix)
}

// StripPreviewRef 去掉 preview 前缀,恢复原始的 sed:/file-service 引用。
func StripPreviewRef(ref string) string {
	return strings.TrimPrefix(ref, previewRefPrefix)
}

// PublicFileID 返回前端可见的 file_id,去掉内部 preview/sed 前缀。
func PublicFileID(ref string) string {
	ref = StripPreviewRef(ref)
	return strings.TrimPrefix(ref, "sed:")
}

func computeImgSig(taskID string, idx int, expMs int64) string {
	mac := hmac.New(sha256.New, imageProxySecret)
	fmt.Fprintf(mac, "%s|%d|%d", taskID, idx, expMs)
	return hex.EncodeToString(mac.Sum(nil))[:24]
}
