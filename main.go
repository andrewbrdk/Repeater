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
	"strconv"
	"strings"
	"sync"
	"syscall"
	texttemplate "text/template"
	"time"
	"unicode"

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
	logsDir  string
}

type RunStatus int

const (
	RunSuccess RunStatus = iota
	RunFailure
	Running
	NoRun
)

type Task struct {
	Name       string   `toml:"name"`
	Cmd        string   `toml:"cmd"`
	Emails     []string `toml:"emails"`
	Retries    int      `toml:"retries"`
	TimeoutSec int      `toml:"timeout"`
}

type TaskRun struct {
	Idx               int
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
	timeout           int
	ctxCancelFn       context.CancelFunc
	logfile           string
}

type JobRun struct {
	Idx           int
	ScheduledTime time.Time
	StartTime     time.Time
	EndTime       time.Time
	Status        RunStatus
	TasksHistory  []*TaskRun
	ctxCancelFn   context.CancelFunc
}

type Job struct {
	Id             int
	file           string
	md5            [16]byte
	Title          string `toml:"title"`
	Cron           string `toml:"cron"`
	HCron          string
	Tasks          []*Task    `toml:"tasks"`
	Order          [][]string `toml:"order"`
	OrderProvided  bool       `toml:"-"`
	taskMap        map[string]*Task
	cronID         cron.EntryID
	RunHistory     []*JobRun
	OnOff          bool
	NextScheduled  time.Time
	Retries        int      `toml:"retries"`
	TaskTimeoutSec int      `toml:"task_timeout"`
	Emails         []string `toml:"emails"`
}

type JobsAndCron struct {
	Jobs       map[int]*Job
	cron       *cron.Cron
	parser     cron.Parser
	jobCounter int
	//todo: add config, make global?
	//no need for mutex?
	//Jobs is modified from scanAndScheduleJobs only
	//calls to scanAndScheduleJobs don't overlap
}

func main() {
	initConfig()
	jwtSecretKey = generateRandomKey(32)
	infoLog = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	errorLog = log.New(os.Stdout, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
	webLog = log.New(&webLogBuf, "", log.Ldate|log.Ltime)
	JC := &JobsAndCron{
		Jobs:       make(map[int]*Job),
		parser:     cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		jobCounter: 0,
	}
	JC.cron = cron.New(cron.WithParser(JC.parser))
	JC.cron.Start()
	scanAndScheduleJobs(JC)
	go startFSWatcher(JC)
	httpServer(JC)
}

func initConfig() {
	CONF.port = ":8080"
	CONF.jobsDir = "./examples/"
	CONF.password = ""
	CONF.notify = "python3 ./examples/notify.py"
	CONF.logsDir = "/tmp/repeater/"
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
	if logsDir := os.Getenv("REPEATER_LOGS_DIRECTORY"); logsDir != "" {
		CONF.logsDir = logsDir
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
		jb, err := processJobFile(f, JC)
		if jb != nil && err == nil {
			scheduleJob(jb, JC)
		} else {
			infoLog.Printf("Skipping %s", f)
		}
	}
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
	var toremove []int
	for id, jb := range JC.Jobs {
		md5, haskey := files[jb.file]
		if !haskey {
			infoLog.Printf("Marking %s for deletion", jb.Title)
			toremove = append(toremove, id)
		} else if md5 != jb.md5 {
			infoLog.Printf("File %s has changed, marking for reloading", jb.file)
			toremove = append(toremove, id)
		} else if md5 == jb.md5 {
			infoLog.Printf("File %s has not changed, skipping", jb.file)
			delete(files, jb.file)
		} else {
			panic("This is not supposed to happen")
		}
	}
	for _, id := range toremove {
		JC.cron.Remove(JC.Jobs[id].cronID)
		cancelActiveJobRuns(JC.Jobs[id])
		delete(JC.Jobs, id)
	}
}

func processJobFile(filePath string, JC *JobsAndCron) (*Job, error) {
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
	taskNames := make(map[string]bool)
	for _, t := range jb.Tasks {
		if len(t.Name) == 0 || len(t.Cmd) == 0 {
			errorLog.Printf("%s: Task name or cmd is empty. Skipping job altogether.\n", filePath)
			webLog.Printf("%s: Task name or cmd is empty. Skipping job altogether. \n", filePath)
			return nil, nil
		}
		if taskNames[t.Name] {
			errorLog.Printf("%s: Duplicate task name '%s'. Skipping job altogether.\n", filePath, t.Name)
			webLog.Printf("%s: Duplicate task name '%s'. Skipping job altogether.\n", filePath, t.Name)
			return nil, fmt.Errorf("duplicate task name '%s'", t.Name)
		} else {
			taskNames[t.Name] = true
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
	if jb.TaskTimeoutSec < 0 {
		errorLog.Printf("Job '%s' has negative task_timeout (%d), setting to 0", jb.Title, jb.TaskTimeoutSec)
		webLog.Printf("Job '%s' has negative task_timeout (%d), setting to 0", jb.Title, jb.TaskTimeoutSec)
		jb.TaskTimeoutSec = 0
	}
	for _, t := range jb.Tasks {
		if t.TimeoutSec < 0 {
			errorLog.Printf("Task '%s' in job '%s' has negative timeout (%d), setting to 0", t.Name, jb.Title, t.TimeoutSec)
			webLog.Printf("Task '%s' in job '%s' has negative timeout (%d), setting to 0", t.Name, jb.Title, t.TimeoutSec)
			t.TimeoutSec = 0
		}
	}
	jb.taskMap = make(map[string]*Task)
	for _, t := range jb.Tasks {
		jb.taskMap[t.Name] = t
	}
	if len(jb.Order) == 0 {
		jb.OrderProvided = false
		jb.Order = make([][]string, 0, len(jb.Tasks))
		for _, t := range jb.Tasks {
			jb.Order = append(jb.Order, []string{t.Name})
		}
	} else {
		jb.OrderProvided = true
	}
	for _, order := range jb.Order {
		for _, taskName := range order {
			if _, ok := jb.taskMap[taskName]; !ok {
				errorLog.Printf("%s: Task '%s' in Order is not defined. Skipping job altogether.\n", filePath, taskName)
				webLog.Printf("%s: Task '%s' in Order is not defined. Skipping job altogether. \n", filePath, taskName)
				return nil, errors.New("Task in Order is not defined")
			}
		}
	}
	_, err = JC.parser.Parse(jb.Cron)
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
	jb.Id = JC.jobCounter
	JC.Jobs[JC.jobCounter] = jb
	JC.jobCounter += 1
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
		Idx:           len(jb.RunHistory),
		ScheduledTime: scheduled_time,
		StartTime:     time.Now(),
	}
	idx := 0
	for _, taskGr := range jb.Order {
		for _, taskName := range taskGr {
			t, _ := jb.taskMap[taskName]
			emails := t.Emails
			if len(emails) == 0 {
				emails = jb.Emails
			}
			retries := t.Retries
			if retries == 0 {
				retries = jb.Retries
			}
			timeout := t.TimeoutSec
			if timeout == 0 {
				timeout = jb.TaskTimeoutSec
			}
			run.TasksHistory = append(run.TasksHistory, &TaskRun{
				Name:    t.Name,
				Idx:     idx,
				cmd:     t.Cmd,
				Status:  NoRun,
				Attempt: 0,
				cmdTemplateParams: map[string]string{
					"title":        jb.Title,
					"scheduled_dt": run.ScheduledTime.Format("2006-01-02"),
				},
				retries: retries,
				timeout: timeout,
				emails:  emails,
				logfile: "",
			})
			idx += 1
		}
	}
	jb.RunHistory = append(jb.RunHistory, run)
	return run
}

func runJob(run *JobRun, jb *Job) error {
	//todo: check race conditions
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
	idx := 0
	for _, parallelGroup := range jb.Order {
		var wg sync.WaitGroup
		errCh := make(chan error, len(parallelGroup))
		for range parallelGroup {
			tr := run.TasksHistory[idx]
			wg.Add(1)
			go func(tr *TaskRun) {
				defer wg.Done()
				var lastErr error
				for attempt := 1; attempt <= tr.retries+1; attempt++ {
					infoLog.Printf("Running task '%s' (attempt %d/%d)", tr.Name, attempt, tr.retries+1)
					lastErr = runTask(ctx, tr)
					if lastErr == nil {
						break
					} else if lastErr == context.Canceled {
						infoLog.Printf("Task '%s' cancelled", tr.Name)
						break
					}
					errorLog.Printf("Task '%s' failed (attempt %d/%d)", tr.Name, attempt, tr.retries+1)
					if attempt > tr.retries {
						break
					}
				}
				if lastErr != nil {
					errCh <- lastErr
				}
			}(tr)
			idx += 1
		}
		wg.Wait()
		close(errCh)
		if len(errCh) > 0 {
			jobFail = true
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
	//
	var execCtx context.Context
	var timeoutFunc context.CancelFunc
	if ctx == nil {
		ctx = context.Background()
	}
	cancelCtx, cancelFunc := context.WithCancel(ctx)
	if tr.timeout > 0 {
		execCtx, timeoutFunc = context.WithTimeout(cancelCtx, time.Duration(tr.timeout)*time.Second)
	} else {
		execCtx = cancelCtx
		timeoutFunc = func() {}
	}
	tr.ctxCancelFn = func() {
		timeoutFunc()
		cancelFunc()
	}
	defer func() {
		if tr.ctxCancelFn != nil {
			tr.ctxCancelFn()
			tr.ctxCancelFn = nil
		}
	}()
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "task_running", "name": "%s"}`, tr.Name))
	output, err := executeCmd(execCtx, tr.RenderedCmd)
	tr.EndTime = time.Now()
	if err != nil {
		errorLog.Printf("Error executing '%s'-'%s': %v\n", tr.cmdTemplateParams["title"], tr.Name, err)
		output = output + "\nERROR: " + err.Error()
		tr.Status = RunFailure
		notifyTaskFailure(tr)
	} else {
		tr.Status = RunSuccess
	}
	saveOutputOnDisk(output, tr)
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "task_finished", "name": "%s"}`, tr.Name))
	return err
}

func executeCmd(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), ctx.Err()
	}
	return string(output), err
}

func saveOutputOnDisk(output string, tr *TaskRun) {
	if CONF.logsDir == "" {
		return
	}
	if err := os.MkdirAll(CONF.logsDir, 0755); err != nil {
		errorLog.Printf("Failed to create logs directory %s: %v", CONF.logsDir, err)
		return
	}
	tr.logfile = fmt.Sprintf("%s_%s_%s.log", //todo: add indexes and attempt
		tr.StartTime.Format("20060102T150405"),
		escapeName(tr.cmdTemplateParams["title"]), // todo: use job.Title
		escapeName(tr.Name),
	)
	filename := filepath.Join(CONF.logsDir, tr.logfile)
	if err := os.WriteFile(filename, []byte(output), 0644); err != nil {
		errorLog.Printf("Failed to write task output to file %s: %v", filename, err)
	} else {
		infoLog.Printf("Task output saved to %s", filename)
	}
}

func escapeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func readTaskOutput(tr *TaskRun) (string, error) {
	if tr.logfile == "" {
		return "", nil
	}
	filename := filepath.Join(CONF.logsDir, tr.logfile)
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("failed to read logfile %s: %w", tr.logfile, err)
	}
	return string(data), nil
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
		tr.logfile = ""
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
			taskRun.logfile = ""
			taskRun.EndTime = time.Time{}
			taskRun.Status = RunFailure
		} else {
			panic("Can't cancel a running task. This is not supposed to happen.")
		}
	}
	updateJobRunStatusFromTasks(jobRun)
	broadcastSSEUpdate(fmt.Sprintf(`{"event": "task_cancel", "name": "%s"}`, taskRun.Name))
}

func cancelActiveJobRuns(jb *Job) {
	jb.OnOff = false
	for _, run := range jb.RunHistory {
		if run.Status == Running {
			cancelJobRun(jb, run)
		}
	}
	jb.NextScheduled = time.Time{}
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

func jobOnOff(jb *Job, JC *JobsAndCron) error {
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

func httpServer(JC *JobsAndCron) {
	http.HandleFunc("/", httpIndex)
	http.HandleFunc("/login", httpLogin)
	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		httpJobs(w, r, JC)
	})
	http.HandleFunc("/events", httpEvents)
	http.HandleFunc("/onoff", func(w http.ResponseWriter, r *http.Request) {
		httpOnOff(w, r, JC)
	})
	http.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
		httpRestart(w, r, JC)
	})
	http.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		httpCancel(w, r, JC)
	})
	http.HandleFunc("/runnow", func(w http.ResponseWriter, r *http.Request) {
		httpRunNow(w, r, JC)
	})
	http.HandleFunc("/lastoutput", func(w http.ResponseWriter, r *http.Request) {
		httpLastOutput(w, r, JC)
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

func httpJobs(w http.ResponseWriter, r *http.Request, JC *JobsAndCron) {
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

func httpOnOff(w http.ResponseWriter, r *http.Request, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	job, _, _ := httpParseJobRunTask(r, JC)
	if job == nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	jobOnOff(job, JC)
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpRestart(w http.ResponseWriter, r *http.Request, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	job, run, task := httpParseJobRunTask(r, JC)
	if run != nil && task != nil {
		//todo: check concurrency issues
		go restartTaskRun(task, run)
	} else if run != nil {
		go restartJobRun(job, run)
	} else {
		http.Error(w, "JobRun or TaskRun not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpCancel(w http.ResponseWriter, r *http.Request, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	job, run, task := httpParseJobRunTask(r, JC)
	if run != nil && task != nil {
		//todo: check concurrency issues
		go cancelTaskRun(task, run)
	} else if run != nil {
		go cancelJobRun(job, run)
	} else {
		http.Error(w, "JobRun or TaskRun not found", http.StatusNotFound)
		return
	}
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpRunNow(w http.ResponseWriter, r *http.Request, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	job, _, _ := httpParseJobRunTask(r, JC)
	if job == nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}
	runNow(job)
	// todo: w.Write(json.Marshal(JC))
	w.WriteHeader(http.StatusOK)
}

func httpLastOutput(w http.ResponseWriter, r *http.Request, JC *JobsAndCron) {
	err, code, msg := httpCheckAuth(w, r)
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	_, _, task := httpParseJobRunTask(r, JC)
	if task == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	var output string
	output, err = readTaskOutput(task)
	if err != nil {
		errorLog.Printf("Failed to read task run log: %v", err)
		http.Error(w, "ERROR: Failed to read task run log", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(output))
}

func httpParseJobRunTask(r *http.Request, JC *JobsAndCron) (*Job, *JobRun, *TaskRun) {
	var jb *Job
	var run *JobRun
	var task *TaskRun
	job_str := r.URL.Query().Get("job")
	jb_id, err := strconv.Atoi(job_str)
	if err != nil {
		return nil, nil, nil
	}
	// todo: use map[int] *Job in JobsAndCron
	for _, job := range JC.Jobs {
		if job.Id == jb_id {
			jb = job
			break
		}
	}
	if jb == nil {
		return nil, nil, nil
	}
	run_str := r.URL.Query().Get("run")
	run_idx, err := strconv.Atoi(run_str)
	if err != nil {
		return jb, nil, nil
	} else if run_idx < 0 || run_idx >= len(jb.RunHistory) {
		return jb, nil, nil
	}
	run = jb.RunHistory[run_idx]
	task_str := r.URL.Query().Get("task")
	task_idx, err := strconv.Atoi(task_str)
	if err != nil {
		return jb, run, nil
	} else if task_idx < 0 || task_idx >= len(run.TasksHistory) {
		return jb, run, nil
	}
	task = run.TasksHistory[task_idx]
	return jb, run, task
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
