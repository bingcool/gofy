package repository

import (
	"context"
	"encoding/json"
	"log"

	"github.com/bingcool/gen"
	"github.com/bingcool/gen/field"
	"github.com/bingcool/gofy/app/Io/db"
	"github.com/bingcool/gofy/app/dao/builder"
	"github.com/bingcool/gofy/app/entity"
	"github.com/jinzhu/copier"
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
	Create(ctx context.Context, cronTaskEntity *entity.CronTaskEntity) int64
	Update(ctx context.Context, where []gen.Condition, cronTaskEntity *entity.CronTaskEntity) int64
	SimpleList(ctx context.Context, where []gen.Condition, orderBy []field.Expr) []*entity.CronTaskEntity
	Delete(ctx context.Context, where []gen.Condition) int64
	ForceDelete(ctx context.Context, where []gen.Condition) int64
	ModelConvertToEntity(cronTask *entity.CronTask) *entity.CronTaskEntity
	BatchModelConvertToEntity(cronTaskEntityList []*entity.CronTask) []*entity.CronTaskEntity
	EntityConvertToModel(cronTaskEntity *entity.CronTaskEntity) *entity.CronTask
	BatchEntityConvertToModel(cronTaskEntityList []*entity.CronTaskEntity) []*entity.CronTask
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
	cronTaskEntity := r.ModelConvertToEntity(first)
	return cronTaskEntity
}

// Create 保存数据
func (r *CronTaskRepos) Create(ctx context.Context, cronTaskEntity *entity.CronTaskEntity) int64 {
	conTask := r.EntityConvertToModel(cronTaskEntity)
	result := r.Db.Create(conTask)
	return result.RowsAffected
}

// Update 保存数据
func (r *CronTaskRepos) Update(ctx context.Context, where []gen.Condition, cronTaskEntity *entity.CronTaskEntity) int64 {
	conTask := r.EntityConvertToModel(cronTaskEntity)
	updates, err := r.query.CronTask.WithContext(ctx).Where(where...).Updates(conTask)
	if err != nil {
		panic(err)
	}
	return updates.RowsAffected
}

// SimpleList 简单少数量的数据分页查询，不适合分页
func (r *CronTaskRepos) SimpleList(ctx context.Context, where []gen.Condition, orderBy []field.Expr) []*entity.CronTaskEntity {
	var list1 []*entity.CronTask
	var list2 []*entity.CronTaskEntity
	var err error
	if len(orderBy) > 0 {
		list1, err = r.query.CronTask.WithContext(ctx).Where(where...).Order(orderBy...).Find()
	} else {
		list1, err = r.query.CronTask.WithContext(ctx).Where(where...).Find()
	}
	if err != nil {
		panic(err)
	}

	for _, v := range list1 {
		cronTaskEntity := r.ModelConvertToEntity(v)
		list2 = append(list2, cronTaskEntity)
	}

	return list2
}

// List 批量加载数据
func (r *CronTaskRepos) List(ctx context.Context, ids []int32) []*entity.CronTask {
	list, err := r.query.CronTask.WithContext(ctx).Where(r.query.CronTask.ID.In(ids...)).Find()
	if err != nil {
		panic(err)
	}
	return list
}

// Delete 删除数据--模型包含了 gorm.DeletedAt字段（在gorm.Model中），那么该模型将会自动获得软删除的能力
func (r *CronTaskRepos) Delete(ctx context.Context, where []gen.Condition) int64 {
	deletes, err := r.query.CronTask.WithContext(ctx).Where(where...).Delete()
	if err != nil {
		panic(err)
	}
	return deletes.RowsAffected
}

// ForceDelete 强制删除数据
func (r *CronTaskRepos) ForceDelete(ctx context.Context, where []gen.Condition) int64 {
	deletes, err := r.query.CronTask.WithContext(ctx).Unscoped().Where(where...).Delete()
	if err != nil {
		panic(err)
	}
	return deletes.RowsAffected
}

// ModelConvertToEntity 查询数据后将model数据赋值到entity实体
func (r *CronTaskRepos) ModelConvertToEntity(cronTask *entity.CronTask) *entity.CronTaskEntity {
	// 自动处理类型转换和嵌套字段
	cronTaskEntity := &entity.CronTaskEntity{}
	err := copier.Copy(cronTaskEntity, cronTask)
	if err != nil {
		panic(err.Error())
	}

	if cronTask.CronSkip != nil {
		// json 数据转换为结构体
		var cronSkip [][]string
		if err := json.Unmarshal(*cronTask.CronSkip, &cronSkip); err != nil {
			log.Fatal("解析失败:", err)
		}
		cronTaskEntity.CronSkip = cronSkip
	}

	if cronTask.HTTPHeaders != nil {
		// json 数据转换为结构体
		httpHeaders := &entity.HttpHeaders{}
		if err := json.Unmarshal(*cronTask.HTTPHeaders, httpHeaders); err != nil {
			log.Fatal("解析失败:", err)
		}
		cronTaskEntity.HTTPHeaders = httpHeaders
	}

	return cronTaskEntity
}

// BatchModelConvertToEntity 查询数据后将model数据赋值到entity实体
func (r *CronTaskRepos) BatchModelConvertToEntity(
	cronTaskList []*entity.CronTask,
) []*entity.CronTaskEntity {
	// 自动处理类型转换和嵌套字段
	cronTaskEntityList := make([]*entity.CronTaskEntity, 0)
	for _, v := range cronTaskList {
		cronTaskEntity := r.ModelConvertToEntity(v)
		cronTaskEntityList = append(cronTaskEntityList, cronTaskEntity)
	}

	return cronTaskEntityList
}

// EntityConvertToModel Entity实体数据转换为model
func (r *CronTaskRepos) EntityConvertToModel(cronTaskEntity *entity.CronTaskEntity) *entity.CronTask {
	conTask := &entity.CronTask{}
	err := copier.Copy(conTask, cronTaskEntity)
	if err != nil {
		panic(err.Error())
	}
	if cronTaskEntity.CronSkip != nil {
		CronSkip, err := json.Marshal(cronTaskEntity.CronSkip)
		if err != nil {
			log.Fatal("json.Marshal() 失败:", err)
		}
		conTask.CronSkip = (*datatypes.JSON)(&CronSkip)
	}

	if cronTaskEntity.HTTPHeaders != nil {
		httpHeaders, err := json.Marshal(cronTaskEntity.HTTPHeaders)
		if err != nil {
			log.Fatal("json.Marshal() 失败:", err)
		}
		conTask.HTTPHeaders = (*datatypes.JSON)(&httpHeaders)
	}

	return conTask
}

// BatchEntityConvertToModel Entity实体数据转换为model
func (r *CronTaskRepos) BatchEntityConvertToModel(
	cronTaskEntityList []*entity.CronTaskEntity,
) []*entity.CronTask {
	// 自动处理类型转换和嵌套字段
	cronTaskList := make([]*entity.CronTask, 0)
	for _, v := range cronTaskEntityList {
		cronTaskEntity := r.EntityConvertToModel(v)
		cronTaskList = append(cronTaskList, cronTaskEntity)
	}

	return cronTaskList
}
