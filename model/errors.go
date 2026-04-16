package model

import "errors"

var (
	ErrDatabase           = errors.New("database error")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrTokenInvalid       = errors.New("invalid token")
)
