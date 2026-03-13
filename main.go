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
	ID           int    `db:"id"`
	UUID         string `db:"uuid"`
	OriginalName string `db:"original_name"`
	Filename     string `db:"filename"`
	Size         int64  `db:"size"`
	Ext          string `db:"ext"`
	IsPrivate    int    `db:"is_private"`
	CreatedAt    string `db:"created_at"`
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

	ext := filepath.Ext(file.Filename)
	uid := strings.ReplaceAll(uuid.NewString(), "-", "")
	saveName := uid + ext
	savePath := "./uploads/" + saveName

	src, _ := file.Open()
	defer src.Close()
	dst, _ := os.Create(savePath)
	defer dst.Close()
	io.Copy(dst, src)

	if isCompress == 1 && isImage(ext) {
		compressImage(savePath, savePath, quality)
	}

	db.MustExec(`
	INSERT INTO files(uuid, original_name, filename, size, ext, is_private)
	VALUES(?,?,?,?,?,?)`,
		uid, file.Filename, saveName, file.Size, ext, isPrivate)

	return c.JSON(200, map[string]string{
		"url": c.Scheme() + "://" + c.Request().Host + "/i/" + saveName,
	})
}

func serveFile(c *echo.Context) error {
	filename := c.Param("filename")
	path := "./uploads/" + filename

	var f File
	uuid := strings.TrimSuffix(filename, filepath.Ext(filename))
	err := db.Get(&f, "SELECT is_private FROM files WHERE uuid=?", uuid)
	if err != nil {
		return c.NoContent(404)
	}

	if f.IsPrivate == 1 {
		token := c.QueryParam("token")
		if token == "" || token != config.Security.Token {
			return c.NoContent(403)
		}
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

	ext := strings.ToLower(filepath.Ext(src))
	switch ext {
	case ".jpg", ".jpeg":
		f, _ := os.Create(dst)
		defer f.Close()
		return jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
	case ".png":
		f, _ := os.Create(dst)
		defer f.Close()
		return png.Encode(f, img)
	}
	return nil
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
	case pageSize > 50:
		pageSize = 50
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
		SELECT id,uuid,original_name,filename,size,is_private,created_at
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

	_ = os.Remove("./uploads/" + f.Filename)
	_, _ = db.Exec("DELETE FROM files WHERE id=?", id)

	return c.JSON(200, map[string]string{"msg": "删除成功"})
}

func isImage(ext string) bool {
	ext = strings.ToLower(ext)
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif"
}
