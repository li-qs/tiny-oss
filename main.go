package main

import (
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/disintegration/imaging"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"gopkg.in/yaml.v3"
)

type Config struct {
	DB struct {
		DSN string `yaml:"dsn"`
	} `yaml:"db"`
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
	Security struct {
		Token    string `yaml:"token"`
		APIToken string `yaml:"api_token"`
	} `yaml:"security"`
	App struct {
		FileBaseURL string `yaml:"file_base_url"`
	} `yaml:"app"`
}

var (
	db     *sqlx.DB
	config Config
)

func loadConfig() error {
	file, err := os.Open("config.yaml")
	if err != nil {
		return err
	}
	defer file.Close()
	return yaml.NewDecoder(file).Decode(&config)
}

type File struct {
	ID           int    `db:"id" json:"id"`
	UUID         string `db:"uuid" json:"uuid"`
	OriginalName string `db:"original_name" json:"original_name"`
	Filename     string `db:"filename" json:"filename"`
	Size         int64  `db:"size" json:"size"`
	Ext          string `db:"ext" json:"ext"`
	IsPrivate    int    `db:"is_private" json:"is_private"`
	CreatedAt    string `db:"created_at" json:"created_at"`
}

func main() {
	err := loadConfig()
	if err != nil {
		panic("配置加载失败：" + err.Error())
	}

	db, err = sqlx.Connect("mysql", config.DB.DSN)
	if err != nil {
		panic(err)
	}

	_ = os.MkdirAll("./uploads", 0755)
	e := echo.New()
	e.Use(middleware.RequestLogger())
	e.Use(middleware.BodyLimit(50 << 20))

	// CORS 修复：去掉 echo.GET 常量
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders: []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAuthorization},
	}))

	// 全部使用指针路由
	e.GET("/i/:filename", serveFile)
	api := e.Group("/api", apiAuth)
	api.POST("/upload", upload)
	api.GET("/files", listFiles)
	api.DELETE("/file/:id", deleteFile)

	e.Start(config.Server.Port)
}

// ==============================
// 🔴 API 鉴权中间件（必须传 token）
// ==============================
func apiAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		token := c.Request().Header.Get("X-API-Token")
		if token == "" {
			token = c.QueryParam("api_token")
		}

		if token == "" || token != config.Security.APIToken {
			return c.JSON(403, map[string]string{"msg": "无权限访问"})
		}
		return next(c)
	}
}

// ==============================
// 上传
// ==============================
func upload(c *echo.Context) error {
	isCompress, _ := strconv.Atoi(c.FormValue("compress"))
	quality, _ := strconv.Atoi(c.FormValue("quality"))
	isPrivate, _ := strconv.Atoi(c.FormValue("private"))

	if quality < 10 || quality > 100 {
		quality = 75
	}
	if isPrivate != 0 && isPrivate != 1 {
		isPrivate = 0
	}

	file, err := c.FormFile("file")
	if err != nil {
		return c.JSON(400, map[string]string{"msg": "请上传文件"})
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	uid := strings.ReplaceAll(uuid.NewString(), "-", "")

	// 分桶目录
	dir := filepath.Join("uploads", uid[:2], uid[2:4])
	if err := os.MkdirAll(dir, 0755); err != nil {
		return c.JSON(500, map[string]string{"msg": "目录创建失败"})
	}

	filename := uid + ext
	savePath := filepath.Join(dir, filename)

	// 保存原文件
	src, err := file.Open()
	if err != nil {
		return c.JSON(500, map[string]string{"msg": "文件读取失败"})
	}
	defer src.Close()

	dst, err := os.Create(savePath)
	if err != nil {
		return c.JSON(500, map[string]string{"msg": "文件创建失败"})
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return c.JSON(500, map[string]string{"msg": "文件保存失败"})
	}

	// 👉 压缩（覆盖原文件）
	if isCompress == 1 && canCompress(ext) {
		if err := compressImage(savePath, savePath, quality); err != nil {
			return c.JSON(500, map[string]string{"msg": "图片压缩失败"})
		}
	}

	// 👉 获取最终文件大小（关键）
	stat, err := os.Stat(savePath)
	if err != nil {
		return c.JSON(500, map[string]string{"msg": "获取文件信息失败"})
	}

	finalSize := stat.Size()

	// 👉 DB 写入失败回滚文件
	_, err = db.Exec(`
		INSERT INTO files(uuid, original_name, filename, size, ext, is_private)
		VALUES(?,?,?,?,?,?)`,
		uid, file.Filename, filename, finalSize, ext, isPrivate,
	)
	if err != nil {
		_ = os.Remove(savePath)
		return c.JSON(500, map[string]string{"msg": "数据库写入失败"})
	}

	return c.JSON(200, map[string]any{
		"url":      buildURL(filename),
		"filename": filename,
		"size":     finalSize,
	})
}

func buildURL(filename string) string {
	return config.App.FileBaseURL + "/" + filename
}

func serveFile(c *echo.Context) error {
	filename := c.Param("filename")

	// 👉 分桶路径推导
	ext := filepath.Ext(filename)
	uuid := strings.TrimSuffix(filename, ext)
	dir := filepath.Join("uploads", uuid[:2], uuid[2:4])
	path := filepath.Join(dir, filename)

	// 👉 DB 校验
	var f File
	err := db.Get(&f, "SELECT is_private FROM files WHERE uuid=?", uuid[:32])
	if err != nil {
		return c.NoContent(404)
	}

	if f.IsPrivate == 1 {
		token := c.QueryParam("token")
		if token != config.Security.Token {
			return c.NoContent(403)
		}
	}

	// 👉 文件存在检查
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return c.NoContent(404)
	}

	return c.File(path)
}

// ==============================
// 压缩 + 旋转
// ==============================
func compressImage(src, dst string, quality int) error {
	img, err := imaging.Open(src, imaging.AutoOrientation(true))
	if err != nil {
		return err
	}

	if img.Bounds().Dx() > 1600 {
		img = imaging.Resize(img, 1600, 0, imaging.Lanczos)
	}

	tmp := dst + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	ext := strings.ToLower(filepath.Ext(src))
	switch ext {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
	case ".png":
		err = png.Encode(f, img)
	}
	f.Close()

	if err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return os.Rename(tmp, dst) // 原子替换
}

// ==============================
// 分页列表
// ==============================
func listFiles(c *echo.Context) error {
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

	sql := fmt.Sprintf(`
		SELECT *
		FROM files
		ORDER BY %s %s
		LIMIT ? OFFSET ?
	`, sortBy, sortOrder)

	var list []File
	err := db.Select(&list, sql, pageSize, offset)
	if err != nil {
		return c.JSON(500, map[string]any{"msg": "获取失败", "err": err.Error()})
	}

	var total int
	_ = db.Get(&total, "SELECT COUNT(*) FROM files")

	return c.JSON(200, map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"list":      list,
	})
}

// ==============================
// 删除
// ==============================
func deleteFile(c *echo.Context) error {
	id := c.Param("id")

	var f File
	err := db.Get(&f, "SELECT filename FROM files WHERE id=?", id)
	if err != nil {
		return c.JSON(404, map[string]string{"msg": "文件不存在"})
	}

	uuid := strings.Split(f.Filename, ".")[0]
	dir := filepath.Join("uploads", uuid[:2], uuid[2:4])
	path := filepath.Join(dir, f.Filename)

	_ = os.Remove(path)

	res, err := db.Exec("DELETE FROM files WHERE id=?", id)
	if err != nil {
		return c.JSON(500, map[string]string{"msg": "删除失败"})
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.JSON(404, map[string]string{"msg": "文件不存在"})
	}

	return c.JSON(200, map[string]string{"msg": "删除成功"})
}

func isImage(ext string) bool {
	ext = strings.ToLower(ext)
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif"
}

func canCompress(ext string) bool {
	ext = strings.ToLower(ext)
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png"
}
