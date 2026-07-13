package httpapi

import (
	"crypto/sha256"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"mailmanager/internal/connectors"
	"mailmanager/internal/events"
	"mailmanager/internal/id"
	"mailmanager/internal/repository"
)

type draftRequest struct {
	AccountID        string               `json:"account_id"`
	IdentityID       string               `json:"identity_id"`
	ReplyToMessageID string               `json:"reply_to_message_id"`
	ForwardMessageID string               `json:"forward_message_id"`
	To               []repository.Address `json:"to"`
	CC               []repository.Address `json:"cc"`
	BCC              []repository.Address `json:"bcc"`
	Subject          string               `json:"subject"`
	BodyText         string               `json:"body_text"`
	BodyHTML         string               `json:"body_html"`
	Version          int                  `json:"version"`
}

func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) {
	items, err := s.repository.ListDrafts(r.Context())
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListResponse[repository.Draft]{Items: items})
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	draft, err := s.repository.GetDraft(r.Context(), chi.URLParam(r, "draftID"))
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, draft)
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	var request draftRequest
	if !DecodeJSON(w, r, &request) {
		return
	}
	input, ok := s.sanitizedDraftInput(w, r, request)
	if !ok {
		return
	}
	draft, err := s.repository.CreateDraft(r.Context(), input)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	s.events.Publish(events.Event{Type: "draft.updated", ResourceID: draft.ID, Version: int64(draft.Version)})
	WriteJSON(w, http.StatusCreated, draft)
}

func (s *Server) updateDraft(w http.ResponseWriter, r *http.Request) {
	var request draftRequest
	if !DecodeJSON(w, r, &request) {
		return
	}
	if request.Version == 0 || request.IdentityID == "" {
		existing, err := s.repository.GetDraft(r.Context(), chi.URLParam(r, "draftID"))
		if err != nil {
			s.writeRepositoryError(w, r, err)
			return
		}
		if request.Version == 0 {
			request.Version = existing.Version
		}
		if request.IdentityID == "" {
			request.IdentityID = existing.IdentityID
		}
	}
	input, ok := s.sanitizedDraftInput(w, r, request)
	if !ok {
		return
	}
	draft, err := s.repository.UpdateDraft(r.Context(), chi.URLParam(r, "draftID"), input)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	s.events.Publish(events.Event{Type: "draft.updated", ResourceID: draft.ID, Version: int64(draft.Version)})
	WriteJSON(w, http.StatusOK, draft)
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	draftID := chi.URLParam(r, "draftID")
	attachments, err := s.repository.DraftAttachmentRecords(r.Context(), draftID)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	if err := s.repository.DeleteDraft(r.Context(), draftID); err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	for _, attachment := range attachments {
		_ = s.removeDraftBlob(attachment.StoragePath)
	}
	s.events.Publish(events.Event{Type: "draft.deleted", ResourceID: draftID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addDraftAttachment(w http.ResponseWriter, r *http.Request) {
	draftID := chi.URLParam(r, "draftID")
	if _, err := s.repository.GetDraft(r.Context(), draftID); err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxAttachmentBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		WriteError(w, r, http.StatusRequestEntityTooLarge, "attachment_too_large", "附件超过允许的大小")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		WriteError(w, r, http.StatusBadRequest, "attachment_missing", "请选择要上传的附件")
		return
	}
	defer file.Close()
	directory := filepath.Join(s.draftBlobDir, draftID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_ = temporary.Chmod(0o600)
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(file, s.maxAttachmentBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		s.writeServiceError(w, r, firstError(copyErr, closeErr))
		return
	}
	if written > s.maxAttachmentBytes {
		WriteError(w, r, http.StatusRequestEntityTooLarge, "attachment_too_large", "附件超过允许的大小")
		return
	}
	draft, err := s.repository.GetDraft(r.Context(), draftID)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	total := written
	for _, existing := range draft.Attachments {
		total += existing.SizeBytes
	}
	if total > s.maxAttachmentBytes {
		WriteError(w, r, http.StatusRequestEntityTooLarge, "message_too_large", "全部附件总大小超过允许的限制")
		return
	}
	finalPath := filepath.Join(directory, id.New()+".blob")
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	filename := connectors.SanitizeFilename(header.Filename)
	contentType := header.Header.Get("Content-Type")
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = parsed
	} else {
		contentType = "application/octet-stream"
	}
	attachment, err := s.repository.AddDraftAttachment(r.Context(), draftID, filename, contentType, finalPath, written, digest.Sum(nil))
	if err != nil {
		_ = os.Remove(finalPath)
		s.writeRepositoryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, attachment)
}

func (s *Server) deleteDraftAttachment(w http.ResponseWriter, r *http.Request) {
	path, err := s.repository.DeleteDraftAttachment(r.Context(), chi.URLParam(r, "draftID"), chi.URLParam(r, "attachmentID"))
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	if err := s.removeDraftBlob(path); err != nil {
		s.logger.Warn("remove draft attachment", "request_id", RequestIDFromContext(r.Context()), "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeDraftBlob(path string) error {
	base, err := filepath.Abs(s.draftBlobDir)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return os.ErrPermission
	}
	return os.Remove(target)
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (s *Server) sendDraft(w http.ResponseWriter, r *http.Request) {
	draftID := chi.URLParam(r, "draftID")
	outboxID, err := s.repository.QueueDraft(r.Context(), draftID)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	s.runtime.WakeOutbox()
	s.events.Publish(events.Event{Type: "outbox.updated", ResourceID: outboxID, State: "queued"})
	WriteJSON(w, http.StatusAccepted, map[string]string{"id": outboxID, "state": "queued"})
}

func (s *Server) getOutboxStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.repository.GetOutboxStatus(r.Context(), chi.URLParam(r, "outboxID"))
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, status)
}

func (s *Server) sanitizedDraftInput(w http.ResponseWriter, r *http.Request, request draftRequest) (repository.DraftInput, bool) {
	if request.IdentityID == "" {
		identityID, err := s.repository.PrimaryIdentityID(r.Context(), request.AccountID)
		if err != nil {
			s.writeRepositoryError(w, r, err)
			return repository.DraftInput{}, false
		}
		request.IdentityID = identityID
	}
	clean, err := connectors.SanitizeHTML(request.BodyHTML, false)
	if err != nil {
		WriteError(w, r, http.StatusUnprocessableEntity, "body_invalid", "邮件正文无法解析")
		return repository.DraftInput{}, false
	}
	return repository.DraftInput{
		AccountID: request.AccountID, IdentityID: request.IdentityID,
		ReplyToMessageID: request.ReplyToMessageID, ForwardMessageID: request.ForwardMessageID,
		To: request.To, CC: request.CC, BCC: request.BCC, Subject: request.Subject,
		BodyText: request.BodyText, BodyHTML: clean.HTML, Version: request.Version,
	}, true
}
