package validate

import (
	"fmt"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/locales/zh"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
	validatorzh "github.com/go-playground/validator/v10/translations/zh"
)

var trans ut.Translator

func GetTranslator() ut.Translator {
	return trans
}

func init() {
	translator := zh.New()
	universalTranslator := ut.New(translator)
	trans = universalTranslator.GetFallback()
}

func InitGinValidate() {
	v, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		panic("获取gin参数验证器失败")
	}

	err := validatorzh.RegisterDefaultTranslations(v, trans)
	if err != nil {
		panic(fmt.Errorf("注册中文验证翻译器失败: %w", err))
	}
}
