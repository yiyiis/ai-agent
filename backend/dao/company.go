package dao

import (
	"context"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"
	"gorm.io/gorm"
)

// GetCompanyByID 根据主键 ID 查询企业
func GetCompanyByID(ctx context.Context, id int64) (*model.Company, error) {
	q := db.Ctx(ctx)
	c := q.Company
	comp, err := c.WithContext(ctx).Where(c.ID.Eq(id)).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询企业失败"), errors.NewMsg("系统异常"))
	}
	return comp, nil
}

// GetCompanyByCode 根据企业唯一标识代号查询
func GetCompanyByCode(ctx context.Context, code string) (*model.Company, error) {
	q := db.Ctx(ctx)
	c := q.Company
	comp, err := c.WithContext(ctx).Where(c.Code.Eq(code)).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Join(err, errors.New("查询企业失败"), errors.NewMsg("系统异常"))
	}
	return comp, nil
}

// CreateCompany 创建新企业
func CreateCompany(ctx context.Context, company *model.Company) error {
	if company == nil {
		return errors.New("company 不能为空")
	}
	q := db.Ctx(ctx)
	err := q.Company.WithContext(ctx).Create(company)
	if err != nil {
		return errors.Join(err, errors.New("创建企业失败"), errors.NewMsg("创建企业失败"))
	}
	return nil
}

// ListCompanies 列出全量有效企业
func ListCompanies(ctx context.Context) ([]*model.Company, error) {
	q := db.Ctx(ctx)
	c := q.Company
	list, err := c.WithContext(ctx).Where(c.Status.Eq(1)).Order(c.ID.Asc()).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("获取企业列表失败"), errors.NewMsg("系统异常"))
	}
	return list, nil
}

// EnsureDefaultCompany 确保存在默认企业 (id=1, code="default")
func EnsureDefaultCompany(ctx context.Context) (*model.Company, error) {
	c, err := GetCompanyByCode(ctx, "default")
	if err != nil {
		return nil, err
	}
	if c != nil {
		return c, nil
	}

	def := &model.Company{
		ID:     1,
		Name:   "默认企业",
		Code:   "default",
		Status: 1,
	}
	if err := CreateCompany(ctx, def); err != nil {
		if existing, getErr := GetCompanyByID(ctx, 1); getErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return def, nil
}
