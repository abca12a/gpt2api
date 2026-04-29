package image

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/pkg/resp"
)

// AdminHandler 管理员视角下的生成记录接口。
type AdminHandler struct {
	dao *DAO
}

// NewAdminHandler 构造。
func NewAdminHandler(dao *DAO) *AdminHandler {
	return &AdminHandler{dao: dao}
}

// List GET /api/admin/image-tasks
// 查询参数:page / page_size / user_id / keyword(prompt 或邮箱模糊) / status
func (h *AdminHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	userID, _ := strconv.ParseUint(c.Query("user_id"), 10, 64)

	f := AdminTaskFilter{
		UserID:  userID,
		Keyword: c.Query("keyword"),
		Status:  c.Query("status"),
	}

	rows, total, err := h.dao.ListAdmin(c.Request.Context(), f, size, (page-1)*size)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}

	// 把任务图片转成前端可加载的本站签名代理 URL。
	type rowOut struct {
		AdminTaskRow
		ResultURLsParsed     []string   `json:"result_urls_parsed"`
		ResultCount          int        `json:"result_count"`
		HasResult            bool       `json:"has_result"`
		ErrorCode            string     `json:"error_code,omitempty"`
		ErrorMessage         string     `json:"error_message,omitempty"`
		ErrorDetail          string     `json:"error_detail,omitempty"`
		ProviderTrace        *TaskTrace `json:"provider_trace,omitempty"`
		ProviderTraceSummary string     `json:"provider_trace_summary,omitempty"`
	}
	out := make([]rowOut, 0, len(rows))
	for _, r := range rows {
		fileIDs := r.DecodeFileIDs()
		resultCount := len(fileIDs)
		if resultCount == 0 && r.Status == StatusSuccess {
			resultCount = r.N
			if resultCount <= 0 {
				resultCount = 1
			}
		}
		row := rowOut{
			AdminTaskRow: r,
			ResultCount:  resultCount,
			HasResult:    len(fileIDs) > 0 || r.Status == StatusSuccess,
		}
		row.ProviderTrace = r.DecodeProviderTrace()
		row.ProviderTraceSummary = TaskTraceSummary(row.ProviderTrace)
		if r.Status == StatusFailed || r.Error != "" {
			row.ErrorCode, row.ErrorDetail, row.ErrorMessage = TaskErrorFields(r.Error)
		}
		out = append(out, row)
	}

	resp.OK(c, gin.H{
		"list":      out,
		"total":     total,
		"page":      page,
		"page_size": size,
	})
}

// Images GET /api/admin/image-tasks/:id/images
func (h *AdminHandler) Images(c *gin.Context) {
	taskID := c.Param("id")
	if taskID == "" {
		resp.BadRequest(c, "task id required")
		return
	}

	t, err := h.dao.Get(c.Request.Context(), taskID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.NotFound(c, "task not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}

	urls := BuildTaskImageURLs(t, ImageProxyTTL)
	out := gin.H{
		"task_id":                t.TaskID,
		"status":                 t.Status,
		"error":                  t.Error,
		"result_urls_parsed":     urls,
		"result_count":           len(urls),
		"provider_trace":         t.DecodeProviderTrace(),
		"provider_trace_summary": TaskTraceSummary(t.DecodeProviderTrace()),
	}
	if t.Status == StatusFailed || t.Error != "" {
		code, detail, message := TaskErrorFields(t.Error)
		out["error_code"] = code
		out["error_detail"] = detail
		out["error_message"] = message
	}
	resp.OK(c, out)
}
