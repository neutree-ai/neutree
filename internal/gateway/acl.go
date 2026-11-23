package gateway

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kong/go-kong/kong"
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	NeutreeACLGroupPrefix = "nt:"

	ACLResourceEndpoint         = "endpoint"
	ACLResourceExternalEndpoint = "external-endpoint"
)

func BuildNeutreeACLGroup(workspace, resourceType, resourceName string) string {
	sum := sha256.Sum256([]byte(workspace + "\x00" + resourceType + "\x00" + resourceName))
	return fmt.Sprintf("%s%s:%x", NeutreeACLGroupPrefix, resourceType, sum)
}

func IsNeutreeACLGroup(group string) bool {
	return strings.HasPrefix(group, NeutreeACLGroupPrefix)
}

// permissionModelInvoke lets a key reach every endpoint and external endpoint
// of its workspace. endpoint:read no longer counts: reading an endpoint and
// calling its model are granted apart.
const permissionModelInvoke = "model:invoke"

func (k *Kong) desiredAPIKeyACLGroups(apiKey *v1.ApiKey) ([]string, error) {
	if apiKey == nil || apiKey.Metadata == nil || apiKey.Metadata.Workspace == "" || apiKey.OwnerID() == "" {
		return nil, nil
	}

	workspace := apiKey.Metadata.Workspace

	// The owner is a user or a ServiceAccount; both are decided by their own
	// role assignments.
	canInvoke, err := k.hasGatewayPermission(apiKey.OwnerID(), workspace, permissionModelInvoke)
	if err != nil {
		return nil, err
	}

	if !canInvoke {
		return []string{}, nil
	}

	return k.workspaceACLGroups(workspace)
}

// workspaceACLGroups are the ACL groups of every live endpoint and external
// endpoint of workspace, sorted.
func (k *Kong) workspaceACLGroups(workspace string) ([]string, error) {
	groups, err := k.endpointACLGroups(workspace)
	if err != nil {
		return nil, err
	}

	externalGroups, err := k.externalEndpointACLGroups(workspace)
	if err != nil {
		return nil, err
	}

	groups = append(groups, externalGroups...)
	sort.Strings(groups)

	return groups, nil
}

func (k *Kong) hasGatewayPermission(userID, workspace, permission string) (bool, error) {
	var result bool
	err := k.storage.CallDatabaseFunction("has_permission", map[string]interface{}{
		"user_uuid":           userID,
		"required_permission": permission,
		"workspace":           workspace,
	}, &result)

	if err != nil {
		return false, errors.Wrapf(err, "failed to check %s for user %s in workspace %s", permission, userID, workspace)
	}

	return result, nil
}

func (k *Kong) endpointACLGroups(workspace string) ([]string, error) {
	endpoints, err := k.storage.ListEndpoint(storage.ListOption{
		Filters: workspaceFilters(workspace),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list endpoints in workspace %s", workspace)
	}

	groups := make([]string, 0, len(endpoints))

	for i := range endpoints {
		ep := endpoints[i]
		if ep.Metadata == nil || ep.Metadata.DeletionTimestamp != "" {
			continue
		}

		groups = append(groups, BuildNeutreeACLGroup(workspace, ACLResourceEndpoint, ep.Metadata.Name))
	}

	return groups, nil
}

func (k *Kong) externalEndpointACLGroups(workspace string) ([]string, error) {
	externalEndpoints, err := k.storage.ListExternalEndpoint(storage.ListOption{
		Filters: workspaceFilters(workspace),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list external endpoints in workspace %s", workspace)
	}

	groups := make([]string, 0, len(externalEndpoints))

	for i := range externalEndpoints {
		ee := externalEndpoints[i]
		if ee.Metadata == nil || ee.Metadata.DeletionTimestamp != "" {
			continue
		}

		groups = append(groups, BuildNeutreeACLGroup(workspace, ACLResourceExternalEndpoint, ee.Metadata.Name))
	}

	return groups, nil
}

func workspaceFilters(workspace string) []storage.Filter {
	return []storage.Filter{
		{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    strconv.Quote(workspace),
		},
	}
}

func diffNeutreeACLGroups(current []*kong.ACLGroup, desired []string) ([]string, []*kong.ACLGroup) {
	desiredSet := make(map[string]struct{}, len(desired))

	for _, group := range desired {
		desiredSet[group] = struct{}{}
	}

	currentSet := make(map[string]*kong.ACLGroup, len(current))

	for _, group := range current {
		if group == nil || group.Group == nil {
			continue
		}

		currentSet[*group.Group] = group
	}

	toCreate := make([]string, 0)

	for _, group := range desired {
		if _, ok := currentSet[group]; !ok {
			toCreate = append(toCreate, group)
		}
	}

	toDelete := make([]*kong.ACLGroup, 0)

	for _, group := range current {
		if group == nil || group.Group == nil || !IsNeutreeACLGroup(*group.Group) {
			continue
		}

		if _, ok := desiredSet[*group.Group]; !ok {
			toDelete = append(toDelete, group)
		}
	}

	sort.Strings(toCreate)
	sort.Slice(toDelete, func(i, j int) bool {
		return *toDelete[i].Group < *toDelete[j].Group
	})

	return toCreate, toDelete
}
