package controller

import (
	"fmt"
	"strconv"
	"time"

	"github.com/bingcool/gen"
	"github.com/bingcool/gofy/app/Io/db"
	"github.com/bingcool/gofy/app/dao/builder"
	"github.com/bingcool/gofy/app/entity"
	"github.com/bingcool/gofy/app/repository"
	"github.com/bingcool/gofy/src/system"
	"github.com/gin-gonic/gin"
	"github.com/gogf/gf/v2/util/gutil"
)

type Order struct {
}

func NewOrder() *Order {
	return &Order{}
}

type GetOrderListRequest struct {
	OrderId     uint32   `json:"order_id" form:"order_id"`
	UserId      uint32   `json:"user_id" form:"user_id"`
	CategoryIds []uint32 `json:"category_ids" form:"category_ids" default:"[1,2]"`
	Page        uint32   `json:"page" form:"page" default:"1"`
	Size        uint32   `json:"size" form:"size" default:"10"`
}

type GetOrderListRequest1 struct {
	OrderId     uint32   `json:"order_id" form:"order_id"`
	UserId      uint32   `json:"user_id" form:"user_id" validate:"required,min=1" message:"required:姓名不能为空,min:姓名长度至少3个字符"`
	CategoryIds []uint32 `json:"category_ids" form:"category_ids" default:"[1,2]"`
	Page        uint32   `json:"page" form:"page" default:"1"`
	Size        uint32   `json:"size" form:"size" default:"10"`
}

type GetOrderListResponse struct {
	OrderList []OrderItem `json:"order_list"`
}

type OrderItem struct {
	OrderId  uint32 `json:"order_id"`
	UserId   uint32 `json:"user_id"`
	Env      string `json:"env"`
	RunModel bool   `json:"run_model"`
}

func (order *Order) GetOrderList(ctx *gin.Context, req *GetOrderListRequest) (res *GetOrderListResponse, err error) {
	query := builder.Use(db.GetDb())

	cronTaskQuery := query.CronTask

	var findList []struct {
		ID          uint32 `json:"id"`
		ExecBatchID string `json:"exec_batch_id"`
	}

	err = cronTaskQuery.WithContext(ctx).Select(
		cronTaskQuery.ID.As("id"),
		query.CronTaskLog.ExecBatchID.As("exec_batch_id"),
	).Where(
		cronTaskQuery.ID.Eq(1),
	).RightJoin(query.CronTaskLog, query.CronTaskLog.CronID.EqCol(cronTaskQuery.ID)).
		Limit(3).
		Scan(&findList)

	//for _, v := range findList {
	//	fmt.Println(v.ID, v.ExecBatchID)
	//}

	cronSkip1 := []string{"2023-07-01 00:00:00", "2023-07-02 00:00:00"}
	cronSkip2 := []string{"2023-07-03 00:00:00", "2023-07-04 00:00:00"}

	cronTaskRepos := repository.NewCronTaskRepos()

	where := []gen.Condition{
		cronTaskRepos.Query().CronTask.ID.Eq(2),
	}
	first := cronTaskRepos.First(ctx, where)
	first.CronSkip = make([][]string, 0)
	first.CronSkip = append(first.CronSkip, cronSkip1, cronSkip2)
	rowsAffected, _ := cronTaskRepos.Update(ctx, where, first)

	where1 := []gen.Condition{
		cronTaskRepos.Query().CronTask.ID.Eq(3),
	}
	// 创建
	cronTaskEntity := entity.NewCronTaskEntity()
	cronTaskEntity.CronSkip = make([][]string, 0)
	cronTaskEntity.CronSkip = append(cronTaskEntity.CronSkip, cronSkip1, cronSkip2)
	rowsAffected1, _ := cronTaskRepos.Update(ctx, where1, cronTaskEntity)
	fmt.Println("rowsAffected1", rowsAffected1)

	fmt.Println("rowsAffected", rowsAffected)

	// 创建
	cronTaskEntity = entity.NewCronTaskEntity()
	cronTaskEntity.Name = "test-aa" + strconv.Itoa(int(time.Now().Unix()))

	cronTaskEntity.CronSkip = make([][]string, 0)
	cronTaskEntity.CronSkip = append(cronTaskEntity.CronSkip, cronSkip1, cronSkip2)

	cronTaskEntity.HTTPHeaders = &entity.HttpHeaders{
		Token: "123",
		Xyz:   "456",
	}

	insertId1, err := cronTaskRepos.Create(ctx, cronTaskEntity)

	fmt.Println("insertId1", insertId1)

	// 列表查询
	list := cronTaskRepos.SimpleList(
		ctx,
		[]gen.Condition{
			cronTaskRepos.Query().CronTask.ID.Gt(5),
			cronTaskRepos.Query().CronTask.ID.Lt(10),
		},
		nil,
	)

	gutil.Dump(len(list))

	cronTaskList1, _ := cronTaskRepos.Query().CronTask.WithContext(ctx).Where(
		cronTaskRepos.Query().CronTask.ID.Gt(5),
		cronTaskRepos.Query().CronTask.ID.Lt(10),
	).Order(cronTaskRepos.Query().CronTask.ID.Desc()).Find()

	list2 := cronTaskRepos.BatchModelConvertToEntity(cronTaskList1)

	gutil.Dump(list2)

	//cronTaskService := service.NewCronTaskService()
	//list := cronTaskService.GetCronTaskList()
	//
	//gutil.Dump(list)

	if err != nil {
		return nil, err
	}

	orderItem := &OrderItem{
		OrderId:  1,
		UserId:   1,
		Env:      system.GetEnv(),
		RunModel: system.IsCliService(),
	}

	fmt.Println("aaaaaaaaaaaaaa")

	orderList := make([]OrderItem, 0)
	orderList = append(orderList, *orderItem)
	res = &GetOrderListResponse{
		OrderList: orderList,
	}
	return res, nil
}
