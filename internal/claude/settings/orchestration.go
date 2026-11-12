package settings

type Orchestration struct {
	IsolatedWorkers *bool `json:"isolatedWorkers,omitempty"`
	SnapshotDirty   *bool `json:"snapshotDirty,omitempty"`
	MaxConcurrent   *int  `json:"maxConcurrent,omitempty"`
	MaxDispatches   *int  `json:"maxDispatches,omitempty"`
	MaxDepth        *int  `json:"maxDepth,omitempty"`
	TimeoutSeconds  *int  `json:"timeoutSeconds,omitempty"`
}

func (o *Orchestration) merge(next Orchestration) {
	if next.IsolatedWorkers != nil {
		v := *next.IsolatedWorkers
		o.IsolatedWorkers = &v
	}
	if next.SnapshotDirty != nil {
		v := *next.SnapshotDirty
		o.SnapshotDirty = &v
	}
	for _, p := range []struct {
		dst **int
		src *int
		max int
	}{{&o.MaxConcurrent, next.MaxConcurrent, 64}, {&o.MaxDispatches, next.MaxDispatches, 10000}, {&o.MaxDepth, next.MaxDepth, 8}, {&o.TimeoutSeconds, next.TimeoutSeconds, 86400}} {
		if p.src != nil && *p.src > 0 && *p.src <= p.max {
			v := *p.src
			*p.dst = &v
		}
	}
}

func PositiveOr(value *int, fallback int) int {
	if value != nil && *value > 0 {
		return *value
	}
	return fallback
}
