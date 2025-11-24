package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/supabase-community/gotrue-go/types"

	authmocks "github.com/neutree-ai/neutree/internal/auth/mocks"
)

func TestCreateUserSuccess(t *testing.T) {
	mockClient := authmocks.NewMockClient(t)
	userID := uuid.New()

	req := CreateUserRequest{
		Email:    "test@example.com",
		Password: "securePassword123",
		Username: "testuser",
	}

	expectedGoTrueUser := &types.AdminCreateUserResponse{
		User: types.User{
			ID:           userID,
			Email:        "test@example.com",
			UserMetadata: map[string]any{"username": "testuser"},
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		},
	}

	mockClient.On("AdminCreateUser", mock.MatchedBy(func(params types.AdminCreateUserRequest) bool {
		return params.Email == req.Email &&
			*params.Password == req.Password &&
			params.EmailConfirm == true &&
			params.UserMetadata["username"] == req.Username
	})).Return(expectedGoTrueUser, nil)

	resp, err := createUser(mockClient, req)

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, userID.String(), resp.ID)
	assert.Equal(t, "test@example.com", resp.Email)
	assert.Equal(t, "testuser", resp.Username)

	mockClient.AssertExpectations(t)
}

func TestCreateUserMissingUsername(t *testing.T) {
	mockClient := authmocks.NewMockClient(t)

	req := CreateUserRequest{
		Email:    "test@example.com",
		Password: "securePassword123",
		Username: "",
	}

	resp, err := createUser(mockClient, req)

	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "username is required")

	mockClient.AssertNotCalled(t, "AdminCreateUser")
}

func TestCreateUserGoTrueAPIError(t *testing.T) {
	mockClient := authmocks.NewMockClient(t)

	req := CreateUserRequest{
		Email:    "test@example.com",
		Password: "securePassword123",
		Username: "testuser",
	}

	goTrueError := errors.New("user already exists")

	mockClient.On("AdminCreateUser", mock.Anything).Return(nil, goTrueError)

	resp, err := createUser(mockClient, req)

	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to create user in GoTrue")
	assert.Contains(t, err.Error(), "user already exists")

	mockClient.AssertExpectations(t)
}

func TestCreateUserUsernameInMetadata(t *testing.T) {
	mockClient := authmocks.NewMockClient(t)
	userID := uuid.New()

	req := CreateUserRequest{
		Email:    "test@example.com",
		Password: "securePassword123",
		Username: "customuser",
	}

	expectedGoTrueUser := &types.AdminCreateUserResponse{
		User: types.User{
			ID:           userID,
			Email:        "test@example.com",
			UserMetadata: map[string]any{"username": "customuser"},
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		},
	}

	mockClient.On("AdminCreateUser", mock.Anything).Return(expectedGoTrueUser, nil)

	resp, err := createUser(mockClient, req)

	assert.NoError(t, err)
	assert.Equal(t, "customuser", resp.Username)

	mockClient.AssertExpectations(t)
}

func TestCreateUserNoUsernameInMetadata(t *testing.T) {
	mockClient := authmocks.NewMockClient(t)
	userID := uuid.New()

	req := CreateUserRequest{
		Email:    "test@example.com",
		Password: "securePassword123",
		Username: "testuser",
	}

	expectedGoTrueUser := &types.AdminCreateUserResponse{
		User: types.User{
			ID:           userID,
			Email:        "test@example.com",
			UserMetadata: map[string]any{},
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		},
	}

	mockClient.On("AdminCreateUser", mock.Anything).Return(expectedGoTrueUser, nil)

	resp, err := createUser(mockClient, req)

	assert.NoError(t, err)
	assert.Equal(t, "", resp.Username)

	mockClient.AssertExpectations(t)
}

func TestCreateUserInvalidUsernameType(t *testing.T) {
	mockClient := authmocks.NewMockClient(t)
	userID := uuid.New()

	req := CreateUserRequest{
		Email:    "test@example.com",
		Password: "securePassword123",
		Username: "testuser",
	}

	expectedGoTrueUser := &types.AdminCreateUserResponse{
		User: types.User{
			ID:           userID,
			Email:        "test@example.com",
			UserMetadata: map[string]any{"username": 12345},
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		},
	}

	mockClient.On("AdminCreateUser", mock.Anything).Return(expectedGoTrueUser, nil)

	resp, err := createUser(mockClient, req)

	assert.NoError(t, err)
	assert.Equal(t, "", resp.Username)

	mockClient.AssertExpectations(t)
}
