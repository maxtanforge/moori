package localinv

import (
	"encoding/json"
	"fmt"
)

// MaxSnapshotBytes reserves space for the agentwire envelope.
const MaxSnapshotBytes = 768 << 10

// Bound keeps aggregate counts and hosts with large Docker installations
// reportable even when one individual container or tag is unusually large.
func (o *Overview) Bound() {
	o.Truncated = len(o.Containers) < o.ContainerCount || len(o.Images) < o.ImageCount || len(o.Networks) < o.NetworkCount || len(o.Volumes) < o.VolumeCount
	lists := []budgetList{
		listBudget(&o.Containers), listBudget(&o.Images), listBudget(&o.Networks), listBudget(&o.Volumes),
		listBudget(&o.Filesystems), listBudget(&o.ComposeProjects),
	}
	if o.Host != nil {
		host := *o.Host
		o.Host = &host
		lists = append(lists, listBudget(&host.Interfaces))
	}
	removed := 0
	for {
		data, err := json.Marshal(o)
		if err == nil && len(data) <= MaxSnapshotBytes-256 {
			break
		}
		index, largest := -1, 0
		for j, l := range lists {
			if size := l.size(); size > largest {
				index, largest = j, size
			}
		}
		if index < 0 {
			// A malformed scalar can exceed the budget even without any lists.
			o.Host = nil
			o.Engine = nil
			o.Message = ""
			o.Warning = "资源详情超过传输限制"
			o.SectionTimes = nil
			o.Truncated = true
			break
		}
		removed += lists[index].trim(largest / 2)
		o.Truncated = true
	}
	if removed > 0 {
		o.warn(fmt.Sprintf("快照达到字节上限，已省略 %d 条明细", removed))
	}
}

type budgetList struct {
	size func() int
	trim func(int) int
}

func listBudget[T any](rows *[]T) budgetList {
	return budgetList{
		size: func() int {
			if len(*rows) == 0 {
				return 0
			}
			data, _ := json.Marshal(*rows)
			return len(data)
		},
		trim: func(bytesLeft int) int {
			var kept []T
			for _, row := range *rows {
				encoded, err := json.Marshal(row)
				if err != nil || len(encoded)+1 > bytesLeft {
					continue
				}
				kept = append(kept, row)
				bytesLeft -= len(encoded) + 1
			}
			removed := len(*rows) - len(kept)
			*rows = kept
			return removed
		},
	}
}
