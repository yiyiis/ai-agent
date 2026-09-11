package apiwarp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"github.com/gin-gonic/gin"
)

type FullReq struct {
	ID    int32  `uri:"id" binding:"required"`
	Page  int    `form:"page"`
	Name  string `json:"name"`
}

type FullResp struct {
	Result string `json:"result"`
	ID     int32  `json:"id"`
	Page   int    `json:"page"`
	Name   string `json:"name"`
}

func testHandler(ctx context.Context, req *FullReq) (*FullResp, error) {
	return &FullResp{
		Result: "success",
		ID:     req.ID,
		Page:   req.Page,
		Name:   req.Name,
	}, nil
}

func TestControllerComprehensiveBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// 挂载泛型 Controller 包装的接口
	r.POST("/user/:id", Controller(testHandler))

	// 发起组合测试：既有 URI 路径参数，又有 Query 参数，又有 JSON Body
	w := httptest.NewRecorder()
	httpReq, _ := http.NewRequest("POST", "/user/888?page=5", strings.NewReader(`{"name":"jack"}`))
	httpReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, httpReq)

	t.Logf("Response: %s", w.Body.String())
	if !strings.Contains(w.Body.String(), `"id":888`) ||
		!strings.Contains(w.Body.String(), `"page":5`) ||
		!strings.Contains(w.Body.String(), `"name":"jack"`) {
		t.Fatalf("Comprehensive binding failed: %s", w.Body.String())
	}
}
