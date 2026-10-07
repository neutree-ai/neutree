package auth

import (
	"github.com/supabase-community/gotrue-go/types"
)

// Client defines the interface for GoTrue authentication operations
type Client interface {
	AdminGetUser(req types.AdminGetUserRequest) (*types.AdminGetUserResponse, error)
	AdminCreateUser(params types.AdminCreateUserRequest) (*types.AdminCreateUserResponse, error)
	AdminUpdateUser(req types.AdminUpdateUserRequest) (*types.AdminUpdateUserResponse, error)
	AdminDeleteUser(req types.AdminDeleteUserRequest) error
}
