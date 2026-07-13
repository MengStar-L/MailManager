package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"mailmanager/internal/events"
	"mailmanager/internal/repository"
)

func (s *Server) createOperation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AccountID            string   `json:"account_id"`
		Type                 string   `json:"type"`
		Kind                 string   `json:"kind"`
		TargetMessageIDs     []string `json:"target_message_ids"`
		ConversationIDs      []string `json:"conversation_ids"`
		DestinationMailboxID string   `json:"destination_mailbox_id"`
	}
	if !DecodeJSON(w, r, &input) {
		return
	}
	if input.Type == "" {
		input.Type = input.Kind
	}
	targetGroups := map[string][]string{input.AccountID: input.TargetMessageIDs}
	if len(input.ConversationIDs) > 0 {
		var err error
		targetGroups, err = s.repository.OperationTargetsForConversations(r.Context(), input.ConversationIDs)
		if err != nil {
			s.writeRepositoryError(w, r, err)
			return
		}
	}
	var operations []repository.Operation
	for accountID, targetIDs := range targetGroups {
		operation, err := s.repository.CreateOperation(r.Context(), accountID, input.Type, targetIDs, input.DestinationMailboxID)
		if err != nil {
			s.writeRepositoryError(w, r, err)
			return
		}
		operations = append(operations, operation)
		s.events.Publish(events.Event{Type: "operation.updated", ResourceID: operation.ID, State: operation.Status})
	}
	if len(operations) == 0 {
		WriteError(w, r, http.StatusUnprocessableEntity, "invalid_input", "没有可操作的邮件")
		return
	}
	s.runtime.WakeOperations()
	response := operations[0]
	groupID := strings.Join(operationIDs(operations), ",")
	WriteJSON(w, http.StatusAccepted, map[string]any{
		"id": groupID, "kind": input.Type, "status": "pending",
		"conversation_ids": input.ConversationIDs, "undoable_until": response.UndoUntil,
		"operation_ids": operationIDs(operations),
	})
}

func operationIDs(operations []repository.Operation) []string {
	ids := make([]string, len(operations))
	for index, operation := range operations {
		ids[index] = operation.ID
	}
	return ids
}

func (s *Server) undoOperation(w http.ResponseWriter, r *http.Request) {
	operationIDs := strings.Split(chi.URLParam(r, "operationID"), ",")
	var first repository.Operation
	for index, operationID := range operationIDs {
		operation, err := s.repository.UndoOperation(r.Context(), operationID)
		if err != nil {
			s.writeRepositoryError(w, r, err)
			return
		}
		if index == 0 {
			first = operation
		}
		s.events.Publish(events.Event{Type: "operation.updated", ResourceID: operation.ID, State: operation.Status})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"id": chi.URLParam(r, "operationID"), "kind": first.Kind, "status": "undone"})
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.repository.SystemStatus(r.Context())
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, status)
}
