package repository

import (
	"reflect"
	"strings"
	"sync"

	"gorm.io/gorm/schema"
)

const (
	maxRepositoryInsertBatchSize = 900
	sqliteVariableLimit          = 999
)

var insertBatchColumnCountCache sync.Map

func insertBatchSize(model any) int {
	columnCount := insertBatchColumnCount(model)
	if columnCount <= 0 {
		return maxRepositoryInsertBatchSize
	}
	batchSize := sqliteVariableLimit / columnCount
	if batchSize <= 0 {
		return 1
	}
	return min(maxRepositoryInsertBatchSize, batchSize)
}

func insertBatchColumnCount(model any) int {
	modelType := reflect.TypeOf(model)
	if modelType == nil {
		return 0
	}
	for modelType.Kind() == reflect.Pointer || modelType.Kind() == reflect.Slice || modelType.Kind() == reflect.Array {
		modelType = modelType.Elem()
	}
	if cached, ok := insertBatchColumnCountCache.Load(modelType); ok {
		return cached.(int)
	}

	parsed, err := schema.Parse(reflect.New(modelType).Interface(), &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return 0
	}
	columnCount := 0
	for _, field := range parsed.Fields {
		if field.DBName == "" || field.AutoIncrement {
			continue
		}
		if strings.Contains(field.Tag.Get("gorm"), "->") {
			continue
		}
		columnCount++
	}
	insertBatchColumnCountCache.Store(modelType, columnCount)
	return columnCount
}
