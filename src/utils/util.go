package utils

import (
	"math"
	"strconv"
)

// IsInt 判断 value 是否为整数
func IsInt(value string) bool {
	_, err := strconv.Atoi(value)
	return err == nil
}

// IsFloat 判断 value 是否为浮点数
func IsFloat(value string) bool {
	_, err := strconv.ParseFloat(value, 64)
	return err == nil
}

// IsNumber 判断 value 是否为数字
func IsNumber(value string) bool {
	_, err := strconv.ParseFloat(value, 64)
	return err == nil
}

// IsValidIndexOfSlice 判断 index 是否在切片的索引范围内
func IsValidIndexOfSlice[T any](s []T, index int) (int, bool) {
	if index >= 0 && index < len(s) {
		return index, true
	}
	return 0, false
}

// IsInt32 判断 int64 是否为 int32
func IsInt32(n int64) bool {
	return n >= math.MinInt32 && n <= math.MaxInt32
}

// InSlice 判断 target 是否在 slices 中
func InSlice[T string | int8 | int | int32 | int64 | uint8 | uint32 | uint64 | float32 | float64](
	target T,
	slices []T,
) bool {
	for _, item := range slices {
		if item == target {
			return true
		}
	}
	return false
}
