package auth

import (
	"github.com/supabase-community/gotrue-go/types"
)

// Client defines the interface for GoTrue authentication operations
type Client interface {
	AdminCreateUser(params types.AdminCreateUserRequest) (*types.AdminCreateUserResponse, error)
	AdminDeleteUser(req types.AdminDeleteUserRequest) error
}
