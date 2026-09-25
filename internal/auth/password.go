package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const minPasswordLen = 8

var ErrPasswordTooShort = errors.New("password must be at least 8 characters")

func HashPassword(password string) ([]byte, error) {
	if len(password) < minPasswordLen {
		return nil, ErrPasswordTooShort
	}
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

func CheckPassword(hash []byte, password string) bool {
	if len(hash) == 0 {
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}
