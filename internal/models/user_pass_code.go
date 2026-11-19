package models

import (
	"time"

	"github.com/google/uuid"
)

// UserPassCode stores a user's bcrypt-hashed login passcode. One row per user.
type UserPassCode struct {
	ID           uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	UserID       uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"user_id"`
	PassCodeHash string    `gorm:"column:pass_code_hash;size:255;not null" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (UserPassCode) TableName() string { return "user_pass_code" }

// SetPassCodeRequest is the body of PUT /users/:userID/passcode.
type SetPassCodeRequest struct {
	Passcode string `json:"passcode" validate:"required,numeric,min=4,max=6"`
}

// PasscodeLoginRequest is the body of POST /auth/login/passcode.
type PasscodeLoginRequest struct {
	UserID   string `json:"userId" validate:"required,uuid"`
	Passcode string `json:"passcode" validate:"required,numeric,min=4,max=6"`
}

// PasscodeLoginResponse is returned by a successful passcode login.
type PasscodeLoginResponse struct {
	Token        string `json:"token"`
	UserID       string `json:"userId"`
	UserName     string `json:"userName"`
	UserEmail    string `json:"userEmail"`
	ProfileImage string `json:"profileImage"`
	Mobile       string `json:"mobile"`
}
