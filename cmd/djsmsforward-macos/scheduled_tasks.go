package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	scheduledTaskStoreVersion = 1
	maxScheduledTaskHistory   = 100
)

var (
	errScheduledTaskNotFound = errors.New("scheduled task not found")
	errScheduledTaskRunning  = errors.New("scheduled task is running")
)

type scheduledTaskValidationError struct {
	message string
}

func (e *scheduledTaskValidationError) Error() string {
	return e.message
}

func invalidScheduledTask(message string) error {
	return &scheduledTaskValidationError{message: message}
}

type scheduledTask struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	IntervalDays  int    `json:"interval_days"`
	RunTime       string `json:"run_time"`
	PhoneNumber   string `json:"phone_number"`
	Message       string `json:"message"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	LastRunAt     int64  `json:"last_run_at,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastSegments  int    `json:"last_segments,omitempty"`
	NextRunAt     int64  `json:"next_run_at,omitempty"`
}

type scheduledTaskView struct {
	scheduledTask
	Running bool `json:"running"`
}

type scheduledTaskRun struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	TaskName  string `json:"task_name"`
	Trigger   string `json:"trigger"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
	Status    string `json:"status"`
	Segments  int    `json:"segments,omitempty"`
	Error     string `json:"error,omitempty"`
}

type scheduledTaskInput struct {
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	IntervalDays int    `json:"interval_days"`
	RunTime      string `json:"run_time"`
	PhoneNumber  string `json:"phone_number"`
	Message      string `json:"message"`
}

type scheduledTaskStore struct {
	Version int                `json:"version"`
	Tasks   []scheduledTask    `json:"tasks"`
	History []scheduledTaskRun `json:"history"`
}

type scheduledTaskSnapshot struct {
	Tasks   []scheduledTaskView `json:"tasks"`
	History []scheduledTaskRun  `json:"history"`
}

type scheduledTaskService struct {
	mu         sync.Mutex
	tasks      map[string]*scheduledTask
	history    []scheduledTaskRun
	running    map[string]bool
	path       string
	executeSMS func(phone, message string) (int, error)
	notify     func(notificationEvent)
	startOnce  sync.Once
}

func scheduledTasksFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "DJSMSForward", "scheduled-tasks.json"), nil
}

func (a *app) initScheduledTasks() {
	if a.scheduledTasks != nil {
		return
	}
	path, err := scheduledTasksFile()
	if err != nil {
		log.Printf("scheduled tasks path unavailable: %v", err)
	}
	executeSMS := a.sendTextSMS
	if a.demo {
		path = ""
		executeSMS = func(_, _ string) (int, error) { return 1, nil }
	}
	notify := func(event notificationEvent) {
		if a.notifications != nil {
			a.notifications.submit(event)
		}
	}
	service, loadErr := newScheduledTaskService(path, executeSMS, notify)
	a.scheduledTasks = service
	if loadErr != nil {
		log.Printf("scheduled tasks could not be loaded; using an empty task list: %v", loadErr)
	}
}

func newScheduledTaskService(path string, executeSMS func(string, string) (int, error), notify func(notificationEvent)) (*scheduledTaskService, error) {
	service := &scheduledTaskService{
		tasks:      make(map[string]*scheduledTask),
		running:    make(map[string]bool),
		path:       path,
		executeSMS: executeSMS,
		notify:     notify,
	}
	if strings.TrimSpace(path) == "" {
		return service, nil
	}
	if err := service.load(); err != nil {
		return service, err
	}
	return service, nil
}

func (s *scheduledTaskService) load() error {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect scheduled tasks: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("scheduled tasks file must be a regular file")
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("secure scheduled tasks directory: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("secure scheduled tasks file: %w", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read scheduled tasks: %w", err)
	}
	var stored scheduledTaskStore
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode scheduled tasks: %w", err)
	}
	if stored.Version != scheduledTaskStoreVersion {
		return fmt.Errorf("unsupported scheduled tasks version %d", stored.Version)
	}
	now := time.Now()
	for i := range stored.Tasks {
		task := stored.Tasks[i]
		if task.ID == "" {
			return errors.New("scheduled task has an empty ID")
		}
		if _, exists := s.tasks[task.ID]; exists {
			return fmt.Errorf("duplicate scheduled task ID %q", task.ID)
		}
		if err := validateScheduledTaskInput(task.input()); err != nil {
			return fmt.Errorf("invalid scheduled task %q: %w", task.ID, err)
		}
		if task.LastRunStatus == "running" {
			task.LastRunStatus = "failed"
			task.LastError = "服务停止时任务仍在执行"
			task.NextRunAt = nextTaskRun(now, task.RunTime, 1).UnixMilli()
		}
		if task.Enabled && task.NextRunAt <= 0 {
			task.NextRunAt = nextTaskRun(now, task.RunTime, 0).UnixMilli()
		}
		if !task.Enabled {
			task.NextRunAt = 0
		}
		s.tasks[task.ID] = &task
	}
	s.history = append([]scheduledTaskRun(nil), stored.History...)
	if len(s.history) > maxScheduledTaskHistory {
		s.history = s.history[:maxScheduledTaskHistory]
	}
	return nil
}

func (task scheduledTask) input() scheduledTaskInput {
	return scheduledTaskInput{
		Name:         task.Name,
		Enabled:      task.Enabled,
		IntervalDays: task.IntervalDays,
		RunTime:      task.RunTime,
		PhoneNumber:  task.PhoneNumber,
		Message:      task.Message,
	}
}

func (s *scheduledTaskService) persistLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create scheduled tasks directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("secure scheduled tasks directory: %w", err)
	}
	if info, err := os.Lstat(s.path); err == nil && !info.Mode().IsRegular() {
		return errors.New("scheduled tasks file must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect scheduled tasks file: %w", err)
	}
	store := scheduledTaskStore{
		Version: scheduledTaskStoreVersion,
		Tasks:   s.taskRecordsLocked(),
		History: append([]scheduledTaskRun(nil), s.history...),
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".scheduled-tasks-*.tmp")
	if err != nil {
		return fmt.Errorf("create scheduled tasks temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure scheduled tasks temporary file: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(store); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode scheduled tasks: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync scheduled tasks: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close scheduled tasks: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace scheduled tasks: %w", err)
	}
	return os.Chmod(s.path, 0o600)
}

func (s *scheduledTaskService) taskRecordsLocked() []scheduledTask {
	tasks := make([]scheduledTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, *task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt == tasks[j].CreatedAt {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt > tasks[j].CreatedAt
	})
	return tasks
}

func (s *scheduledTaskService) snapshot() scheduledTaskSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.taskRecordsLocked()
	views := make([]scheduledTaskView, 0, len(records))
	for _, task := range records {
		views = append(views, scheduledTaskView{scheduledTask: task, Running: s.running[task.ID]})
	}
	return scheduledTaskSnapshot{
		Tasks:   views,
		History: append([]scheduledTaskRun(nil), s.history...),
	}
}

func validateScheduledTaskInput(input scheduledTaskInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.PhoneNumber = strings.TrimSpace(input.PhoneNumber)
	input.Message = strings.TrimSpace(input.Message)
	input.RunTime = strings.TrimSpace(input.RunTime)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 80 {
		return invalidScheduledTask("任务名称必须为 1 到 80 个字符")
	}
	if input.IntervalDays < 1 || input.IntervalDays > 3650 {
		return invalidScheduledTask("执行间隔必须为 1 到 3650 天")
	}
	if _, _, err := parseTaskRunTime(input.RunTime); err != nil {
		return err
	}
	if input.PhoneNumber == "" || utf8.RuneCountInString(input.PhoneNumber) > 40 {
		return invalidScheduledTask("目标号码必须为 1 到 40 个字符")
	}
	for _, r := range input.PhoneNumber {
		if (r < '0' || r > '9') && r != '+' {
			return invalidScheduledTask("目标号码只能包含数字和开头的加号")
		}
	}
	if strings.Count(input.PhoneNumber, "+") > 1 || (strings.Contains(input.PhoneNumber, "+") && !strings.HasPrefix(input.PhoneNumber, "+")) {
		return invalidScheduledTask("目标号码中的加号只能出现在开头")
	}
	if strings.TrimPrefix(input.PhoneNumber, "+") == "" {
		return invalidScheduledTask("目标号码必须包含数字")
	}
	if input.Message == "" || utf8.RuneCountInString(input.Message) > 1000 {
		return invalidScheduledTask("短信内容必须为 1 到 1000 个字符")
	}
	return nil
}

func normalizeScheduledTaskInput(input scheduledTaskInput) scheduledTaskInput {
	input.Name = strings.TrimSpace(input.Name)
	input.PhoneNumber = strings.TrimSpace(input.PhoneNumber)
	input.Message = strings.TrimSpace(input.Message)
	input.RunTime = strings.TrimSpace(input.RunTime)
	return input
}

func parseTaskRunTime(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, 0, invalidScheduledTask("执行时间必须使用 HH:MM 格式")
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func nextTaskRun(now time.Time, runTime string, daysAfter int) time.Time {
	hour, minute, _ := parseTaskRunTime(runTime)
	candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if daysAfter > 0 {
		candidate = candidate.AddDate(0, 0, daysAfter)
	}
	for !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

func newScheduledTaskID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func (s *scheduledTaskService) create(input scheduledTaskInput, now time.Time) (scheduledTaskView, error) {
	input = normalizeScheduledTaskInput(input)
	if err := validateScheduledTaskInput(input); err != nil {
		return scheduledTaskView{}, err
	}
	id, err := newScheduledTaskID()
	if err != nil {
		return scheduledTaskView{}, fmt.Errorf("create task ID: %w", err)
	}
	task := &scheduledTask{
		ID:           id,
		Name:         input.Name,
		Enabled:      input.Enabled,
		IntervalDays: input.IntervalDays,
		RunTime:      input.RunTime,
		PhoneNumber:  input.PhoneNumber,
		Message:      input.Message,
		CreatedAt:    now.UnixMilli(),
		UpdatedAt:    now.UnixMilli(),
	}
	if task.Enabled {
		task.NextRunAt = nextTaskRun(now, task.RunTime, 0).UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
	if err := s.persistLocked(); err != nil {
		delete(s.tasks, task.ID)
		return scheduledTaskView{}, err
	}
	return scheduledTaskView{scheduledTask: *task}, nil
}

func (s *scheduledTaskService) update(id string, input scheduledTaskInput, now time.Time) (scheduledTaskView, error) {
	input = normalizeScheduledTaskInput(input)
	if err := validateScheduledTaskInput(input); err != nil {
		return scheduledTaskView{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return scheduledTaskView{}, errScheduledTaskNotFound
	}
	if s.running[id] {
		return scheduledTaskView{}, errScheduledTaskRunning
	}
	previous := *task
	task.Name = input.Name
	task.Enabled = input.Enabled
	task.IntervalDays = input.IntervalDays
	task.RunTime = input.RunTime
	task.PhoneNumber = input.PhoneNumber
	task.Message = input.Message
	task.UpdatedAt = now.UnixMilli()
	if task.Enabled {
		task.NextRunAt = nextTaskRun(now, task.RunTime, 0).UnixMilli()
	} else {
		task.NextRunAt = 0
	}
	if err := s.persistLocked(); err != nil {
		*task = previous
		return scheduledTaskView{}, err
	}
	return scheduledTaskView{scheduledTask: *task}, nil
}

func (s *scheduledTaskService) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return errScheduledTaskNotFound
	}
	if s.running[id] {
		return errScheduledTaskRunning
	}
	delete(s.tasks, id)
	if err := s.persistLocked(); err != nil {
		s.tasks[id] = task
		return err
	}
	return nil
}

func (s *scheduledTaskService) trigger(id, trigger string, now time.Time) error {
	s.mu.Lock()
	task, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return errScheduledTaskNotFound
	}
	if s.running[id] {
		s.mu.Unlock()
		return errScheduledTaskRunning
	}
	previous := *task
	s.running[id] = true
	task.LastRunAt = now.UnixMilli()
	task.LastRunStatus = "running"
	task.LastError = ""
	if err := s.persistLocked(); err != nil {
		*task = previous
		delete(s.running, id)
		s.mu.Unlock()
		return err
	}
	taskCopy := *task
	s.mu.Unlock()
	go s.executeTask(taskCopy, trigger, now)
	return nil
}

func (s *scheduledTaskService) executeTask(task scheduledTask, trigger string, startedAt time.Time) {
	segments := 0
	var runErr error
	if s.executeSMS == nil {
		runErr = errors.New("短信发送服务不可用")
	} else {
		segments, runErr = s.executeSMS(task.PhoneNumber, task.Message)
	}
	endedAt := time.Now()
	status := "success"
	errorText := ""
	if runErr != nil {
		status = "failed"
		errorText = sanitizeScheduledTaskError(runErr.Error())
	}
	run := scheduledTaskRun{
		ID:        fmt.Sprintf("%s-%d", task.ID, startedAt.UnixNano()),
		TaskID:    task.ID,
		TaskName:  task.Name,
		Trigger:   trigger,
		StartedAt: startedAt.UnixMilli(),
		EndedAt:   endedAt.UnixMilli(),
		Status:    status,
		Segments:  segments,
		Error:     errorText,
	}

	s.mu.Lock()
	if current, ok := s.tasks[task.ID]; ok {
		current.LastRunAt = startedAt.UnixMilli()
		current.LastRunStatus = status
		current.LastError = errorText
		current.LastSegments = segments
		if current.Enabled {
			retryDays := current.IntervalDays
			if status == "failed" {
				retryDays = 1
			}
			current.NextRunAt = nextTaskRun(endedAt, current.RunTime, retryDays).UnixMilli()
		} else {
			current.NextRunAt = 0
		}
		current.UpdatedAt = endedAt.UnixMilli()
	}
	delete(s.running, task.ID)
	s.history = append([]scheduledTaskRun{run}, s.history...)
	if len(s.history) > maxScheduledTaskHistory {
		s.history = s.history[:maxScheduledTaskHistory]
	}
	if err := s.persistLocked(); err != nil {
		log.Printf("persist scheduled task result failed: %v", err)
	}
	s.mu.Unlock()

	if s.notify != nil {
		s.notify(newScheduledTaskEvent(task, run))
	}
}

func sanitizeScheduledTaskError(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	return truncateRunes(value, 240)
}

func newScheduledTaskEvent(task scheduledTask, run scheduledTaskRun) notificationEvent {
	trigger := "自动执行"
	if run.Trigger == "manual" {
		trigger = "立即执行"
	}
	kind := "scheduled_task_success"
	title := "DJSMSForward 定时任务成功"
	body := fmt.Sprintf("任务：%s\n目标：%s\n方式：%s\n短信分段：%d", task.Name, task.PhoneNumber, trigger, run.Segments)
	if run.Status == "failed" {
		kind = "scheduled_task_failure"
		title = "DJSMSForward 定时任务失败"
		body = fmt.Sprintf("任务：%s\n目标：%s\n方式：%s\n原因：%s", task.Name, task.PhoneNumber, trigger, valueOrUnknown(run.Error))
	}
	return notificationEvent{
		ID:        "scheduled-task:" + run.ID + ":" + run.Status,
		Kind:      kind,
		Title:     title,
		Body:      body,
		Number:    task.PhoneNumber,
		CreatedAt: time.UnixMilli(run.EndedAt),
	}
}

func (s *scheduledTaskService) runDue(now time.Time) {
	s.mu.Lock()
	var due []string
	for id, task := range s.tasks {
		if task.Enabled && task.NextRunAt > 0 && task.NextRunAt <= now.UnixMilli() && !s.running[id] {
			due = append(due, id)
		}
	}
	s.mu.Unlock()
	sort.Strings(due)
	for _, id := range due {
		if err := s.trigger(id, "scheduled", now); err != nil && !errors.Is(err, errScheduledTaskRunning) {
			log.Printf("trigger scheduled task %s failed: %v", id, err)
		}
	}
}

func (s *scheduledTaskService) start(ctx context.Context) {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		go func() {
			s.runDue(time.Now())
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					s.runDue(now)
				}
			}
		}()
	})
}

func (a *app) scheduledTaskSnapshot(w http.ResponseWriter, _ *http.Request) {
	if a.scheduledTasks == nil {
		writeError(w, http.StatusServiceUnavailable, "定时任务服务不可用")
		return
	}
	writeJSON(w, http.StatusOK, a.scheduledTasks.snapshot())
}

func (a *app) createScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.scheduledTasks == nil {
		writeError(w, http.StatusServiceUnavailable, "定时任务服务不可用")
		return
	}
	var input scheduledTaskInput
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := a.scheduledTasks.create(input, time.Now())
	if err != nil {
		scheduledTaskHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func scheduledTaskHTTPError(w http.ResponseWriter, err error) {
	var validationError *scheduledTaskValidationError
	switch {
	case errors.As(err, &validationError):
		writeError(w, http.StatusBadRequest, validationError.Error())
	case errors.Is(err, errScheduledTaskNotFound):
		writeError(w, http.StatusNotFound, "定时任务不存在")
	case errors.Is(err, errScheduledTaskRunning):
		writeError(w, http.StatusConflict, "任务正在执行，请稍后再试")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (a *app) updateScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.scheduledTasks == nil {
		writeError(w, http.StatusServiceUnavailable, "定时任务服务不可用")
		return
	}
	var input scheduledTaskInput
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := a.scheduledTasks.update(r.PathValue("id"), input, time.Now())
	if err != nil {
		scheduledTaskHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (a *app) deleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.scheduledTasks == nil {
		writeError(w, http.StatusServiceUnavailable, "定时任务服务不可用")
		return
	}
	if err := a.scheduledTasks.delete(r.PathValue("id")); err != nil {
		scheduledTaskHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *app) runScheduledTask(w http.ResponseWriter, r *http.Request) {
	if a.scheduledTasks == nil {
		writeError(w, http.StatusServiceUnavailable, "定时任务服务不可用")
		return
	}
	if err := a.scheduledTasks.trigger(r.PathValue("id"), "manual", time.Now()); err != nil {
		scheduledTaskHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}
