package migrations

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type SourceDepartment struct {
	ID          int64           `gorm:"column:id"`
	Name        string          `gorm:"column:name"`
	Handle      string          `gorm:"column:handle"`
	Meta        json.RawMessage `gorm:"column:meta"`
	CreatedAt   *string         `gorm:"column:created_at"`
	UpdatedAt   *string         `gorm:"column:updated_at"`
	DeletedAt   *string         `gorm:"column:deleted_at"`
	SuspendedAt *string         `gorm:"column:suspended_at"`
}

type TargetDepartment struct {
	ID        uuid.UUID  `gorm:"column:id"`
	Name      string     `gorm:"column:name"`
	Code      *string    `gorm:"column:code"`
	ParentID  *uuid.UUID `gorm:"column:parent_id"`
	Level     int        `gorm:"column:level"`
	Path      string     `gorm:"column:path"`
	IsActive  bool       `gorm:"column:is_active"`
	CreatedAt *string    `gorm:"column:created_at"`
	UpdatedAt *string    `gorm:"column:updated_at"`
	DeletedAt *string    `gorm:"column:deleted_at"`
}

func MigrateDepartments(srcDB *gorm.DB, destDB *gorm.DB) error {
	var source []SourceDepartment

	// 🔹 Load source data
	if err := srcDB.Table("department").Find(&source).Error; err != nil {
		return err
	}

	// 🔹 Map oldID → newUUID
	idMap := make(map[int64]uuid.UUID)
	for _, d := range source {
		idMap[d.ID] = uuid.New()
	}

	// 🔹 Extract parent relationships
	parentMap := make(map[int64]*int64)

	for _, d := range source {
		var meta map[string]interface{}
		if len(d.Meta) > 0 {
			_ = json.Unmarshal(d.Meta, &meta)

			if pid, ok := meta["parent_id"].(float64); ok {
				val := int64(pid)
				parentMap[d.ID] = &val
			}
		}
	}

	// 🔹 Build target data
	var target []TargetDepartment

	for _, d := range source {
		newID := idMap[d.ID]

		var parentUUID *uuid.UUID
		level := 0
		path := newID.String()

		currID := d.ID

		for {
			pid, ok := parentMap[currID]
			if !ok || pid == nil {
				break
			}

			parent := idMap[*pid]
			parentUUID = &parent
			level++

			path = parent.String() + "." + path
			currID = *pid
		}

		target = append(target, TargetDepartment{
			ID:        newID,
			Name:      d.Name,
			Code:      &d.Handle,
			ParentID:  parentUUID,
			Level:     level,
			Path:      path,
			IsActive:  d.SuspendedAt == nil,
			CreatedAt: d.CreatedAt,
			UpdatedAt: d.UpdatedAt,
			DeletedAt: d.DeletedAt,
		})
	}

	// 🔹 Insert with transaction
	return destDB.Transaction(func(tx *gorm.DB) error {
		for _, d := range target {
			if err := tx.Table("departments").Create(&d).Error; err != nil {
				return fmt.Errorf("failed for %s: %w", d.Name, err)
			}
		}
		return nil
	})
}
