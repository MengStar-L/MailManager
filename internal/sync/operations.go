package sync

type MoveStrategy string

const (
	MoveDirect            MoveStrategy = "uid_move"
	MoveCopyDeleteUIDPlus MoveStrategy = "copy_delete_uidplus"
	MoveNeedsAttention    MoveStrategy = "needs_attention"
)

type MoveCapabilities struct {
	Move    bool
	UIDPlus bool
}

type MovePlan struct {
	Strategy            MoveStrategy
	UseSelectiveExpunge bool
	Reason              string
}

func PlanMove(capabilities MoveCapabilities) MovePlan {
	if capabilities.Move {
		return MovePlan{Strategy: MoveDirect}
	}
	if capabilities.UIDPlus {
		return MovePlan{Strategy: MoveCopyDeleteUIDPlus, UseSelectiveExpunge: true}
	}
	return MovePlan{
		Strategy: MoveNeedsAttention,
		Reason:   "server supports neither UID MOVE nor safe UIDPLUS selective expunge",
	}
}
