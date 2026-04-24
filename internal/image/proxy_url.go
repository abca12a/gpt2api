package image

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const previewRefPrefix = "preview:"
const imageRefMetaPrefix = "meta:"

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
	_, _, ref = DecodeImageRefMeta(ref)
	return strings.HasPrefix(ref, previewRefPrefix)
}

// StripPreviewRef 去掉 preview 前缀,恢复原始的 sed:/file-service 引用。
func StripPreviewRef(ref string) string {
	_, _, ref = DecodeImageRefMeta(ref)
	return strings.TrimPrefix(ref, previewRefPrefix)
}

// PublicFileID 返回前端可见的 file_id,去掉内部 preview/sed 前缀。
func PublicFileID(ref string) string {
	ref = StripPreviewRef(ref)
	return strings.TrimPrefix(ref, "sed:")
}

// EncodeImageRefMeta 把单张图对应的 account/conversation/ref 打包进 file_ids 元素。
// 这是为了支持 N>1 并发生图:每张图可能来自不同账号、不同 conversation,
// 图片代理必须按单图元信息回源下载,不能只依赖 image_tasks 的单个 account_id。
func EncodeImageRefMeta(accountID uint64, conversationID, ref string) string {
	if accountID == 0 || conversationID == "" || ref == "" {
		return ref
	}
	enc := base64.RawURLEncoding
	return fmt.Sprintf("%s%d:%s:%s", imageRefMetaPrefix, accountID,
		enc.EncodeToString([]byte(conversationID)), enc.EncodeToString([]byte(ref)))
}

// DecodeImageRefMeta 解出 EncodeImageRefMeta 写入的单图元信息。
// 非 meta 格式会原样返回 ref,便于兼容历史任务。
func DecodeImageRefMeta(stored string) (uint64, string, string) {
	if !strings.HasPrefix(stored, imageRefMetaPrefix) {
		return 0, "", stored
	}
	parts := strings.SplitN(strings.TrimPrefix(stored, imageRefMetaPrefix), ":", 3)
	if len(parts) != 3 {
		return 0, "", stored
	}
	accountID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || accountID == 0 {
		return 0, "", stored
	}
	enc := base64.RawURLEncoding
	convBytes, err := enc.DecodeString(parts[1])
	if err != nil || len(convBytes) == 0 {
		return 0, "", stored
	}
	refBytes, err := enc.DecodeString(parts[2])
	if err != nil || len(refBytes) == 0 {
		return 0, "", stored
	}
	return accountID, string(convBytes), string(refBytes)
}

func computeImgSig(taskID string, idx int, expMs int64) string {
	mac := hmac.New(sha256.New, imageProxySecret)
	fmt.Fprintf(mac, "%s|%d|%d", taskID, idx, expMs)
	return hex.EncodeToString(mac.Sum(nil))[:24]
}
