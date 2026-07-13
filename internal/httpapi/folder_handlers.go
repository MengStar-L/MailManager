package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) updateFolderRole(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Role string `json:"role"`
	}
	if !DecodeJSON(w, r, &request) {
		return
	}
	mailbox, err := s.repository.UpdateFolderRole(r.Context(), chi.URLParam(r, "folderID"), request.Role)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, mailbox)
}
