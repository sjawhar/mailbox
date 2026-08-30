package cli

import (
	"fmt"
	"strings"

	"github.com/sjawhar/mailbox/internal/filter"
)

// resolveFilter maps --filter to a compiled config filter. Callers reach it
// only after loadConfig has succeeded (via start, startWrite, or loadConfig).
func (cc *cmdCtx) resolveFilter() (*filter.Filter, error) {
	if cc.filterFlag == "" {
		return nil, nil
	}
	if f, ok := cc.cfg.Filter(cc.filterFlag); ok {
		return f, nil
	}
	if names := cc.cfg.FilterNames(); len(names) > 0 {
		return nil, fmt.Errorf("unknown filter %q; defined filters: %s", cc.filterFlag, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("unknown filter %q; no filters are defined (config: %s)", cc.filterFlag, cc.cfg.DisplayPath())
}

func filterName(f *filter.Filter) string {
	if f == nil {
		return ""
	}
	return f.Name
}
