package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"mailmanager/internal/updater"
)

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.updater.Status(r.Context())
	if err != nil {
		WriteError(w, r, http.StatusInternalServerError, "update_status_failed", "无法读取更新状态")
		return
	}
	WriteJSON(w, http.StatusOK, status)
}

func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	status, err := s.updater.Check(r.Context())
	if err != nil {
		s.logger.Warn("check software update", "error", err)
		WriteError(w, r, http.StatusBadGateway, "update_check_failed", "无法连接 GitHub 检查更新")
		return
	}
	WriteJSON(w, http.StatusOK, status)
}

func (s *Server) installUpdate(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version string `json:"version"`
	}
	if !DecodeJSON(w, r, &request) {
		return
	}
	request.Version = strings.TrimSpace(request.Version)
	if request.Version == "" {
		WriteError(w, r, http.StatusUnprocessableEntity, "update_version_invalid", "请选择要安装的版本")
		return
	}
	status, err := s.updater.Install(r.Context(), request.Version)
	if err == nil {
		WriteJSON(w, http.StatusAccepted, status)
		return
	}
	switch {
	case errors.Is(err, updater.ErrConflict):
		WriteError(w, r, http.StatusConflict, "update_in_progress", "已有更新任务正在执行")
	case errors.Is(err, updater.ErrUnsupported):
		WriteError(w, r, http.StatusConflict, "update_unsupported", "当前部署不支持网页自动更新")
	case errors.Is(err, updater.ErrVersion):
		WriteError(w, r, http.StatusUnprocessableEntity, "update_version_invalid", "目标版本不是当前最新的稳定版本")
	default:
		s.logger.Error("queue software update", "error", err)
		WriteError(w, r, http.StatusInternalServerError, "update_install_failed", "无法创建更新任务")
	}
}
