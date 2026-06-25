package model

type File struct {
	ID           int    `db:"id" json:"id"`
	UUID         string `db:"uuid" json:"uuid"`
	OriginalName string `db:"original_name" json:"original_name"`
	Filename     string `db:"filename" json:"filename"`
	Size         int64  `db:"size" json:"size"`
	Ext          string `db:"ext" json:"ext"`
	IsPrivate    int    `db:"is_private" json:"is_private"`
	CreatedAt    string `db:"created_at" json:"created_at"`
	URL          string `json:"url"`
}
