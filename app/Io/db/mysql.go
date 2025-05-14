package db

import (
	"runtime"
	"sync"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var (
	db     *gorm.DB
	dbOnce sync.Once
)

// GetDb 初始化数据库连接
func GetDb() *gorm.DB {
	osName := runtime.GOOS
	//fmt.Println(osName)
	switch osName {
	case "darwin":
		dsn := "root:123456@tcp(127.0.0.1:3307)/bingcool?charset=utf8mb4&parseTime=True&loc=Local"
		return initDb(dsn)
	case "linux":
		dsn := "root:root@galaxy1024@tcp(192.168.25.59:13)/bingcool?charset=utf8mb4&parseTime=True&loc=Local"
		return initDb(dsn)
	default:
		dsn := "root:root@galaxy1024@tcp(localhost:3306)/bingcool?charset=utf8mb4&parseTime=True&loc=Local"
		return initDb(dsn)
	}
}

func initDb(dsn string) *gorm.DB {
	var err error
	dbOnce.Do(func() {
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		if err != nil {
			// 处理数据库连接错误
			panic(err)
		}

		// 设置连接池大小
		sqlDB, err := db.DB()
		if err != nil {
			panic(err)
		} else {
			sqlDB.SetMaxIdleConns(3)
			sqlDB.SetMaxOpenConns(5)
		}
	})
	return db
}
