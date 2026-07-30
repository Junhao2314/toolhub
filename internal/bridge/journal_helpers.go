package bridge

import (
	"sort"
	"strings"

	"github.com/Junhao2314/toolhub/internal/bridgeprotocol"
)

func sortBackups(items []bridgeprotocol.Backup) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return strings.Compare(items[i].ID, items[j].ID) > 0
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
}
