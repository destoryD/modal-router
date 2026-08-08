package modalauth

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed modal_login.py
var modalLoginScript string

const (
	JobQueued   = "queued"
	JobRunning  = "running"
	JobDone     = "done"
	JobFailed   = "failed"
	JobCanceled = "cancelled"
)

const jobTimeout = 330

type Job struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Password   string     `json:"-"`
	AuxEmail   string     `json:"auxEmail,omitempty"`
	Name       string     `json:"name,omitempty"`
	Status     string     `json:"status"`
	Message    string     `json:"message,omitempty"`
	AccountID  string     `json:"accountId,omitempty"`
	Console    string     `json:"console,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type JobManager struct {
	mu        sync.Mutex
	store     *Store
	jobs      []Job
	running   bool
	active    int
	headless  bool
}

func NewJobManager(store *Store) *JobManager {
	return &JobManager{store: store}
}

type LoginResult struct {
	OK           bool   `json:"ok"`
	Cookie       string `json:"cookie"`
	Email        string `json:"email"`
	WorkspaceURL string `json:"workspaceUrl"`
	Error        string `json:"error"`
}

func (jm *JobManager) AddBatch(text, name string, headless bool) (int, []string, []Job) {
	lines := strings.Split(text, "\n")
	var jobs []Job
	var errs []string
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		email, password, aux, ok := parseBatchLine(line)
		if !ok {
			errs = append(errs, fmt.Sprintf("line %d: invalid format (expected email|password[|auxEmail])", i+1))
			continue
		}
		jobs = append(jobs, Job{
			ID:        newID("mj"),
			Email:     email,
			Password:  password,
			AuxEmail:  aux,
			Name:      name,
			Status:    JobQueued,
			CreatedAt: time.Now(),
		})
	}

	settings := jm.store.GetSettings()
	jm.mu.Lock()
	for i := range jobs {
		jobs[i].Console = settings.BaseURL
	}
	jm.jobs = append(jm.jobs, jobs...)
	jm.headless = headless
	startWorker := !jm.running
	if startWorker {
		jm.running = true
	}
	snapshot := jm.snapshotLocked()
	jm.mu.Unlock()

	if startWorker {
		go jm.worker(headless)
	}
	return len(jobs), errs, snapshot
}

func parseBatchLine(line string) (email, password, aux string, ok bool) {
	for _, sep := range []string{"|", "\t", "----", ":"} {
		if strings.Contains(line, sep) {
			parts := strings.SplitN(line, sep, 3)
			if len(parts) >= 2 {
				email = strings.TrimSpace(parts[0])
				password = strings.TrimSpace(parts[1])
				if email != "" && password != "" {
					if len(parts) >= 3 {
						aux = strings.TrimSpace(parts[2])
					}
					return email, password, aux, true
				}
			}
		}
	}
	return "", "", "", false
}

func (jm *JobManager) snapshotLocked() []Job {
	out := make([]Job, len(jm.jobs))
	copy(out, jm.jobs)
	return out
}

func (jm *JobManager) ListJobs() ([]Job, bool, int) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.snapshotLocked(), jm.running, jm.active
}

func (jm *JobManager) ClearFinished() []Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	next := jm.jobs[:0]
	for _, j := range jm.jobs {
		if j.Status == JobQueued || j.Status == JobRunning {
			next = append(next, j)
		}
	}
	jm.jobs = next
	return jm.snapshotLocked()
}

func (jm *JobManager) JobAction(id, op string) ([]Job, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	startWorker := false
	if op == "cancel" {
		for i := range jm.jobs {
			if jm.jobs[i].ID == id && (jm.jobs[i].Status == JobQueued || jm.jobs[i].Status == JobRunning) {
				jm.jobs[i].Status = JobCanceled
				now := time.Now()
				if jm.jobs[i].FinishedAt == nil {
					jm.jobs[i].FinishedAt = &now
				}
				if jm.jobs[i].Message == "" {
					jm.jobs[i].Message = "cancelled"
				}
				break
			}
		}
	} else if op == "retry" {
		for i := range jm.jobs {
			if jm.jobs[i].ID == id {
				jm.jobs[i].Status = JobQueued
				jm.jobs[i].Message = ""
				jm.jobs[i].AccountID = ""
				jm.jobs[i].StartedAt = nil
				jm.jobs[i].FinishedAt = nil
				break
			}
		}
		startWorker = !jm.running
		if startWorker {
			jm.running = true
		}
	}
	headless := jm.headless
	snapshot := jm.snapshotLocked()
	jm.mu.Unlock()

	if startWorker {
		go jm.worker(headless)
	}
	return snapshot, nil
}

func (jm *JobManager) worker(headless bool) {
	settings := jm.store.GetSettings()
	concurrency := settings.JobConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > 10 {
		concurrency = 10
	}
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jm.jobLoop(headless)
		}()
	}
	wg.Wait()
	jm.mu.Lock()
	jm.running = false
	jm.active = 0
	jm.mu.Unlock()
}

func (jm *JobManager) jobLoop(headless bool) {
	for {
		jm.mu.Lock()
		job, baseURL, proxyURL, ok := jm.claimNextLocked()
		if !ok {
			jm.active--
			jm.mu.Unlock()
			return
		}
		jm.active++
		jm.mu.Unlock()

		log.Printf("[modal-jobs] running job %s for %s", job.ID, job.Email)
		result, output, runErr := jm.runLogin(job.Email, job.Password, job.AuxEmail, baseURL, proxyURL, headless)

		jm.mu.Lock()
		jm.active--
		ji := -1
		for i, j := range jm.jobs {
			if j.ID == job.ID {
				ji = i
				break
			}
		}
		finishNow := time.Now()
		if ji >= 0 {
			jm.jobs[ji].FinishedAt = &finishNow
			if runErr != nil {
				msg := runErr.Error()
				if output != "" {
					tail := strings.TrimSpace(output)
					if len(tail) > 500 {
						tail = "..." + tail[len(tail)-500:]
					}
					msg = msg + "\n" + tail
				}
				jm.jobs[ji].Status = JobFailed
				jm.jobs[ji].Message = msg
				log.Printf("[modal-jobs] job %s FAILED for %s: %s", job.ID, job.Email, runErr)
			} else {
				cookie := result.Cookie
				if jm.store.CookieExists(cookie) {
					jm.jobs[ji].Status = JobFailed
					jm.jobs[ji].Message = "duplicate cookie; this account was already imported"
				} else {
					acc, buildErr := jm.store.AddAccount(result.Email, job.Name, cookie, result.WorkspaceURL)
					if buildErr != nil {
						jm.jobs[ji].Status = JobFailed
						jm.jobs[ji].Message = "save failed: " + buildErr.Error()
					} else {
						jm.jobs[ji].Status = JobDone
						jm.jobs[ji].AccountID = acc.ID
						jm.jobs[ji].Message = "imported"
						log.Printf("[modal-jobs] job %s OK for %s -> account %s", job.ID, job.Email, acc.ID)
					}
				}
			}
		}
		jm.mu.Unlock()
	}
}

func (jm *JobManager) claimNextLocked() (job Job, baseURL, proxyURL string, ok bool) {
	settings := jm.store.GetSettings()
	for i := range jm.jobs {
		if jm.jobs[i].Status == JobQueued {
			now := time.Now()
			jm.jobs[i].Status = JobRunning
			jm.jobs[i].StartedAt = &now
			job = jm.jobs[i]
			baseURL = settings.BaseURL
			proxyURL = settings.ProxyURL
			return job, baseURL, proxyURL, true
		}
	}
	return Job{}, "", "", false
}

func (jm *JobManager) runLogin(email, password, auxEmail, baseURL, proxyURL string, headless bool) (LoginResult, string, error) {
	pyExe := strings.TrimSpace(os.Getenv("MODAL_PYTHON"))
	if pyExe == "" {
		pyExe = "python"
	}

	scriptFile, err := os.CreateTemp("", "modal_login-*.py")
	if err != nil {
		return LoginResult{}, "", err
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, err := scriptFile.WriteString(modalLoginScript); err != nil {
		scriptFile.Close()
		return LoginResult{}, "", err
	}
	scriptFile.Close()

	outFile, err := os.CreateTemp("", "modal_result-*.json")
	if err != nil {
		return LoginResult{}, "", err
	}
	outPath := outFile.Name()
	outFile.Close()
	defer os.Remove(outPath)

	args := []string{
		scriptPath,
		"--email", email,
		"--password", password,
		"--base-url", baseURL,
		"--out", outPath,
		"--timeout", strconv.Itoa(jobTimeout),
	}
	if auxEmail != "" {
		args = append(args, "--aux-email", auxEmail)
	}
	if proxyURL != "" {
		args = append(args, "--proxy", proxyURL)
	}
	if headless {
		args = append(args, "--headless")
	}

	cmd := exec.Command(pyExe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	log.Printf("[modal-login] running %s for %s", pyExe, email)
	runErr := cmd.Run()
	output := stdout.String() + stderr.String()
	if output != "" {
		log.Printf("[modal-login] script output:\n%s", output)
	}

	result := LoginResult{}
	if raw, readErr := os.ReadFile(outPath); readErr == nil {
		_ = json.Unmarshal(raw, &result)
	}
	if runErr != nil {
		if result.Error != "" {
			return result, output, fmt.Errorf("%s", result.Error)
		}
		return result, output, fmt.Errorf("login failed: %v", runErr)
	}
	if !result.OK {
		msg := strings.TrimSpace(result.Error)
		if msg == "" {
			msg = "login did not complete"
		}
		return result, output, fmt.Errorf("%s", msg)
	}
	if strings.TrimSpace(result.Cookie) == "" {
		return result, output, fmt.Errorf("no modal cookie captured")
	}
	return result, output, nil
}
