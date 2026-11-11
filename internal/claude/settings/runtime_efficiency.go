package settings

// RuntimeEfficiency controls optional local processing reuse and repeated-error
// limits. Pointer fields preserve explicit false/zero overrides across scopes.
type RuntimeEfficiency struct {
	ReadCache        *bool `json:"readCache,omitempty"`
	ReadCacheEntries *int  `json:"readCacheEntries,omitempty"`
	ReadCacheBytes   *int  `json:"readCacheBytes,omitempty"`
	CycleLimit       *int  `json:"cycleLimit,omitempty"`
	ProgressLimit    *int  `json:"progressLimit,omitempty"`
}

func (r *RuntimeEfficiency) merge(next RuntimeEfficiency) {
	if next.ReadCache != nil {
		v := *next.ReadCache
		r.ReadCache = &v
	}
	for _, field := range []struct {
		dst      **int
		src      *int
		min, max int
	}{
		{&r.ReadCacheEntries, next.ReadCacheEntries, 1, 256},
		{&r.ReadCacheBytes, next.ReadCacheBytes, 1024, 64 << 20},
	} {
		if field.src != nil && *field.src >= field.min && *field.src <= field.max {
			v := *field.src
			*field.dst = &v
		}
	}
	if n := next.ProgressLimit; n != nil && (*n == 0 || (*n >= 2 && *n <= 20)) {
		v := *n
		r.ProgressLimit = &v
	}
	if n := next.CycleLimit; n != nil && (*n == 0 || (*n >= 2 && *n <= 10)) {
		v := *n
		r.CycleLimit = &v
	}
}
