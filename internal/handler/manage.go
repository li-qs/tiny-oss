package handler

import (
	"fmt"
	"myoss/internal/db"
	"myoss/internal/model"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
)

// 列出所有文件
func ListFiles(c *echo.Context) error {
	page, _ := strconv.Atoi(c.QueryParam("page"))
	pageSize, _ := strconv.Atoi(c.QueryParam("page_size"))

	if page < 1 {
		page = 1
	}
	switch {
	case pageSize < 1:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	// 指定排序方式
	sortBy := c.QueryParam("sort_by")
	sortOrder := c.QueryParam("sort_order")

	allowedSort := map[string]bool{
		"id":            true,
		"created_at":    true,
		"size":          true,
		"original_name": true,
	}
	if !allowedSort[sortBy] {
		sortBy = "id"
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "desc"
	}

	// 分页读取
	sql := fmt.Sprintf(`
		SELECT *
		FROM files
		ORDER BY %s %s
		LIMIT ? OFFSET ?
	`, sortBy, sortOrder)

	var list []model.File
	err := db.DB.Select(&list, sql, pageSize, offset)
	if err != nil {
		return c.JSON(500, map[string]any{"msg": "获取失败", "err": err.Error()})
	}

	for i := range list {
		list[i].URL = buildURL(c, list[i].Filename)
	}

	var total int
	_ = db.DB.Get(&total, "SELECT COUNT(*) FROM files")

	return c.JSON(200, map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"list":      list,
	})
}

// 删除指定文件
func DeleteFile(c *echo.Context) error {
	id := c.Param("id")

	// 数据库验证
	var f model.File
	err := db.DB.Get(&f, "SELECT filename FROM files WHERE id=?", id)
	if err != nil {
		return c.JSON(404, map[string]string{"msg": "文件不存在"})
	}

	// 组装文件路径
	uuid := strings.Split(f.Filename, ".")[0]
	dir := filepath.Join(GetUploadDir(), uuid[:2], uuid[2:4])
	path := filepath.Join(dir, f.Filename)

	// 删除文件
	_ = os.Remove(path)

	// 清理空的分桶目录
	_ = os.Remove(filepath.Dir(path))
	_ = os.Remove(filepath.Dir(filepath.Dir(path)))

	// 删除数据库记录
	res, err := db.DB.Exec("DELETE FROM files WHERE id=?", id)
	if err != nil {
		return c.JSON(500, map[string]string{"msg": "删除失败"})
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.JSON(404, map[string]string{"msg": "文件不存在"})
	}

	return c.JSON(200, map[string]string{"msg": "删除成功"})
}
