package apiwarp

import (
	"context"
	"backend/pkg/errors"
	"backend/pkg/validate"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"log/slog"
	"reflect"
	"strconv"

	stderr "errors"
)

type FilePathData struct {
	FileType string // 文件类型
	Path     string // 文件路径
}

type FileBytesData struct {
	FileType string // 文件类型
	Data     []byte // 文件数据
}

// SSEData SSE 流式响应：Controller 识别到该类型后切换为 text/event-stream 长连接。
// Stream 内每 emit 一个对象，即被 JSON 序列化为一帧 `data: {...}` 写出并立刻 Flush。
// 流开始后错误已无法转为 HTTP 状态码——Stream 返回的 error 会被包装成
// {"type":"error","message":...} 事件发给前端后正常收尾。
type SSEData struct {
	Stream func(ctx context.Context, emit func(payload any)) error
}

// SetCookie 随响应写回的 Cookie（登录态注入/清除等）
type SetCookie struct {
	Name     string
	Value    string
	MaxAge   int // 秒；负数表示删除
	Path     string
	Secure   bool
	HttpOnly bool
}

// CookieResp 带 Cookie 副作用的标准响应：先落 Cookie，再按默认信封包装 Body
type CookieResp struct {
	Cookies []SetCookie
	Body    any
}

// bindUriParams 自动将动态路由中的 URL 路径参数（如 /api/user/:id）映射到结构体带有 uri:"xxx" 标签的字段中
func bindUriParams(params gin.Params, obj any) {
	if len(params) == 0 {
		return
	}
	val := reflect.ValueOf(obj)
	if val.Kind() != reflect.Ptr || val.IsNil() {
		return
	}
	elem := val.Elem()
	if elem.Kind() != reflect.Struct {
		return
	}

	typ := elem.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("uri")
		if tag == "" {
			continue
		}
		paramVal := params.ByName(tag)
		if paramVal == "" {
			continue
		}

		fieldVal := elem.Field(i)
		if !fieldVal.CanSet() {
			continue
		}

		switch fieldVal.Kind() {
		case reflect.String:
			fieldVal.SetString(paramVal)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if intVal, err := strconv.ParseInt(paramVal, 10, 64); err == nil {
				fieldVal.SetInt(intVal)
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if uintVal, err := strconv.ParseUint(paramVal, 10, 64); err == nil {
				fieldVal.SetUint(uintVal)
			}
		case reflect.Bool:
			if boolVal, err := strconv.ParseBool(paramVal); err == nil {
				fieldVal.SetBool(boolVal)
			}
		case reflect.Float32, reflect.Float64:
			if floatVal, err := strconv.ParseFloat(paramVal, 64); err == nil {
				fieldVal.SetFloat(floatVal)
			}
		}
	}
}

// Controller 泛型控制器包装器：将 (ctx, *In) (*Out, error) 自动包装为标准 gin.HandlerFunc
// 并统一处理参数绑定（支持 URI 动态路径、Query、Body 表单/JSON）、参数校验、异常捕获堆栈日志、以及规范的 JSON 响应格式
func Controller[In any, Out any](fc func(ctx context.Context, in *In) (*Out, error)) gin.HandlerFunc {
	return func(gCtx *gin.Context) {
		var in In

		// 1. 自动注入动态路由路径参数（如 :id 映射到 uri:"id"）
		bindUriParams(gCtx.Params, &in)

		// 2. 如果存在 Query 参数，预先绑定 Query（防止后续 JSON 绑定覆盖忽略 Query）
		if gCtx.Request.URL != nil && gCtx.Request.URL.RawQuery != "" {
			_ = gCtx.ShouldBindQuery(&in)
		}

		// 3. 绑定 Body（JSON 或 Form），并在此处触发统一参数校验。
		//    无请求体的调用（如退出登录）跳过绑定；chunked 空体的 io.EOF 同样放行
		var bindErr error
		if gCtx.Request != nil && gCtx.Request.Body != nil && gCtx.Request.ContentLength != 0 {
			bindErr = gCtx.ShouldBind(&in)
		}
		if bindErr != nil && !stderr.Is(bindErr, io.EOF) {
			errMap := make(map[string]string)

			var validatorErr validator.ValidationErrors
			if errors.As(bindErr, &validatorErr) {
				for _, fieldErr := range validatorErr {
					errMap[fieldErr.Field()] = fieldErr.Translate(validate.GetTranslator())
				}
			}

			gCtx.JSON(200, map[string]any{
				"code": 1,
				"msg":  "参数错误",
				"data": errMap,
			})
			return
		}

		// 执行业务函数
		out, err := fc(gCtx.Request.Context(), &in)
		if err != nil {
			slog.Default().With("url", gCtx.FullPath()).ErrorContext(gCtx.Request.Context(), fmt.Sprintf("\n%+v\n", err))

			var msgErr *errors.MsgErr
			msg := "系统异常"

			if errors.As(err, &msgErr) {
				msg = msgErr.Error()
			}

			gCtx.JSON(200, map[string]any{
				"code": 1,
				"msg":  msg,
			})
			return
		}

		var outAny any = out

		// 多返回值类型支持（支持文件路径流、字节流、SSE 流、Cookie 等特殊响应）
		switch outData := outAny.(type) {
		case *SSEData:
			writeSSE(gCtx, outData)
		case SSEData:
			writeSSE(gCtx, &outData)
		case *FilePathData:
			gCtx.Writer.Header().Set("Content-Type", outData.FileType)
			gCtx.File(outData.Path)
		case FilePathData:
			gCtx.Writer.Header().Set("Content-Type", outData.FileType)
			gCtx.File(outData.Path)
		case *FileBytesData:
			gCtx.Data(200, outData.FileType, outData.Data)
		case FileBytesData:
			gCtx.Data(200, outData.FileType, outData.Data)
		case *CookieResp:
			writeCookies(gCtx, outData.Cookies)
			writeEnvelope(gCtx, outData.Body)
		case CookieResp:
			writeCookies(gCtx, outData.Cookies)
			writeEnvelope(gCtx, outData.Body)
		default:
			// 默认标准 JSON 响应包装
			writeEnvelope(gCtx, out)
		}
	}
}

// writeEnvelope 标准响应信封：恒 200 + {code, msg, data}
func writeEnvelope(gCtx *gin.Context, data any) {
	gCtx.JSON(200, map[string]any{
		"code": 0,
		"msg":  "",
		"data": data,
	})
}

func writeCookies(gCtx *gin.Context, cookies []SetCookie) {
	for _, ck := range cookies {
		gCtx.SetCookie(ck.Name, ck.Value, ck.MaxAge, ck.Path, "", ck.Secure, ck.HttpOnly)
	}
}

// writeSSE 切换为 text/event-stream 长连接并驱动业务流：
// 每帧写出后立即 Flush；断连由请求 ctx 取消传导给 Stream。
func writeSSE(gCtx *gin.Context, sse *SSEData) {
	h := gCtx.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	gCtx.Writer.WriteHeader(http.StatusOK)
	gCtx.Writer.Flush()

	emit := func(payload any) {
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(gCtx.Writer, "data: %s\n\n", b)
		gCtx.Writer.Flush()
	}
	if err := sse.Stream(gCtx.Request.Context(), emit); err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
	}
}
