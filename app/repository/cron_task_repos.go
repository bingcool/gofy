package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bingcool/gen"
	"github.com/bingcool/gen/field"
	"github.com/bingcool/gofy/app/Io/db"
	"github.com/bingcool/gofy/app/dao/builder"
	"github.com/bingcool/gofy/app/entity"
	"github.com/bingcool/gofy/src/log"
	"github.com/jinzhu/copier"
	"go.uber.org/zap"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type CronTaskRepos struct {
	query *builder.Query
	Db    *gorm.DB
}

type CronTaskReposInterface interface {
	Query() *builder.Query
	First(ctx context.Context, where []gen.Condition) *entity.CronTaskEntity
	Create(ctx context.Context, cronTaskEntity *entity.CronTaskEntity) (int64, error)
	Update(ctx context.Context, where []gen.Condition, cronTaskEntity *entity.CronTaskEntity) (int64, error)
	SimpleList(ctx context.Context, where []gen.Condition, orderBy []field.Expr) ([]*entity.CronTaskEntity, error)
	Delete(ctx context.Context, where []gen.Condition) (int64, error)
	ForceDelete(ctx context.Context, where []gen.Condition) (int64, error)
	ModelConvertToEntity(cronTask *entity.CronTask) (*entity.CronTaskEntity, error)
	BatchModelConvertToEntity(cronTaskEntityList []*entity.CronTask) ([]*entity.CronTaskEntity, error)
	EntityConvertToModel(cronTaskEntity *entity.CronTaskEntity) (*entity.CronTask, error)
	BatchEntityConvertToModel(cronTaskEntityList []*entity.CronTaskEntity) ([]*entity.CronTask, error)
}

func NewCronTaskRepos() *CronTaskRepos {
	DbObj := db.GetDb()
	conTaskRepos := &CronTaskRepos{
		query: builder.Use(DbObj),
		Db:    DbObj,
	}
	return conTaskRepos
}

// Query 查询器
func (r *CronTaskRepos) Query() *builder.Query {
	return r.query
}

// First 加载数据
func (r *CronTaskRepos) First(ctx context.Context, where []gen.Condition) *entity.CronTaskEntity {
	first, err := r.query.CronTask.WithContext(ctx).Where(where...).First()
	if err != nil {
		return nil
	}
	cronTaskEntity, err1 := r.ModelConvertToEntity(first)
	if err1 != nil {
		return nil
	}
	return cronTaskEntity
}

// Create 保存数据
func (r *CronTaskRepos) Create(_ context.Context, cronTaskEntity *entity.CronTaskEntity) (int64, error) {
	conTask, err := r.EntityConvertToModel(cronTaskEntity)
	if err != nil {
		return 0, err
	}
	result := r.Db.Create(conTask)
	return result.RowsAffected, result.Error
}

// CreateInBatches 批量插入
func (r *CronTaskRepos) CreateInBatches(ctx context.Context, cronTaskEntityList []*entity.CronTaskEntity) error {
	if len(cronTaskEntityList) == 0 {
		return nil
	}

	conTaskList := make([]*entity.CronTask, 0)
	for _, v := range cronTaskEntityList {
		conTask, err := r.EntityConvertToModel(v)
		if err != nil {
			return err
		}
		conTaskList = append(conTaskList, conTask)
	}
	return r.query.CronTask.WithContext(ctx).CreateInBatches(conTaskList, 100)
}

// Update 保存数据
func (r *CronTaskRepos) Update(
	ctx context.Context,
	where []gen.Condition,
	cronTaskEntity *entity.CronTaskEntity,
) (int64, error) {
	conTask, err := r.EntityConvertToModel(cronTaskEntity)
	if err != nil {
		return 0, err
	}
	updates, err := r.query.CronTask.WithContext(ctx).Where(where...).Updates(conTask)
	return updates.RowsAffected, err
}

// SimpleList 简单少数量的数据分页查询，不适合分页
func (r *CronTaskRepos) SimpleList(
	ctx context.Context,
	where []gen.Condition,
	orderBy []field.Expr,
) ([]*entity.CronTaskEntity, error) {
	var list1 []*entity.CronTask
	var list2 []*entity.CronTaskEntity
	var err error
	if len(orderBy) > 0 {
		list1, err = r.query.CronTask.WithContext(ctx).Where(where...).Order(orderBy...).Find()
	} else {
		list1, err = r.query.CronTask.WithContext(ctx).Where(where...).Find()
	}

	if err != nil {
		return nil, err
	}

	for _, v := range list1 {
		cronTaskEntity, err1 := r.ModelConvertToEntity(v)
		if err1 != nil {
			return nil, err1
		}
		list2 = append(list2, cronTaskEntity)
	}

	return list2, nil
}

// List 批量加载数据
func (r *CronTaskRepos) List(ctx context.Context, ids []int32) ([]*entity.CronTask, error) {
	list, err := r.query.CronTask.WithContext(ctx).Where(r.query.CronTask.ID.In(ids...)).Find()
	if err != nil {
		return nil, err
	}
	return list, nil
}

// Delete 删除数据--模型包含了 gorm.DeletedAt字段（在gorm.Model中），那么该模型将会自动获得软删除的能力
func (r *CronTaskRepos) Delete(ctx context.Context, where []gen.Condition) (int64, error) {
	deletes, err := r.query.CronTask.WithContext(ctx).Where(where...).Delete()
	if err != nil {
		return 0, err
	}
	return deletes.RowsAffected, err
}

// ForceDelete 强制删除数据
func (r *CronTaskRepos) ForceDelete(ctx context.Context, where []gen.Condition) (int64, error) {
	deletes, err := r.query.CronTask.WithContext(ctx).Unscoped().Where(where...).Delete()
	if err != nil {
		return 0, err
	}
	return deletes.RowsAffected, err
}

// ModelConvertToEntity 查询数据后将model数据赋值到entity实体
func (r *CronTaskRepos) ModelConvertToEntity(cronTask *entity.CronTask) (*entity.CronTaskEntity, error) {
	// 自动处理类型转换和嵌套字段
	cronTaskEntity := &entity.CronTaskEntity{}
	err := copier.Copy(cronTaskEntity, cronTask)
	if err != nil {
		log.Info(
			"CronTaskEntity的copy失败",
			zap.Any("error", err.Error()),
			zap.Any("CronTaskModel", cronTask),
		)
		return nil, errors.New("CronTaskEntity的copy失败")
	}

	if cronTask.CronSkip != nil {
		// json 数据转换为结构体
		var cronSkip [][]string
		if err := json.Unmarshal(*cronTask.CronSkip, &cronSkip); err != nil {
			log.Info(
				"CronTaskEntity的ModelConvertToEntity解析CronSkip失败",
				zap.Any("error", err.Error()),
				zap.Any("CronTaskModel", cronTask),
			)
		}
		cronTaskEntity.CronSkip = cronSkip
	}

	if cronTask.HTTPHeaders != nil {
		// json 数据转换为结构体
		httpHeaders := &entity.HttpHeaders{}
		if err := json.Unmarshal(*cronTask.HTTPHeaders, httpHeaders); err != nil {
			log.Info(
				"CronTaskEntity的ModelConvertToEntity解析HTTPHeaders失败",
				zap.Any("error", err.Error()),
				zap.Any("CronTaskModel", cronTask),
			)
		}
		cronTaskEntity.HTTPHeaders = httpHeaders
	}

	return cronTaskEntity, nil
}

// BatchModelConvertToEntity 查询数据后将model数据赋值到entity实体
func (r *CronTaskRepos) BatchModelConvertToEntity(
	cronTaskList []*entity.CronTask,
) ([]*entity.CronTaskEntity, error) {
	// 自动处理类型转换和嵌套字段
	cronTaskEntityList := make([]*entity.CronTaskEntity, 0)
	for _, v := range cronTaskList {
		cronTaskEntity, err := r.ModelConvertToEntity(v)
		if err != nil {
			return make([]*entity.CronTaskEntity, 0), err
		}
		cronTaskEntityList = append(cronTaskEntityList, cronTaskEntity)
	}

	return cronTaskEntityList, nil
}

// EntityConvertToModel Entity实体数据转换为model
func (r *CronTaskRepos) EntityConvertToModel(cronTaskEntity *entity.CronTaskEntity) (*entity.CronTask, error) {
	conTask := &entity.CronTask{}
	err := copier.Copy(conTask, cronTaskEntity)
	if err != nil {
		log.Info(
			"CronTaskEntity的copy失败",
			zap.Any("error", err.Error()),
			zap.Any("cronTaskEntity", cronTaskEntity),
		)
		return nil, errors.New("EntityConvertToModel的copy失败")
	}
	if cronTaskEntity.CronSkip != nil {
		CronSkip, err := json.Marshal(cronTaskEntity.CronSkip)
		if err != nil {
			log.Info(
				"CronTaskEntity的EntityConvertToModel解析CronSkip失败",
				zap.Any("error", err.Error()),
				zap.Any("cronTaskEntity", cronTaskEntity),
			)
		}
		conTask.CronSkip = (*datatypes.JSON)(&CronSkip)
	}

	if cronTaskEntity.HTTPHeaders != nil {
		httpHeaders, err := json.Marshal(cronTaskEntity.HTTPHeaders)
		if err != nil {
			return nil, err
		}
		conTask.HTTPHeaders = (*datatypes.JSON)(&httpHeaders)
	}

	return conTask, nil
}

// BatchEntityConvertToModel Entity实体数据转换为model
func (r *CronTaskRepos) BatchEntityConvertToModel(
	cronTaskEntityList []*entity.CronTaskEntity,
) ([]*entity.CronTask, error) {
	// 自动处理类型转换和嵌套字段
	cronTaskList := make([]*entity.CronTask, 0)
	for _, v := range cronTaskEntityList {
		cronTaskEntity, err := r.EntityConvertToModel(v)
		if err != nil {
			return nil, err
		}
		cronTaskList = append(cronTaskList, cronTaskEntity)
	}

	return cronTaskList, nil
}
