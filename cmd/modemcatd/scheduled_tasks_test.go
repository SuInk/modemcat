package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validScheduledTaskInput() scheduledTaskInput {
	return scheduledTaskInput{
		Name:         "查询流量",
		Enabled:      true,
		IntervalDays: 90,
		RunTime:      "08:00",
		PhoneNumber:  "10086",
		Message:      "CXLL",
	}
}

func TestScheduledTaskSendAppearsInConversationHistory(t *testing.T) {
	instance := &app{demo: true}
	instance.initScheduledTasks()
	segments, err := instance.scheduledTasks.executeSMS("10086", "CXLL")
	if err != nil {
		t.Fatal(err)
	}
	if segments != 1 || len(instance.sms) != 1 {
		t.Fatalf("send result = segments %d messages %d", segments, len(instance.sms))
	}
	message := instance.sms[0]
	if message.Direction != "outgoing" || message.Recipient != "10086" || message.Content != "CXLL" {
		t.Fatalf("scheduled SMS history = %+v", message)
	}
}

func TestValidateScheduledTaskInput(t *testing.T) {
	valid := validScheduledTaskInput()
	if err := validateScheduledTaskInput(valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*scheduledTaskInput)
	}{
		{name: "empty name", mutate: func(input *scheduledTaskInput) { input.Name = "" }},
		{name: "invalid interval", mutate: func(input *scheduledTaskInput) { input.IntervalDays = 0 }},
		{name: "invalid time", mutate: func(input *scheduledTaskInput) { input.RunTime = "8am" }},
		{name: "invalid phone", mutate: func(input *scheduledTaskInput) { input.PhoneNumber = "+86 138" }},
		{name: "plus only", mutate: func(input *scheduledTaskInput) { input.PhoneNumber = "+" }},
		{name: "empty message", mutate: func(input *scheduledTaskInput) { input.Message = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid
			tt.mutate(&input)
			if err := validateScheduledTaskInput(input); err == nil {
				t.Fatal("invalid input unexpectedly accepted")
			}
		})
	}
}

func TestNextTaskRunUsesLocalTimeAndInterval(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 8, 3, 7, 30, 0, 0, location)
	if got := nextTaskRun(now, "08:00", 0); !got.Equal(time.Date(2026, 8, 3, 8, 0, 0, 0, location)) {
		t.Fatalf("next run = %s", got)
	}
	if got := nextTaskRun(now, "08:00", 90); !got.Equal(time.Date(2026, 11, 1, 8, 0, 0, 0, location)) {
		t.Fatalf("interval run = %s", got)
	}
	if got := nextTaskRun(time.Date(2026, 8, 3, 9, 0, 0, 0, location), "08:00", 0); !got.Equal(time.Date(2026, 8, 4, 8, 0, 0, 0, location)) {
		t.Fatalf("past time next run = %s", got)
	}
}

func TestScheduledTaskPersistenceAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ModemCat")
	path := filepath.Join(dir, "scheduled-tasks.json")
	service, err := newScheduledTaskService(path, func(string, string) (int, error) { return 1, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := validScheduledTaskInput()
	created, err := service.create(input, time.Date(2026, 8, 3, 7, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if created.NextRunAt <= 0 {
		t.Fatal("enabled task has no next run")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
	reloaded, err := newScheduledTaskService(path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reloaded.snapshot()
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != created.ID {
		t.Fatalf("reloaded tasks = %#v", snapshot.Tasks)
	}
}

func TestScheduledTaskLoadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"tasks":[],"history":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scheduled-tasks.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := newScheduledTaskService(path, nil, nil); err == nil {
		t.Fatal("symlinked task file unexpectedly accepted")
	}
}

func waitScheduledEvent(t *testing.T, events <-chan notificationEvent) notificationEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduled task event")
		return notificationEvent{}
	}
}

func TestScheduledTaskSuccessAndFailureNotifications(t *testing.T) {
	tests := []struct {
		name      string
		execute   func(string, string) (int, error)
		wantKind  string
		wantState string
	}{
		{
			name: "success",
			execute: func(string, string) (int, error) {
				return 2, nil
			},
			wantKind:  "scheduled_task_success",
			wantState: "success",
		},
		{
			name: "failure",
			execute: func(string, string) (int, error) {
				return 0, errors.New("modem\r\nfailed")
			},
			wantKind:  "scheduled_task_failure",
			wantState: "failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan notificationEvent, 1)
			service, err := newScheduledTaskService("", tt.execute, func(event notificationEvent) { events <- event })
			if err != nil {
				t.Fatal(err)
			}
			input := validScheduledTaskInput()
			input.Enabled = true
			created, err := service.create(input, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := service.trigger(created.ID, "manual", time.Now()); err != nil {
				t.Fatal(err)
			}
			event := waitScheduledEvent(t, events)
			if event.Kind != tt.wantKind {
				t.Fatalf("event kind = %q, want %q", event.Kind, tt.wantKind)
			}
			snapshot := service.snapshot()
			if len(snapshot.History) != 1 || snapshot.History[0].Status != tt.wantState {
				t.Fatalf("history = %#v", snapshot.History)
			}
			if snapshot.Tasks[0].LastRunStatus != tt.wantState {
				t.Fatalf("task status = %q", snapshot.Tasks[0].LastRunStatus)
			}
			if tt.wantState == "failed" {
				next := time.UnixMilli(snapshot.Tasks[0].NextRunAt)
				if !next.After(time.Now()) || next.Sub(time.Now()) > 48*time.Hour || next.Hour() != 8 || next.Minute() != 0 {
					t.Fatalf("failure retry = %s, want the next 08:00 retry window", next)
				}
			}
		})
	}
}

func TestScheduledTaskCannotRunTwiceConcurrently(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	events := make(chan notificationEvent, 1)
	service, err := newScheduledTaskService("", func(string, string) (int, error) {
		close(started)
		<-release
		return 1, nil
	}, func(event notificationEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	input := validScheduledTaskInput()
	input.Enabled = false
	created, err := service.create(input, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.trigger(created.ID, "manual", time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	if err := service.trigger(created.ID, "manual", time.Now()); !errors.Is(err, errScheduledTaskRunning) {
		t.Fatalf("second trigger error = %v", err)
	}
	close(release)
	_ = waitScheduledEvent(t, events)
}

func TestRunDueTriggersEnabledTask(t *testing.T) {
	events := make(chan notificationEvent, 1)
	service, err := newScheduledTaskService("", func(string, string) (int, error) { return 1, nil }, func(event notificationEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("CST", 8*60*60)
	createdAt := time.Date(2026, 8, 3, 7, 0, 0, 0, location)
	input := validScheduledTaskInput()
	created, err := service.create(input, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	service.runDue(time.Date(2026, 8, 3, 8, 1, 0, 0, location))
	event := waitScheduledEvent(t, events)
	if event.Kind != "scheduled_task_success" || event.ID == "" {
		t.Fatalf("event = %#v", event)
	}
	snapshot := service.snapshot()
	if snapshot.Tasks[0].ID != created.ID || snapshot.Tasks[0].LastRunStatus != "success" {
		t.Fatalf("task = %#v", snapshot.Tasks[0])
	}
}

func TestScheduledTaskCreateHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		service    *scheduledTaskService
		body       string
		wantStatus int
	}{
		{
			name:       "invalid input",
			service:    &scheduledTaskService{tasks: make(map[string]*scheduledTask), running: make(map[string]bool)},
			body:       `{"name":"","interval_days":1,"run_time":"08:00","phone_number":"10086","message":"CXLL"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "persistence failure",
			service: &scheduledTaskService{
				tasks:   make(map[string]*scheduledTask),
				running: make(map[string]bool),
				path:    filepath.Join(t.TempDir(), "missing", "scheduled-tasks.json"),
			},
			body:       `{"name":"查询流量","enabled":true,"interval_days":1,"run_time":"08:00","phone_number":"10086","message":"CXLL"}`,
			wantStatus: http.StatusInternalServerError,
		},
	}

	if err := os.WriteFile(filepath.Dir(tests[1].service.path), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := &app{scheduledTasks: tt.service}
			request := httptest.NewRequest(http.MethodPost, "/api/scheduled-tasks", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			instance.createScheduledTask(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}
}
