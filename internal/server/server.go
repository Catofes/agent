package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"classroom-agent/internal/agent"
	"classroom-agent/internal/config"
	"classroom-agent/internal/store"
)

type authKey struct{}
type principal struct {
	Session store.Session
	Student store.Student
}

type classroomEvent struct {
	Type   string `json:"type"`
	Locked bool   `json:"locked"`
	RunID  string `json:"run_id,omitempty"`
	Target string `json:"-"`
}
type screenState struct {
	Name     string          `json:"name,omitempty"`
	Persona  string          `json:"persona,omitempty"`
	SkillMD  string          `json:"skill_md,omitempty"`
	Tools    []string        `json:"tools,omitempty"`
	MaxTurns int             `json:"max_turns,omitempty"`
	Messages []store.Message `json:"messages,omitempty"`
	Empty    bool            `json:"empty"`
}

type Server struct {
	Config     config.Config
	Store      *store.Store
	Agent      *agent.Engine
	WebFS      fs.FS
	Templates  fs.FS
	Logger     *slog.Logger
	studentHub *Hub[classroomEvent]
	wallHub    *Hub[struct{}]
	screenHub  *Hub[screenState]
	screenMu   sync.RWMutex
	screen     screenState
	shutdown   context.Context
	cancel     context.CancelFunc
	stopOnce   sync.Once
}

func New(cfg config.Config, st *store.Store, engine *agent.Engine, webFS, templates fs.FS, logger *slog.Logger) *Server {
	shutdown, cancel := context.WithCancel(context.Background())
	return &Server{Config: cfg, Store: st, Agent: engine, WebFS: webFS, Templates: templates, Logger: logger, studentHub: NewHub[classroomEvent](), wallHub: NewHub[struct{}](), screenHub: NewHub[screenState](), screen: screenState{Empty: true}, shutdown: shutdown, cancel: cancel}
}

// Shutdown cancels server-owned long-running work before http.Server.Shutdown
// waits for handlers to return. It is safe to call more than once.
func (s *Server) Shutdown() {
	s.stopOnce.Do(s.cancel)
}

func (s *Server) withShutdown(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.shutdown, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, s.logRequest, s.limitBody)
	r.Get("/healthz", s.health)
	r.Get("/", s.page("index.html"))
	r.Get("/teacher", s.page("teacher.html"))
	r.Get("/screen", s.page("screen.html"))
	r.Handle("/assets/*", http.FileServer(http.FS(s.WebFS)))
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", s.login)
		r.Post("/teacher/login", s.teacherLogin)
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)
			r.Get("/me", s.me)
			r.Post("/logout", s.logout)
			r.Group(func(r chi.Router) {
				r.Use(s.requireStudent)
				r.Get("/design", s.getDesign)
				r.Put("/design", s.saveDesign)
				r.Get("/templates", s.listTemplates)
				r.Get("/conversations", s.conversations)
				r.Post("/conversations", s.createConversation)
				r.Get("/messages", s.messages)
				r.Post("/chat", s.chat)
				r.Get("/events", s.studentEvents)
			})
			r.Route("/teacher", func(r chi.Router) {
				r.Use(s.requireTeacher)
				r.Get("/wall", s.wallEvents)
				r.Get("/student/{id}", s.teacherStudent)
				r.Post("/lock", s.lock)
				r.Post("/run", s.createRun)
				r.Post("/spotlight", s.spotlight)
			})
		})
		r.Get("/screen/events", s.screenEvents)
	})
	return r
}

func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(s.WebFS, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	}
}
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.Logger.Info("request", "method", r.Method, "path", r.URL.Path, "request_id", middleware.GetReqID(r.Context()), "duration_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "数据库不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Config.Version})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID          string `json:"id"`
		NameInitial string `json:"name_initial"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.ID = strings.TrimSpace(in.ID)
	run, err := s.Store.ActiveRun(r.Context())
	if err != nil {
		writeError(w, 503, "NO_ACTIVE_RUN", "当前没有可用课堂场次")
		return
	}
	st, err := s.Store.Student(r.Context(), run.ID, in.ID)
	if err != nil {
		writeError(w, 401, "INVALID_LOGIN", "学号或校验信息不正确")
		return
	}
	initial := strings.TrimSpace(in.NameInitial)
	if (s.Config.RequireNameInitial && initial == "") || (initial != "" && !strings.HasPrefix(st.Name, initial)) {
		writeError(w, 401, "INVALID_LOGIN", "学号或校验信息不正确")
		return
	}
	raw, hash, err := newToken()
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "无法创建会话")
		return
	}
	if err = s.Store.CreateSession(r.Context(), store.Session{TokenHash: hash, RunID: run.ID, StudentID: st.ID, ExpiresAt: time.Now().UTC().Add(s.Config.SessionTTL)}); err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "登录失败")
		return
	}
	s.studentHub.Publish(classroomEvent{Type: "session_invalidated", Target: st.ID})
	s.setCookie(w, raw)
	s.wallHub.Publish(struct{}{})
	writeJSON(w, 200, map[string]any{"student": st, "run": run})
}

func (s *Server) teacherLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	a, b := []byte(in.Password), []byte(s.Config.AdminPassword)
	if len(a) != len(b) || subtle.ConstantTimeCompare(a, b) != 1 {
		s.Logger.Warn("teacher login failed", "request_id", middleware.GetReqID(r.Context()))
		writeError(w, 401, "INVALID_LOGIN", "口令不正确")
		return
	}
	raw, hash, err := newToken()
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "无法创建会话")
		return
	}
	if err = s.Store.CreateSession(r.Context(), store.Session{TokenHash: hash, IsTeacher: true, ExpiresAt: time.Now().UTC().Add(s.Config.SessionTTL)}); err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "登录失败")
		return
	}
	s.setCookie(w, raw)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("classroom_session")
		if err != nil {
			writeError(w, 401, "UNAUTHENTICATED", "请先登录")
			return
		}
		hash := hashToken(cookie.Value)
		sess, err := s.Store.Session(r.Context(), hash)
		if err != nil || sess.RevokedAt != nil || time.Now().UTC().After(sess.ExpiresAt) {
			code := "UNAUTHENTICATED"
			msg := "登录已失效，请重新登录"
			if err == nil && sess.RevokedAt != nil {
				code = "SESSION_REPLACED"
				msg = "该学号已在另一台设备登录"
			}
			writeError(w, 401, code, msg)
			return
		}
		p := principal{Session: sess}
		if !sess.IsTeacher {
			run, e := s.Store.Run(r.Context(), sess.RunID)
			if e != nil || run.Status != "active" {
				writeError(w, 401, "RUN_ENDED", "课堂场次已结束，请重新登录")
				return
			}
			p.Student, e = s.Store.Student(r.Context(), sess.RunID, sess.StudentID)
			if e != nil {
				writeError(w, 401, "UNAUTHENTICATED", "学生信息不存在")
				return
			}
		}
		_ = s.Store.TouchSession(r.Context(), hash)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKey{}, p)))
	})
}
func (s *Server) requireStudent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalOf(r).Session.IsTeacher {
			writeError(w, 403, "FORBIDDEN", "该接口仅供学生使用")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) requireTeacher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principalOf(r).Session.IsTeacher {
			writeError(w, 403, "FORBIDDEN", "该接口仅供教师使用")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func principalOf(r *http.Request) principal {
	p, _ := r.Context().Value(authKey{}).(principal)
	return p
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("classroom_session")
	if c != nil {
		_ = s.Store.RevokeSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "classroom_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Config.CookieSecure})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.Session.IsTeacher {
		writeJSON(w, http.StatusOK, map[string]any{"role": "teacher"})
		return
	}
	run, err := s.Store.Run(r.Context(), p.Session.RunID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "RUN_ENDED", "课堂场次已结束")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": "student", "student": p.Student, "run": run})
}
func (s *Server) setCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{Name: "classroom_session", Value: raw, Path: "/", MaxAge: int(s.Config.SessionTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Config.CookieSecure})
}

func newToken() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, 400, "INVALID_JSON", "请求格式不正确")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func runeLen(v string) int { return utf8.RuneCountInString(v) }
func parseCursor(v string) int64 {
	n, _ := strconv.ParseInt(v, 10, 64)
	if n < 0 {
		return 0
	}
	return n
}
func safeFileBase(v string) bool { return v == path.Base(v) && !strings.Contains(v, "..") }
