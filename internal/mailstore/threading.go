package mailstore

import (
	"errors"
	"sort"
	"strings"
)

type ThreadMessage struct {
	LocalID    string
	AccountID  string
	MessageID  string
	InReplyTo  []string
	References []string
}

type ThreadGroup struct {
	AccountID string
	LocalIDs  []string
}

// GroupThreads associates messages only through RFC message identifiers found
// within the same account. It deliberately does not use subject matching.
func GroupThreads(messages []ThreadMessage) ([]ThreadGroup, error) {
	parents := make([]int, len(messages))
	seenLocal := make(map[string]struct{}, len(messages))
	byAccountMessageID := make(map[string]int, len(messages))
	for index, msg := range messages {
		if strings.TrimSpace(msg.LocalID) == "" || strings.TrimSpace(msg.AccountID) == "" {
			return nil, errors.New("local ID and account ID are required")
		}
		if _, duplicate := seenLocal[msg.LocalID]; duplicate {
			return nil, errors.New("local message IDs must be unique")
		}
		seenLocal[msg.LocalID] = struct{}{}
		parents[index] = index
		if id := normalizeMessageID(msg.MessageID); id != "" {
			key := scopedMessageID(msg.AccountID, id)
			if previous, ok := byAccountMessageID[key]; ok {
				union(parents, index, previous)
			} else {
				byAccountMessageID[key] = index
			}
		}
	}
	for index, msg := range messages {
		for _, id := range append(append([]string(nil), msg.References...), msg.InReplyTo...) {
			if target, ok := byAccountMessageID[scopedMessageID(msg.AccountID, normalizeMessageID(id))]; ok {
				union(parents, index, target)
			}
		}
	}
	groups := make(map[int]*ThreadGroup)
	for index, msg := range messages {
		root := find(parents, index)
		group := groups[root]
		if group == nil {
			group = &ThreadGroup{AccountID: msg.AccountID}
			groups[root] = group
		}
		group.LocalIDs = append(group.LocalIDs, msg.LocalID)
	}
	result := make([]ThreadGroup, 0, len(groups))
	for _, group := range groups {
		sort.Strings(group.LocalIDs)
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AccountID != result[j].AccountID {
			return result[i].AccountID < result[j].AccountID
		}
		return result[i].LocalIDs[0] < result[j].LocalIDs[0]
	})
	return result, nil
}

func normalizeMessageID(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "<>"))
}

func scopedMessageID(accountID, messageID string) string {
	if messageID == "" {
		return ""
	}
	return accountID + "\x00" + messageID
}

func find(parents []int, value int) int {
	for parents[value] != value {
		parents[value] = parents[parents[value]]
		value = parents[value]
	}
	return value
}

func union(parents []int, left, right int) {
	leftRoot, rightRoot := find(parents, left), find(parents, right)
	if leftRoot != rightRoot {
		parents[rightRoot] = leftRoot
	}
}
