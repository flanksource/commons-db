package helpers

import (
	"fmt"
	"time"
)

type TestLog struct {
	Timestamp time.Time
	Level     string
	Message   string
	Labels    map[string]string
	Fields    map[string]interface{}
}

type TestLogBatch struct {
	Logs []TestLog
}

func GenerateTestLogs(count int) []TestLog {
	logs := make([]TestLog, count)
	levels := []string{"debug", "info", "warn", "error"}
	services := []string{"api", "database", "cache", "queue"}

	for i := 0; i < count; i++ {
		level := levels[i%len(levels)]
		service := services[i%len(services)]

		logs[i] = TestLog{
			Timestamp: time.Now().Add(-time.Duration(count-i) * time.Second),
			Level:     level,
			Message:   fmt.Sprintf("Log message from %s service (entry %d)", service, i),
			Labels: map[string]string{
				"service":   service,
				"level":     level,
				"pod":       fmt.Sprintf("pod-%d", i%10),
				"namespace": "default",
			},
			Fields: map[string]interface{}{
				"request_id": fmt.Sprintf("req-%d", i),
				"duration":   float64(i % 100),
				"status":     200 + (i%4)*100,
			},
		}
	}

	return logs
}

func GenerateTestLogsWithLevel(count int, level string) []TestLog {
	logs := make([]TestLog, count)
	services := []string{"api", "database", "cache", "queue"}

	for i := 0; i < count; i++ {
		service := services[i%len(services)]

		logs[i] = TestLog{
			Timestamp: time.Now().Add(-time.Duration(count-i) * time.Second),
			Level:     level,
			Message:   fmt.Sprintf("%s: Log message from %s service (entry %d)", level, service, i),
			Labels: map[string]string{
				"service":   service,
				"level":     level,
				"pod":       fmt.Sprintf("pod-%d", i%10),
				"namespace": "default",
			},
			Fields: map[string]interface{}{
				"request_id": fmt.Sprintf("req-%d", i),
				"duration":   float64(i % 100),
				"status":     200 + (i%4)*100,
			},
		}
	}

	return logs
}

func GenerateTestLogsWithService(count int, service string) []TestLog {
	logs := make([]TestLog, count)
	levels := []string{"debug", "info", "warn", "error"}

	for i := 0; i < count; i++ {
		level := levels[i%len(levels)]

		logs[i] = TestLog{
			Timestamp: time.Now().Add(-time.Duration(count-i) * time.Second),
			Level:     level,
			Message:   fmt.Sprintf("Log message from %s service (entry %d)", service, i),
			Labels: map[string]string{
				"service":   service,
				"level":     level,
				"pod":       fmt.Sprintf("pod-%d", i%10),
				"namespace": "default",
			},
			Fields: map[string]interface{}{
				"request_id": fmt.Sprintf("req-%d", i),
				"duration":   float64(i % 100),
				"status":     200 + (i%4)*100,
			},
		}
	}

	return logs
}

func GenerateTestFile(name string, size int) []byte {
	content := fmt.Sprintf("Test file: %s\n", name)
	for i := 0; i < size; i++ {
		content += fmt.Sprintf("Line %d: This is a test file content line\n", i)
	}
	return []byte(content)
}

func GenerateBucketName(prefix string) string {
	return fmt.Sprintf("%s-e2e-%d", prefix, time.Now().UnixNano())
}

func GenerateContainerName(prefix string) string {
	return fmt.Sprintf("%s-e2e-%d", prefix, time.Now().UnixNano())
}

type TestAssertion struct {
	Name     string
	Check    func() error
	OnFailure string
}

func NewTestAssertion(name string, check func() error) *TestAssertion {
	return &TestAssertion{
		Name:  name,
		Check: check,
	}
}

func (ta *TestAssertion) WithFailureMessage(msg string) *TestAssertion {
	ta.OnFailure = msg
	return ta
}

func (ta *TestAssertion) Execute() error {
	if err := ta.Check(); err != nil {
		if ta.OnFailure != "" {
			return fmt.Errorf("%s: %s (original error: %w)", ta.Name, ta.OnFailure, err)
		}
		return fmt.Errorf("%s: %w", ta.Name, err)
	}
	return nil
}
