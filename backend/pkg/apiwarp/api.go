package apiwarp

import (
	"context"
	"backend/pkg/errors"
	"backend/pkg/validate"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"log/slog"
	"reflect"
	"strconv"
)

type FilePathData struct {
	FileType string // 文件类型
	Path     string // 文件路径
}

type FileBytesData struct {
	FileType string // 文件类型
	Data     []byte // 文件数据
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

		// 3. 绑定 Body（JSON 或 Form），并在此处触发统一参数校验
		err := gCtx.ShouldBind(&in)
		if err != nil {
			errMap := make(map[string]string)

			var validatorErr validator.ValidationErrors
			if errors.As(err, &validatorErr) {
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

		// 多返回值类型支持（支持文件路径流、字节流等特殊响应）
		switch outData := outAny.(type) {
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
		default:
			// 默认标准 JSON 响应包装
			gCtx.JSON(200, map[string]any{
				"code": 0,
				"msg":  "",
				"data": out,
			})
		}
	}
}
