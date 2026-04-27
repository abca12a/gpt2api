package image

import (
	"strings"
	"testing"
)

func TestBuildAdminTaskWhereIncludesDownstreamKeywordFields(t *testing.T) {
	where, args := buildAdminTaskWhere(AdminTaskFilter{Keyword: "mantou", Status: StatusSuccess})

	for _, field := range []string{"t.prompt", "u.email", "t.downstream_user_id", "t.downstream_username", "t.downstream_user_email", "t.downstream_user_label"} {
		if !strings.Contains(where, field+" LIKE ?") {
			t.Fatalf("where does not include %s LIKE ?: %s", field, where)
		}
	}
	if !strings.Contains(where, "t.status = ?") {
		t.Fatalf("where does not include status filter: %s", where)
	}
	if len(args) != 7 {
		t.Fatalf("args length = %d, want 7 (%#v)", len(args), args)
	}
	if args[0] != StatusSuccess {
		t.Fatalf("first arg = %#v, want status", args[0])
	}
	for i, arg := range args[1:] {
		if arg != "%mantou%" {
			t.Fatalf("keyword arg %d = %#v", i, arg)
		}
	}
}

func TestAdminTaskListSQLSkipsLargeResultURLs(t *testing.T) {
	sql := adminTaskListSQL("1=1")

	if strings.Contains(sql, "t.result_urls") {
		t.Fatalf("admin list sql selects large result_urls column: %s", sql)
	}
	if !strings.Contains(sql, "t.file_ids") {
		t.Fatalf("admin list sql should keep file_ids for lightweight result count: %s", sql)
	}
}
