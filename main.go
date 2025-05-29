package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log" //todo: use log/slog
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	texttemplate "text/template"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
	"github.com/golang-jwt/jwt/v5"
	hcron "github.com/lnquy/cron"
	"github.com/robfig/cron/v3"
)

//go:embed index.html
var embedded embed.FS

var jwtSecretKey []byte

var infoLog *log.Logger
var errorLog *log.Logger
var webLog *log.Logger
var webLogBuf strings.Builder

var CONF Config

type Config struct {
	port     string
	jobsDir  string
	password string
	notify   string
}

type RunStatus int

const (
	RunSuccess RunStatus = iota
	RunFailure
	Running
	NoRun
)

type Task struct {
	Name    string   `toml:"name"`
	Cmd     string   `toml:"cmd"`
	Emails  []string `toml:"emails"`
	Retries int      `toml:"retries"`
}

type TaskRun struct {
	Name              string
	cmd               string
	RenderedCmd       string
	StartTime         time.Time
	EndTime           time.Time
	Status            RunStatus
	Attempt           int
	cmdTemplateParams map[string]string
	emails            []string
	retries           int
	ctxCancelFn       context.CancelFunc
	//todo: store in db
	lastOutput string
}

type JobRun struct {
	ScheduledTime time.Time
	StartTime     time.Time
	EndTime       time.Time
	Status        RunStatus
	TasksHistory  []*TaskRun
	ctxCancelFn   context.CancelFunc
}

type Job struct {
	file          string
	md5           [16]byte
	Title         string `toml:"title"`
	Cron          string `toml:"cron"`
	HCron         string
	Tasks         []*Task `toml:"tasks"`
	cronID        cron.EntryID
	RunHistory    []*JobRun
	OnOff         bool
	NextScheduled time.Time
	Retries       int      `toml:"retries"`
	Emails        []string `toml:"emails"`
}

type JobsAndCron struct {
	Jobs []*Job
	cron *cron.Cron
	//todo: add config, make global?
	//no need for mutex?
	//Jobs is modified from scanAndScheduleJobs only
	//calls to scanAndScheduleJobs don't overlap
}

func main() {
	var JC JobsAndCron
	initConfig()
	jwtSecretKey = generateRandomKey(32)
	infoLog = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	errorLog = log.New(os.Stdout, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
	webLog = log.New(&webLogBuf, "", log.Ldate|log.Ltime)
	JC.cron = cron.New(cron.WithSeconds())
	JC.cron.Start()
	scanAndScheduleJobs(&JC)
	go startFSWatcher(&JC)
	httpServer(&JC)
}

func initConfig() {
	CONF.port = ":8080"
	CONF.jobsDir = "./examples/"
	CONF.password = ""
	CONF.notify = "python3 ./examples/notify.py"
	if port := os.Getenv("REPEATER_PORT"); port != "" {
		CONF.port = port
	}
	if jobsDir := os.Getenv("REPEATER_JOBS_DIRECTORY"); jobsDir != "" {
		CONF.jobsDir = jobsDir
	}
	CONF.password = os.Getenv("REPEATER_PASSWORD")
	if notify := os.Getenv("REPEATER_NOTIFY"); notify != "" {
		CONF.notify = notify
	}
}

func generateRandomKey(size int) []byte {
	key := make([]byte, size)
	_, err := rand.Read(key)
	if err != nil {
		errorLog.Printf("Failed to generate a JWT secret key. Aborting.")
		os.Exit(1)
	}
	return key
}

func startFSWatcher(JC *JobsAndCron) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()
	go watchFS(JC, watcher)
	err = watcher.Add(CONF.jobsDir)
	if err != nil {
		log.Fatal(err)
	}
	select {}
}

func watchFS(JC *JobsAndCron, watcher *fsnotify.Watcher) {
	for {
		select {
		case _, ok := <-watcher.Events:
			if !ok {
				return
			}
			scanAndScheduleJobs(JC)
			//todo: avoid full rescan on each event
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			errorLog.Println("fsnotify:", err)
		}
	}
}

func scanAndScheduleJobs(JC *JobsAndCron) {
	files := make(map[string][16]byte)
	err := scanFiles(files)
	webLogBuf.Reset()
	if err != nil {
		errorLog.Printf("Errors while reading files: %s", err)
		webLog.Printf("Errors while reading files: %s", err)
	}
	removeJobsWithoutFiles(files, JC)
	for f := range files {
		infoLog.Printf("Loading %s", f)
		jb, err := processJobFile(f)
		if jb != nil && err == nil {
			scheduleJob(jb, JC)
		} else {
			infoLog.Printf("Skipping %s", f)
		}
	}
	sort.SliceStable(JC.Jobs, func(i, j int) bool {
		return JC.Jobs[i].Title < JC.Jobs[j].Title
	})
	//todo: gen_event()
	broadcastSSEUpdate(`{"event": "jobs_updated"}`)
}

func scanFiles(files map[string][16]byte) error {
	err := filepath.Walk(CONF.jobsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			errorLog.Printf("Error accessing path %s: %v\n", path, err)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".job" {
			f, err := os.ReadFile(path)
			if err != nil {
				errorLog.Printf("Error reading file %s: %v\n", path, err)
				return err
			}
			files[path] = md5.Sum(f)
		}
		return nil
	})
	return err
}

func removeJobsWithoutFiles(files map[string][16]byte, JC *JobsAndCron) {
	//todo: simplify removing
	var toremove []int
	for idx, jb := range JC.Jobs {
		md5, haskey := files[jb.file]
		if !haskey {
			infoLog.Printf("Marking %s for deletion", jb.Title)
			toremove = append(toremove, idx)
		} else if md5 != jb.md5 {
			infoLog.Printf("File %s has changed, marking for reloading", jb.file)
			toremove = append(toremove, idx)
		} else if md5 == jb.md5 {
			infoLog.Printf("File %s has not changed, skipping", jb.file)
			delete(files, jb.file)
		} else {
			panic("This is not supposed to happen")
		}
	}
	if len(toremove) > 0 {
		sort.Sort(sort.Reverse(sort.IntSlice(toremove)))
		last_idx := len(JC.Jobs) - 1
		for _, jb_idx := range toremove {
			JC.cron.Remove(JC.Jobs[jb_idx].cronID)
			//todo: terminate running commands before removing
			JC.Jobs[jb_idx] = JC.Jobs[last_idx]
			last_idx = last_idx - 1
		}
		if last_idx >= 0 {
			JC.Jobs = JC.Jobs[:last_idx+1]
		} else {
			JC.Jobs = nil
		}
	}
}

func processJobFile(filePath string) (*Job, error) {
	var jb Job
	jb.file = filePath
	jb.OnOff = false
	f, err := os.ReadFile(filePath)
	if err != nil {
		errorLog.Printf("Error reading file %s: %v\n", filePath, err)
		webLog.Printf("Error reading file %s: %v\n", filePath, err)
		return nil, err
	}
	jb.md5 = md5.Sum(f)
	jb.RunHistory = make([]*JobRun, 0)
	err = toml.Unmarshal(f, &jb)
	if err != nil {
		errorLog.Printf("Error parsing file %s: %v\n", filePath, err)
		webLog.Printf("Error parsing file %s: %v\n", filePath, err)
		return nil, err
	}
	if jb.Title == "" {
		errorLog.Printf("%s: missing job title. Skipping.\n", filePath)
		webLog.Printf("%s: missing job title. Skipping. \n", filePath)
		return nil, nil
	}
	if len(jb.Tasks) == 0 {
		errorLog.Printf("%s: no tasks. Skipping.\n", filePath)
		webLog.Printf("%s: no tasks. Skipping. \n", filePath)
		return nil, nil
	}
	for _, t := range jb.Tasks {
		if len(t.Name) == 0 || len(t.Cmd) == 0 {
			errorLog.Printf("%s: Task name or cmd is empty. Skipping job altogether.\n", filePath)
			webLog.Printf("%s: Task name or cmd is empty. Skipping job altogether. \n", filePath)
			return nil, nil
		}
	}
	if jb.Retries < 0 {
		errorLog.Printf("Job '%s' has negative retries (%d), setting to 0", jb.Title, jb.Retries)
		webLog.Printf("Job '%s' has negative retries (%d), setting to 0", jb.Title, jb.Retries)
		jb.Retries = 0
	}
	for _, t := range jb.Tasks {
		if t.Retries < 0 {
			errorLog.Printf("Task '%s' in job '%s' has negative retries (%d), setting to 0", t.Name, jb.Title, t.Retries)
			webLog.Printf("Task '%s' in job '%s' has negative retries (%d), setting to 0", t.Name, jb.Title, t.Retries)
			t.Retries = 0
		}
	}
	//todo: move parser spec into JobsAndCron
	specParser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err = specParser.Parse(jb.Cron)
	if jb.Cron != "" && err != nil {
		errorLog.Printf("%s: can't parse cron \"%s\". %v.\n", filePath, jb.Cron, err)
		webLog.Printf("%s: can't parse cron \"%s\". %v.\n", filePath, jb.Cron, err)
		return nil, err
	}
	exprDesc, _ := hcron.NewDescriptor(hcron.Use24HourTimeFormat(true))
	jb.HCron, err = exprDesc.ToDescription(jb.Cron, hcron.Locale_en)
	if jb.Cron == "" {
		jb.HCron = ""
	} else if err != nil {
		jb.HCron = jb.Cron
	}
	return &jb, nil
}

func scheduleJob(jb *Job, JC *JobsAndCron) {
	var err error
	JC.Jobs = append(JC.Jobs, jb)
	if jb.Cron != "" {
		jb.cronID, err = JC.cron.AddFunc(
			jb.Cron,
			func() { runScheduled(jb, JC.cron) },
		)
		if err != nil {
			errorLog.Printf("Error scheduling job '%s' from file '%s': %v\n", jb.Title, jb.file, err)
			webLog.Printf("Error scheduling job '%s' from file '%s': %v\n", jb.Title, jb.file, err)
		}
		infoLog.Printf("Added job '%s' from file '%s'", jb.Title, jb.file)
	}
}

func runScheduled(jb *Job, c *cron.Cron) {
	//todo: check race conditions
	if !jb.OnOff {
		infoLog.Printf("Skipping '%s'", jb.Title)
		return
	}
	run := initRun(jb, c)
	go runJob(run, jb)
	jb.NextScheduled = c.Entry(jb.cronID).Next
	//todo: check for errors
}

func initRun(jb *Job, c *cron.Cron) *JobRun {
	var scheduled_time time.Time
	if c != nil {
		scheduled_time = c.Entry(jb.cronID).Prev
	} else {
		scheduled_time = time.Now()
	}
	run := &JobRun{
		ScheduledTime: scheduled_time,
		StartTime:     time.Now(),
	}
	for _, t := range jb.Tasks {
		emails := t.Emails
		if len(emails) == 0 {
			emails = jb.Emails
		}
		retries := t.Retries
		if retries == 0 {
			retries = jb.Retries
		}
		run.TasksHistory = append(run.TasksHistory, &TaskRun{
			Name:    t.Name,
			cmd:     t.Cmd,
			Status:  NoRun,
			Attempt: 0,
			cmdTemplateParams: map[string]string{
				"title":        jb.Title,
				"scheduled_dt": run.ScheduledTime.Format("2006-01-02"),
			},
			emails:  emails,
			retries: retries,
		})
	}
	jb.RunHistory = append(jb.RunHistory, run)
	return run
}

func runJob(run *JobRun, jb *Job) error {
	//todo: check race conditions
	var err error
	ctx, cancel := context.WithCancel(context.Background())
	run.ctxCancelFn = cancel
	defer func() {
		if run.ctxCancelFn != nil {
			run.ctxCancelFn()
			run.ctxCancelFn = nil
		}
	}()
	run.Status = Running
	infoLog.Printf("Running '%s'", jb.Title)
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "job_running", "name": "%s"}`, jb.Title))
	var jobFail bool
	for _, tr := range run.TasksHistory {
		for attempt := 1; attempt <= tr.retries+1; attempt++ {
			infoLog.Printf("Running task '%s' (attempt %d/%d)", tr.Name, attempt, tr.retries+1)
			err = runTask(ctx, tr)
			if err == nil {
				break
			}
			errorLog.Printf("Task '%s' failed (attempt %d/%d)", tr.Name, attempt, tr.retries+1)
			if attempt > tr.retries {
				jobFail = true
				break
			}
		}
		if jobFail {
			break
		}
	}
	run.Status = RunSuccess
	if jobFail {
		run.Status = RunFailure
	}
	run.EndTime = time.Now()
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "job_finished", "name": "%s"}`, jb.Title))
	//todo: return error
	return nil
}

func runTask(ctx context.Context, tr *TaskRun) error {
	tmpl := texttemplate.New("tmpl").Option("missingkey=error")
	tmpl, err := tmpl.Parse(tr.cmd)
	if err != nil {
		errorLog.Printf("Error parsing command template '%s'-'%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, tr.cmd, err)
		return err
	}
	sb := new(strings.Builder)
	err = tmpl.Execute(sb, tr.cmdTemplateParams)
	if err != nil {
		errorLog.Printf("Error rendering command template '%s'-'%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, tr.cmd, err)
		return err
	}
	tr.StartTime = time.Now()
	tr.Attempt += 1
	tr.RenderedCmd = sb.String()
	tr.Status = Running
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	tr.ctxCancelFn = cancel
	defer func() {
		if tr.ctxCancelFn != nil {
			tr.ctxCancelFn()
			tr.ctxCancelFn = nil
		}
	}()
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "task_running", "name": "%s"}`, tr.Name))
	output, err := executeCmd(ctx, tr.RenderedCmd)
	tr.lastOutput = output
	tr.EndTime = time.Now()
	if err != nil {
		errorLog.Printf("Error executing '%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, err)
		tr.Status = RunFailure
		notifyTaskFailure(tr)
	} else {
		tr.Status = RunSuccess
	}
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "task_finished", "name": "%s"}`, tr.Name))
	return err
}

func executeCmd(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	output, err := cmd.CombinedOutput()

	if ctx.Err() != nil {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return string(output), ctx.Err()
	}
	return string(output), err
}

func notifyTaskFailure(tr *TaskRun) {
	infoLog.Printf("notifyTaskFailure called for job: %s, task: %s", tr.cmdTemplateParams["title"], tr.Name)
	if CONF.notify == "" {
		return
	}
	// todo: simplify
	const notifyCmdTemplate = `{{.Notify}} --job "{{.Job}}" --task "{{.Task}}" --start "{{.Start}}" --end "{{.End}}" {{if .Emails}}--emails {{range .Emails}}"{{.}}" {{end}}{{end}}`
	type NotifyParams struct {
		Notify string
		Job    string
		Task   string
		Start  string
		End    string
		Emails []string
	}
	data := NotifyParams{
		Notify: CONF.notify,
		Job:    tr.cmdTemplateParams["title"],
		Task:   tr.Name,
		Start:  tr.StartTime.Format(time.RFC3339),
		End:    tr.EndTime.Format(time.RFC3339),
		Emails: tr.emails,
	}
	tmpl, err := texttemplate.New("notify").Parse(notifyCmdTemplate)
	if err != nil {
		errorLog.Printf("Failed to parse notify template: %v", err)
		return
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		errorLog.Printf("Failed to execute notify template: %v", err)
		return
	}
	//todo: add timeout
	command := sb.String()
	infoLog.Printf("Executing notification command: %s", command)
	cmd := exec.Command("/bin/bash", "-c", command)
	go func() {
		output, err := cmd.CombinedOutput()
		if err != nil {
			errorLog.Printf("Notification script failed: %v, output: %s", err, output)
		} else {
			infoLog.Printf("Notification script executed: %s", output)
		}
	}()
}

func restartJobRun(jb *Job, run *JobRun) {
	run.Status = NoRun
	run.StartTime = time.Time{}
	run.EndTime = time.Time{}
	for _, tr := range run.TasksHistory {
		tr.Status = NoRun
		tr.StartTime = time.Time{}
		tr.EndTime = time.Time{}
		tr.RenderedCmd = ""
		tr.lastOutput = ""
		tr.Attempt = 0
	}
	go runJob(run, jb)
}

func restartTaskRun(taskRun *TaskRun, jobRun *JobRun) {
	go func() {
		runTask(nil, taskRun)
		updateJobRunStatusFromTasks(jobRun)
	}()
	//todo: add error check
}

func cancelJobRun(jb *Job, run *JobRun) {
	for _, tr := range run.TasksHistory {
		cancelTaskRun(tr, run)
	}
	updateJobRunStatusFromTasks(run)
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "job_cancel", "title": "%s"}`, jb.Title))
}

func cancelTaskRun(taskRun *TaskRun, jobRun *JobRun) {
	if taskRun.Status == Running {
		if taskRun.ctxCancelFn != nil {
			taskRun.ctxCancelFn()
			taskRun.ctxCancelFn = nil
			taskRun.lastOutput = ""
			taskRun.EndTime = time.Time{}
			taskRun.Status = RunFailure
		} else {
			panic("Can't cancel a running task. This is not supposed to happen.")
		}
	}
	updateJobRunStatusFromTasks(jobRun)
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "task_cancel", "name": "%s"}`, taskRun.Name))
}

func updateJobRunStatusFromTasks(jobRun *JobRun) {
	for _, tr := range jobRun.TasksHistory {
		if tr.Status == RunFailure {
			jobRun.Status = RunFailure
			return
		} else if tr.Status == Running {
			jobRun.Status = Running
			return
		} else if tr.Status == NoRun {
			jobRun.Status = RunFailure
			return
		}
	}
	jobRun.Status = RunSuccess
}

func jobOnOff(jobidx int, JC *JobsAndCron) error {
	if jobidx >= len(JC.Jobs) || jobidx < 0 {
		errorLog.Printf("incorrect job index %v", jobidx)
		return errors.New("incorrect job index")
	}
	jb := JC.Jobs[jobidx]
	jb.OnOff = !jb.OnOff
	if jb.OnOff {
		jb.NextScheduled = JC.cron.Entry(jb.cronID).Next
	} else {
		jb.NextScheduled = time.Time{}
	}
	infoLog.Printf("Toggled state of %s to %v", jb.Title, jb.OnOff)
	return nil
}

func runNow(jb *Job) error {
	run := initRun(jb, nil)
	go runJob(run, jb)
	//todo: check for errors
	return nil
}

type HTTPQueryParams struct {
	jobIndex  int
	runIndex  int
	taskIndex int
}

func httpServer(JC *JobsAndCron) {
	httpQPars := new(HTTPQueryParams)
	http.HandleFunc("/", httpIndex)
	http.HandleFunc("/login", httpLogin)
	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		httpJobs(w, r, httpQPars, JC)
	})
	http.HandleFunc("/events", httpEvents)
	http.HandleFunc("/onoff", func(w http.ResponseWriter, r *http.Request) {
		httpOnOff(w, r, httpQPars, JC)
	})
	http.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		httpRestart(w, r, httpQPars, JC)
	})
	http.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		httpCancel(w, r, httpQPars, JC)
	})
	http.HandleFunc("/runnow", func(w http.ResponseWriter, r *http.Request) {
		httpRunNow(w, r, httpQPars, JC)
	})
	http.HandleFunc("/lastoutput", func(w http.ResponseWriter, r *http.Request) {
		httpLastOutput(w, r, httpQPars, JC)
	})
	http.HandleFunc("/parsingerrors", httpParsingErrors)
	log.Fatal(http.ListenAndServe(CONF.port, nil))
}

func httpIndex(w http.ResponseWriter, r *http.Request) {
	data, err := embedded.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Error loading the page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write(data)
}

func httpLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	var creds struct {
		Password string `json:"password"`
	}
	err := json.NewDecoder(r.Body).Decode(&creds)
	if err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}
	if creds.Password != CONF.password {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	expirationTime := time.Now().Add(15 * time.Minute)
	claims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(expirationTime),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(jwtSecretKey)
	if err != nil {
		http.Error(w, "Failed to create token", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    tokenString,
		Expires:  expirationTime,
		HttpOnly: true,
	})
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Login successful!"))
}

func httpCheckAuth(w http.ResponseWriter, r *http.Request) (error, int, string) {
	if CONF.password == "" {
		return nil, http.StatusOK, "Ok"
	}
	cookie, err := r.Cookie("token")
	if err != nil {
		if err == http.ErrNoCookie {
			return err, http.StatusUnauthorized, "Unauthorized"
		}
		return err, http.StatusBadRequest, "Bad request"
	}
	tokenStr := cookie.Value
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		return jwtSecretKey, nil
	})
	if err != nil || !token.Valid {
		return err, http.StatusUnauthorized, "Unauthorized"
	}
	//todo: prolong token
	return nil, http.StatusOK, "Ok"
}

func httpJobs(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	jData, err := json.Marshal(JC)
	if err != nil {
		errorLog.Println(err)
		http.Error(w, "No Jobs Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(jData)
}

func httpOnOff(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	httpParseJobRunTask(r, httpQPars, JC)
	err = jobOnOff(httpQPars.jobIndex, JC)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpRestart(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	httpParseJobRunTask(r, httpQPars, JC)
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	var rn *JobRun
	rn = nil
	if jb != nil && httpQPars.runIndex != -1 {
		rn = jb.RunHistory[httpQPars.runIndex]
	}
	var t *TaskRun
	t = nil
	if rn != nil && httpQPars.taskIndex != -1 {
		t = rn.TasksHistory[httpQPars.taskIndex]
	}
	if rn != nil && t != nil {
		//todo: handle mutex and cancellation
		go restartTaskRun(t, rn)
	} else if rn != nil {
		//go restartJobRun?
		restartJobRun(jb, rn)
	} else {
		http.Error(w, "JobRun or TaskRun not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpCancel(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	httpParseJobRunTask(r, httpQPars, JC)
	// todo: simplify
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	var rn *JobRun
	rn = nil
	if jb != nil && httpQPars.runIndex != -1 {
		rn = jb.RunHistory[httpQPars.runIndex]
	}
	var t *TaskRun
	t = nil
	if rn != nil && httpQPars.taskIndex != -1 {
		t = rn.TasksHistory[httpQPars.taskIndex]
	}
	if rn != nil && t != nil {
		//todo: handle mutex and cancellation
		go cancelTaskRun(t, rn)
	} else if rn != nil {
		//go restartJobRun?
		cancelJobRun(jb, rn)
	} else {
		http.Error(w, "JobRun or TaskRun not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpRunNow(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	httpParseJobRunTask(r, httpQPars, JC)
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	err = runNow(jb)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpLastOutput(w http.ResponseWriter, r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	httpParseJobRunTask(r, httpQPars, JC)
	var jb *Job
	jb = nil
	if httpQPars.jobIndex != -1 {
		jb = JC.Jobs[httpQPars.jobIndex]
	}
	var rn *JobRun
	rn = nil
	if jb != nil && httpQPars.runIndex != -1 {
		rn = jb.RunHistory[httpQPars.runIndex]
	}
	var t *TaskRun
	t = nil
	if rn != nil && httpQPars.taskIndex != -1 {
		t = rn.TasksHistory[httpQPars.taskIndex]
	}
	if t == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(t.lastOutput))
}

func httpParseJobRunTask(r *http.Request, httpQPars *HTTPQueryParams, JC *JobsAndCron) {
	//todo: use errors instead of -1
	job_str := r.URL.Query().Get("job")
	jb, err := strconv.Atoi(job_str)
	if err != nil {
		jb = -1
	} else if jb < 0 || jb >= len(JC.Jobs) {
		jb = -1
	}
	httpQPars.jobIndex = jb
	run_str := r.URL.Query().Get("run")
	run, err := strconv.Atoi(run_str)
	if err != nil {
		run = -1
	} else if jb != -1 && (run < 0 || run >= len(JC.Jobs[jb].RunHistory)) {
		run = -1
	}
	httpQPars.runIndex = run
	task_str := r.URL.Query().Get("task")
	task, err := strconv.Atoi(task_str)
	if err != nil {
		task = -1
	} else if jb != -1 && run != -1 && (task < 0 || task >= len(JC.Jobs[jb].RunHistory[run].TasksHistory)) {
		task = -1
	}
	httpQPars.taskIndex = task
}

func httpParsingErrors(w http.ResponseWriter, r *http.Request) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	//todo: unnecessary conversions?
	w.Write([]byte(webLogBuf.String()))
}

type sseClients struct {
	clients map[chan string]bool
	mu      sync.Mutex
}

// todo: avoid global variables
var SSECLIENTS = &sseClients{
	clients: make(map[chan string]bool),
}

func addSSEClient(ch chan string) {
	SSECLIENTS.mu.Lock()
	defer SSECLIENTS.mu.Unlock()
	SSECLIENTS.clients[ch] = true
}

func removeSSEClient(ch chan string) {
	SSECLIENTS.mu.Lock()
	defer SSECLIENTS.mu.Unlock()
	delete(SSECLIENTS.clients, ch)
	close(ch)
}

func broadcastSSEUpdate(msg string) {
	SSECLIENTS.mu.Lock()
	defer SSECLIENTS.mu.Unlock()
	infoLog.Printf("Event '%s'", msg)
	for ch := range SSECLIENTS.clients {
		select {
		case ch <- msg:
		default: // drop message if channel overflows
		}
	}
}

func httpEvents(w http.ResponseWriter, r *http.Request) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	clientChan := make(chan string, 30)
	addSSEClient(clientChan)
	defer removeSSEClient(clientChan)

	for msg := range clientChan {
		fmt.Fprintf(w, "data: %s\n\n", msg)
		flusher.Flush()
	}
}
