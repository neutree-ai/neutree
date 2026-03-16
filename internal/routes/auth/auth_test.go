package auth

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	authmocks "github.com/neutree-ai/neutree/internal/auth/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
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

func TestResolveEmailByUsername_UsernameFound(t *testing.T) {
	mockStorage := storagemocks.NewMockStorage(t)

	body, _ := json.Marshal(map[string]interface{}{
		"email":    "admin",
		"password": "secret",
	})

	mockStorage.On("ListUserProfile", mock.MatchedBy(func(opt storage.ListOption) bool {
		return len(opt.Filters) == 1 &&
			opt.Filters[0].Column == "metadata->name" &&
			opt.Filters[0].Value == `"admin"`
	})).Return([]v1.UserProfile{
		{
			Spec: &v1.UserProfileSpec{Email: "admin@example.com"},
		},
	}, nil)

	result := resolveEmailByUsername(mockStorage, body)

	var resultBody map[string]interface{}
	err := json.Unmarshal(result, &resultBody)
	assert.NoError(t, err)
	assert.Equal(t, "admin@example.com", resultBody["email"])
	assert.Equal(t, "secret", resultBody["password"])
}

func TestResolveEmailByUsername_UsernameNotFound(t *testing.T) {
	mockStorage := storagemocks.NewMockStorage(t)

	body, _ := json.Marshal(map[string]interface{}{
		"email":    "nonexistent",
		"password": "secret",
	})

	mockStorage.On("ListUserProfile", mock.Anything).Return([]v1.UserProfile{}, nil)

	result := resolveEmailByUsername(mockStorage, body)

	var resultBody map[string]interface{}
	err := json.Unmarshal(result, &resultBody)
	assert.NoError(t, err)
	assert.Equal(t, "nonexistent", resultBody["email"])
}

func TestResolveEmailByUsername_InvalidJSON(t *testing.T) {
	mockStorage := storagemocks.NewMockStorage(t)

	body := []byte("not json")

	result := resolveEmailByUsername(mockStorage, body)
	assert.Equal(t, body, result)
}

func TestResolveEmailByUsername_StorageError(t *testing.T) {
	mockStorage := storagemocks.NewMockStorage(t)

	body, _ := json.Marshal(map[string]interface{}{
		"email":    "admin",
		"password": "secret",
	})

	mockStorage.On("ListUserProfile", mock.Anything).Return(nil, errors.New("db error"))

	result := resolveEmailByUsername(mockStorage, body)

	var resultBody map[string]interface{}
	err := json.Unmarshal(result, &resultBody)
	assert.NoError(t, err)
	assert.Equal(t, "admin", resultBody["email"])
}

func TestResolveEmailByUsername_EmailInputAlsoResolved(t *testing.T) {
	mockStorage := storagemocks.NewMockStorage(t)

	body, _ := json.Marshal(map[string]interface{}{
		"email":    "user@old-domain.com",
		"password": "secret",
	})

	mockStorage.On("ListUserProfile", mock.MatchedBy(func(opt storage.ListOption) bool {
		return opt.Filters[0].Value == `"user@old-domain.com"`
	})).Return([]v1.UserProfile{
		{
			Spec: &v1.UserProfileSpec{Email: "user@new-domain.com"},
		},
	}, nil)

	result := resolveEmailByUsername(mockStorage, body)

	var resultBody map[string]interface{}
	err := json.Unmarshal(result, &resultBody)
	assert.NoError(t, err)
	assert.Equal(t, "user@new-domain.com", resultBody["email"])
}
