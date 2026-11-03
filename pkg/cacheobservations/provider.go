// Package cacheobservations defines optional cache-provider monitoring queries.
package cacheobservations

import "time"

type Scope struct {
	Workspace, Cluster, Endpoint, Window string
	Nodes                                []string
}

type Sample struct {
	Metric     string            `json:"metric"`
	Labels     map[string]string `json:"labels"`
	Value      float64           `json:"value"`
	ObservedAt float64           `json:"observed_at"`
}

type Query struct {
	Expression string
	Decode     func([]byte, time.Time) ([]Sample, error)
}

type ResourceProvider interface{ ResourceQueries(Scope) []Query }
type PerformanceProvider interface{ PerformanceQueries(Scope) []string }
