package dashboard

import (
	"fmt"
	"sort"
	"strings"
)

// ExtractReplicaShortID parses a Ray Serve actor name of the form
// "SERVE_REPLICA::<app>#<deployment>#<short_id>" and returns short_id.
// Returns "" when the name does not have at least one '#' separator or
// ends with one.
func ExtractReplicaShortID(actorName string) string {
	idx := strings.LastIndex(actorName, "#")
	if idx < 0 || idx+1 >= len(actorName) {
		return ""
	}

	return actorName[idx+1:]
}

// ReplicaShortIDFromActor returns the short_id parsed from actor.Name,
// falling back to actor.ActorID when the name does not match the Ray
// Serve naming convention.
func ReplicaShortIDFromActor(a *Actor) string {
	if id := ExtractReplicaShortID(a.Name); id != "" {
		return id
	}

	return a.ActorID
}

// ListFailedActorsForDeployment fetches DEAD actors that belong to the
// given Ray Serve <app>:<deployment> via the dashboard state API.
//
// limit=100 is intentional: the goal is to surface the most recent few
// failures, not the full history. A deployment that has flapped more
// than 100 times within Ray's actor-table retention window will trim
// the oldest records, which is acceptable.
func ListFailedActorsForDeployment(svc DashboardService, appName, deploymentName string) ([]Actor, error) {
	className := fmt.Sprintf("ServeReplica:%s:%s", appName, deploymentName)
	resp, err := svc.ListActors([]ActorFilter{
		{Key: "class_name", Predicate: "=", Value: className},
		{Key: "state", Predicate: "=", Value: "DEAD"},
	}, true, 100)

	if err != nil {
		return nil, fmt.Errorf("list actors %s: %w", className, err)
	}

	return resp.Data.Result.Result, nil
}

// FindFailedActorForDeployment returns the most recently started DEAD
// actor for the deployment, or nil if none exist. "Most recent" is
// decided by the actor's start_time (unix ms from GCS ActorTableData);
// ties fall back to actor_id lexicographic order so the result is
// deterministic.
func FindFailedActorForDeployment(svc DashboardService, appName, deploymentName string) (*Actor, error) {
	actors, err := ListFailedActorsForDeployment(svc, appName, deploymentName)
	if err != nil {
		return nil, err
	}

	if len(actors) == 0 {
		return nil, nil
	}

	pick := 0

	for i := 1; i < len(actors); i++ {
		switch {
		case actors[i].StartTime > actors[pick].StartTime:
			pick = i
		case actors[i].StartTime == actors[pick].StartTime && actors[i].ActorID > actors[pick].ActorID:
			pick = i
		}
	}

	a := actors[pick]

	return &a, nil
}

// FindFailedActorByReplicaID returns the DEAD actor whose name encodes
// the requested replica_id, or nil if no DEAD actor matches.
//
// Match priority:
//  1. ExtractReplicaShortID(actor.Name) == replicaID — the canonical
//     Ray Serve naming `SERVE_REPLICA::<app>#<deployment>#<short_id>`.
//  2. actor.ActorID == replicaID — covers the degenerate case where a
//     synthesized replica_id used actor_id (ReplicaShortIDFromActor
//     falls back to actor_id when actor.Name does not match the
//     convention). Without this, the synthesize → click → fetch
//     round-trip would be unresolvable for unconventionally named
//     actors.
func FindFailedActorByReplicaID(svc DashboardService, appName, deploymentName, replicaID string) (*Actor, error) {
	actors, err := ListFailedActorsForDeployment(svc, appName, deploymentName)
	if err != nil {
		return nil, err
	}

	for i := range actors {
		if ExtractReplicaShortID(actors[i].Name) == replicaID || actors[i].ActorID == replicaID {
			return &actors[i], nil
		}
	}

	return nil, nil
}

// LookupFailedActorAcrossDeployments scans the given deployment names
// for a DEAD actor whose name encodes the requested replica_id.
// Returns the first match, or (nil, nil) when no DEAD actor matches in
// any deployment.
//
// Names are sorted before iteration so the search order is
// deterministic across calls — Go map iteration order is randomized,
// which would otherwise make the result depend on map seed when an
// app has multiple deployments (e.g. P/D, prefill+decode).
func LookupFailedActorAcrossDeployments(svc DashboardService, appName string, deploymentNames []string, replicaID string) (*Actor, error) {
	sorted := append([]string(nil), deploymentNames...)
	sort.Strings(sorted)

	for _, deploymentName := range sorted {
		actor, err := FindFailedActorByReplicaID(svc, appName, deploymentName, replicaID)
		if err != nil {
			return nil, err
		}

		if actor != nil {
			return actor, nil
		}
	}

	return nil, nil
}
