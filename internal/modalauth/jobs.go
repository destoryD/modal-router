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
	ProxyURL   string     `json:"proxyUrl,omitempty"`
	Mode       string     `json:"mode,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type JobManager struct {
	mu           sync.Mutex
	store        *Store
	jobs         []Job
	running      bool
	active       int
	headless     bool
	cmds         map[string]*exec.Cmd
	onSetupDone  func(accountID, email, baseURL, apiKey, stripeURL string)
}

func NewJobManager(store *Store) *JobManager {
	return &JobManager{store: store, cmds: make(map[string]*exec.Cmd)}
}

func (jm *JobManager) SetOnSetupDone(fn func(accountID, email, baseURL, apiKey, stripeURL string)) {
	jm.onSetupDone = fn
}

type LoginResult struct {
	OK           bool   `json:"ok"`
	Cookie       string `json:"cookie"`
	Email        string `json:"email"`
	WorkspaceURL string `json:"workspaceUrl"`
	Error        string `json:"error"`
}

func (jm *JobManager) AddBatch(text, name, proxyURL, mode string, headless bool) (int, []string, []Job) {
	if mode == "" {
		mode = "signup"
	}
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
			ProxyURL:  strings.TrimSpace(proxyURL),
			Mode:      mode,
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

func (jm *JobManager) ListSetupJobs() []SetupJob {
	return jm.store.ListSetupJobs()
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
				// Kill the running process if any
				if cmd, ok := jm.cmds[id]; ok {
					log.Printf("[modal-jobs] killing process for cancelled job %s", id)
					cmd.Process.Kill()
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
			jm.mu.Unlock()
			return
		}
		jm.active++
		jm.mu.Unlock()

		log.Printf("[modal-jobs] running job %s for %s", job.ID, job.Email)
		result, output, runErr := jm.runLogin(job.ID, job.Email, job.Password, job.AuxEmail, baseURL, proxyURL, job.Mode, headless)

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
			// If job was cancelled while running, keep the cancelled status
			if jm.jobs[ji].Status == JobCanceled {
				log.Printf("[modal-jobs] job %s was cancelled for %s", job.ID, job.Email)
				jm.mu.Unlock()
				continue
			}
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
			proxyURL = job.ProxyURL
			if proxyURL == "" {
				proxyURL = settings.ProxyURL
			}
			return job, baseURL, proxyURL, true
		}
	}
	return Job{}, "", "", false
}

func (jm *JobManager) runLogin(jobID, email, password, auxEmail, baseURL, proxyURL, mode string, headless bool) (LoginResult, string, error) {
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
		"--mode", mode,
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

	// Register the cmd so it can be killed on cancel
	jm.mu.Lock()
	jm.cmds[jobID] = cmd
	jm.mu.Unlock()
	defer func() {
		jm.mu.Lock()
		delete(jm.cmds, jobID)
		jm.mu.Unlock()
	}()

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

type SetupJob struct {
	ID        string     `json:"id"`
	AccountID string     `json:"accountId"`
	Email     string     `json:"email"`
	Status    string     `json:"status"`
	Message   string     `json:"message,omitempty"`
	BaseURL   string     `json:"baseUrl,omitempty"`
	APIKey    string     `json:"apiKey,omitempty"`
	StripeURL string     `json:"stripeUrl,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	FinAt     *time.Time `json:"finishedAt,omitempty"`
}

func (jm *JobManager) RunSetup(accountID, email, cookie, proxyURL string, headless bool) (*SetupJob, error) {
	job := SetupJob{
		ID:        newID("sj"),
		AccountID: accountID,
		Email:     email,
		Status:    JobRunning,
		CreatedAt: time.Now(),
	}
	now := time.Now()
	job.StartedAt = &now

	// Persist immediately
	jm.store.AddSetupJob(job)

	go func() {
		result := jm.runSetupHTTP(cookie, proxyURL)
		finNow := time.Now()
		jm.store.UpdateSetupJob(job.ID, func(j *SetupJob) {
			j.FinAt = &finNow
			if result.Error != "" {
				j.Status = JobFailed
				j.Message = result.Error
				log.Printf("[modal-setup] FAILED for %s: %s", email, result.Error)
			} else {
				j.Status = JobDone
				j.BaseURL = result.BaseURL
				j.APIKey = result.APIKey
				j.StripeURL = result.StripeURL
				j.Message = "setup completed"
				log.Printf("[modal-setup] OK for %s: base=%s key=%s stripe=%s", email, result.BaseURL, maskKey(result.APIKey), result.StripeURL)
			}
		})
		if result.Error == "" && jm.onSetupDone != nil {
			jm.onSetupDone(accountID, email, result.BaseURL, result.APIKey, result.StripeURL)
		}
	}()

	return &job, nil
}

type SetupHTTPResult struct {
	BaseURL   string
	APIKey    string
	StripeURL string
	Error     string
}

func (jm *JobManager) runSetupHTTP(cookie, proxyURL string) SetupHTTPResult {
	settings := jm.store.GetSettings()
	baseURL := strings.TrimRight(settings.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://modal.com"
	}

	csrf := extractCookieValue(cookie, "modal-csrf-token")

	workspace := ""
	jm.store.mu.Lock()
	for _, acc := range jm.store.state.Accounts {
		if acc.CookieCipher != "" {
			plain, err := jm.store.decrypt(acc.CookieCipher)
			if err == nil && plain == cookie {
				workspace = acc.Workspace
				if workspace == "" {
					workspace = workspaceFromURL(acc.WorkspaceURL)
				}
				break
			}
		}
	}
	jm.store.mu.Unlock()

	if workspace == "" {
		return SetupHTTPResult{Error: "could not determine workspace"}
	}
	log.Printf("[modal-setup] workspace=%s", workspace)

	env := "main"

	epResult := createEndpoint(baseURL, workspace, env, cookie, csrf, proxyURL, "")
	stripeResult := getStripeLink(baseURL, workspace, cookie, proxyURL)

	result := SetupHTTPResult{
		BaseURL:   epResult.BaseURL,
		APIKey:    epResult.APIKey,
		StripeURL: stripeResult.StripeURL,
	}
	if epResult.Error != "" {
		result.Error = epResult.Error
	}
	if stripeResult.Error != "" && result.Error == "" {
		result.Error = "endpoint ok; stripe: " + stripeResult.Error
	}
	return result
}

func extractCookieValue(cookie, name string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, name+"=") {
			return strings.TrimPrefix(part, name+"=")
		}
	}
	return ""
}
