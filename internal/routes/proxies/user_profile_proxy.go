package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var userProfileAdmissionResource = admission.NewResource[v1.UserProfile](storage.USER_PROFILE_TABLE)

func validateUserProfileDeleteDependencies(s storage.Storage, candidate v1.UserProfile) error {
	username := candidate.GetName()

	userProfiles, err := s.ListUserProfile(storage.ListOption{Filters: []storage.Filter{{Column: "metadata->>name", Operator: "eq", Value: username}}})
	if err != nil {
		return fmt.Errorf("failed to get user profile: %w", err)
	}

	if len(userProfiles) == 0 {
		return nil
	}

	userID := userProfiles[0].ID

	count, err := s.Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{{Column: "spec->>user_id", Operator: "eq", Value: userID}})
	if err != nil {
		return fmt.Errorf("failed to count role assignments: %w", err)
	}

	if count > 0 {
		return newLegacyDeleteDependencyError(
			10130,
			fmt.Sprintf("cannot delete user_profile '%s'", username),
			fmt.Sprintf("%d role assignment(s) still reference this user", count),
		)
	}

	return nil
}

func RegisterUserProfileRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/user_profiles")
	proxyGroup.Use(middlewares...)

	if err := registerUserProfileAdmission(deps); err != nil {
		return err
	}

	var createRunner, patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		createRunner = CreateAdmissionRunnerWithOptions(deps.Admission, userProfileAdmissionResource, legacyCreateAdmissionRunnerOptions)
		patchRunner = CreatePatchAdmissionRunner(deps, storage.USER_PROFILE_TABLE, userProfileAdmissionResource)
	}

	handler := CreateStructProxyHandler[v1.UserProfile](deps, storage.USER_PROFILE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", withAdmissionRunner(createRunner, handler)...)
	proxyGroup.PATCH("", withAdmissionRunner(patchRunner, handler)...)

	return nil
}

func registerUserProfileAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}

	if err := deps.Admission.RegisterResource(userProfileAdmissionResource); err != nil {
		return err
	}

	return deps.Admission.RegisterHook(userProfileAdmissionResource, admission.ValidateDelete(
		admission.HookMeta{Name: "community.user-profile.dependencies.delete", Order: 10}, 10130,
		func(_ admission.RequestContext, _, candidate v1.UserProfile) error {
			return validateUserProfileDeleteDependencies(deps.Storage, candidate)
		},
	))
}
