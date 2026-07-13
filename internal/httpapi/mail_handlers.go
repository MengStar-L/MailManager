package httpapi

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"mailmanager/internal/connectors"
	"mailmanager/internal/repository"
	"mailmanager/internal/search"
)

func (s *Server) listMailboxes(w http.ResponseWriter, r *http.Request) {
	items, err := s.repository.ListMailboxes(r.Context(), r.URL.Query().Get("account_id"))
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListResponse[repository.MailboxSummary]{Items: items})
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	limit, err := PageSize(r)
	if err != nil {
		WriteError(w, r, http.StatusBadRequest, "invalid_limit", "每页数量必须在 1 到 100 之间")
		return
	}
	cursor, err := DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "分页位置无效，请刷新列表")
		return
	}
	searchValue := r.URL.Query().Get("q")
	if searchValue == "" {
		searchValue = r.URL.Query().Get("query")
	}
	mailboxRole := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mailbox")))
	query := repository.ConversationQuery{
		AccountID: r.URL.Query().Get("account_id"), MailboxID: r.URL.Query().Get("mailbox_id"),
		SearchExpression: search.MatchQuery(searchValue), UnreadOnly: queryBool(r, "unread"),
		StarredOnly: queryBool(r, "starred") || mailboxRole == "starred", InboxOnly: mailboxRole == "" || mailboxRole == "inbox",
		Before: cursor.Timestamp, BeforeID: cursor.ID, Limit: limit,
	}
	if mailboxRole != "" && mailboxRole != "inbox" && mailboxRole != "starred" && mailboxRole != "custom" {
		query.MailboxRole = mapMailboxRole(mailboxRole)
		query.InboxOnly = false
	}
	rawAttachment := r.URL.Query().Get("has_attachment")
	if rawAttachment == "" {
		rawAttachment = r.URL.Query().Get("has_attachments")
	}
	if raw := rawAttachment; raw != "" {
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			WriteError(w, r, http.StatusBadRequest, "invalid_filter", "附件筛选值无效")
			return
		}
		query.HasAttachments = &value
	}
	items, next, err := s.repository.ListConversations(r.Context(), query)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	response := ListResponse[repository.ConversationSummary]{Items: items}
	if next != nil {
		response.NextCursor = EncodeCursor(Cursor{Timestamp: next.Timestamp, ID: next.ID})
	}
	WriteJSON(w, http.StatusOK, response)
}

func mapMailboxRole(role string) string {
	if role == "spam" {
		return "junk"
	}
	return role
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	detail, err := s.repository.GetConversation(r.Context(), chi.URLParam(r, "conversationID"))
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	for index := range detail.Messages {
		detail.Messages[index].BodyHTML = ""
		detail.Messages[index].BodyText = ""
	}
	WriteJSON(w, http.StatusOK, detail)
}

func (s *Server) getMessageBody(w http.ResponseWriter, r *http.Request) {
	message, err := s.repository.GetMessage(r.Context(), chi.URLParam(r, "messageID"))
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	allowRemote := r.URL.Query().Get("remote_images") != "block"
	rendered, err := connectors.RenderRemoteImages(message.BodyHTML, allowRemote)
	if err != nil {
		WriteError(w, r, http.StatusInternalServerError, "body_render_failed", "邮件正文暂时无法显示")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"html": rendered, "plain_text": message.BodyText,
		"remote_images_blocked": message.RemoteImagesBlocked && !allowRemote,
	})
}

func (s *Server) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	record, err := s.repository.GetAttachment(r.Context(), chi.URLParam(r, "attachmentID"))
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	path := record.CachePath
	if path == "" {
		path, err = s.runtime.LoadAttachment(r.Context(), record)
		if err != nil {
			WriteError(w, r, http.StatusBadGateway, "attachment_unavailable", "暂时无法从邮箱服务器下载该附件")
			return
		}
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		WriteError(w, r, http.StatusNotFound, "attachment_missing", "附件缓存不存在，请重试")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		WriteError(w, r, http.StatusNotFound, "attachment_missing", "附件缓存不存在，请重试")
		return
	}
	contentType := record.ContentType
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		contentType = "application/octet-stream"
	}
	filename := connectors.SanitizeFilename(record.Filename)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", percentEncodeFilename(filename)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

func queryBool(r *http.Request, name string) bool {
	raw := r.URL.Query().Get(name)
	if raw == "allow" {
		return true
	}
	if raw == "block" {
		return false
	}
	value, _ := strconv.ParseBool(raw)
	return value
}

func percentEncodeFilename(value string) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || strings.ContainsRune("!#$&+-.^_`|~", rune(b)) {
			builder.WriteByte(b)
		} else {
			fmt.Fprintf(&builder, "%%%02X", b)
		}
	}
	return builder.String()
}

func (s *Server) writeRepositoryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		WriteError(w, r, http.StatusNotFound, "not_found", "请求的内容不存在")
	case errors.Is(err, repository.ErrConflict):
		WriteError(w, r, http.StatusConflict, "conflict", "内容已在其他页面发生变化，请刷新后重试")
	case errors.Is(err, repository.ErrInvalid):
		WriteError(w, r, http.StatusUnprocessableEntity, "invalid_input", "请求内容不符合要求")
	default:
		s.logger.Error("repository request failed", "request_id", RequestIDFromContext(r.Context()), "path", r.URL.Path, "error", err)
		WriteError(w, r, http.StatusInternalServerError, "internal_error", "服务暂时无法处理该请求")
	}
}
